package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/session"
)

const (
	holderEnv = "LYS_WM_HOLDER_FD"
	holderFD  = 3
)

type holder struct {
	l       *loop.Loop
	lg      *session.Logind
	current *holderWorker
	workers map[int]*holderWorker
	devices map[session.Device]int
	status  int
}

type holderWorker struct {
	pid int
	fd  int
}

func (h *holder) spawn(binary string, args []string, extra []*os.File, stderr *os.File) (*holderWorker, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("socketpair for a worker: %w", err)
	}
	child := os.NewFile(uintptr(pair[1]), "holder")
	defer child.Close()
	cmd := exec.Command(binary, args...) //nolint:gosec // this process's own image, or the path the user named in lys-wmctl upgrade
	cmd.ExtraFiles = append([]*os.File{child}, extra...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, stderr
	cmd.Env = append(os.Environ(), holderEnv+"="+strconv.Itoa(holderFD))
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start %s: %w", binary, err), unix.Close(pair[0]))
	}
	w := &holderWorker{pid: cmd.Process.Pid, fd: pair[0]}
	h.workers[w.pid] = w
	go h.serve(w)
	go func() {
		err := cmd.Wait()
		_ = h.l.Post(func() error { return h.exited(w, err) })
	}()
	return w, nil
}

func (h *holder) exited(w *holderWorker, err error) error {
	delete(h.workers, w.pid)
	if w != h.current {
		return errors.Join(unix.Close(w.fd), h.notify(session.HolderMsg{Op: session.OpExited, PID: w.pid}))
	}
	h.status = 0
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			h.status = 128 + int(status.Signal())
		} else {
			h.status = exit.ExitCode()
		}
	}
	h.l.Stop()
	return nil
}

func (h *holder) serve(w *holderWorker) {
	for {
		m, fds, err := session.RecvHolder(w.fd)
		if err != nil {
			return
		}
		_ = h.l.Post(func() error {
			say(h.handle(w, m, fds))
			return nil
		})
	}
}

func say(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-wm: holder:", err)
	}
}

func (h *holder) handle(w *holderWorker, m session.HolderMsg, fds []int) error {
	if m.Op == session.OpPauseDone {
		d := session.Device{Major: m.Major, Minor: m.Minor}
		if _, held := h.devices[d]; w != h.current || h.lg == nil || !held {
			return nil
		}
		return h.lg.CompletePause(d)
	}
	reply := session.HolderMsg{Op: session.OpReply, ID: m.ID}
	var out []int
	var err error
	switch m.Op {
	case session.OpHello:
		reply.Protocol = session.HolderProtocol
	case session.OpOpen:
		if h.lg == nil {
			if h.lg, err = session.Open(h.l, session.SystemBus(os.Getenv), os.Getenv, h); err == nil {
				h.lg.DeferPauses()
			}
		}
		if err == nil {
			reply.Seat, reply.On = h.lg.Seat(), h.lg.Active()
		}
	case session.OpTake:
		var fd int
		var d session.Device
		if fd, d, err = h.take(m.Path); err == nil {
			out, reply.Major, reply.Minor = []int{fd}, d.Major, d.Minor
		}
	case session.OpRelease:
		err = h.release(w, session.Device{Major: m.Major, Minor: m.Minor})
	case session.OpInhibit:
		var f *os.File
		if f, err = h.lg.InhibitLidSwitch(); err == nil {
			defer f.Close()
			out = []int{int(f.Fd())}
		}
	case session.OpLid:
		reply.On, err = h.lg.LidClosed()
	case session.OpSuspend:
		err = h.lg.Suspend()
	case session.OpSwitchTo:
		err = h.lg.SwitchTo(m.VT)
	case session.OpSpawn:
		if len(fds) != 2 {
			err = fmt.Errorf("spawn with %d fds", len(fds))
			break
		}
		handover, stderr := os.NewFile(uintptr(fds[0]), "handover"), os.NewFile(uintptr(fds[1]), "stderr")
		defer handover.Close()
		defer stderr.Close()
		var child *holderWorker
		if child, err = h.spawn(m.Binary, append([]string{"--handover-fd", strconv.Itoa(holderFD + 1)}, m.Args...), []*os.File{handover}, stderr); err == nil {
			reply.PID = child.pid
		}
	case session.OpCommit:
		next, ok := h.workers[m.PID]
		if !ok || next != w {
			err = fmt.Errorf("process %d is not a worker of this session", m.PID)
			break
		}
		h.current = next
	default:
		err = fmt.Errorf("unknown request %q", m.Op)
	}
	if err != nil {
		reply.Err = err.Error()
	}
	return session.SendHolder(w.fd, reply, out...)
}

func (h *holder) take(path string) (int, session.Device, error) {
	d, err := session.DeviceOf(path)
	if err != nil {
		return -1, session.Device{}, err
	}
	fd, cached := h.devices[d]
	if !cached {
		if fd, _, err = h.lg.TakeDevice(path); err != nil {
			return -1, session.Device{}, err
		}
		h.devices[d] = fd
	}
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, session.Device{}, fmt.Errorf("duplicate %s: %w", path, err)
	}
	return dup, d, nil
}

func (h *holder) release(w *holderWorker, d session.Device) error {
	if w != h.current {
		return nil
	}
	fd, ok := h.devices[d]
	if !ok {
		return nil
	}
	delete(h.devices, d)
	return errors.Join(unix.Close(fd), h.lg.ReleaseDevice(d))
}

func (h *holder) notify(m session.HolderMsg, fds ...int) error {
	if h.current == nil {
		return nil
	}
	return session.SendHolder(h.current.fd, m, fds...)
}

func (h *holder) DevicePaused(d session.Device) error {
	say(h.notify(session.HolderMsg{Op: session.OpPaused, Major: d.Major, Minor: d.Minor}))
	return nil
}

func (h *holder) DeviceResumed(d session.Device, fd int) error {
	old, cached := h.devices[d]
	if cached {
		say(unix.Close(old))
		h.devices[d] = fd
	}
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		say(fmt.Errorf("duplicate device %d:%d: %w", d.Major, d.Minor, err))
		return nil
	}
	say(errors.Join(h.notify(session.HolderMsg{Op: session.OpResumed, Major: d.Major, Minor: d.Minor}, dup), unix.Close(dup)))
	if !cached {
		say(unix.Close(fd))
	}
	return nil
}

func (h *holder) ActiveChanged(active bool) error {
	say(h.notify(session.HolderMsg{Op: session.OpActive, On: active}))
	return nil
}

func (h *holder) PrepareForSleep(sleeping bool) error {
	say(h.notify(session.HolderMsg{Op: session.OpSleep, On: sleeping}))
	return nil
}

func runHolder(args []string) (int, error) {
	l, err := loop.New()
	if err != nil {
		return 1, err
	}
	h := &holder{l: l, workers: map[int]*holderWorker{}, devices: map[session.Device]int{}}
	if _, err := l.AddSignals(func(sig os.Signal) error {
		if s, ok := sig.(syscall.Signal); ok && h.current != nil {
			return unix.Kill(h.current.pid, s)
		}
		return nil
	}, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP); err != nil {
		return 1, err
	}
	self, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("find this binary: %w", err)
	}
	first, err := h.spawn(self, args, nil, os.Stderr)
	if err != nil {
		return 1, err
	}
	h.current = first
	err = l.Run()
	if h.lg != nil {
		err = errors.Join(err, h.lg.Close())
	}
	return h.status, err
}
