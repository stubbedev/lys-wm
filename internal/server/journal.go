package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/wire"
)

type retained struct {
	seq    uint64
	class  requestClass
	opcode wire.Opcode
	key    uint32
	target *record
	data   []byte
	fds    []int
	file   *keptFile
	refs   []argRef
	bare   []bareArg
	count  int
}

type keptFile struct {
	offset int64
	data   []byte
}

type bareArg struct {
	id    wire.ObjectID
	iface string
}

type argRef struct {
	off int
	rec *record
}

type record struct {
	id      wire.ObjectID
	iface   string
	creator *retained
	msgs    []*retained
	end     *retained
	dead    bool
	refs    int
}

type journal struct {
	policies map[*wire.Interface]*objectPolicy
	records  map[wire.ObjectID]*record
	seq      uint64
}

func (j *journal) policy(iface *wire.Interface) *objectPolicy {
	p, ok := j.policies[iface]
	if !ok {
		p = policyOf(iface)
		j.policies[iface] = p
	}
	return p
}

func (j *journal) observe(r wire.Resource, m *wire.Message) error {
	id := r.Base().ID()
	rec := j.records[id]
	iface := r.Base().Interface()
	p := j.policy(iface)
	if p.class != classJournal || int(m.Opcode) >= len(iface.Requests) {
		return nil
	}
	if rec == nil {
		rec = &record{id: id, iface: iface.Name}
		j.records[id] = rec
	}
	class := p.requests[m.Opcode]
	if class == requestDropped {
		return nil
	}
	return j.keep(rec, class, iface.Requests[m.Opcode], m)
}

func (j *journal) keep(rec *record, class requestClass, spec wire.MessageSpec, m *wire.Message) error {
	data := m.Raw()
	nfds := 0
	var newID wire.ObjectID
	var childIface string
	msg := &retained{class: class, opcode: m.Opcode, target: rec}
	if err := wire.ArgWalk(data, spec.Args, func(a wire.Arg, off int) {
		switch a.Type {
		case wire.ArgFD:
			nfds++
		case wire.ArgObject:
			if target := wire.ObjectAt(data, off); target != 0 {
				if dep := j.records[target]; dep != nil {
					msg.refs = append(msg.refs, argRef{off, dep})
				} else {
					msg.bare = append(msg.bare, bareArg{target, a.Interface})
				}
			}
		case wire.ArgString:
			if strings.HasSuffix(a.Name, "interface") {
				childIface = wire.StringAt(data, off)
			}
		case wire.ArgNewID:
			newID = wire.ObjectAt(data, off)
			msg.refs = append(msg.refs, argRef{off, nil})
			if a.Interface != "" {
				childIface = a.Interface
			}
		}
	}); err != nil {
		return nil
	}
	if class == requestLatest || class == requestLatestKeyed || class == requestCount {
		if class == requestLatestKeyed && len(data) >= 12 {
			msg.key = binary.NativeEndian.Uint32(data[8:])
		}
		i := slices.IndexFunc(rec.msgs, func(o *retained) bool { return o.opcode == msg.opcode && o.key == msg.key })
		if i >= 0 {
			if class == requestCount {
				rec.msgs[i].count++
				return nil
			}
			j.release(rec.msgs[i])
			rec.msgs = slices.Delete(rec.msgs, i, i+1)
		}
	}
	msg.count = 1
	msg.data = slices.Clone(data)
	j.seq++
	msg.seq = j.seq
	for _, fd := range m.PeekFDs(nfds) {
		dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("server: keep an fd of %s.%s: %w", rec.iface, spec.Name, err)
		}
		msg.fds = append(msg.fds, dup)
	}
	if newID != 0 {
		rec.refs++
	}
	if class != requestDestructor || newID != 0 {
		for _, a := range msg.refs {
			if a.rec != nil {
				a.rec.refs++
			}
		}
	}
	switch {
	case newID != 0:
		child := &record{id: newID, iface: childIface, creator: msg}
		for i := range msg.refs {
			if msg.refs[i].rec == nil {
				msg.refs[i].rec = child
			}
		}
		j.records[newID] = child
	case class == requestDestructor:
		rec.end = msg
	default:
		rec.msgs = append(rec.msgs, msg)
	}
	return nil
}

func KeepFile(r wire.Resource, request string, offset int64, data []byte) error {
	c := clientOf(r)
	if !c.display.journaling {
		return nil
	}
	id, iface := r.Base().ID(), r.Base().Interface()
	op := slices.IndexFunc(iface.Requests, func(spec wire.MessageSpec) bool { return spec.Name == request })
	if op < 0 {
		return fmt.Errorf("server: %s has no request %s", iface.Name, request)
	}
	rec := c.journal.records[id]
	var msg *retained
	if rec != nil {
		if i := slices.IndexFunc(rec.msgs, func(m *retained) bool { return int(m.opcode) == op }); i >= 0 {
			msg = rec.msgs[i]
		}
	}
	if msg == nil {
		return fmt.Errorf("server: %s@%d has no journaled %s to keep a file of", iface.Name, id, request)
	}
	for _, fd := range msg.fds {
		_ = unix.Close(fd)
	}
	msg.fds = nil
	msg.file = &keptFile{offset: offset, data: data}
	return nil
}

func (f *keptFile) open() (int, error) {
	fd, err := unix.MemfdCreate("lys-wm-handover", unix.MFD_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("server: a memfd for a kept file: %w", err)
	}
	if err := unix.Ftruncate(fd, f.offset+int64(len(f.data))); err != nil {
		return -1, errors.Join(fmt.Errorf("server: size a kept file to %d bytes: %w", f.offset+int64(len(f.data)), err), unix.Close(fd))
	}
	for n := 0; n < len(f.data); {
		k, err := unix.Pwrite(fd, f.data[n:], f.offset+int64(n))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return -1, errors.Join(fmt.Errorf("server: write a kept file: %w", err), unix.Close(fd))
		}
		n += k
	}
	return fd, nil
}

func (j *journal) release(msg *retained) {
	for _, fd := range msg.fds {
		_ = unix.Close(fd)
	}
	msg.fds = nil
	creates := false
	for _, a := range msg.refs {
		if a.rec != nil && a.rec.creator == msg {
			creates = true
			continue
		}
		if a.rec != nil {
			j.unref(a.rec)
		}
	}
	if creates {
		j.unref(msg.target)
	}
}

func (j *journal) unref(rec *record) {
	rec.refs--
	j.collect(rec)
}

func (j *journal) collect(rec *record) {
	if !rec.dead || rec.refs > 0 {
		return
	}
	msgs := rec.msgs
	rec.msgs = nil
	for _, m := range msgs {
		j.release(m)
	}
	if rec.end != nil {
		for _, fd := range rec.end.fds {
			_ = unix.Close(fd)
		}
	}
	if creator := rec.creator; creator != nil {
		j.release(creator)
		rec.creator = nil
	}
}

func (j *journal) removed(id wire.ObjectID) {
	rec, ok := j.records[id]
	if !ok {
		return
	}
	delete(j.records, id)
	rec.dead = true
	j.collect(rec)
}

func (j *journal) close() {
	for id := range j.records {
		j.removed(id)
	}
}

func (j *journal) size() (records, messages int) {
	for _, r := range j.reachable() {
		records++
		messages += len(r.msgs)
		if r.creator != nil {
			messages++
		}
		if r.end != nil {
			messages++
		}
	}
	return records, messages
}

func (c *Client) JournalSize() (records, messages int) {
	return c.journal.size()
}

func (c *Client) JournalFileBytes() int {
	n := 0
	for _, r := range c.journal.reachable() {
		for _, m := range append([]*retained{r.creator, r.end}, r.msgs...) {
			if m != nil && m.file != nil {
				n += len(m.file.data)
			}
		}
	}
	return n
}
