package harness

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	chgs "github.com/stubbedev/lys-wm/internal/protocol/client/hyprlandglobalshortcuts"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const (
	key1 = 2
	keyS = 31
	keyD = 32
	keyF = 33
	keyB = 48
)

type shortcutRig struct {
	t    *testing.T
	h    *Harness
	c    *testclient.Client
	mgr  *chgs.HyprlandGlobalShortcutsManagerV1
	logs map[string]*shortcutLog
}

func startShortcuts(t *testing.T, ids []string, specs ...bindSpec) *shortcutRig {
	h := StartWith(t, withBinds(t, specs...), small)
	c := testclient.Connect(t, h.Socket())
	mgr, err := chgs.NewHyprlandGlobalShortcutsManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	r := &shortcutRig{t: t, h: h, c: c, mgr: mgr, logs: map[string]*shortcutLog{}}
	for _, id := range ids {
		l := &shortcutLog{}
		if _, err := mgr.RegisterShortcut(l, id, "t", id, ""); err != nil {
			t.Fatal(err)
		}
		r.logs[id] = l
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *shortcutRig) key(at time.Duration, code uint32, pressed bool) {
	r.h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		return comp.Input(b.Keyboard(), &backend.Key{Time: at, Code: code, Pressed: pressed})
	})
}

func (r *shortcutRig) button(at time.Duration, code uint32, pressed bool) {
	r.h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		return errors.Join(comp.Input(b.Pointer(), &backend.PointerButton{Time: at, Button: code, Pressed: pressed}), comp.Input(b.Pointer(), &backend.PointerFrame{}))
	})
}

func (r *shortcutRig) fired(id string, want int) {
	r.t.Helper()
	if err := r.c.Display.Roundtrip(); err != nil {
		r.t.Fatal(err)
	}
	if got := len(r.logs[id].events); got != want*2 {
		r.t.Fatalf("%s: %d shortcut events %q, want %d presses with their releases", id, got, r.logs[id].events, want)
	}
	for i := 0; i < len(r.logs[id].events); i += 2 {
		if r.logs[id].events[i] != "pressed" || r.logs[id].events[i+1] != "released" {
			r.t.Fatalf("%s: events %q are not press-release pairs", id, r.logs[id].events)
		}
	}
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestALoneSuperTapFiresAndChordsDuringTheHoldDoNot(t *testing.T) {
	t.Parallel()
	r := startShortcuts(t, []string{"launcher"},
		bindSpec{chord: "SUPER", action: "global t:launcher", description: "Open the launcher"},
		bindSpec{chord: "SUPER + 1", action: "workspace 2"},
	)
	r.key(ms(1000), keySuper, true)
	r.key(ms(1100), keySuper, false)
	r.fired("launcher", 1)

	r.key(ms(2000), keySuper, true)
	r.key(ms(2050), key1, true)
	r.key(ms(2100), key1, false)
	r.key(ms(2200), keySuper, false)
	r.h.Eventually("SUPER + 1 to switch to workspace 2", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws, _ := comp.WM().ActiveOn(comp.WM().FocusedOutput())
		return ws == 2
	})
	r.fired("launcher", 1)

	r.key(ms(3000), keySuper, true)
	r.button(ms(3050), btnLeft, true)
	r.button(ms(3100), btnLeft, false)
	r.key(ms(3200), keySuper, false)
	r.fired("launcher", 1)

	r.key(ms(4000), keySuper, true)
	r.key(ms(4100), keySuper, false)
	r.fired("launcher", 2)
}

func TestReleaseLongPressAndCooldownJudgeEventTimes(t *testing.T) {
	t.Parallel()
	r := startShortcuts(t, []string{"release", "long", "cool"},
		bindSpec{chord: "A", action: "global t:release", flags: binds.Flags{Release: true}},
		bindSpec{chord: "S", action: "global t:long", flags: binds.Flags{LongPress: ms(500)}},
		bindSpec{chord: "D", action: "global t:cool", flags: binds.Flags{Cooldown: ms(300)}},
	)
	r.key(ms(1000), 30, true)
	r.fired("release", 0)
	r.key(ms(1010), 30, false)
	r.fired("release", 1)
	r.key(ms(1100), 30, true)
	r.key(ms(1110), keyB, true)
	r.key(ms(1120), keyB, false)
	r.key(ms(1130), 30, false)
	r.fired("release", 1)

	r.key(ms(10000), keyS, true)
	r.key(ms(10499), keyS, false)
	r.fired("long", 0)
	r.key(ms(11000), keyS, true)
	r.key(ms(11500), keyS, false)
	r.fired("long", 1)

	for _, at := range []int{20000, 20100, 20299, 20300, 20650} {
		r.key(ms(at), keyD, true)
		r.key(ms(at+5), keyD, false)
	}
	r.fired("cool", 3)
}

func TestAPassBindLetsTheClientHaveTheKey(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withBinds(t,
		bindSpec{chord: "F", action: "global t:pass", flags: binds.Flags{Pass: true}},
		bindSpec{chord: "A", action: "global t:eat"},
	), small)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	mgr, err := chgs.NewHyprlandGlobalShortcutsManagerV1(r.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.c.Bind(mgr)
	pass, eat := &shortcutLog{}, &shortcutLog{}
	if _, err := mgr.RegisterShortcut(pass, "pass", "t", "pass", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.RegisterShortcut(eat, "eat", "t", "eat", ""); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Keyboard().Key(30, true), b.Keyboard().Key(30, false), b.Keyboard().Key(keyF, true), b.Keyboard().Key(keyF, false))
	})
	r.expect("key 33 1", "key 33 0")
	r.c.DispatchUntil("both shortcuts", func() bool { return len(pass.events) == 2 && len(eat.events) == 2 })
}

func TestAKeyEventLooksUpBindsOnceWithoutAllocating(t *testing.T) {
	h := StartUntraced(t, small)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		press, release := &backend.Key{Code: 30, Pressed: true}, &backend.Key{Code: 30}
		cycle := func() {
			if err := errors.Join(comp.Input(b.Keyboard(), press), comp.Input(b.Keyboard(), release)); err != nil {
				t.Fatal(err)
			}
		}
		for range 20 {
			cycle()
		}
		before := comp.BindLookups()
		cycle()
		if n := comp.BindLookups() - before; n != 1 {
			t.Errorf("a key press and release looked binds up %d times, want 1", n)
		}
		if n := allocsPerRun(100, func() {}, cycle); n != 0 {
			t.Errorf("a key press and release allocated %d times", n)
		}
		return nil
	})
}

func TestBindsAreQueriedAndAnnouncedOnReload(t *testing.T) {
	t.Parallel()
	var next compositor.Config
	var comp *compositor.Compositor
	h := StartWith(t, func(cfg *compositor.Config) {
		withBinds(t,
			bindSpec{chord: "SUPER", action: "exec launcher", description: "Open the launcher"},
			bindSpec{chord: "SUPER + Q", action: "kill", flags: binds.Flags{Release: true, InhibitProof: true, Cooldown: ms(250)}},
		)(cfg)
		next = compositor.PlainConfig(cfg.Seat.Keymap, cfg.Font)
		next.Report = cfg.Report
		withBinds(t, bindSpec{chord: "SUPER + R", action: "reload", description: "Reload the config"})(&next)
	}, small)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		comp = c
		return nil
	})
	got := query[[]ipc.Bind](h, wmipc.KindBinds)
	want := []ipc.Bind{
		{Chord: "SUPER", Action: "exec launcher", Description: "Open the launcher", Flags: []string{"release"}},
		{Chord: "SUPER + Q", Action: "kill", Flags: []string{"release", "inhibit_proof"}, CooldownMS: 250},
	}
	if !slices.EqualFunc(got, want, func(a, b ipc.Bind) bool { return fmt.Sprint(a) == fmt.Sprint(b) }) {
		t.Fatalf("binds %+v, want %+v", got, want)
	}
	lines := h.watch()
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return comp.Reconfigure(next) })
	expectEvent(t, lines, `{"event":"binds","binds":[{"submap":"","chord":"SUPER + R","action":"reload","description":"Reload the config","flags":[]}]}`)
	if got := query[[]ipc.Bind](h, wmipc.KindBinds); len(got) != 1 || !strings.Contains(got[0].Description, "Reload") {
		t.Fatalf("binds after the reload: %+v", got)
	}
}

func TestReleasingTheModifiersOfAHeldChordIsReported(t *testing.T) {
	t.Parallel()
	r := startShortcuts(t, nil)
	released := 0
	r.key(ms(1000), keySuper, true)
	r.key(ms(1010), keyShift, true)
	r.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		mods := comp.Seat().DepressedModifiers()
		comp.OnModifiersReleased(mods, func() error {
			released++
			return nil
		})
		return nil
	})
	r.key(ms(1020), key1, true)
	r.key(ms(1030), key1, false)
	if released != 0 {
		t.Fatal("releasing a key that is no modifier reported the modifiers released")
	}
	r.key(ms(1040), keyShift, false)
	r.key(ms(1050), keySuper, false)
	if released != 1 {
		t.Fatalf("releasing the modifiers reported %d times, want once", released)
	}
}
