package compositor

import (
	"errors"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/binds"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	sxh "github.com/stubbedev/lys-wm/internal/protocol/server/xxhotkey"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

const (
	hotkeyVersion = 1
	allModifiers  = sxh.XxHotkeyManagerV1ModifiersShift | sxh.XxHotkeyManagerV1ModifiersCtrl | sxh.XxHotkeyManagerV1ModifiersAlt | sxh.XxHotkeyManagerV1ModifiersSuper
	btnSide       = 0x113
	btnTask       = 0x117
)

var hotkeyModifiers = []struct {
	wire sxh.XxHotkeyManagerV1Modifiers
	set  binds.ModifierSet
}{
	{sxh.XxHotkeyManagerV1ModifiersShift, binds.ModShift},
	{sxh.XxHotkeyManagerV1ModifiersCtrl, binds.ModControl},
	{sxh.XxHotkeyManagerV1ModifiersAlt, binds.ModAlt},
	{sxh.XxHotkeyManagerV1ModifiersSuper, binds.ModSuper},
}

type hotkeyTrigger struct {
	keysym uint32
	button uint32
	mods   binds.ModifierSet
}

func (t hotkeyTrigger) tap() bool {
	return t.keysym != 0 && xkb.IsModifier(t.keysym)
}

func (t hotkeyTrigger) permitted() bool {
	switch {
	case t.mods&(binds.ModControl|binds.ModAlt|binds.ModSuper) != 0:
		return true
	case t.keysym == 0:
		return t.button >= btnSide && t.button <= btnTask
	}
	return !xkb.TypesText(t.keysym)
}

type hotkey struct {
	c       *Compositor
	res     *sxh.XxHotkeyV1
	owner   *hotkeyManager
	pending *hotkeyTrigger
	bound   *hotkeyTrigger
}

type heldHotkey struct {
	trigger binds.Trigger
	hotkey  *hotkey
}

type hotkeyTap struct {
	code    uint32
	hotkeys []*hotkey
}

type hotkeyState struct {
	global *server.Global
	all    []*hotkey
	held   []heldHotkey
	tap    *hotkeyTap
}

type hotkeyManager struct {
	c         *Compositor
	cl        *server.Client
	appID     string
	committed bool
}

func (c *Compositor) syncHotkeys() error {
	h := &c.hotkeys
	if !c.clientHotkeys {
		var errs []error
		for _, k := range h.all {
			errs = append(errs, k.revoke("client hotkeys are turned off"))
		}
		if h.global != nil {
			errs = append(errs, h.global.Remove())
			h.global = nil
		}
		return errors.Join(errs...)
	}
	var errs []error
	for _, k := range h.all {
		if k.bound != nil && c.boundByConfig(*k.bound) {
			errs = append(errs, k.revoke("a lys-wm bind now uses this trigger"))
		}
	}
	if h.global == nil {
		g, err := c.display.AddGlobal(sxh.XxHotkeyManagerV1Interface, hotkeyVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
			_, err := sxh.NewXxHotkeyManagerV1(cl, id, version, &hotkeyManager{c: c, cl: cl})
			return err
		})
		h.global = g
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (*hotkeyManager) Destroy(*sxh.XxHotkeyManagerV1) error { return nil }

func (m *hotkeyManager) SetAppID(obj *sxh.XxHotkeyManagerV1, appID string) error {
	if appID == "" || m.appID != "" || m.committed {
		return obj.ProtocolError(sxh.XxHotkeyManagerV1ErrorInvalidAppID, "app id %q is empty, set twice or set after a commit", appID)
	}
	m.appID = appID
	return nil
}

func (m *hotkeyManager) CreateHotkey(_ *sxh.XxHotkeyManagerV1, res *sxh.XxHotkeyV1) (sxh.XxHotkeyV1Handler, error) {
	k := &hotkey{c: m.c, res: res, owner: m}
	server.Track(res, &m.c.hotkeys.all, k)
	server.OnDestroy(res, func() error {
		k.release()
		return nil
	})
	return k, nil
}

func (*hotkey) Destroy(*sxh.XxHotkeyV1) error { return nil }

func (*hotkey) SetDescription(*sxh.XxHotkeyV1, string) error { return nil }

func (*hotkey) SetSeat(*sxh.XxHotkeyV1, *sw.WlSeat) error { return nil }

func modifierSet(obj *sxh.XxHotkeyV1, mods sxh.XxHotkeyManagerV1Modifiers) (binds.ModifierSet, error) {
	if mods&^allModifiers != 0 {
		return 0, obj.ProtocolError(sxh.XxHotkeyV1ErrorInvalidTrigger, "unknown modifier bits %#x", uint32(mods&^allModifiers))
	}
	var set binds.ModifierSet
	for _, m := range hotkeyModifiers {
		if mods&m.wire != 0 {
			set |= m.set
		}
	}
	return set, nil
}

func (k *hotkey) SetKeyTrigger(obj *sxh.XxHotkeyV1, keysym uint32, mods sxh.XxHotkeyManagerV1Modifiers) error {
	if keysym == 0 {
		return obj.ProtocolError(sxh.XxHotkeyV1ErrorInvalidTrigger, "a null keysym")
	}
	set, err := modifierSet(obj, mods)
	if err != nil {
		return err
	}
	k.pending = &hotkeyTrigger{keysym: keysym, mods: set}
	return nil
}

func (k *hotkey) SetButtonTrigger(obj *sxh.XxHotkeyV1, button uint32, mods sxh.XxHotkeyManagerV1Modifiers) error {
	set, err := modifierSet(obj, mods)
	if err != nil {
		return err
	}
	k.pending = &hotkeyTrigger{button: button, mods: set}
	return nil
}

func (k *hotkey) Commit(obj *sxh.XxHotkeyV1) error {
	if k.pending == nil {
		return obj.ProtocolError(sxh.XxHotkeyV1ErrorNoTrigger, "commit before a trigger")
	}
	k.owner.committed = true
	t := *k.pending
	switch {
	case !t.permitted():
		return obj.SendDenied(sxh.XxHotkeyV1DenyReasonNotPermitted, "the trigger types text; add Ctrl, Alt or Super")
	case k.c.boundByConfig(t):
		return obj.SendDenied(sxh.XxHotkeyV1DenyReasonAlreadyBound, "a lys-wm bind uses this trigger")
	case slices.ContainsFunc(k.c.hotkeys.all, func(o *hotkey) bool { return o != k && o.bound != nil && *o.bound == t }):
		return obj.SendDenied(sxh.XxHotkeyV1DenyReasonAlreadyBound, "another application holds this trigger")
	}
	if k.bound != nil && *k.bound != t {
		k.release()
	}
	k.bound = &t
	return obj.SendBound()
}

func (k *hotkey) revoke(message string) error {
	if k.bound == nil {
		return nil
	}
	k.release()
	k.bound = nil
	return k.res.SendRevoked(message)
}

func (k *hotkey) release() {
	h := &k.c.hotkeys
	h.held = slices.DeleteFunc(h.held, func(held heldHotkey) bool { return held.hotkey == k })
	if h.tap != nil {
		h.tap.hotkeys = slices.DeleteFunc(h.tap.hotkeys, func(o *hotkey) bool { return o == k })
	}
}

func (c *Compositor) boundByConfig(t hotkeyTrigger) bool {
	km := c.seatConfig.Keymap
	mask, err := t.mods.Mask(km)
	if err != nil {
		return false
	}
	return c.binds.Any(func(b binds.Bind) bool {
		if b.Chord.Mods != mask {
			return false
		}
		switch tr := b.Chord.Trigger.(type) {
		case binds.Key:
			return t.keysym != 0 && km.Unshifted(tr.Code, t.keysym)
		case binds.Button:
			return t.keysym == 0 && tr.Code == t.button
		}
		return false
	})
}

func (c *Compositor) hotkeysOpen() bool {
	return c.clientHotkeys && !c.shell.Locked() && c.inhibiting == nil
}

func (c *Compositor) hotkeysFor(t binds.Trigger, tap bool, mods uint32) []*hotkey {
	km := c.seatConfig.Keymap
	var out []*hotkey
	for _, k := range c.hotkeys.all {
		b := k.bound
		if b == nil || b.tap() != tap {
			continue
		}
		mask, err := b.mods.Mask(km)
		if err != nil || mask != c.binds.Held(mods) {
			continue
		}
		switch tr := t.(type) {
		case binds.Key:
			if b.keysym != 0 && km.Unshifted(tr.Code, b.keysym) {
				out = append(out, k)
			}
		case binds.Button:
			if b.keysym == 0 && b.button == tr.Code {
				out = append(out, k)
			}
		}
	}
	return out
}

func (k *hotkey) send(released bool) error {
	serial := k.c.seat.ActivationSerial(k.owner.cl)
	at := wire.Millis(backend.Now())
	if released {
		return k.res.SendReleased(serial, at)
	}
	return k.res.SendTriggered(serial, at)
}

func (c *Compositor) pressHotkey(t binds.Trigger) (bool, error) {
	fired := c.hotkeysFor(t, false, c.seat.DepressedModifiers())
	var errs []error
	for _, k := range fired {
		c.hotkeys.held = append(c.hotkeys.held, heldHotkey{trigger: t, hotkey: k})
		errs = append(errs, k.send(false))
	}
	return len(fired) > 0, errors.Join(errs...)
}

func (c *Compositor) releaseHotkeys(t binds.Trigger) error {
	var errs []error
	c.hotkeys.held = slices.DeleteFunc(c.hotkeys.held, func(h heldHotkey) bool {
		if h.trigger != t {
			return false
		}
		errs = append(errs, h.hotkey.send(true))
		return true
	})
	return errors.Join(errs...)
}

func (c *Compositor) watchTap(code uint32, pressed bool) error {
	h := &c.hotkeys
	if pressed {
		h.tap = nil
		if c.hotkeysOpen() {
			if tapped := c.hotkeysFor(binds.Key{Code: code}, true, c.seat.DepressedModifiers()); len(tapped) > 0 {
				h.tap = &hotkeyTap{code: code, hotkeys: tapped}
			}
		}
		return nil
	}
	tap := h.tap
	if tap == nil || tap.code != code {
		return nil
	}
	h.tap = nil
	if !c.hotkeysOpen() {
		return nil
	}
	var errs []error
	for _, k := range tap.hotkeys {
		errs = append(errs, k.send(false), k.send(true))
	}
	return errors.Join(errs...)
}

func (c *Compositor) interruptTap() {
	c.hotkeys.tap = nil
}
