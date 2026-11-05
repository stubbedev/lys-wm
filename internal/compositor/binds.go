package compositor

import (
	"errors"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type repeat struct {
	timer *loop.Timer
	held  *repeating
}

type repeating struct {
	key  uint32
	fire func()
}

func (r *repeat) stop() error {
	r.held = nil
	return r.timer.Disarm()
}

func (c *Compositor) run(a wm.Action, trigger binds.Trigger) {
	if err := c.dispatch(a, trigger); err != nil {
		c.report(err)
	}
}

const (
	keysymSwitchVT1  = 0x1008fe01
	keysymSwitchVT12 = 0x1008fe0c
)

func vtSwitch(keysym uint32) uint32 {
	if keysym < keysymSwitchVT1 || keysym > keysymSwitchVT12 {
		return 0
	}
	return keysym - keysymSwitchVT1 + 1
}

func (c *Compositor) match(t binds.Trigger) (binds.Bind, bool) {
	return c.binds.Match(c.submap, c.seat.DepressedModifiers(), t, c.shell.Locked() || c.inhibiting != nil)
}

func (c *Compositor) bindKey(code uint32, pressed bool) (bool, error) {
	if !pressed {
		i := slices.Index(c.held, code)
		if i < 0 {
			return false, nil
		}
		c.held = slices.Delete(c.held, i, i+1)
		released := errors.Join(c.releaseShortcuts(binds.Key{Code: code}), c.releaseHotkeys(binds.Key{Code: code}))
		if h := c.repeat.held; h != nil && h.key == code {
			return true, errors.Join(released, c.repeat.stop())
		}
		return true, released
	}
	if vt := vtSwitch(c.seat.Keysym(code)); vt != 0 && c.hooks.SwitchVT != nil {
		c.held = append(c.held, code)
		return true, c.hooks.SwitchVT(vt)
	}
	b, ok := c.match(binds.Key{Code: code})
	if !ok {
		if !c.hotkeysOpen() {
			return false, nil
		}
		fired, err := c.pressHotkey(binds.Key{Code: code})
		if fired {
			c.held = append(c.held, code)
		}
		return fired, err
	}
	c.held = append(c.held, code)
	c.run(b.Action, binds.Key{Code: code})
	if !b.Flags.Repeating || c.seatConfig.RepeatRate == 0 {
		return true, nil
	}
	return true, c.repeatKey(code, func() { c.run(b.Action, nil) })
}

func (c *Compositor) repeatKey(code uint32, fire func()) error {
	c.repeat.held = &repeating{code, fire}
	return c.repeat.timer.Arm(time.Duration(c.seatConfig.RepeatDelay) * time.Millisecond)
}

func (c *Compositor) repeatFired() error {
	h := c.repeat.held
	if h == nil {
		return nil
	}
	h.fire()
	if c.repeat.held != h {
		return nil
	}
	return c.repeat.timer.Arm(time.Second / time.Duration(c.seatConfig.RepeatRate))
}
