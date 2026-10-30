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
	return c.dispatch(a, nil)
}

func (c *Compositor) dispatch(a wm.Action, trigger binds.Trigger) error {
	s, effects, err := wm.Dispatch(c.wm, a, c.settings)
	if err != nil {
		return err
	}
	if err := c.update(s, nil); err != nil {
		return err
	}
	if c.dispatched != nil {
		c.dispatched(a)
	}
	for _, e := range effects {
		if err := c.perform(e, trigger); err != nil {
			return fmt.Errorf("compositor: %s: %w", a, err)
		}
	}
	return nil
}

func (c *Compositor) perform(e wm.Effect, trigger binds.Trigger) error {
	switch e := e.(type) {
	case wm.CloseWindowAction:
		w := c.windowByID(e.ID)
		if w == nil {
			return fmt.Errorf("close unknown window %d", e.ID)
		}
		return w.top.close()
	case wm.ExecAction:
		return c.spawn(e.Command)
	case wm.GlobalAction:
		return c.pressShortcut(e, trigger)
	case wm.SubmapAction:
		c.submap = e.Name
		return nil
	case wm.DragAction:
		return c.beginDrag(e.Mode, trigger)
	case wm.DPMSAction:
		return c.dpms(e)
	case wm.ReloadAction:
		if c.hooks.Reload == nil {
			return errors.New("no config file to reload")
		}
		return c.hooks.Reload()
	case wm.ExitAction:
		if c.hooks.Exit == nil {
			return errors.New("nothing to exit to: this compositor was started without a way to stop")
		}
		return c.hooks.Exit()
	}
	return fmt.Errorf("no handler for effect %T", e)
}

func (c *Compositor) spawn(command string) error {
	sh, err := exec.LookPath("sh")
	if err != nil {
		return fmt.Errorf("exec %q: %w", command, err)
	}
	cmd := exec.CommandContext(context.Background(), sh, "-c", command) //nolint:gosec // binds and lys-wmctl dispatch run the user's own commands
	cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY="+c.display.Path())
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec %q: %w", command, err)
	}
	go c.reap(command, cmd)
	return nil
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
