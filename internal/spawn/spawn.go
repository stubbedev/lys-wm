// Package spawn starts the processes lys-wm launches from a spawner: a child
// lys-wm starts before it changes its own limits or scheduling, which keeps
// what lys-wm started with and hands it to every process it forks.
package spawn

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/fdmsg"
)

const closeLimit = 5 * time.Second

type Command struct {
	Path        string
	Args        []string
	Env         []string
	Stdout      io.Writer
	Stderr      io.Writer
	ExtraFiles  []*os.File
	Setsid      bool
	DeathSignal syscall.Signal
}

type Spawner struct {
	sock    int
	devnull *os.File
	process *os.Process
	exited  chan error
	read    chan struct{}

	mu    sync.Mutex
	next  uint64
	procs map[uint64]*Process
	lost  error
}

type Process struct {
	Pid     int
	id      uint64
	running bool
	started chan error
	exit    chan error
	copies  []chan error

	mu    sync.Mutex
	pidfd int
}

type ExitError struct {
	Status syscall.WaitStatus
}

func (e *ExitError) Error() string {
	if e.Status.Signaled() {
		return "killed by signal " + unix.SignalName(e.Status.Signal())
	}
	return "exit status " + strconv.Itoa(e.Status.ExitStatus())
}

func exitError(status syscall.WaitStatus) error {
	if status.Exited() && status.ExitStatus() == 0 {
		return nil
	}
	return &ExitError{Status: status}
}

func Start() (*Spawner, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("spawn: socketpair for the spawner: %w", err)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("spawn: open %s: %w", os.DevNull, err), unix.Close(pair[0]), unix.Close(pair[1]))
	}
	s := &Spawner{sock: pair[0], devnull: devnull, exited: make(chan error, 1), read: make(chan struct{}), procs: map[uint64]*Process{}}
	started := make(chan error, 1)
	go s.run(os.NewFile(uintptr(pair[1]), "spawner"), started)
	if err := <-started; err != nil {
		return nil, errors.Join(err, unix.Close(s.sock), devnull.Close())
	}
	go s.receive()
	return s, nil
}

func (s *Spawner) run(child *os.File, started chan<- error) {
	runtime.LockOSThread()
	cmd := exec.Command(spawnerExec)
	cmd.Args = []string{spawnerName}
	cmd.Env = append(os.Environ(), spawnerEnv+"="+strconv.Itoa(socketFD))
	cmd.ExtraFiles = []*os.File{child}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	err := cmd.Start()
	_ = child.Close()
	if err != nil {
		started <- fmt.Errorf("spawn: start the spawner from %s: %w", spawnerExec, err)
		return
	}
	s.process = cmd.Process
	started <- nil
	if err := cmd.Wait(); err != nil {
		s.exited <- fmt.Errorf("spawn: the spawner, pid %d: %w", cmd.Process.Pid, err)
		return
	}
	s.exited <- nil
}

func (s *Spawner) receive() {
	defer close(s.read)
	r := fdmsg.NewReceiver(s.sock, maxEvent, 1)
	for {
		ev, fds, err := fdmsg.Recv[event](r)
		if errors.Is(err, os.ErrClosed) {
			s.lose(errors.New("spawn: the spawner is gone"))
			return
		}
		if err != nil {
			s.lose(fmt.Errorf("spawn: lost the spawner: %w", err))
			return
		}
		if err := s.deliver(ev, fds); err != nil {
			s.lose(fmt.Errorf("spawn: lost the spawner: %w", err))
			return
		}
	}
}

func (s *Spawner) deliver(ev event, fds []int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.procs[ev.ID]
	if p == nil || p.running != (ev.Kind == eventExited) {
		return errors.Join(fmt.Errorf("an event %q for request %d, which is not waiting for one", ev.Kind, ev.ID), fdmsg.CloseAll(fds))
	}
	switch ev.Kind {
	case eventStarted:
		if len(fds) != 1 {
			return errors.Join(fmt.Errorf("pid %d started with %d fds, want its pidfd", ev.PID, len(fds)), fdmsg.CloseAll(fds))
		}
		p.Pid, p.pidfd, p.running = ev.PID, fds[0], true
		p.started <- nil
		return nil
	case eventRefused:
		delete(s.procs, ev.ID)
		p.started <- errors.New(ev.Err)
		return fdmsg.CloseAll(fds)
	case eventExited:
		delete(s.procs, ev.ID)
		p.exit <- exitError(ev.Status)
		return fdmsg.CloseAll(fds)
	}
	return errors.Join(fmt.Errorf("an event of unknown kind %q", ev.Kind), fdmsg.CloseAll(fds))
}

func (s *Spawner) lose(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lost = err
	for id, p := range s.procs {
		delete(s.procs, id)
		if p.running {
			p.exit <- err
			continue
		}
		p.started <- err
	}
}

func (s *Spawner) Spawn(c Command) (*Process, error) {
	p := &Process{started: make(chan error, 1), exit: make(chan error, 1), pidfd: -1}
	stdout, out, err := s.output(c.Stdout)
	if err != nil {
		return nil, fmt.Errorf("spawn %s: %w", c.Path, err)
	}
	stderr, errOut := stdout, (*pipe)(nil)
	if !sameWriter(c.Stdout, c.Stderr) {
		if stderr, errOut, err = s.output(c.Stderr); err != nil {
			return nil, errors.Join(fmt.Errorf("spawn %s: %w", c.Path, err), out.abandon())
		}
	}
	fds := make([]int, 0, stdFiles+len(c.ExtraFiles))
	fds = append(fds, int(s.devnull.Fd()), stdout, stderr)
	for _, f := range c.ExtraFiles {
		fds = append(fds, int(f.Fd()))
	}
	sent := s.send(p, request{Path: c.Path, Args: c.Args, Env: c.Env, Setsid: c.Setsid, DeathSignal: c.DeathSignal}, fds)
	out.sent()
	errOut.sent()
	if sent == nil {
		sent = <-p.started
	}
	if sent != nil {
		return nil, errors.Join(fmt.Errorf("spawn %s: %w", c.Path, sent), out.abandon(), errOut.abandon())
	}
	p.copies = append(out.copy(), errOut.copy()...)
	return p, nil
}

func (s *Spawner) send(p *Process, req request, fds []int) error {
	s.mu.Lock()
	if s.lost != nil {
		s.mu.Unlock()
		return s.lost
	}
	s.next++
	p.id, req.ID = s.next, s.next
	s.procs[p.id] = p
	s.mu.Unlock()
	if err := fdmsg.Send(s.sock, req, fds...); err != nil {
		s.mu.Lock()
		delete(s.procs, p.id)
		s.mu.Unlock()
		return err
	}
	return nil
}

func sameWriter(a, b io.Writer) bool {
	t := reflect.TypeOf(a)
	return t != nil && t == reflect.TypeOf(b) && t.Comparable() && a == b
}

type pipe struct {
	w io.Writer
	r *os.File
	f *os.File
}

func (s *Spawner) output(w io.Writer) (int, *pipe, error) {
	switch f := w.(type) {
	case nil:
		return int(s.devnull.Fd()), nil, nil
	case *os.File:
		return int(f.Fd()), nil, nil
	}
	r, f, err := os.Pipe()
	if err != nil {
		return -1, nil, fmt.Errorf("pipe for an output: %w", err)
	}
	return int(f.Fd()), &pipe{w: w, r: r, f: f}, nil
}

func (p *pipe) sent() {
	if p != nil {
		_ = p.f.Close()
	}
}

func (p *pipe) abandon() error {
	if p == nil {
		return nil
	}
	_ = p.f.Close()
	return p.r.Close()
}

func (p *pipe) copy() []chan error {
	if p == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(p.w, p.r)
		done <- errors.Join(err, p.r.Close())
	}()
	return []chan error{done}
}

func (p *Process) Wait() error {
	err := <-p.exit
	p.mu.Lock()
	if p.pidfd >= 0 {
		err = errors.Join(err, unix.Close(p.pidfd))
		p.pidfd = -1
	}
	p.mu.Unlock()
	for _, c := range p.copies {
		if copyErr := <-c; copyErr != nil {
			err = errors.Join(err, fmt.Errorf("copy the output of pid %d: %w", p.Pid, copyErr))
		}
	}
	return err
}

func (p *Process) Signal(sig syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pidfd < 0 {
		return os.ErrProcessDone
	}
	exited, err := p.exitedLocked()
	if err != nil {
		return err
	}
	if exited {
		return os.ErrProcessDone
	}
	err = unix.PidfdSendSignal(p.pidfd, sig, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return os.ErrProcessDone
	}
	if err != nil {
		return fmt.Errorf("spawn: send %s to pid %d: %w", unix.SignalName(sig), p.Pid, err)
	}
	return nil
}

func (p *Process) exitedLocked() (bool, error) {
	fds := []unix.PollFd{{Fd: int32(p.pidfd), Events: unix.POLLIN}} //nolint:gosec // fds fit an int32
	for {
		n, err := unix.Poll(fds, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("spawn: poll the pidfd of pid %d: %w", p.Pid, err)
		}
		return n > 0, nil
	}
}

func (s *Spawner) Close() error {
	shut := unix.Shutdown(s.sock, unix.SHUT_RDWR)
	<-s.read
	var exited error
	select {
	case exited = <-s.exited:
	case <-time.After(closeLimit):
		exited = errors.Join(fmt.Errorf("spawn: the spawner, pid %d, did not exit within %v of its socket closing", s.process.Pid, closeLimit), s.process.Kill(), <-s.exited)
	}
	if shut != nil {
		shut = fmt.Errorf("spawn: shut the spawner's socket down: %w", shut)
	}
	return errors.Join(shut, exited, unix.Close(s.sock), s.devnull.Close())
}
