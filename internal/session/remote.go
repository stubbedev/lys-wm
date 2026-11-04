package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
)

const (
	HolderProtocol = 1
	maxHolderMsg   = 64 << 10
	holderTimeout  = 15 * time.Second
)

type HolderOp string

const (
	OpHello     HolderOp = "hello"
	OpOpen      HolderOp = "open"
	OpTake      HolderOp = "take"
	OpRelease   HolderOp = "release"
	OpInhibit   HolderOp = "inhibit"
	OpSuspend   HolderOp = "suspend"
	OpSwitchTo  HolderOp = "switch"
	OpSpawn     HolderOp = "spawn"
	OpCommit    HolderOp = "commit"
	OpPauseDone HolderOp = "pausedone"
	OpPaused    HolderOp = "paused"
	OpResumed   HolderOp = "resumed"
	OpActive    HolderOp = "active"
	OpSleep     HolderOp = "sleep"
	OpReply     HolderOp = "reply"
	OpExited    HolderOp = "exited"
)

type HolderMsg struct {
	Op       HolderOp `json:"op"`
	ID       uint64   `json:"id,omitempty"`
	Path     string   `json:"path,omitempty"`
	Binary   string   `json:"binary,omitempty"`
	Args     []string `json:"args,omitempty"`
	Major    uint32   `json:"major,omitempty"`
	Minor    uint32   `json:"minor,omitempty"`
	VT       uint32   `json:"vt,omitempty"`
	Seat     string   `json:"seat,omitempty"`
	On       bool     `json:"on,omitempty"`
	PID      int      `json:"pid,omitempty"`
	Protocol int      `json:"protocol,omitempty"`
	Err      string   `json:"err,omitempty"`
}

func SendHolder(fd int, m HolderMsg, fds ...int) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("session: encode a message to the holder: %w", err)
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
			return fmt.Errorf("session: send %s: %w", m.Op, err)
		}
		return nil
	}
}

func RecvHolder(fd int) (HolderMsg, []int, error) {
	body := make([]byte, maxHolderMsg)
	oob := make([]byte, unix.CmsgSpace(4*4))
	for {
		n, oobn, _, _, err := unix.Recvmsg(fd, body, oob, unix.MSG_CMSG_CLOEXEC)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return HolderMsg{}, nil, fmt.Errorf("session: read from the holder: %w", err)
		}
		if n == 0 {
			return HolderMsg{}, nil, os.ErrClosed
		}
		var fds []int
		if oobn > 0 {
			msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
			if err != nil {
				return HolderMsg{}, nil, fmt.Errorf("session: parse the holder's fds: %w", err)
			}
			for i := range msgs {
				got, err := unix.ParseUnixRights(&msgs[i])
				if err != nil {
					return HolderMsg{}, nil, fmt.Errorf("session: parse the holder's fds: %w", err)
				}
				fds = append(fds, got...)
			}
		}
		var m HolderMsg
		if err := json.Unmarshal(body[:n], &m); err != nil {
			return HolderMsg{}, nil, errors.Join(fmt.Errorf("session: decode a message from the holder: %w", err), closeAll(fds))
		}
		return m, fds, nil
	}
}

func closeAll(fds []int) error {
	var errs []error
	for _, fd := range fds {
		errs = append(errs, unix.Close(fd))
	}
	return errors.Join(errs...)
}

type holderReply struct {
	m   HolderMsg
	fds []int
}

type Holder struct {
	fd       int
	loop     *loop.Loop
	next     atomic.Uint64
	mu       sync.Mutex
	pending  map[uint64]chan holderReply
	listener Listener
	seat     string
	active   atomic.Bool
	exits    map[int]func()
}

func DialHolder(fd int, l *loop.Loop) (*Holder, error) {
	h := &Holder{fd: fd, loop: l, pending: map[uint64]chan holderReply{}}
	go h.read()
	reply, _, err := h.call(HolderMsg{Op: OpHello, Protocol: HolderProtocol})
	if err != nil {
		return nil, err
	}
	if reply.Protocol != HolderProtocol {
		return nil, fmt.Errorf("session: the holder speaks protocol %d, this lys-wm %d; log out to move between them", reply.Protocol, HolderProtocol)
	}
	return h, nil
}

func (h *Holder) read() {
	for {
		m, fds, err := RecvHolder(h.fd)
		if err != nil {
			_ = h.loop.Post(func() error {
				h.loop.Stop()
				return fmt.Errorf("session: lost the holder: %w", err)
			})
			return
		}
		if m.Op == OpReply {
			h.mu.Lock()
			ch := h.pending[m.ID]
			delete(h.pending, m.ID)
			h.mu.Unlock()
			if ch != nil {
				ch <- holderReply{m, fds}
			}
			continue
		}
		_ = h.loop.Post(func() error { return h.event(m, fds) })
	}
}

func (h *Holder) OnExit(pid int, f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exits == nil {
		h.exits = map[int]func(){}
	}
	h.exits[pid] = f
}

func (h *Holder) event(m HolderMsg, fds []int) error {
	if m.Op == OpExited {
		h.mu.Lock()
		f := h.exits[m.PID]
		delete(h.exits, m.PID)
		h.mu.Unlock()
		if f != nil {
			f()
		}
		return nil
	}
	if h.listener == nil {
		return closeAll(fds)
	}
	d := Device{Major: m.Major, Minor: m.Minor}
	switch m.Op {
	case OpPaused:
		err := h.listener.DevicePaused(d)
		return errors.Join(err, SendHolder(h.fd, HolderMsg{Op: OpPauseDone, Major: m.Major, Minor: m.Minor}))
	case OpResumed:
		if len(fds) != 1 {
			return fmt.Errorf("session: the holder resumed %d:%d with %d fds", m.Major, m.Minor, len(fds))
		}
		return h.listener.DeviceResumed(d, fds[0])
	case OpActive:
		h.active.Store(m.On)
		return h.listener.ActiveChanged(m.On)
	case OpSleep:
		return h.listener.PrepareForSleep(m.On)
	}
	return closeAll(fds)
}

func (h *Holder) call(m HolderMsg, fds ...int) (HolderMsg, []int, error) {
	m.ID = h.next.Add(1)
	ch := make(chan holderReply, 1)
	h.mu.Lock()
	h.pending[m.ID] = ch
	h.mu.Unlock()
	if err := SendHolder(h.fd, m, fds...); err != nil {
		return HolderMsg{}, nil, err
	}
	select {
	case r := <-ch:
		if r.m.Err != "" {
			return r.m, r.fds, fmt.Errorf("session: the holder: %s", r.m.Err)
		}
		return r.m, r.fds, nil
	case <-time.After(holderTimeout):
		return HolderMsg{}, nil, fmt.Errorf("session: the holder did not answer %s in %v", m.Op, holderTimeout)
	}
}

func (h *Holder) Open(listener Listener) error {
	h.listener = listener
	reply, _, err := h.call(HolderMsg{Op: OpOpen})
	if err != nil {
		return err
	}
	h.seat = reply.Seat
	h.active.Store(reply.On)
	return nil
}

func (h *Holder) Seat() string { return h.seat }

func (h *Holder) Active() bool { return h.active.Load() }

func (h *Holder) TakeDevice(path string) (int, Device, error) {
	reply, fds, err := h.call(HolderMsg{Op: OpTake, Path: path})
	if err != nil {
		return -1, Device{}, err
	}
	if len(fds) != 1 {
		return -1, Device{}, fmt.Errorf("session: the holder took %s with %d fds", path, len(fds))
	}
	return fds[0], Device{Major: reply.Major, Minor: reply.Minor}, nil
}

func (h *Holder) ReleaseDevice(d Device) error {
	_, _, err := h.call(HolderMsg{Op: OpRelease, Major: d.Major, Minor: d.Minor})
	return err
}

func (h *Holder) InhibitLidSwitch() (*os.File, error) {
	_, fds, err := h.call(HolderMsg{Op: OpInhibit})
	if err != nil {
		return nil, err
	}
	if len(fds) != 1 {
		return nil, fmt.Errorf("session: the holder took the inhibitor with %d fds", len(fds))
	}
	return os.NewFile(uintptr(fds[0]), "logind-lid-inhibitor"), nil
}

func (h *Holder) Suspend() error {
	_, _, err := h.call(HolderMsg{Op: OpSuspend})
	return err
}

func (h *Holder) SwitchTo(vt uint32) error {
	_, _, err := h.call(HolderMsg{Op: OpSwitchTo, VT: vt})
	return err
}

func (h *Holder) Close() error {
	return nil
}

func (h *Holder) Spawn(binary string, args []string, handover, stderr int) (int, error) {
	reply, _, err := h.call(HolderMsg{Op: OpSpawn, Binary: binary, Args: args}, handover, stderr)
	return reply.PID, err
}

func (h *Holder) Commit() error {
	_, _, err := h.call(HolderMsg{Op: OpCommit, PID: os.Getpid()})
	return err
}
