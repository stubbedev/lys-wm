// Package handover moves a running session's kernel objects and state to the
// next lys-wm process over a Unix socket.
package handover

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

const (
	Format      = 2
	maxFDsAMsg  = 200
	maxFrame    = 1 << 30
	frameHeader = 8
)

type FDs struct {
	fds   []int
	owned []int
	err   error
}

func (t *FDs) AddOwned(fds []int) []int {
	out := make([]int, len(fds))
	for i, fd := range fds {
		t.owned = append(t.owned, fd)
		t.fds = append(t.fds, fd)
		out[i] = len(t.fds) - 1
	}
	return out
}

func (t *FDs) CloseOwned() {
	for _, fd := range t.owned {
		_ = unix.Close(fd)
	}
	t.owned = nil
}

func (t *FDs) Add(fd int) int {
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.err = errors.Join(t.err, fmt.Errorf("handover: keep fd %d for the new process: %w", fd, err))
		dup = fd
	} else {
		t.owned = append(t.owned, dup)
	}
	t.fds = append(t.fds, dup)
	return len(t.fds) - 1
}

func (t *FDs) AddAll(fds []int) []int {
	out := make([]int, len(fds))
	for i, fd := range fds {
		out[i] = t.Add(fd)
	}
	return out
}

func (t *FDs) Get(i int) (int, error) {
	if i < 0 || i >= len(t.fds) {
		return -1, fmt.Errorf("handover: fd index %d of %d", i, len(t.fds))
	}
	return t.fds[i], nil
}

func (t *FDs) GetAll(list []int) ([]int, error) {
	out := make([]int, len(list))
	for i, n := range list {
		fd, err := t.Get(n)
		if err != nil {
			return nil, err
		}
		out[i] = fd
	}
	return out, nil
}

func writeAll(fd int, data []byte, rights []int) error {
	var oob []byte
	if len(rights) > 0 {
		oob = unix.UnixRights(rights...)
	}
	for len(data) > 0 {
		n, err := unix.SendmsgN(fd, data, oob, nil, unix.MSG_NOSIGNAL)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("handover: write %d bytes: %w", len(data), err)
		}
		data, oob = data[n:], nil
	}
	return nil
}

func Send(fd int, payload any, t *FDs) error {
	if t != nil && t.err != nil {
		return t.err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("handover: encode the state: %w", err)
	}
	if len(body) > maxFrame {
		return fmt.Errorf("handover: state of %d bytes passes the limit of %d", len(body), maxFrame)
	}
	head := make([]byte, frameHeader)
	binary.LittleEndian.PutUint32(head, uint32(len(body))) //nolint:gosec // bounded by maxFrame
	var count int
	if t != nil {
		count = len(t.fds)
	}
	binary.LittleEndian.PutUint32(head[4:], uint32(count))
	if err := writeAll(fd, head, nil); err != nil {
		return err
	}
	if t != nil {
		for rest := t.fds; len(rest) > 0; {
			n := min(len(rest), maxFDsAMsg)
			if err := writeAll(fd, []byte{1}, rest[:n]); err != nil {
				return fmt.Errorf("handover: pass %d fds: %w", n, err)
			}
			rest = rest[n:]
		}
	}
	return writeAll(fd, body, nil)
}

func readFull(fd int, p []byte) error {
	for len(p) > 0 {
		n, err := unix.Read(fd, p)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("handover: read: %w", err)
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		p = p[n:]
	}
	return nil
}

func Receive(fd int, payload any) (*FDs, error) {
	head := make([]byte, frameHeader)
	if err := readFull(fd, head); err != nil {
		return nil, err
	}
	size, count := binary.LittleEndian.Uint32(head), int(binary.LittleEndian.Uint32(head[4:]))
	if size > maxFrame {
		return nil, fmt.Errorf("handover: state of %d bytes passes the limit of %d", size, maxFrame)
	}
	t := &FDs{}
	oob := make([]byte, unix.CmsgSpace(maxFDsAMsg*4))
	for len(t.fds) < count {
		var one [1]byte
		n, oobn, _, _, err := unix.Recvmsg(fd, one[:], oob, unix.MSG_CMSG_CLOEXEC)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("handover: receive fds: %w", err)
		}
		if n == 0 {
			return nil, io.ErrUnexpectedEOF
		}
		msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			return nil, fmt.Errorf("handover: parse the fds: %w", err)
		}
		for i := range msgs {
			fds, err := unix.ParseUnixRights(&msgs[i])
			if err != nil {
				return nil, fmt.Errorf("handover: parse the fds: %w", err)
			}
			t.fds = append(t.fds, fds...)
		}
	}
	body := make([]byte, size)
	if err := readFull(fd, body); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, payload); err != nil {
		return nil, fmt.Errorf("handover: decode the state: %w", err)
	}
	return t, nil
}
