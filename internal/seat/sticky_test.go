package seat

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/xkb"
)

const (
	keyLeftShift = 42
	keyLeftCtrl  = 29
	keyA         = 30
	keyCapsLock  = 58
)

func (e *env) tap(codes ...uint32) {
	e.t.Helper()
	e.on(func() {
		for _, code := range codes {
			if err := e.seat.Key(code, true, 0); err != nil {
				e.t.Error(err)
			}
		}
		for _, code := range slices.Backward(codes) {
			if err := e.seat.Key(code, false, 0); err != nil {
				e.t.Error(err)
			}
		}
	})
}

func (e *env) modifiers() xkb.Modifiers {
	var m xkb.Modifiers
	e.on(func() { m = e.seat.state.Modifiers() })
	return m
}

func (e *env) mask(name string) uint32 {
	e.t.Helper()
	m, err := e.keymap.ModMask(name)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func TestAStickyModifierLatchesThenLocksThenLetsGo(t *testing.T) {
	e := setup(t)
	e.on(func() { e.seat.SetStickyKeys(true) })
	shift := e.mask("Shift")
	e.tap(keyLeftShift)
	if m := e.modifiers(); m.Latched != shift || m.Locked != 0 {
		t.Fatalf("one Shift tap: %+v, want Shift latched", m)
	}
	var sym uint32
	e.on(func() {
		sym = e.seat.Keysym(keyA)
		if err := e.seat.Key(keyA, true, 0); err != nil {
			t.Error(err)
		}
		if err := e.seat.Key(keyA, false, 0); err != nil {
			t.Error(err)
		}
	})
	if sym != 'A' {
		t.Errorf("the key after a latched Shift typed %#x, want A", sym)
	}
	if m := e.modifiers(); m.Latched != 0 {
		t.Fatalf("the latch outlived the next key: %+v", m)
	}
	e.tap(keyLeftShift)
	e.tap(keyLeftShift)
	if m := e.modifiers(); m.Locked != shift || m.Latched != 0 {
		t.Fatalf("two Shift taps: %+v, want Shift locked", m)
	}
	e.tap(keyA)
	if m := e.modifiers(); m.Locked != shift {
		t.Fatalf("a key let go of the locked Shift: %+v", m)
	}
	e.tap(keyLeftShift)
	if m := e.modifiers(); m.Locked != 0 || m.Latched != 0 {
		t.Fatalf("a third Shift tap: %+v, want nothing held", m)
	}
}

func TestAChordIsNoStickyTap(t *testing.T) {
	e := setup(t)
	e.on(func() { e.seat.SetStickyKeys(true) })
	e.tap(keyLeftCtrl, keyA)
	if m := e.modifiers(); m.Latched != 0 || m.Locked != 0 {
		t.Fatalf("Ctrl+A latched %+v", m)
	}
}

func TestCapsLockIsNotLatchedAsWell(t *testing.T) {
	e := setup(t)
	e.on(func() { e.seat.SetStickyKeys(true) })
	e.tap(keyCapsLock)
	if m := e.modifiers(); m.Latched != 0 || m.Locked != e.mask("Lock") {
		t.Fatalf("Caps Lock under sticky keys: %+v, want only its own lock", m)
	}
}

func TestWithoutStickyKeysAModifierTapDoesNothing(t *testing.T) {
	e := setup(t)
	e.tap(keyLeftShift)
	if m := e.modifiers(); m.Latched != 0 || m.Locked != 0 {
		t.Fatalf("a Shift tap without sticky keys left %+v", m)
	}
}
