package compositor

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/spawn"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) Dispatch(a wm.Action) error {
	if c.parked {
		return errors.New("compositor: the session is moving to another binary")
	}
	return c.dispatch(a, nil, wm.FocusOther)
}

func (c *Compositor) dispatch(a wm.Action, trigger binds.Trigger, by wm.FocusOrigin) error {
	outer := c.swapOrigin(by)
	defer c.swapOrigin(outer)
	if err := c.endWalkUnless(a); err != nil {
		return err
	}
	before := c.markFocus()
	s, effects, err := wm.Dispatch(c.wm, a, c.settings)
	if err != nil {
		return err
	}
	if err := c.update(s, nil); err != nil {
		return err
	}
	if c.ipcEvents != nil {
		c.ipcEvents.Dispatched(a)
	}
	for _, e := range effects {
		if err := c.perform(e, trigger); err != nil {
			return fmt.Errorf("compositor: %s: %w", a, err)
		}
	}
	if by != wm.FocusKeyboard {
		return nil
	}
	return c.warpAfterKeyboard(before)
}

func (c *Compositor) perform(e wm.Effect, trigger binds.Trigger) error {
	switch e := e.(type) {
	case wm.CloseWindowAction:
		w := c.windowByID(e.ID)
		if w == nil {
			return fmt.Errorf("close unknown window %d", e.ID)
		}
		return w.top.close()
	case wm.MinimizeAction:
		return c.withWindow(e.ID, c.minimize)
	case wm.RestoreAction:
		return c.withWindow(e.ID, c.restore)
	case wm.FocusLastAction:
		return c.focusLast(c.focusBy)
	case wm.FocusUrgentAction:
		return c.focusUrgent(c.focusBy)
	case wm.CycleAction:
		return c.cycle(e, c.focusBy)
	case wm.SwitcherAction:
		return c.switcherAction(e)
	case wm.OverviewAction:
		return c.overviewAction(e.Mode, e.Scope)
	case wm.ExecAction:
		return c.spawn(e.Command, c.shownTarget())
	case wm.SetPropAction:
		return c.withWindow(e.ID, func(w *window) error { return c.setProp(w, e.Key, e.Value) })
	case wm.ShowDesktopAction:
		return c.showDesktop(e)
	case wm.ScratchpadExecAction:
		return c.spawn(e.Command, launchTarget{scratchpad: e.Name})
	case wm.GlobalAction:
		return c.pressShortcut(e, trigger)
	case wm.SubmapAction:
		c.submap = e.Name
		c.stateChanged()
		return nil
	case wm.DragAction:
		return c.beginDrag(e.Mode, trigger)
	case wm.DPMSAction:
		return c.dpms(e)
	case wm.ZoomAction:
		return c.applyZoom(e)
	case wm.DebugOverlayAction:
		return c.debugOverlay(e)
	case wm.DebugRecordAction:
		return c.debugRecord(e)
	case wm.DebugProtocolAction:
		c.display.RecordProtocol(e.Mode.From(c.display.RecordingProtocol()))
		return nil
	case wm.DebugProtocolDumpAction:
		return c.dumpProtocol(e.Path)
	case wm.DebugSpinAction:
		return spinWithoutBlocking(e.For)
	case wm.CastTargetAction:
		return c.castTargetAction(e)
	case wm.ReloadAction:
		if c.hooks.Reload == nil {
			return errors.New("no config file to reload")
		}
		if apply := c.loadedConfig; apply != nil {
			c.loadedConfig = nil
			return apply()
		}
		c.ReloadConfig()
		return nil
	case wm.DismissConfigNoticeAction:
		c.dismissConfigNotice()
		return nil
	case wm.NextLayoutAction:
		return c.seat.NextLayout()
	case wm.StickyKeysAction:
		c.seatConfig.StickyKeys = e.On
		c.seat.SetStickyKeys(e.On)
		return nil
	case wm.SlowKeysAction:
		return c.setSlowKeys(e.Delay)
	case wm.ExitAction:
		if c.hooks.Exit == nil {
			return errors.New("nothing to exit to: this compositor was started without a way to stop")
		}
		return c.hooks.Exit()
	}
	return fmt.Errorf("no handler for effect %T", e)
}

func (c *Compositor) withWindow(id wm.WindowID, f func(*window) error) error {
	w := c.windowByID(id)
	if w == nil {
		return fmt.Errorf("unknown window %d", id)
	}
	return f(w)
}

func (c *Compositor) spawn(command string, target launchTarget) error {
	token, err := c.mintLaunchToken(true, target)
	if err != nil {
		return fmt.Errorf("compositor: exec %q: %w", command, err)
	}
	c.spawnWith(command, append(os.Environ(), "WAYLAND_DISPLAY="+c.display.Path(), "XDG_ACTIVATION_TOKEN="+token, "DESKTOP_STARTUP_ID="+token), func(pid int) { c.spawned(token, pid) })
	return nil
}

func (c *Compositor) spawnWith(command string, env []string, started func(pid int)) {
	output := c.execOutput
	c.execs.Run(func() {
		p, err := c.startCommand(command, env, output)
		if err == nil {
			go c.reap(command, p)
			c.loop.Hand("a started exec", func() error { started(p.Pid); return nil })
			return
		}
		c.loop.Hand("a failed exec", func() error { c.report(fmt.Errorf("compositor: %w", err)); return nil })
	})
}

const spinLeastProcs = 2

func spinWithoutBlocking(d time.Duration) error {
	if procs := runtime.GOMAXPROCS(0); procs < spinLeastProcs {
		return fmt.Errorf("compositor: debug spin %v: GOMAXPROCS is %d, and a spin holds its processor until it ends, so nothing else, a priority guard included, could run", d, procs)
	}
	return withoutGC(func() error {
		start, now, errno := spinThreadCPU(d.Nanoseconds())
		if errno == 0 {
			errno = spinPreemptibly(start, now, d.Nanoseconds())
		}
		if errno != 0 {
			return fmt.Errorf("compositor: debug spin %v: read this thread's CPU clock or scheduling policy: %w", d, errno)
		}
		return nil
	})
}

func withoutGC(f func() error) error {
	limit := debug.SetMemoryLimit(math.MaxInt64)
	percent := debug.SetGCPercent(-1)
	defer func() {
		debug.SetGCPercent(percent)
		debug.SetMemoryLimit(limit)
	}()
	return f()
}

//go:nosplit
func spinThreadCPU(ns int64) (start, now int64, errno syscall.Errno) {
	was, errno := blockSignals()
	if errno != 0 {
		return 0, 0, errno
	}
	start, now, errno = spinUnsignalled(ns)
	if restored := setSignalMask(was); errno == 0 {
		errno = restored
	}
	return start, now, errno
}

//go:nosplit
func spinUnsignalled(ns int64) (start, now int64, errno syscall.Errno) {
	policy, errno := threadPolicy()
	if errno != 0 {
		return 0, 0, errno
	}
	if start, errno = threadCPUNanos(); errno != 0 {
		return 0, 0, errno
	}
	for still := policy; still == policy; {
		if now, errno = threadCPUNanos(); errno != 0 || now-start >= ns {
			return start, now, errno
		}
		if still, errno = threadPolicy(); errno != 0 {
			return start, now, errno
		}
	}
	return start, now, 0
}

func spinPreemptibly(start, now, ns int64) syscall.Errno {
	var errno syscall.Errno
	for now-start < ns {
		if now, errno = threadCPUNanos(); errno != 0 {
			return errno
		}
	}
	return 0
}

const signalSetBytes = 8

//go:nosplit
func blockSignals() (was uint64, errno syscall.Errno) {
	all := ^uint64(0)
	_, _, errno = syscall.RawSyscall6(unix.SYS_RT_SIGPROCMASK, unix.SIG_SETMASK, uintptr(unsafe.Pointer(&all)), uintptr(unsafe.Pointer(&was)), signalSetBytes, 0, 0) //nolint:gosec // the kernel reads all and writes was during the call
	return was, errno
}

//go:nosplit
func setSignalMask(mask uint64) syscall.Errno {
	_, _, errno := syscall.RawSyscall6(unix.SYS_RT_SIGPROCMASK, unix.SIG_SETMASK, uintptr(unsafe.Pointer(&mask)), 0, signalSetBytes, 0, 0) //nolint:gosec // the kernel reads mask during the call
	return errno
}

//go:nosplit
func threadPolicy() (uintptr, syscall.Errno) {
	policy, _, errno := syscall.RawSyscall(unix.SYS_SCHED_GETSCHEDULER, 0, 0, 0)
	return policy, errno
}

//go:nosplit
func threadCPUNanos() (int64, syscall.Errno) {
	var ts unix.Timespec
	_, _, errno := syscall.RawSyscall(unix.SYS_CLOCK_GETTIME, unix.CLOCK_THREAD_CPUTIME_ID, uintptr(unsafe.Pointer(&ts)), 0) //nolint:gosec // the kernel writes ts during the call
	return ts.Sec*int64(time.Second) + ts.Nsec, errno
}

func (c *Compositor) WaitForExecs() {
	c.execs.Wait()
}

func (c *Compositor) startCommand(command string, env []string, output ExecOutput) (*spawn.Process, error) {
	sh, err := c.lookPath("sh")
	if err != nil {
		return nil, fmt.Errorf("exec %q: %w", command, err)
	}
	if c.hooks.Spawner == nil {
		return nil, fmt.Errorf("exec %q: this compositor was started without a spawner", command)
	}
	p, err := c.hooks.Spawner.Spawn(spawn.Command{Path: sh, Args: []string{sh, "-c", command}, Env: env, Stdout: output.Stdout, Stderr: output.Stderr, Setsid: true})
	if err != nil {
		return nil, fmt.Errorf("exec %q: %w", command, err)
	}
	return p, nil
}

func (c *Compositor) reap(command string, p *spawn.Process) {
	exited := exitError(command, p.Wait())
	if exited == nil {
		return
	}
	if err := c.loop.Post(func() error {
		c.report(exited)
		return nil
	}); err != nil && !errors.Is(err, loop.ErrClosed) {
		panic(fmt.Sprintf("compositor: report how %q exited: %v", command, err))
	}
}

func exitError(command string, err error) error {
	exit, ok := errors.AsType[*spawn.ExitError](err)
	if !ok {
		if err != nil {
			return fmt.Errorf("compositor: wait for %q: %w", command, err)
		}
		return nil
	}
	if exit.Status.Signaled() {
		return fmt.Errorf("compositor: %q was killed by signal %s", command, unix.SignalName(exit.Status.Signal()))
	}
	return fmt.Errorf("compositor: %q exited with status %d", command, exit.Status.ExitStatus())
}

func (c *Compositor) ReloadConfig() {
	var apply func() error
	c.reloading++
	c.offLoop(func() error {
		apply = c.hooks.Reload()
		return nil
	}, func(error) error {
		c.reloading--
		if err := apply(); err != nil {
			c.report(fmt.Errorf("compositor: reload: %w", err))
		}
		return nil
	})
}

func (c *Compositor) ReloadPending() bool {
	return c.reloading > 0
}

func (c *Compositor) PrepareDispatch(a wm.Action) func() error {
	if _, reload := a.(wm.ReloadAction); !reload || c.hooks.Reload == nil {
		return nil
	}
	return c.hooks.Reload()
}

func (c *Compositor) DispatchPrepared(a wm.Action, loaded func() error) error {
	c.loadedConfig = loaded
	err := c.Dispatch(a)
	c.loadedConfig = nil
	return err
}
