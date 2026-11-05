package harness

import (
	"errors"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cxh "github.com/stubbedev/lys-wm/internal/protocol/client/xxhotkey"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const (
	keysymP      = 0x70
	keysymQ      = 0x71
	keysymA      = 0x61
	keysymF9     = 0xffc6
	keysymSuperL = 0xffeb
	keyP         = 25
	keyF9        = 67
	btnSide      = 0x113
)

type hotkeyLog struct{ log testclient.Log }

func (l *hotkeyLog) Bound(*cxh.XxHotkeyV1) error { return l.log.Note("bound") }

func (l *hotkeyLog) Denied(_ *cxh.XxHotkeyV1, reason cxh.XxHotkeyV1DenyReason, _ string) error {
	return l.log.Note("denied %d", reason)
}

func (l *hotkeyLog) Revoked(*cxh.XxHotkeyV1, string) error { return l.log.Note("revoked") }

func (l *hotkeyLog) Triggered(_ *cxh.XxHotkeyV1, serial, _ uint32) error {
	if serial == 0 {
		return l.log.Note("triggered without a serial")
	}
	return l.log.Note("triggered")
}

func (l *hotkeyLog) Released(*cxh.XxHotkeyV1, uint32, uint32) error { return l.log.Note("released") }

type hotkeys struct {
	t   *testing.T
	p   *party
	mgr *cxh.XxHotkeyManagerV1
}

func hotkeyClient(t *testing.T, h *Harness, name string) *hotkeys {
	t.Helper()
	p := join(t, h, name)
	mgr, err := cxh.NewXxHotkeyManagerV1(p.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.c.Bind(mgr)
	return &hotkeys{t: t, p: p, mgr: mgr}
}

func (k *hotkeys) bind(set func(*cxh.XxHotkeyV1) error) (*cxh.XxHotkeyV1, *hotkeyLog) {
	k.t.Helper()
	l := &hotkeyLog{}
	hk, err := k.mgr.CreateHotkey(l)
	if err != nil {
		k.t.Fatal(err)
	}
	send(k.t, func() error { return set(hk) }, hk.Commit, k.p.c.Display.Flush)
	k.p.c.DispatchUntil("the commit answered", func() bool { return len(l.log) > 0 })
	return hk, l
}

func keyTrigger(keysym uint32, mods cxh.XxHotkeyManagerV1Modifiers) func(*cxh.XxHotkeyV1) error {
	return func(hk *cxh.XxHotkeyV1) error { return hk.SetKeyTrigger(keysym, mods) }
}

func hotkeyConfig(t *testing.T, cfg *compositor.Config) {
	withBinds(t, bindSpec{chord: "SUPER + Q", action: "kill"})(cfg)
	cfg.ClientHotkeys = true
}

func TestAClientHotkeyFiresWhileAnotherClientHasFocus(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) { hotkeyConfig(t, cfg) }, small)
	app := hotkeyClient(t, h, "app")
	editor := join(t, h, "editor")
	editor.seen(t, "the editor focused", logged(&editor.in.Log, "kb_enter editor"))

	_, super := app.bind(keyTrigger(keysymP, cxh.XxHotkeyManagerV1ModifiersSuper))
	_, f9 := app.bind(keyTrigger(keysymF9, 0))
	_, side := app.bind(func(hk *cxh.XxHotkeyV1) error { return hk.SetButtonTrigger(btnSide, 0) })
	for _, l := range []*hotkeyLog{super, f9, side} {
		if !slices.Equal(l.log, testclient.Log{"bound"}) {
			t.Fatalf("an allowed trigger got %q", l.log)
		}
		l.log = nil
	}

	editor.in.Log = nil
	h.key(keySuper, true)
	h.tap(keyP)
	h.key(keySuper, false)
	until(t, app.p.c, "Super+P pressed and released", &super.log, "triggered", "released")
	h.tap(keyF9)
	until(t, app.p.c, "F9 pressed and released", &f9.log, "triggered", "released")
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Button(btnSide, true), p.Frame(), p.Button(btnSide, false), p.Frame())
	})
	until(t, app.p.c, "the side button pressed and released", &side.log, "triggered", "released")
	editor.c.Sync()
	for _, consumed := range []string{"key 25 1", "key 25 0", "key 67 1", "button 275 1"} {
		if slices.Contains(editor.in.Log, consumed) {
			t.Fatalf("the focused editor saw a hotkey's %q: %q", consumed, editor.in.Log)
		}
	}
}

func TestATapOnALoneModifierFiresOnReleaseAndStillReachesTheFocus(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) { hotkeyConfig(t, cfg) }, small)
	app := hotkeyClient(t, h, "app")
	editor := join(t, h, "editor")
	editor.seen(t, "the editor focused", logged(&editor.in.Log, "kb_enter editor"))
	_, tap := app.bind(keyTrigger(keysymSuperL, 0))
	tap.log = nil

	h.key(keySuper, true)
	app.p.c.Sync()
	if len(tap.log) != 0 {
		t.Fatalf("a tap fired on press: %q", tap.log)
	}
	h.key(keySuper, false)
	until(t, app.p.c, "the tap on release", &tap.log, "triggered", "released")
	editor.seen(t, "the tapped Super reaching the editor", logged(&editor.in.Log, "key 125 0"))

	h.key(keySuper, true)
	h.tap(keyP)
	h.key(keySuper, false)
	app.p.c.Sync()
	if len(tap.log) != 0 {
		t.Fatalf("Super held over another key still tapped: %q", tap.log)
	}
}

func TestAClientHotkeyPolicyDeniesTextAndTakenTriggers(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) { hotkeyConfig(t, cfg) }, small)
	app := hotkeyClient(t, h, "app")
	other := hotkeyClient(t, h, "other")
	notPermitted := testclient.Log{"denied 1"}
	taken := testclient.Log{"denied 0"}
	for _, c := range []struct {
		k       *hotkeys
		trigger func(*cxh.XxHotkeyV1) error
		want    testclient.Log
	}{
		{app, keyTrigger(keysymA, 0), notPermitted},
		{app, keyTrigger(keysymA, cxh.XxHotkeyManagerV1ModifiersShift), notPermitted},
		{app, func(hk *cxh.XxHotkeyV1) error { return hk.SetButtonTrigger(btnLeft, 0) }, notPermitted},
		{app, keyTrigger(keysymQ, cxh.XxHotkeyManagerV1ModifiersSuper), taken},
		{app, keyTrigger(keysymP, cxh.XxHotkeyManagerV1ModifiersCtrl), testclient.Log{"bound"}},
		{other, keyTrigger(keysymP, cxh.XxHotkeyManagerV1ModifiersCtrl), taken},
	} {
		if _, l := c.k.bind(c.trigger); !slices.Equal(l.log, c.want) {
			t.Errorf("got %q, want %q", l.log, c.want)
		}
	}
}

func TestNoClientHotkeyFiresWhileLocked(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) { hotkeyConfig(t, cfg) }, small)
	app := hotkeyClient(t, h, "app")
	_, f9 := app.bind(keyTrigger(keysymF9, 0))
	f9.log = nil
	l := lockSession(t, h)
	l.seen("locked")
	h.tap(keyF9)
	h.key(keySuper, true)
	h.key(keySuper, false)
	app.p.c.Sync()
	if len(f9.log) != 0 {
		t.Fatalf("a hotkey fired on the lock screen: %q", f9.log)
	}
}

func TestClientHotkeysAreOptIn(t *testing.T) {
	t.Parallel()
	off := Start(t, small)
	if testclient.Connect(t, off.Socket()).HasGlobal("xx_hotkey_manager_v1") {
		t.Fatal("xx_hotkey_manager_v1 is offered without client_hotkeys")
	}
	var cfg compositor.Config
	h := StartWith(t, func(c *compositor.Config) {
		hotkeyConfig(t, c)
		cfg = *c
	}, small)
	app := hotkeyClient(t, h, "app")
	_, f9 := app.bind(keyTrigger(keysymF9, 0))
	cfg.ClientHotkeys = false
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
	until(t, app.p.c, "turning client hotkeys off revokes them", &f9.log, "bound", "revoked")
	if testclient.Connect(t, h.Socket()).HasGlobal("xx_hotkey_manager_v1") {
		t.Fatal("xx_hotkey_manager_v1 stayed after client_hotkeys was turned off")
	}
	h.tap(keyF9)
	app.p.c.Sync()
	if len(f9.log) != 0 {
		t.Fatalf("a revoked hotkey fired: %q", f9.log)
	}
}
