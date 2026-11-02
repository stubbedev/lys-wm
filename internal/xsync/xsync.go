// Package xsync is the part of the X SYNC extension _NET_WM_SYNC_REQUEST needs:
// counters and alarms. jezek/xgb does not generate this extension.
package xsync

import (
	"errors"
	"fmt"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

const extension = "SYNC"

const (
	opInitialize    = 0
	opCreateCounter = 2
	opSetCounter    = 3
	opCreateAlarm   = 8
	opChangeAlarm   = 9
	opDestroyAlarm  = 11
)

const (
	maskCounter   = 1 << 0
	maskValueType = 1 << 1
	maskValue     = 1 << 2
	maskTestType  = 1 << 3
	maskEvents    = 1 << 5

	valueAbsolute      = 0
	positiveComparison = 2
)

type (
	Counter uint32
	Alarm   uint32
)

const AlarmDestroyed = 2

func Init(c *xgb.Conn) error {
	reply, err := xproto.QueryExtension(c, uint16(len(extension)), extension).Reply()
	switch {
	case err != nil:
		return fmt.Errorf("query the SYNC extension: %w", err)
	case !reply.Present:
		return errors.New("the X server has no SYNC extension")
	}
	xgb.RegisterExtension(c, extension, reply.MajorOpcode, reply.FirstEvent, reply.FirstError)
	cookie := c.NewCookie(true, true)
	c.NewRequest(request(c, opInitialize, []uint32{3 | 1<<8}), cookie)
	if _, err := cookie.Reply(); err != nil {
		return fmt.Errorf("initialize the SYNC extension: %w", err)
	}
	return nil
}

func init() {
	xgb.NewExtEventFuncs[extension] = map[int]xgb.NewEventFun{1: newAlarmNotify}
	xgb.NewExtErrorFuncs[extension] = map[int]xgb.NewErrorFun{0: newError("Counter"), 1: newError("Alarm")}
}

func request(c *xgb.Conn, op byte, words []uint32) []byte {
	size := 4 + 4*len(words)
	buf := make([]byte, size)
	c.ExtLock.RLock()
	buf[0] = c.Extensions[extension]
	c.ExtLock.RUnlock()
	buf[1] = op
	xgb.Put16(buf[2:], uint16(xgb.Pad(size)/4)) //nolint:gosec // every request here is a few words
	for i, w := range words {
		xgb.Put32(buf[4+4*i:], w)
	}
	return buf
}

func send(c *xgb.Conn, op byte, words []uint32) {
	c.NewRequest(request(c, op, words), c.NewCookie(false, false))
}

func split(v uint64) (hi, lo uint32) {
	return uint32(v >> 32), uint32(v) //nolint:gosec // the low half of the value on purpose
}

func CreateCounter(c *xgb.Conn, id Counter, value uint64) {
	hi, lo := split(value)
	send(c, opCreateCounter, []uint32{uint32(id), hi, lo})
}

func SetCounter(c *xgb.Conn, id Counter, value uint64) {
	hi, lo := split(value)
	send(c, opSetCounter, []uint32{uint32(id), hi, lo})
}

func CreateAlarm(c *xgb.Conn, id Alarm, counter Counter, value uint64) {
	hi, lo := split(value)
	send(c, opCreateAlarm, []uint32{uint32(id), maskCounter | maskValueType | maskValue | maskTestType | maskEvents, uint32(counter), valueAbsolute, hi, lo, positiveComparison, 1})
}

func ChangeAlarm(c *xgb.Conn, id Alarm, value uint64) {
	hi, lo := split(value)
	send(c, opChangeAlarm, []uint32{uint32(id), maskValue, hi, lo})
}

func DestroyAlarm(c *xgb.Conn, id Alarm) {
	send(c, opDestroyAlarm, []uint32{uint32(id)})
}

type AlarmNotify struct {
	Sequence uint16
	Alarm    Alarm
	Value    uint64
	State    byte
}

func newAlarmNotify(buf []byte) xgb.Event {
	return AlarmNotify{
		Sequence: xgb.Get16(buf[2:]),
		Alarm:    Alarm(xgb.Get32(buf[4:])),
		Value:    uint64(xgb.Get32(buf[8:]))<<32 | uint64(xgb.Get32(buf[12:])),
		State:    buf[28],
	}
}

func (e AlarmNotify) Bytes() []byte {
	buf := make([]byte, 32)
	xgb.Put16(buf[2:], e.Sequence)
	xgb.Put32(buf[4:], uint32(e.Alarm))
	hi, lo := split(e.Value)
	xgb.Put32(buf[8:], hi)
	xgb.Put32(buf[12:], lo)
	buf[28] = e.State
	return buf
}

func (e AlarmNotify) String() string {
	return fmt.Sprintf("AlarmNotify {Alarm: %d, Value: %d, State: %d}", e.Alarm, e.Value, e.State)
}

type Error struct {
	Sequence uint16
	Bad      uint32
	Kind     string
}

func newError(kind string) xgb.NewErrorFun {
	return func(buf []byte) xgb.Error {
		return Error{Sequence: xgb.Get16(buf[2:]), Bad: xgb.Get32(buf[4:]), Kind: kind}
	}
}

func (e Error) SequenceId() uint16 {
	return e.Sequence
}

func (e Error) BadId() uint32 {
	return e.Bad
}

func (e Error) Error() string {
	return fmt.Sprintf("bad SYNC %s %d", e.Kind, e.Bad)
}
