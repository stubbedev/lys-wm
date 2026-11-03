package harness

import (
	"errors"
	"image"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	cksi "github.com/stubbedev/lys-wm/internal/protocol/client/keyboardshortcutsinhibit"
	cle "github.com/stubbedev/lys-wm/internal/protocol/client/lysescape"
	cvk "github.com/stubbedev/lys-wm/internal/protocol/client/virtualkeyboard"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

const (
	keyEscape = 1
	keyA      = 30
)

type escapeEvents struct{ log testclient.Log }

func (e *escapeEvents) Pressed(*cle.LysEscapeArmV1) error { return e.log.Note("pressed") }

type escapeArmer struct {
	t   *testing.T
	c   *testclient.Client
	mgr *cle.LysEscapeManagerV1
}

func escapeClient(t *testing.T, h *Harness) *escapeArmer {
	t.Helper()
	return armerOn(t, testclient.Connect(t, h.Socket()))
}

func armerOn(t *testing.T, c *testclient.Client) *escapeArmer {
	t.Helper()
	mgr, err := cle.NewLysEscapeManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	return &escapeArmer{t: t, c: c, mgr: mgr}
}

func (a *escapeArmer) arm() (*cle.LysEscapeArmV1, *escapeEvents) {
	a.t.Helper()
	ev := &escapeEvents{}
	arm, err := a.mgr.Arm(ev)
	if err != nil {
		a.t.Fatal(err)
	}
	a.c.Sync()
	return arm, ev
}

func (a *escapeArmer) fired(ev *escapeEvents) {
	a.t.Helper()
	a.c.DispatchUntil("the escape arm firing", func() bool { return slices.Contains(ev.log, "pressed") })
}

func (a *escapeArmer) neverFired(ev *escapeEvents) {
	a.t.Helper()
	a.c.Sync()
	if len(ev.log) != 0 {
		a.t.Fatalf("the arm fired: %q", ev.log)
	}
}

func (r *inputRig) focused() {
	r.t.Helper()
	r.expect("kb_enter win", "enter win 80,50", "frame")
}

func (r *inputRig) escapeReachesTheWindow() {
	r.t.Helper()
	r.h.tap(keyEscape)
	r.expect("key 1 1", "key 1 0")
}

func (r *inputRig) nothingLeakedBeforeASentinel() {
	r.t.Helper()
	r.h.tap(keyA)
	r.expect("key 30 1", "key 30 0")
}

func TestAnArmTakesOneEscapeAndItsRelease(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	_, ev := a.arm()

	r.h.tap(keyEscape)
	a.fired(ev)
	r.nothingLeakedBeforeASentinel()

	r.escapeReachesTheWindow()
	a.c.Sync()
	if len(ev.log) != 1 {
		t.Fatalf("a spent arm fired again: %q", ev.log)
	}
}

func TestEscapeReachesTheWindowWithoutAnArm(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	r.escapeReachesTheWindow()

	a := escapeClient(t, r.h)
	arm, ev := a.arm()
	if err := arm.Destroy(); err != nil {
		t.Fatal(err)
	}
	a.c.Sync()
	r.escapeReachesTheWindow()
	a.neverFired(ev)
}

func TestOtherKeysPassAnArm(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	r.nothingLeakedBeforeASentinel()
	a.neverFired(ev)
}

func TestAModifiedEscapeReachesTheWindow(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	r.h.key(keyShift, true)
	r.h.tap(keyEscape)
	r.h.key(keyShift, false)
	r.expect("key 42 1", "key 1 1", "key 1 0", "key 42 0")
	a.neverFired(ev)
}

func TestADisarmDuringThePressKeepsTheReleaseFromTheWindow(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	arm, ev := a.arm()
	r.h.key(keyEscape, true)
	a.fired(ev)
	if err := arm.Destroy(); err != nil {
		t.Fatal(err)
	}
	a.c.Sync()
	r.h.key(keyEscape, false)
	r.nothingLeakedBeforeASentinel()
}

func TestATakenEscapeHeldAcrossAModifierStaysTaken(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	r.h.key(keyEscape, true)
	a.fired(ev)
	r.h.key(keyShift, true)
	r.h.key(keyEscape, false)
	r.h.key(keyShift, false)
	r.expect("key 42 1", "key 42 0")
	r.nothingLeakedBeforeASentinel()
}

func TestAPressRepeatedWhileTakenStaysTaken(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	_, first := a.arm()
	r.h.key(keyEscape, true)
	a.fired(first)
	_, second := a.arm()
	r.h.key(keyEscape, true)
	r.h.key(keyEscape, false)
	r.nothingLeakedBeforeASentinel()
	a.neverFired(second)
}

func TestAKeyTheWindowSawDownIsNotTakenByALaterArm(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	r.h.key(keyEscape, true)
	r.expect("key 1 1")
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	r.h.key(keyEscape, true)
	r.h.key(keyEscape, false)
	r.expect("key 1 0")
	a.neverFired(ev)
}

func TestTheArmDiesWithItsClient(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	a.arm()
	w := a.c.Window("armer", testclient.Fill(0x10, 0x20, 0x30))
	a.c.DispatchUntil("the armer's window to map", w.Mapped)
	r.c.DispatchUntil("the armer focused", func() bool { return slices.Contains(r.in.Log, "kb_leave win") })
	if err := a.c.Display.Close(); err != nil {
		t.Fatal(err)
	}
	kbEnters := func() int {
		n := 0
		for _, e := range r.in.Log {
			if e == "kb_enter win" {
				n++
			}
		}
		return n
	}
	r.c.DispatchUntil("the keyboard back on the window", func() bool { return kbEnters() == 2 })
	r.h.tap(keyEscape)
	r.c.DispatchUntil("Escape at the window", func() bool { return slices.Contains(r.in.Log, "key 1 0") })
}

func TestTheNewestArmTakesThePress(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	older, newer := escapeClient(t, r.h), escapeClient(t, r.h)
	_, oldEv := older.arm()
	_, newEv := newer.arm()
	r.h.tap(keyEscape)
	newer.fired(newEv)
	older.neverFired(oldEv)
	r.nothingLeakedBeforeASentinel()
	r.h.tap(keyEscape)
	older.fired(oldEv)
	r.nothingLeakedBeforeASentinel()
}

func TestABindOnEscapeWinsOverAnArm(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withBinds(t, bindSpec{chord: "Escape", action: "workspace 2"}), small)
	r := inputOn(t, h)
	r.focused()
	a := escapeClient(t, h)
	_, ev := a.arm()
	h.tap(keyEscape)
	h.Eventually("the bind ran", showingWorkspace(2))
	a.neverFired(ev)
}

func TestAnArmGivesWayToTheSessionLock(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	l := lockSession(t, r.h)
	l.seen("locked")
	l.cover(image.Pt(160, 100), 0xff, 0, 0)
	l.c.DispatchUntil("the keyboard on the lock surface", func() bool { return slices.Contains(l.in.Log, "kb_enter lock") })
	r.h.tap(keyEscape)
	l.c.DispatchUntil("Escape at the lock", func() bool { return slices.Contains(l.in.Log, "key 1 0") })
	if !slices.Contains(l.in.Log, "key 1 1") {
		t.Fatalf("the lock surface saw %q", l.in.Log)
	}
	a.neverFired(ev)
}

func TestAnArmGivesWayToAnExclusiveLayer(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	c := testclient.Connect(t, r.h.Socket())
	_, in := c.Seat()
	launcher := c.Layer(testclient.LayerSpec{
		Layer:         cwlr.ZwlrLayerShellV1LayerOverlay,
		Size:          image.Pt(60, 30),
		Interactivity: cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityExclusive,
	}, testclient.Fill(0x10, 0x20, 0x30))
	in.Name(launcher.Surface, "launcher")
	c.DispatchUntil("the launcher focused", func() bool { return slices.Contains(in.Log, "kb_enter launcher") })
	r.h.tap(keyEscape)
	c.DispatchUntil("Escape at the launcher", func() bool { return slices.Contains(in.Log, "key 1 0") })
	a.neverFired(ev)
}

func TestAnArmGivesWayToAPopupGrab(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	r.moveOn(0, 70, 40)
	r.expect("motion 70,40", "frame")
	r.click()
	r.expect("button 272 1", "frame", "button 272 0", "frame")
	menu := r.c.Popup(r.w, menuAt(image.Pt(70, 40), image.Pt(40, 30)), &testclient.Grab{Seat: r.seat, Serial: r.in.Press}, testclient.Fill(0x80, 0x80, 0x80))
	r.in.Name(menu.Surface, "menu")
	r.c.DispatchUntil("the menu to map", menu.Mapped)
	r.expect("kb_leave win", "kb_enter menu")
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	r.escapeReachesTheWindow()
	a.neverFired(ev)
}

func TestAnArmGivesWayToAnInputMethodGrab(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	ty := typing(t, h, "editor")
	until(t, ty.c, "the focused text input entered", &ty.log.log, "enter")
	a := escapeClient(t, h)
	_, ev := a.arm()
	m := inputMethod(t, h)
	grab := &grabLog{}
	if _, err := m.im.GrabKeyboard(grab); err != nil {
		t.Fatal(err)
	}
	m.c.DispatchUntil("the grab's keymap", func() bool { return grab.keymap != 0 })
	h.tap(keyEscape)
	m.c.DispatchUntil("the grabbed Escape", func() bool { return len(grab.log) == 2 })
	a.neverFired(ev)
}

func TestAnArmGivesWayToAShortcutsInhibitor(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	mgr, err := cksi.NewZwpKeyboardShortcutsInhibitManagerV1(r.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.c.Bind(mgr)
	l := &constraintLog{}
	if _, err := mgr.InhibitShortcuts(l, r.w.Surface, r.seat); err != nil {
		t.Fatal(err)
	}
	r.await(l, "active")
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	r.escapeReachesTheWindow()
	a.neverFired(ev)
}

func TestAnArmedClientHoldingTheKeyboardGetsEscapeAsAKey(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	a := armerOn(t, r.c)
	_, ev := a.arm()
	r.escapeReachesTheWindow()
	a.neverFired(ev)
}

func TestAnArmFiresWithNothingFocused(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	a := escapeClient(t, h)
	_, ev := a.arm()
	h.tap(keyEscape)
	a.fired(ev)
}

func virtualKeyboardWith(t *testing.T, c *testclient.Client, text string) *cvk.ZwpVirtualKeyboardV1 {
	t.Helper()
	seat, _ := c.Seat()
	mgr, err := cvk.NewZwpVirtualKeyboardManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	kb, err := mgr.CreateVirtualKeyboard(seat)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.MemfdCreate("keymap", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), "keymap")
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(text + "\x00"); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(kb.Keymap(uint32(cw.WlKeyboardKeymapFormatXkbV1), fd, uint32(len(text)+1)), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	return kb
}

func virtualTap(t *testing.T, c *testclient.Client, kb *cvk.ZwpVirtualKeyboardV1, code uint32) {
	t.Helper()
	if err := errors.Join(kb.Key(1, code, uint32(cw.WlKeyboardKeyStatePressed)), kb.Key(2, code, uint32(cw.WlKeyboardKeyStateReleased)), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
}

func TestAnArmTakesEscapeFromAVirtualKeyboard(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	typist := testclient.Connect(t, r.h.Socket())
	kb := virtualKeyboardWith(t, typist, testenv.Keymap(t).Text())
	a := escapeClient(t, r.h)
	_, ev := a.arm()

	virtualTap(t, typist, kb, keyEscape)
	a.fired(ev)
	virtualTap(t, typist, kb, keyA)
	r.expect("key 30 1", "key 30 0")

	virtualTap(t, typist, kb, keyEscape)
	r.expect("key 1 1", "key 1 0")
}

func TestAVirtualModifiedEscapeReachesTheWindow(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	typist := testclient.Connect(t, r.h.Socket())
	kb := virtualKeyboardWith(t, typist, testenv.Keymap(t).Text())
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	shift, err := testenv.Keymap(t).ModMask("Shift")
	if err != nil {
		t.Fatal(err)
	}
	if err := kb.Modifiers(shift, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	virtualTap(t, typist, kb, keyEscape)
	r.expect("key 1 1", "key 1 0")
	a.neverFired(ev)
}

func TestAVirtualKeymapDecidesWhatIsEscape(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	remapped := strings.ReplaceAll(testenv.Keymap(t).Text(), "[ 0xff1b ]", "[ a ]")
	typist := testclient.Connect(t, r.h.Socket())
	kb := virtualKeyboardWith(t, typist, remapped)
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	virtualTap(t, typist, kb, keyEscape)
	r.expect("key 1 1", "key 1 0")
	a.neverFired(ev)
}

func TestAnUncompilableVirtualKeymapPassesItsKeys(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.focused()
	typist := testclient.Connect(t, r.h.Socket())
	kb := virtualKeyboardWith(t, typist, "xkb_keymap {")
	a := escapeClient(t, r.h)
	_, ev := a.arm()
	virtualTap(t, typist, kb, keyEscape)
	virtualTap(t, typist, kb, keyEscape)
	r.expect("key 1 1", "key 1 0", "key 1 1", "key 1 0")
	a.neverFired(ev)
}
