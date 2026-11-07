package harness

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func scratchpadsOf(c *compositor.Compositor) []ipc.Scratchpad {
	return c.Snapshot().Scratchpads
}

func TestAScratchpadRunsItsExecAndTogglesOverTheFocusedOutput(t *testing.T) {
	t.Parallel()
	flower := testenv.Require(t, "weston-flower")
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.WM.Scratchpads = map[wm.ScratchpadName]wm.ScratchpadSpec{"term": {Exec: flower, Size: wm.Extent{W: wm.Length{N: 50, Percent: true}, H: wm.Length{N: 50, Percent: true}}}}
	}, small, small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("base to map", c.Window("base", fillGreen).Mapped)
	h.dispatch("scratchpad toggle term")
	h.Eventually("the exec'd window in the shown scratchpad, focused", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws := windowsOf(comp)
		sp := scratchpadsOf(comp)
		return len(ws) == 2 && ws[1].Scratchpad == "term" && ws[1].Workspace == 0 && ws[1].Focused && ws[1].Stack != nil && *ws[1].Stack == 0 &&
			len(sp) == 1 && sp[0].Shown && sp[0].Output == "HEADLESS-1" && sp[0].Windows == 1
	})
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if r := windowsOf(comp)[1].Rect; r.Width > 80 || r.Height > 50 || r.X < 40 {
			t.Errorf("the scratchpad window at %+v, want inside the middle half of the output", r)
		}
		return nil
	})
	h.dispatch("scratchpad toggle term")
	h.Eventually("hidden, focus back on base", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws := windowsOf(comp)
		sp := scratchpadsOf(comp)
		return len(ws) == 2 && ws[0].Focused && ws[1].Stack == nil && len(sp) == 1 && !sp[0].Shown
	})
	h.dispatch("focus right")
	h.dispatch("scratchpad toggle term")
	h.Eventually("shown on the second output, no second exec", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws := windowsOf(comp)
		sp := scratchpadsOf(comp)
		return len(ws) == 2 && ws[1].Output == "HEADLESS-2" && ws[1].Focused && len(sp) == 1 && sp[0].Shown && sp[0].Output == "HEADLESS-2"
	})
}

func TestWindowsMoveIntoAndOutOfAScratchpadAndAWorkspaceSwitchHidesIt(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.WM.Scratchpads = map[wm.ScratchpadName]wm.ScratchpadSpec{"stays": {Stays: true}}
	}, small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("a to map", c.Window("a", fillGreen).Mapped)
	b := c.Window("b", fillGreen)
	c.DispatchUntil("b to map", b.Mapped)
	h.dispatch("movetoscratchpad notes")
	h.Eventually("b hidden in notes", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		w, ok := windowNamed(comp, "b")
		return ok && w.Scratchpad == "notes" && w.Stack == nil && len(scratchpadsOf(comp)) == 1
	})
	h.dispatch("scratchpad toggle notes")
	h.Eventually("notes shown", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		w, _ := windowNamed(comp, "b")
		return w.Focused && w.Stack != nil
	})
	h.dispatch("workspace 2")
	h.Eventually("the switch hid notes", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		w, _ := windowNamed(comp, "b")
		sp := scratchpadsOf(comp)
		return w.Stack == nil && len(sp) == 1 && !sp[0].Shown
	})

	h.dispatch("scratchpad toggle notes")
	h.dispatch("movetoworkspace 2")
	h.Eventually("b back on workspace 2, its scratchpad hidden and dropped once empty", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		w, _ := windowNamed(comp, "b")
		return w.Scratchpad == "" && w.Workspace == 2 && w.Focused && len(scratchpadsOf(comp)) == 0
	})

	h.dispatch("movetoscratchpad stays")
	h.dispatch("scratchpad toggle stays")
	h.dispatch("workspace 1")
	h.Eventually("a scratchpad that stays still shown after a switch", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		w, _ := windowNamed(comp, "b")
		return w.Scratchpad == "stays" && w.Stack != nil
	})
}

func TestAHiddenScratchpadCostsNoFrames(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	a := c.Window("a", fillGreen)
	c.DispatchUntil("a to map", a.Mapped)
	b := c.Window("b", fillGreen)
	c.DispatchUntil("b to map", b.Mapped)
	a.Stop()
	b.Stop()
	h.dispatch("movetoscratchpad notes")
	h.settled(c, "b hidden", func(w ipc.Window) bool { return w.Stack == nil }, "b")
	var frames uint64
	h.Eventually("the output idle", func(be *headless.Backend, comp *compositor.Compositor) bool {
		frames = be.Outputs()[0].Frames()
		return atRest(be, comp)
	})
	for range 10 {
		if err := b.Redraw(); err != nil {
			t.Fatal(err)
		}
		c.Sync()
	}
	h.Do(func(be *headless.Backend, _ *compositor.Compositor) error {
		if n, owed := be.Outputs()[0].Frames()-frames, be.Outputs()[0].FrameOwed(); n != 0 || owed {
			t.Errorf("10 commits of a hidden scratchpad's window ran %d output frames and left one owed: %v, want none", n, owed)
		}
		return nil
	})
}

func TestAWindowAScratchpadLaunchedLandsInItAfterItWasHidden(t *testing.T) {
	t.Parallel()
	flower := testenv.Require(t, "weston-flower")
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.WM.Scratchpads = map[wm.ScratchpadName]wm.ScratchpadSpec{"term": {Exec: flower}}
	}, small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("base to map", c.Window("base", fillGreen).Mapped)
	h.dispatch("scratchpad toggle term")
	h.dispatch("scratchpad hide")
	h.Eventually("weston-flower, matched by its process, in the hidden scratchpad", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws := windowsOf(comp)
		sp := scratchpadsOf(comp)
		return len(ws) == 2 && ws[1].Scratchpad == "term" && ws[1].Stack == nil && ws[0].Focused && len(sp) == 1 && !sp[0].Shown
	})
}
