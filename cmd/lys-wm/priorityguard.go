package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	sigevThreadID   = 4
	stackDumpBytes  = 1 << 20
	switchesField   = "\nvoluntary_ctxt_switches:\t"
	statusReadBytes = 4096
	settleNap       = time.Microsecond
)

type guardState uint8

const (
	guardOff guardState = iota
	guardWatching
	guardStopped
)

type demotion struct {
	ran, wait time.Duration
	doing     string
}

type guardOps struct {
	switches func() (uint64, error)
	cpu      func() (time.Duration, error)
	arm      func(on bool) error
	demote   func() error
	doing    func() string
	raise    func() error
	settle   func() (uint64, error)
	hand     handFunc
	pause    func(time.Duration) bool
	say      func(string)
	stack    func() string
}

type rtGuard struct {
	name   thread
	budget time.Duration
	ops    guardOps
	wait   time.Duration

	mu    sync.Mutex
	state guardState
	quiet uint64
	since time.Duration
}

func (g *rtGuard) watchLocked() error {
	n, err := g.ops.settle()
	if err != nil {
		return fmt.Errorf("let %s block once at SCHED_RR: %w", g.name, err)
	}
	at, err := g.ops.cpu()
	if err != nil {
		return err
	}
	if err := g.ops.arm(true); err != nil {
		return err
	}
	g.state, g.quiet, g.since = guardWatching, n, at
	return nil
}

func (g *rtGuard) start() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.watchLocked()
}

func (g *rtGuard) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = guardStopped
}

func (g *rtGuard) check() (d *demotion, stop bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.state {
	case guardStopped:
		return nil, true, nil
	case guardOff:
		return nil, false, nil
	}
	n, err := g.ops.switches()
	if err != nil {
		return nil, true, g.blind(err)
	}
	at, err := g.ops.cpu()
	if err != nil {
		return nil, true, g.blind(err)
	}
	if n != g.quiet {
		g.quiet, g.since = n, at
		return nil, false, nil
	}
	if at-g.since < g.budget/2 {
		return nil, false, nil
	}
	doing := g.ops.doing()
	if err := g.ops.demote(); err != nil {
		return nil, true, fmt.Errorf("%s ran %v at SCHED_RR without blocking and could not leave it: %w", g.name, at-g.since, err)
	}
	g.state = guardOff
	return &demotion{ran: at - g.since, wait: g.wait, doing: doing}, false, g.ops.arm(false)
}

func (g *rtGuard) blind(err error) error {
	g.state = guardOff
	return errors.Join(fmt.Errorf("the guard of %s cannot see it run, so it leaves SCHED_RR for good: %w", g.name, err), g.ops.demote())
}

func (g *rtGuard) step() bool {
	d, stop, err := g.check()
	if err != nil {
		g.ops.say(err.Error())
	}
	if stop || err != nil {
		return false
	}
	if d == nil {
		return true
	}
	g.ops.say(fmt.Sprintf("%s ran %v at SCHED_RR without blocking, past the %v its guard allows under RLIMIT_RTTIME, so it runs at normal priority for %v; its thread was %s, its goroutine %s", g.name, d.ran, g.budget, d.wait, d.doing, g.ops.stack()))
	if !g.ops.pause(d.wait) {
		return false
	}
	g.wait = min(2*g.wait, rtRegainMostWait)
	g.ops.hand("regaining SCHED_RR for "+string(g.name), g.regain)
	return true
}

func (g *rtGuard) yield() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != guardWatching {
		return
	}
	if err := g.ops.demote(); err != nil {
		g.ops.say(fmt.Sprintf("%s compiles at SCHED_RR, as it could not leave it: %v", g.name, err))
		return
	}
	g.state = guardOff
	if err := g.ops.arm(false); err != nil {
		g.ops.say(err.Error())
	}
	g.ops.hand("regaining SCHED_RR for "+string(g.name)+" after a compile", func() error { return g.retake(false) })
}

func (g *rtGuard) regain() error {
	return g.retake(true)
}

func (g *rtGuard) retake(say bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != guardOff {
		return nil
	}
	if err := g.ops.raise(); err != nil {
		g.ops.say(fmt.Sprintf("%s keeps normal priority, as SCHED_RR was refused again: %v", g.name, err))
		return nil
	}
	if err := g.watchLocked(); err != nil {
		g.ops.say(errors.Join(fmt.Errorf("%s keeps normal priority, as its guard could not watch it again: %w", g.name, err), g.ops.demote()).Error())
		return nil
	}
	if say {
		g.ops.say(fmt.Sprintf("%s runs at SCHED_RR again", g.name))
	}
	return nil
}

func startRTGuard(name thread, tid int, budget time.Duration, raise func() error, guardRaise func(guard int) error, hand handFunc) (stop func(), yield func(), err error) {
	header := goroutineHeader()
	g := &rtGuard{name: name, budget: budget, wait: rtRegainFirstWait}
	ready := make(chan error, 1)
	done := make(chan struct{})
	quit := make(chan struct{})
	var guard int
	go func() {
		defer close(done)
		runtime.LockOSThread()
		guard = unix.Gettid()
		w, err := newCPUWatch(threadCPUClock(tid), fmt.Sprintf("/proc/self/task/%d/status", tid), budget)
		if err != nil {
			ready <- err
			return
		}
		if err := guardRaise(guard); err != nil {
			ready <- errors.Join(fmt.Errorf("raise the guard of %s to SCHED_RR %d: %w", name, guardRTPriority, err), w.Close())
			return
		}
		g.ops = guardOps{
			switches: w.switches,
			cpu:      func() (time.Duration, error) { return cpuTime(threadCPUClock(tid)) },
			arm:      w.arm,
			demote:   func() error { return setPolicyKeepingNice(tid, unix.SCHED_NORMAL) },
			doing:    func() string { return threadDoing(tid) },
			raise:    raise,
			settle:   settle,
			hand:     hand,
			pause: func(d time.Duration) bool {
				t := time.NewTimer(d)
				defer t.Stop()
				select {
				case <-t.C:
					return true
				case <-quit:
					return false
				}
			},
			say:   func(line string) { fmt.Fprintf(os.Stderr, "lys-wm: %s\n", line) },
			stack: func() string { return string(goroutineStack(header)) },
		}
		ready <- nil
		for {
			if err := w.wait(); err != nil {
				g.ops.say(errors.Join(err, g.blindLocked()).Error())
				break
			}
			if !g.step() {
				break
			}
		}
		if err := w.Close(); err != nil {
			g.ops.say(err.Error())
		}
	}()
	if err := <-ready; err != nil {
		<-done
		return nil, nil, err
	}
	stop = func() {
		g.stop()
		close(quit)
		select {
		case <-done:
			return
		default:
		}
		if err := unix.Tgkill(unix.Getpid(), guard, unix.SIGXCPU); err != nil && !errors.Is(err, unix.ESRCH) {
			fmt.Fprintf(os.Stderr, "lys-wm: wake the guard of %s to stop it: %v\n", name, err)
		}
		<-done
	}
	if err := g.start(); err != nil {
		stop()
		return nil, nil, err
	}
	return stop, g.yield, nil
}

func (g *rtGuard) blindLocked() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != guardWatching {
		return nil
	}
	return g.blind(errors.New("its CPU timer stopped"))
}

type sigevent struct {
	value  uint64
	signo  int32
	notify int32
	tid    int32
	_      [44]byte
}

func threadCPUClock(tid int) int32 {
	return int32(^tid)<<3 | 6 //nolint:gosec // a thread ID fits the clock ID's 29 bits
}

func cpuTime(clock int32) (time.Duration, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(clock, &ts); err != nil {
		return 0, fmt.Errorf("read CPU clock %d: %w", clock, err)
	}
	return time.Duration(ts.Nano()), nil
}

func sigxcpuSet() unix.Sigset_t {
	var set unix.Sigset_t
	set.Val[(unix.SIGXCPU-1)/64] |= 1 << ((unix.SIGXCPU - 1) % 64)
	return set
}

type cpuWatch struct {
	timer  int32
	status int
	path   string
	buf    []byte
	set    unix.Sigset_t
	every  unix.Timespec
}

func newCPUWatch(clock int32, status string, every time.Duration) (*cpuWatch, error) {
	w := &cpuWatch{set: sigxcpuSet(), path: status, buf: make([]byte, statusReadBytes), every: unix.NsecToTimespec(every.Nanoseconds())}
	if err := unix.PthreadSigmask(unix.SIG_BLOCK, &w.set, nil); err != nil {
		return nil, fmt.Errorf("block SIGXCPU on the guard thread: %w", err)
	}
	fd, err := unix.Open(status, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", status, err)
	}
	w.status = fd
	self := int32(unix.Gettid()) //nolint:gosec // a thread ID fits an int32
	ev := sigevent{signo: int32(unix.SIGXCPU), notify: sigevThreadID, tid: self}
	if _, _, errno := unix.Syscall(unix.SYS_TIMER_CREATE, uintptr(clock), uintptr(unsafe.Pointer(&ev)), uintptr(unsafe.Pointer(&w.timer))); errno != 0 { //nolint:gosec // the kernel reads ev and writes the timer ID during the call
		return nil, errors.Join(fmt.Errorf("timer_create on CPU clock %d: %w", clock, errno), unix.Close(fd))
	}
	return w, nil
}

func (w *cpuWatch) Close() error {
	var errs []error
	if _, _, errno := unix.Syscall(unix.SYS_TIMER_DELETE, uintptr(w.timer), 0, 0); errno != 0 { //nolint:gosec // the kernel hands out non-negative timer IDs
		errs = append(errs, fmt.Errorf("timer_delete %d: %w", w.timer, errno))
	}
	if err := unix.Close(w.status); err != nil {
		errs = append(errs, fmt.Errorf("close %s: %w", w.path, err))
	}
	return errors.Join(errs...)
}

func (w *cpuWatch) arm(on bool) error {
	var spec unix.ItimerSpec
	if on {
		spec = unix.ItimerSpec{Interval: w.every, Value: w.every}
	}
	if _, _, errno := unix.Syscall6(unix.SYS_TIMER_SETTIME, uintptr(w.timer), 0, uintptr(unsafe.Pointer(&spec)), 0, 0, 0); errno != 0 { //nolint:gosec // the kernel reads spec during the call
		return fmt.Errorf("timer_settime %d armed %v: %w", w.timer, on, errno)
	}
	return nil
}

func (w *cpuWatch) wait() error {
	for {
		_, _, errno := unix.Syscall6(unix.SYS_RT_SIGTIMEDWAIT, uintptr(unsafe.Pointer(&w.set)), 0, 0, unsafe.Sizeof(w.set.Val[0]), 0, 0) //nolint:gosec // the kernel reads the set during the call
		switch errno {
		case 0:
			return nil
		case unix.EINTR:
			continue
		default:
			return fmt.Errorf("wait for SIGXCPU from timer %d: %w", w.timer, errno)
		}
	}
}

func (w *cpuWatch) switches() (uint64, error) {
	n, err := unix.Pread(w.status, w.buf, 0)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", w.path, err)
	}
	return voluntarySwitches(w.buf[:n])
}

func voluntarySwitches(status []byte) (uint64, error) {
	_, rest, ok := bytes.Cut(status, []byte(switchesField))
	if !ok {
		return 0, fmt.Errorf("no voluntary_ctxt_switches in %q", status)
	}
	value, _, _ := bytes.Cut(rest, []byte("\n"))
	n, err := strconv.ParseUint(string(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("voluntary_ctxt_switches %q: %w", value, err)
	}
	return n, nil
}

func settle() (uint64, error) {
	var before, after unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_THREAD, &before); err != nil {
		return 0, fmt.Errorf("read this thread's context switches: %w", err)
	}
	nap := unix.NsecToTimespec(settleNap.Nanoseconds())
	for {
		if err := unix.Nanosleep(&nap, nil); err != nil && !errors.Is(err, unix.EINTR) {
			return 0, fmt.Errorf("sleep %v: %w", settleNap, err)
		}
		if err := unix.Getrusage(unix.RUSAGE_THREAD, &after); err != nil {
			return 0, fmt.Errorf("read this thread's context switches: %w", err)
		}
		if after.Nvcsw != before.Nvcsw {
			return uint64(after.Nvcsw), nil //nolint:gosec // a switch count is never negative
		}
	}
}

func threadDoing(tid int) string {
	line, err := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/syscall", tid))
	if err != nil {
		return fmt.Sprintf("unknown (%v)", err)
	}
	call, _, _ := bytes.Cut(bytes.TrimSpace(line), []byte(" "))
	if string(call) == "running" {
		return "running outside the kernel"
	}
	return "in system call " + string(call)
}

func goroutineHeader() []byte {
	buf := make([]byte, 64)
	id, _, _ := bytes.Cut(buf[:runtime.Stack(buf, false)], []byte(" ["))
	return append(append([]byte("\n"), id...), " ["...)
}

func goroutineStack(header []byte) []byte {
	buf := make([]byte, stackDumpBytes)
	all := append([]byte("\n"), buf[:runtime.Stack(buf, true)]...)
	_, rest, ok := bytes.Cut(all, header)
	if !ok {
		return fmt.Appendf(nil, "%s is not in the first %d bytes of goroutine stacks", bytes.TrimSpace(header), stackDumpBytes)
	}
	block, _, _ := bytes.Cut(rest, []byte("\n\n"))
	return slices.Concat(header[1:], block)
}
