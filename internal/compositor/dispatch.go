package compositor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/loop"
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
	case wm.OverviewAction:
		return c.overviewAction(e.Mode)
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
	stdout, stderr := os.Stdout, os.Stderr
	c.execs.Run(func() {
		cmd, err := c.startCommand(command, env, stdout, stderr)
		if err == nil {
			pid := cmd.Process.Pid
			go c.reap(command, cmd)
			if posted := c.loop.Post(func() error { started(pid); return nil }); posted != nil && !errors.Is(posted, loop.ErrClosed) {
				panic(fmt.Sprintf("compositor: hand a started exec back to the loop: %v", posted))
			}
			return
		}
		if posted := c.loop.Post(func() error { c.report(fmt.Errorf("compositor: %w", err)); return nil }); posted != nil && !errors.Is(posted, loop.ErrClosed) {
			panic(fmt.Sprintf("compositor: hand a failed exec back to the loop: %v", posted))
		}
	})
}

func (c *Compositor) WaitForExecs() {
	c.execs.Wait()
}

func (c *Compositor) startCommand(command string, env []string, stdout, stderr *os.File) (*exec.Cmd, error) {
	sh, err := c.lookPath("sh")
	if err != nil {
		return nil, fmt.Errorf("exec %q: %w", command, err)
	}
	cmd := exec.CommandContext(context.Background(), sh, "-c", command) //nolint:gosec // binds and lys-wmctl dispatch run the user's own commands
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exec %q: %w", command, err)
	}
	return cmd, nil
}

func (c *Compositor) reap(command string, cmd *exec.Cmd) {
	exited := exitError(command, cmd.Wait())
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
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		if err != nil {
			return fmt.Errorf("compositor: wait for %q: %w", command, err)
		}
		return nil
	}
	if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return fmt.Errorf("compositor: %q was killed by signal %s", command, unix.SignalName(ws.Signal()))
	}
	return fmt.Errorf("compositor: %q exited with status %d", command, exit.ExitCode())
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
