package compositor

import (
	"errors"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/ipc"
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

type armedBind struct {
	set    bool
	button bool
	code   uint32
	bind   int
	at     time.Duration
}

type bindFire struct {
	at    time.Duration
	fired bool
}

type modRelease struct {
	mods uint32
	done func() error
}

func (c *Compositor) run(a wm.Action, trigger binds.Trigger, by wm.FocusOrigin) {
	if err := c.dispatch(a, trigger, by); err != nil {
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

func (c *Compositor) bindGate() binds.Gate {
	switch {
	case c.shell.Locked():
		return binds.GateLocked
	case c.inhibiting != nil:
		return binds.GateInhibited
	}
	return binds.GateOpen
}

func (c *Compositor) match(t binds.Trigger) (binds.Bind, int, bool) {
	return c.binds.Match(c.submap, c.seat.DepressedModifiers(), t, c.bindGate())
}

func (c *Compositor) adoptBinds(m binds.Map) {
	c.binds, c.armed = m, armedBind{}
	c.bindFired = make([]bindFire, m.Len())
	all := m.All()
	c.ipcBinds = make([]ipc.Bind, len(all))
	for i, b := range all {
		c.ipcBinds[i] = ipc.BindOf(b)
	}
}

func (c *Compositor) fire(b binds.Bind, i int, at time.Duration, trigger binds.Trigger, by wm.FocusOrigin) {
	f := &c.bindFired[i]
	if cd := b.Flags.Cooldown; cd > 0 && f.fired && at-f.at < cd {
		return
	}
	f.at, f.fired = at, true
	c.run(b.Action, trigger, by)
}

func (c *Compositor) fireArmed(a armedBind, at time.Duration, by wm.FocusOrigin) {
	b := c.binds.At(a.bind)
	if at-a.at < b.Flags.LongPress {
		return
	}
	c.fire(b, a.bind, at, nil, by)
}

func (c *Compositor) bindKey(code uint32, pressed bool, at time.Duration) (bool, error) {
	if !pressed {
		if a := c.armed; a.set && !a.button && a.code == code {
			c.armed = armedBind{}
			c.fireArmed(a, at, wm.FocusKeyboard)
		}
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
	c.armed = armedBind{}
	if vt := vtSwitch(c.seat.Keysym(code)); vt != 0 && c.hooks.SwitchVT != nil {
		c.held = append(c.held, code)
		return true, c.hooks.SwitchVT(vt)
	}
	tap, isTap := binds.TapOf(c.seat.Keysym(code))
	var b binds.Bind
	var i int
	var ok bool
	if isTap {
		b, i, ok = c.match(tap)
	} else {
		b, i, ok = c.match(binds.Key{Code: code})
	}
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
	consume := !b.Flags.Pass && !isTap
	if consume {
		c.held = append(c.held, code)
	}
	if b.Flags.OnRelease() {
		c.armed = armedBind{set: true, code: code, bind: i, at: at}
		return consume, nil
	}
	var trigger binds.Trigger
	if consume {
		trigger = binds.Key{Code: code}
	}
	c.fire(b, i, at, trigger, wm.FocusKeyboard)
	if !b.Flags.Repeating || c.seatConfig.RepeatRate == 0 {
		return consume, nil
	}
	return consume, c.repeatKey(code, func() { c.run(b.Action, nil, wm.FocusKeyboard) })
}

func (c *Compositor) bindButton(code uint32, pressed bool, at time.Duration) bool {
	if !pressed {
		if a := c.armed; a.set && a.button && a.code == code {
			c.armed = armedBind{}
			c.fireArmed(a, at, wm.FocusClick)
		}
		return false
	}
	b, i, ok := c.match(binds.Button{Code: code})
	if !ok {
		return false
	}
	consume := !b.Flags.Pass
	if consume {
		c.consumed = append(c.consumed, code)
	}
	if b.Flags.OnRelease() {
		c.armed = armedBind{set: true, button: true, code: code, bind: i, at: at}
		return consume
	}
	var trigger binds.Trigger
	if consume {
		trigger = binds.Button{Code: code}
	}
	c.fire(b, i, at, trigger, wm.FocusClick)
	return consume
}

func (c *Compositor) OnModifiersReleased(mods uint32, done func() error) {
	c.modRelease = &modRelease{mods: mods, done: done}
}

func (c *Compositor) modifiersReleased() error {
	w := c.modRelease
	if w == nil || c.binds.Held(c.seat.DepressedModifiers())&w.mods == w.mods {
		return nil
	}
	c.modRelease = nil
	return w.done()
}

func (c *Compositor) BindLookups() uint64 {
	return c.binds.Lookups()
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
