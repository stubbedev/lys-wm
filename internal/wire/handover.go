package wire

import (
	"encoding/binary"
	"fmt"
	"slices"

	"golang.org/x/sys/unix"
)

func NewRecorder() *Conn {
	c := NewConn(-1)
	c.muted = true
	return c
}

func (c *Conn) Mute() {
	c.muted = true
}

func (c *Conn) Recorded() ([]byte, []int) {
	return c.out, c.fdsOut
}

func (c *Conn) Pending() (in []byte, inFDs []int, out []byte, outFDs []int) {
	return slices.Clone(c.in[c.r:c.w]), c.fdsIn.list(), slices.Clone(c.out), slices.Clone(c.fdsOut)
}

func (c *Conn) Feed(in []byte, fds []int) error {
	if c.w != c.r {
		return fmt.Errorf("wire: feed fd %d, which has %d unread bytes", c.fd, c.w-c.r)
	}
	if need := max(len(in), initialBuffer); cap(c.in) < need {
		c.in = make([]byte, need)
	} else {
		c.in = c.in[:cap(c.in)]
	}
	c.r, c.w = 0, copy(c.in, in)
	if len(fds) > len(c.fdsIn.buf)-c.fdsIn.n {
		grown := newFDQueue(c.fdsIn.n + len(fds) + maxFDsIn)
		for _, fd := range c.fdsIn.list() {
			grown.push(fd)
		}
		c.fdsIn = grown
	}
	for _, fd := range fds {
		if !c.fdsIn.push(fd) {
			return fmt.Errorf("wire: feed fd %d: the fd queue is full", c.fd)
		}
	}
	return nil
}

func (c *Conn) Unmute(keep func(id ObjectID, opcode Opcode) (bool, int), prefix []byte, prefixFDs []int) error {
	out := make([]byte, 0, len(prefix)+len(c.out))
	out = append(out, prefix...)
	fds := slices.Clone(prefixFDs)
	rest, restFDs := c.out, c.fdsOut
	for len(rest) > 0 {
		id, opcode, size, err := parseHeader(rest)
		if err != nil {
			return fmt.Errorf("wire: walk the events held for fd %d: %w", c.fd, err)
		}
		kept, n := keep(id, opcode)
		if n > len(restFDs) {
			return fmt.Errorf("wire: event %d.%d held for fd %d names %d fds, %d are held", id, opcode, c.fd, n, len(restFDs))
		}
		if kept {
			out = append(out, rest[:size]...)
			fds = append(fds, restFDs[:n]...)
		} else {
			for _, fd := range restFDs[:n] {
				_ = unix.Close(fd)
			}
		}
		rest, restFDs = rest[size:], restFDs[n:]
	}
	c.out, c.fdsOut, c.muted = out, fds, false
	return nil
}

func ArgWalk(data []byte, args []Arg, f func(a Arg, off int)) error {
	off := headerSize
	blob := func(name string) (int, error) {
		if off+4 > len(data) {
			return 0, fmt.Errorf("wire: message of %d bytes ends before argument %s", len(data), name)
		}
		size := int(binary.NativeEndian.Uint32(data[off:]))
		if size < 0 || size > len(data) || off+4+pad(size) > len(data) {
			return 0, fmt.Errorf("wire: argument %s of %d bytes in a message of %d", name, size, len(data))
		}
		return 4 + pad(size), nil
	}
	for _, a := range args {
		if a.Type == ArgFD {
			f(a, -1)
			continue
		}
		if a.Type == ArgNewID && a.Interface == "" {
			n, err := blob("interface")
			if err != nil {
				return err
			}
			f(Arg{Name: "interface", Type: ArgString}, off)
			off += n + 4
		}
		if off+4 > len(data) {
			return fmt.Errorf("wire: message of %d bytes ends before argument %s", len(data), a.Name)
		}
		n := 4
		if a.Type == ArgString || a.Type == ArgArray {
			var err error
			if n, err = blob(a.Name); err != nil {
				return err
			}
		}
		f(a, off)
		off += n
	}
	return nil
}

func ObjectAt(data []byte, off int) ObjectID {
	return ObjectID(binary.NativeEndian.Uint32(data[off:]))
}

func SetObjectAt(data []byte, off int, id ObjectID) {
	binary.NativeEndian.PutUint32(data[off:], uint32(id))
}

func StringAt(data []byte, off int) string {
	size := int(binary.NativeEndian.Uint32(data[off:]))
	if size == 0 {
		return ""
	}
	return string(data[off+4 : off+4+size-1])
}

func (c *Conn) Raw(data []byte, fds []int) error {
	c.out = append(c.out, data...)
	for _, fd := range fds {
		dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("wire: duplicate fd %d: %w", fd, err)
		}
		c.fdsOut = append(c.fdsOut, dup)
	}
	return nil
}

func (m *Message) Skip() {
	m.off = len(m.data)
}

func (m *Message) Raw() []byte {
	return m.data
}

func (m *Message) PeekFDs(n int) []int {
	return m.fds.first(n)
}
