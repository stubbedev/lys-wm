package harness

import (
	"errors"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/config"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestAnIdleInhibitRuleHoldsOffIdleWhileTheWindowShows(t *testing.T) {
	t.Parallel()
	yes := true
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Rules = []wm.Rule{titled("mpv", wm.Decision{Fullscreen: &yes, IdleInhibit: wm.IdleInhibitVisible})}
	}, small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("mpv to map", c.Window("mpv", fillGreen).Mapped)
	h.settled(c, "mpv fullscreen", func(w ipc.Window) bool { return w.Fullscreen && w.Stack != nil }, "mpv")
	i := newIdler(t, h)
	held := i.watch(heldTimeout, false)
	i.stillHeldOnceInputIdles(held)
	h.dispatch("workspace 2")
	i.c.DispatchUntil("idle once mpv is hidden", func() bool { return slices.Equal(held.log, []string{"idled"}) })
	h.dispatch("workspace 1")
	i.c.DispatchUntil("resumed when it shows again", func() bool { return slices.Equal(held.log, []string{"idled", "resumed"}) })
}

func TestACapturedRuleTurnsTheBorderRedWhileACaptureRuns(t *testing.T) {
	t.Parallel()
	yes := true
	red := render.SolidBorder(render.Color{R: 0xff, A: 0xff})
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.BorderWidth = 4
		cfg.WM.Gaps = wm.Gaps{}
		cfg.Rules = []wm.Rule{{Match: wm.Match{Title: regexp.MustCompile("^shared$"), Captured: &yes}, ActiveBorder: &red}}
	}, small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("shared to map", c.Window("shared", fillGreen).Mapped)
	edge := [3]byte{0xff, 0, 0}
	h.Eventually("the usual border", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[0]), 1, 50) != edge && pixel(snapshot(b.Outputs()[0]), 1, 50) != [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	})
	r := newCaptureRig(t, h, c)
	session, _ := r.outputSession(0)
	h.Eventually("the border red while captured", showing(1, 50, edge))
	if err := errors.Join(session.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the border back once the capture stops", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[0]), 1, 50) != edge
	})
}

func TestPerWindowLookRulesApply(t *testing.T) {
	t.Parallel()
	no := false
	width, square := 6, 0
	thick := render.SolidBorder(render.Color{G: 0xff, A: 0xff})
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.DefaultDecor(compositor.DefaultAccent)
		cfg.Decor.BorderWidth, cfg.Decor.Rounding = 1, 8
		cfg.WM.Gaps = wm.Gaps{}
		cfg.Rules = []wm.Rule{titled("plain", wm.Decision{BorderWidth: &width, InactiveBorder: &thick, ActiveBorder: &thick, Rounding: &square, Shadow: &no})}
	}, desktop)
	c := testclient.Connect(t, h.Socket())
	plain := c.Window("plain", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("plain to map", plain.Mapped)
	c.DispatchUntil("other to map", c.Window("other", testclient.Fill(0, 0, 0xff)).Mapped)
	h.settled(c, "both laid", func(ipc.Window) bool { return true }, "other")
	green, red := [3]byte{0, 0xff, 0}, [3]byte{0xff, 0, 0}
	h.Eventually("plain's 6 px square border, other's rounded 1 px one", func(b *headless.Backend, comp *compositor.Compositor) bool {
		s := snapshot(b.Outputs()[0])
		return pixel(s, 0, 0) == green && pixel(s, 5, 400) == green && pixel(s, 6, 400) == red && pixel(s, 641, 400) == red && pixel(s, 640, 0) != red
	})
	if plain.Size.X != 640-12 {
		t.Fatalf("plain configured %v wide, want its tile less a 6 px border each side", plain.Size)
	}
}

func TestAnAnimationFalseRuleMapsAndClosesWithoutAnimating(t *testing.T) {
	t.Parallel()
	now := time.Hour
	table := anim.MustResolve(anim.DefaultMotion())
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
		cfg.Rules = []wm.Rule{titled("still", wm.Decision{Animation: &wm.WindowAnimation{Off: true}})}
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("still", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("still to map", w.Mapped)
	h.Eventually("drawn in full at once with the clock stopped", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[0]), 2, 2) == [3]byte{0xff, 0, 0} && !comp.Animating(comp.Outputs()[0])
	})
	if err := errors.Join(w.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("gone at once", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[0]), 80, 50) != [3]byte{0xff, 0, 0} && !comp.Animating(comp.Outputs()[0])
	})
}

func TestFocusChangesReevaluateOnlyStateRules(t *testing.T) {
	t.Parallel()
	yes := true
	stateRule := wm.Rule{Match: wm.Match{Title: regexp.MustCompile("."), Focused: &yes}, Shadow: &yes}
	plainRule := wm.Rule{Match: wm.Match{Title: regexp.MustCompile("^a$")}, Shadow: &yes}
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Rules = []wm.Rule{plainRule, stateRule, stateRule, plainRule} }, small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("a to map", c.Window("a", fillGreen).Mapped)
	c.DispatchUntil("b to map", c.Window("b", fillGreen).Mapped)
	h.settled(c, "b focused", func(w ipc.Window) bool { return w.Focused }, "b")
	var before int
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		before = comp.RuleEvaluations()
		return nil
	})
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error { return b.Outputs()[0].ScheduleFrame() })
	h.settled(c, "a steady frame", func(ipc.Window) bool { return true }, "b")
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if n := comp.RuleEvaluations() - before; n != 0 {
			t.Errorf("a steady frame evaluated %d rules", n)
		}
		before = comp.RuleEvaluations()
		return nil
	})
	h.dispatch("focus left")
	h.settled(c, "a focused", func(w ipc.Window) bool { return w.Focused }, "a")
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if n := comp.RuleEvaluations() - before; n != 2*2 {
			t.Errorf("a focus change evaluated %d rules, want 2 state rules for each of the 2 windows it touched", n)
		}
		return nil
	})
}

func TestSetpropAppliesAtOnceAndGoesWithTheWindow(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Hooks.ParseProp = config.ParseProp
		cfg.Decor.BorderWidth = 2
		cfg.WM.Gaps = wm.Gaps{}
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("w", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("w to map", w.Mapped)
	h.settled(c, "w laid", func(ipc.Window) bool { return true }, "w")
	if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: wmipc.KindDispatch, Action: "setprop 1 border {width: 10, active: \"#00ff00\"}"}); err != nil {
		t.Fatal(err)
	}
	h.Eventually("a 10 px green border", showing(9, 50, [3]byte{0, 0xff, 0}))
	c.DispatchUntil("w reconfigured inside it", func() bool { return w.Size.X == 160-20 })
	if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: wmipc.KindDispatch, Action: "setprop 1 border {width: nine}"}); err == nil {
		t.Fatal("a bad value was taken")
	}
	if err := errors.Join(w.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	next := c.Window("w", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("a new w to map", next.Mapped)
	c.DispatchUntil("the new window has the plain border", func() bool { return next.Size.X == 160-4 })
}
