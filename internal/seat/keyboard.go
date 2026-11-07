package seat

import (
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

type keyboardState struct {
	focus     *surface.Surface
	pressed   []uint32
	grab      KeyboardGrab
	grabbed   []uint32
	sticky    uint32
	suspended bool
}

type KeyboardGrab interface {
	Keymap(fd int, size uint32) error
	RepeatInfo(rate, delay int32) error
	Key(serial, ms, code uint32, pressed bool) error
	Modifiers(serial uint32, m xkb.Modifiers) error
}

func (s *Seat) Grab(g KeyboardGrab) error {
	s.keyboard.grab = g
	return errors.Join(g.Keymap(s.keymap.FD(), s.keymap.Size()), g.RepeatInfo(s.cfg.RepeatRate, s.cfg.RepeatDelay), g.Modifiers(s.display.NextSerial(), s.state.Modifiers()))
}

func (s *Seat) Ungrab(g KeyboardGrab) error {
	if s.keyboard.grab != g {
		return nil
	}
	s.keyboard.grab = nil
	kbs := s.focusedKeyboards()
	var errs []error
	for _, kb := range kbs {
		errs = append(errs, s.sendModifiers(kb))
	}
	return errors.Join(errs...)
}

func (s *Seat) grabKey(code uint32, pressed, modsChanged bool, at time.Duration) (bool, error) {
	k := &s.keyboard
	i := slices.Index(k.grabbed, code)
	switch {
	case pressed && k.grab != nil:
		k.grabbed = append(k.grabbed, code)
	case !pressed && i >= 0:
		k.grabbed = slices.Delete(k.grabbed, i, i+1)
		if k.grab == nil {
			return true, nil
		}
	default:
		return false, nil
	}
	err := k.grab.Key(s.display.NextSerial(), wire.Millis(at), code, pressed)
	if modsChanged {
		err = errors.Join(err, k.grab.Modifiers(s.display.NextSerial(), s.state.Modifiers()))
	}
	return true, err
}

func (s *Seat) Focus() *surface.Surface {
	return s.keyboard.focus
}

func (s *Seat) keysArray() []byte {
	keys := make([]byte, 0, 4*len(s.keyboard.pressed))
	for _, k := range s.keyboard.pressed {
		keys = binary.NativeEndian.AppendUint32(keys, k)
	}
	return keys
}

func (s *Seat) sendModifiers(kb *sw.WlKeyboard) error {
	m := s.state.Modifiers()
	return kb.SendModifiers(s.display.NextSerial(), m.Depressed, m.Latched, m.Locked, m.Group)
}

func (s *Seat) enterKeyboard(kb *sw.WlKeyboard) error {
	if _, err := s.syncKeymap(kb, nil); err != nil {
		return err
	}
	if err := kb.SendEnter(s.display.NextSerial(), s.keyboard.focus.Resource(), s.keysArray()); err != nil {
		return err
	}
	return s.sendModifiers(kb)
}

func (s *Seat) receiver() *surface.Surface {
	if s.keyboard.suspended {
		return nil
	}
	return s.keyboard.focus
}

func (s *Seat) leaveKeyboard(target *surface.Surface) error {
	r := s.lookup(target.Client())
	if r == nil {
		return nil
	}
	var errs []error
	for _, kb := range r.keyboards {
		errs = append(errs, kb.SendLeave(s.display.NextSerial(), target.Resource()))
	}
	return errors.Join(errs...)
}

func (s *Seat) enterFocus() error {
	f := s.receiver()
	if f == nil {
		return nil
	}
	r := s.lookup(f.Client())
	if r == nil {
		return nil
	}
	var errs []error
	for _, kb := range r.keyboards {
		errs = append(errs, s.enterKeyboard(kb))
	}
	return errors.Join(errs...)
}

func (s *Seat) SetKeyboardFocus(target *surface.Surface) error {
	if target == s.keyboard.focus {
		return nil
	}
	var errs []error
	if old := s.receiver(); old != nil {
		errs = append(errs, s.leaveKeyboard(old))
	}
	s.keyboard.focus = target
	errs = append(errs, s.listener.KeyboardFocusChanged(target), s.focusPads(target), s.enterFocus())
	return errors.Join(errs...)
}

func (s *Seat) SuspendKeyboard() error {
	old := s.receiver()
	s.keyboard.suspended = true
	if old == nil {
		return nil
	}
	return s.leaveKeyboard(old)
}

func (s *Seat) ResumeKeyboard() error {
	if !s.keyboard.suspended {
		return nil
	}
	s.keyboard.suspended = false
	return s.enterFocus()
}

func (s *Seat) Modifiers() xkb.Modifiers {
	return s.state.Modifiers()
}

func (s *Seat) DepressedModifiers() uint32 {
	return s.state.Modifiers().Depressed
}

func (s *Seat) Key(code uint32, pressed bool, at time.Duration) error {
	k := &s.keyboard
	i := slices.Index(k.pressed, code)
	switch {
	case pressed && i >= 0, !pressed && i < 0:
		return nil
	case pressed:
		k.pressed = append(k.pressed, code)
	default:
		k.pressed = slices.Delete(k.pressed, i, i+1)
	}
	before := s.state.Modifiers()
	modsChanged := s.state.Key(code, pressed)
	if s.cfg.StickyKeys && s.stick(code, pressed, before) {
		modsChanged = true
	}
	if modsChanged {
		s.listener.KeyboardStateChanged()
	}
	if k.suspended && (pressed || !slices.Contains(k.grabbed, code)) {
		return nil
	}
	if grabbed, err := s.grabKey(code, pressed, modsChanged, at); grabbed {
		return err
	}
	return s.sendKey(nil, code, pressed, modsChanged, at)
}

func (s *Seat) stick(code uint32, pressed bool, before xkb.Modifiers) bool {
	k := &s.keyboard
	after := s.state.Modifiers()
	if pressed {
		k.sticky = 0
		if len(k.pressed) == 1 && after.Locked == before.Locked && xkb.IsModifier(s.state.Keysym(code)) {
			k.sticky = code
		}
		return false
	}
	if code != k.sticky {
		return false
	}
	k.sticky = 0
	mask := before.Depressed &^ after.Depressed
	return mask != 0 && s.state.Stick(mask, before)
}

func (s *Seat) SetStickyKeys(on bool) {
	s.cfg.StickyKeys = on
	s.keyboard.sticky = 0
}

func (s *Seat) sendKey(v *VirtualKeyboard, code uint32, pressed, modsChanged bool, at time.Duration) error {
	c, r := s.resourcesAt(s.receiver())
	if r == nil {
		return nil
	}
	state := sw.WlKeyboardKeyStateReleased
	if pressed {
		state = sw.WlKeyboardKeyStatePressed
	}
	ms := wire.Millis(at)
	serial := s.eventSerial(c, pressed)
	var errs error
	for _, kb := range r.keyboards {
		errs = errors.Join(errs, s.useKeymap(kb, v), kb.SendKey(serial, ms, code, state))
		if modsChanged {
			errs = errors.Join(errs, s.sendModifiers(kb))
		}
	}
	return errs
}

func (s *Seat) sendKeyboardConfig(kb *sw.WlKeyboard) error {
	s.keymaps[kb] = sentKeymap{km: s.keymap}
	if err := kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, s.keymap.FD(), s.keymap.Size()); err != nil {
		return err
	}
	if kb.Base().Version() < 4 {
		return nil
	}
	return kb.SendRepeatInfo(s.cfg.RepeatRate, s.cfg.RepeatDelay)
}

type Pending struct {
	cfg    Config
	states map[*xkb.Keymap]*xkb.State
}

func (c Config) keymapOf(device string) *xkb.Keymap {
	if km, ok := c.Devices[device]; ok {
		return km
	}
	return c.Keymap
}

func Prepare(cfg Config) (Pending, error) {
	if cfg.Keymap == nil {
		return Pending{}, errors.New("seat: no keymap")
	}
	states := make(map[*xkb.Keymap]*xkb.State, 1+len(cfg.Devices))
	for _, km := range append([]*xkb.Keymap{cfg.Keymap}, slices.Collect(maps.Values(cfg.Devices))...) {
		if km == nil {
			return Pending{}, errors.New("seat: a device without a keymap")
		}
		if _, ok := states[km]; ok {
			continue
		}
		state := km.NewState()
		if cfg.NumLock {
			if err := state.LockNumLock(); err != nil {
				return Pending{}, fmt.Errorf("seat: turn Num Lock on: %w", err)
			}
		}
		states[km] = state
	}
	return Pending{cfg: cfg, states: states}, nil
}

func (s *Seat) adopt(p Pending) {
	s.cfg, s.states = p.cfg, p.states
	s.keymap = s.cfg.keymapOf(s.device)
	s.state = s.states[s.keymap]
}

// UseKeyboard makes device's keymap the one keys are read with and the one
// whoever holds the keyboard is told about, before a key from that device.
func (s *Seat) UseKeyboard(device string) error {
	s.device = device
	km := s.cfg.keymapOf(device)
	if km == s.keymap {
		return nil
	}
	s.keymap, s.state = km, s.states[km]
	s.listener.KeyboardStateChanged()
	g := s.keyboard.grab
	if g == nil {
		return nil
	}
	return errors.Join(g.Keymap(km.FD(), km.Size()), g.Modifiers(s.display.NextSerial(), s.state.Modifiers()))
}

func (s *Seat) Keymap() *xkb.Keymap {
	return s.keymap
}

func (s *Seat) Reconfigure(p Pending) error {
	s.adopt(p)
	for _, code := range s.keyboard.pressed {
		s.state.Key(code, true)
	}
	s.listener.KeyboardStateChanged()
	clear(s.keymaps)
	var errs []error
	if g := s.keyboard.grab; g != nil {
		errs = append(errs, g.Keymap(s.keymap.FD(), s.keymap.Size()), g.RepeatInfo(s.cfg.RepeatRate, s.cfg.RepeatDelay))
	}
	for _, r := range s.clients {
		focused := s.receiver() != nil && s.receiver().Client() == r.client
		for _, kb := range r.keyboards {
			errs = append(errs, s.sendKeyboardConfig(kb))
			if focused {
				errs = append(errs, s.sendModifiers(kb))
			}
		}
	}
	return errors.Join(errs...)
}

func (r seatRequests) GetKeyboard(obj *sw.WlSeat, kb *sw.WlKeyboard) (sw.WlKeyboardHandler, error) {
	s := r.s
	if !s.everCaps.Has(sw.WlSeatCapabilityKeyboard) {
		return nil, missing(obj, "keyboard")
	}
	res := s.resources(r.c)
	track(s, res, &res.keyboards, kb)
	server.OnDestroy(kb, func() error {
		delete(s.keymaps, kb)
		return nil
	})
	if err := s.sendKeyboardConfig(kb); err != nil {
		return nil, err
	}
	if f := s.receiver(); f != nil && f.Client() == r.c {
		if err := s.enterKeyboard(kb); err != nil {
			return nil, err
		}
	}
	return keyboardRequests{}, nil
}

type keyboardRequests struct{}

func (keyboardRequests) Release(*sw.WlKeyboard) error {
	return nil
}

func (s *Seat) Keysym(code uint32) uint32 {
	return s.state.Keysym(code)
}

func (s *Seat) Layout() uint32 {
	return s.state.Modifiers().Group
}

// NextLayout moves the seat to its next layout and tells whoever holds the
// keyboard; a keymap of one layout changes nothing.
func (s *Seat) NextLayout() error {
	if !s.state.NextLayout() {
		return nil
	}
	s.listener.KeyboardStateChanged()
	var errs []error
	if g := s.keyboard.grab; g != nil {
		errs = append(errs, g.Modifiers(s.display.NextSerial(), s.state.Modifiers()))
	}
	if _, r := s.resourcesAt(s.receiver()); r != nil {
		for _, kb := range r.keyboards {
			errs = append(errs, s.sendModifiers(kb))
		}
	}
	return errors.Join(errs...)
}

func (s *Seat) Locks() xkb.Locks {
	return s.state.Locks()
}

func (s *Seat) BareEscape(code uint32) bool {
	return !slices.Contains(s.keyboard.pressed, code) && s.state.BareEscape(code)
}

func (s *Seat) KeyboardGrabbed() bool {
	return s.keyboard.grab != nil
}
