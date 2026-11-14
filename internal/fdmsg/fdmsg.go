// Package fdmsg sends JSON messages with file descriptors over a
// SOCK_SEQPACKET Unix socket, one message per packet.
package fdmsg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func Send(fd int, m any, fds ...int) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("fdmsg: encode %T: %w", m, err)
	}
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	for {
		err := unix.Sendmsg(fd, body, oob, nil, unix.MSG_NOSIGNAL)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("fdmsg: send %T of %d bytes with %d fds: %w", m, len(body), len(fds), err)
		}
		return nil
	}
}

type Receiver struct {
	fd   int
	body []byte
	oob  []byte
}

func NewReceiver(fd, maxBytes, maxFDs int) *Receiver {
	return &Receiver{fd: fd, body: make([]byte, maxBytes), oob: make([]byte, unix.CmsgSpace(maxFDs*4))}
}

func Recv[T any](r *Receiver) (T, []int, error) {
	var m T
	for {
		n, oobn, flags, _, err := unix.Recvmsg(r.fd, r.body, r.oob, unix.MSG_CMSG_CLOEXEC)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return m, nil, fmt.Errorf("fdmsg: receive %T: %w", m, err)
		}
		if n == 0 && oobn == 0 {
			return m, nil, os.ErrClosed
		}
		fds, err := rights(r.oob[:oobn])
		if err != nil {
			return m, nil, errors.Join(fmt.Errorf("fdmsg: receive %T: %w", m, err), CloseAll(fds))
		}
		switch {
		case flags&unix.MSG_TRUNC != 0:
			return m, nil, errors.Join(fmt.Errorf("fdmsg: a %T did not fit %d bytes", m, len(r.body)), CloseAll(fds))
		case flags&unix.MSG_CTRUNC != 0:
			return m, nil, errors.Join(fmt.Errorf("fdmsg: a %T came with more fds than its %d bytes of control space", m, len(r.oob)), CloseAll(fds))
		}
		if err := json.Unmarshal(r.body[:n], &m); err != nil {
			return m, nil, errors.Join(fmt.Errorf("fdmsg: decode %T from %q: %w", m, r.body[:n], err), CloseAll(fds))
		}
		return m, fds, nil
	}
}

func rights(oob []byte) ([]int, error) {
	if len(oob) == 0 {
		return nil, nil
	}
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("parse control messages: %w", err)
	}
	var fds []int
	for i := range msgs {
		got, err := unix.ParseUnixRights(&msgs[i])
		if err != nil {
			return fds, fmt.Errorf("parse passed fds: %w", err)
		}
		fds = append(fds, got...)
	}
	return fds, nil
}

func CloseAll(fds []int) error {
	var errs []error
	for _, fd := range fds {
		errs = append(errs, unix.Close(fd))
	}
	return errors.Join(errs...)
}
