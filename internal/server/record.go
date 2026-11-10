package server

import (
	"cmp"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/wire"
)

const (
	recordedMessages = 4096
	recordedBody     = 128
)

type recordedMessage struct {
	at       time.Duration
	iface    *wire.Interface
	id       wire.ObjectID
	opcode   wire.Opcode
	incoming bool
	size     uint8
	body     [recordedBody]byte
}

type protocolRing struct {
	messages []recordedMessage
	next     int
	wrapped  bool
}

func newProtocolRing() *protocolRing {
	return &protocolRing{messages: make([]recordedMessage, recordedMessages)}
}

func (r *protocolRing) record(at time.Duration, incoming bool, iface *wire.Interface, id wire.ObjectID, opcode wire.Opcode, body []byte) {
	m := &r.messages[r.next]
	m.at, m.iface, m.id, m.opcode, m.incoming = at, iface, id, opcode, incoming
	m.size = uint8(copy(m.body[:], body)) //nolint:gosec // copy stops at recordedBody, which fits a byte
	if r.next++; r.next == len(r.messages) {
		r.next, r.wrapped = 0, true
	}
}

func (r *protocolRing) all() iter.Seq[*recordedMessage] {
	return func(yield func(*recordedMessage) bool) {
		if r.wrapped {
			for i := r.next; i < len(r.messages); i++ {
				if !yield(&r.messages[i]) {
					return
				}
			}
		}
		for i := range r.next {
			if !yield(&r.messages[i]) {
				return
			}
		}
	}
}

func appendMessage(b []byte, at time.Duration, c *Client, incoming bool, iface *wire.Interface, id wire.ObjectID, opcode wire.Opcode, body []byte) []byte {
	b = fmt.Appendf(b, "[%10.3f] {%d/%d} ", float64(at.Microseconds())/1000, c.creds.Pid, c.serial)
	if !incoming {
		b = append(b, " -> "...)
	}
	if iface == nil {
		return fmt.Appendf(b, "unknown@%d.%d(?)\n", id, opcode)
	}
	specs := iface.Requests
	if !incoming {
		specs = iface.Events
	}
	if int(opcode) >= len(specs) {
		return fmt.Appendf(b, "%s@%d.%d(?)\n", iface.Name, id, opcode)
	}
	b = fmt.Appendf(b, "%s@%d.%s(", iface.Name, id, specs[opcode].Name)
	b = wire.AppendArgs(b, specs[opcode].Args, body)
	return append(b, ")\n"...)
}

func (c *Client) observe(incoming bool, id wire.ObjectID, opcode wire.Opcode, body []byte) {
	d := c.display
	at := time.Since(d.start)
	var iface *wire.Interface
	if r, ok := c.objects[id]; ok {
		iface = r.Base().Interface()
	}
	if c.recorded != nil {
		c.recorded.record(at, incoming, iface, id, opcode, body)
	}
	if d.trace != nil {
		d.traceBuf = appendMessage(d.traceBuf[:0], at, c, incoming, iface, id, opcode, body)
		_, _ = d.trace.Write(d.traceBuf)
	}
}

func (c *Client) watchProtocol() {
	d := c.display
	c.recorded = nil
	if d.recording {
		c.recorded = newProtocolRing()
	}
	if d.recording || d.trace != nil {
		c.conn.SetTracer(c.observe)
		return
	}
	c.conn.SetTracer(nil)
}

func (d *Display) RecordingProtocol() bool {
	return d.recording
}

func (d *Display) RecordProtocol(on bool) {
	if d.recording == on {
		return
	}
	d.recording = on
	for _, c := range d.clients {
		c.watchProtocol()
	}
}

func (d *Display) AppendProtocol(b []byte) ([]byte, error) {
	if !d.recording {
		return b, errors.New("server: no protocol is recorded; start with debug protocol on")
	}
	type line struct {
		c *Client
		m *recordedMessage
	}
	var lines []line
	for _, c := range d.clients {
		for m := range c.recorded.all() {
			lines = append(lines, line{c, m})
		}
	}
	slices.SortStableFunc(lines, func(x, y line) int { return cmp.Compare(x.m.at, y.m.at) })
	b = fmt.Appendf(b, "# lys-wm protocol record of %d clients; [ms] counts from %s\n", len(d.clients), d.start.UTC().Format(time.RFC3339Nano))
	for _, l := range lines {
		m := l.m
		b = appendMessage(b, m.at, l.c, m.incoming, m.iface, m.id, m.opcode, m.body[:m.size])
	}
	return b, nil
}
