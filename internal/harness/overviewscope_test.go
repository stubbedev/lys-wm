package harness

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	keyX         = 45
	keyBackSpace = 14
)

func appWindow(t *testing.T, c *testclient.Client, title, app string) *testclient.Window {
	t.Helper()
	w := c.Window(title, testclient.Fill(0x40, 0x80, 0xc0))
	c.DispatchUntil(title+" to map", w.Mapped)
	if err := errors.Join(w.Toplevel.SetAppID(app), w.Surface.Commit(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	return w
}

func (h *Harness) listed(output int) []wm.WindowID {
	h.t.Helper()
	var ids []wm.WindowID
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		for _, o := range c.Outputs() {
			if o.Backend() == b.Outputs()[output] {
				ids = c.OverviewListed(o)
			}
		}
		return nil
	})
	slices.Sort(ids)
	return ids
}

func (h *Harness) idsOf(titles ...string) []wm.WindowID {
	h.t.Helper()
	ids := make([]wm.WindowID, len(titles))
	for i, title := range titles {
		ids[i] = h.idOf(title)
	}
	slices.Sort(ids)
	return ids
}

func (h *Harness) expectListed(output int, want []wm.WindowID) {
	h.t.Helper()
	if got := h.listed(output); !slices.Equal(got, want) {
		h.t.Fatalf("output %d lists %v, want %v", output, got, want)
	}
}

func TestTheAppScopeListsOneAppAcrossWorkspacesAndOutputs(t *testing.T) {
	t.Parallel()
	h := StartWith(t, plainOverview, overviewMode, overviewMode)
	c := testclient.Connect(t, h.Socket())
	appWindow(t, c, "fire1", "firefox")
	appWindow(t, c, "fire2", "firefox")
	h.dispatch("movetoworkspace empty")
	appWindow(t, c, "term1", "foot")
	h.dispatch("focusoutput right")
	appWindow(t, c, "fire3", "firefox")
	appWindow(t, c, "term2", "foot")
	h.dispatch("focusoutput left")
	h.dispatch(fmt.Sprintf("focuswindow %d", h.idOf("fire1")))
	away, _ := h.workspaceOfWindow(h.idOf("fire2"))
	h.dispatch("overview show app")
	h.expectOverview(true)
	h.expectListed(0, h.idsOf("fire1", "fire2", "fire3"))
	h.expectListed(1, nil)
	fire2 := h.idOf("fire2")
	h.clickAt(h.tileCentre(fire2))
	h.expectOverview(false)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if got := comp.Snapshot().Outputs[0].Workspace; got != away {
			t.Errorf("clicking fire2 shows workspace %d, want its %d", got, away)
		}
		if focused, _ := comp.WM().Focused(); focused != fire2 {
			t.Errorf("window %d focused, want fire2", focused)
		}
		return nil
	})
}

func (h *Harness) workspaceOfWindow(id wm.WindowID) (wm.WorkspaceID, bool) {
	var ws wm.WorkspaceID
	var ok bool
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		ws, ok = c.WM().WorkspaceOf(id)
		return nil
	})
	return ws, ok
}

func TestTheOutputScopeShowsEveryWorkspaceAndADragMovesBetweenThem(t *testing.T) {
	t.Parallel()
	h := StartWith(t, plainOverview, overviewMode)
	c := testclient.Connect(t, h.Socket())
	appWindow(t, c, "a", "one")
	appWindow(t, c, "b", "two")
	appWindow(t, c, "c", "three")
	h.dispatch("movetoworkspace empty")
	moved, _ := h.workspaceOfWindow(h.idOf("c"))
	if moved == 1 {
		t.Fatal("c stayed on workspace 1")
	}
	h.dispatch("overview show")
	h.expectListed(0, h.idsOf("a", "b"))
	h.dispatch("overview show output")
	h.expectListed(0, h.idsOf("a", "b", "c"))
	dragBetween(h, h.tileCentre(h.idOf("c")), h.tileCentre(h.idOf("a")))
	h.expectOverview(true)
	if ws, _ := h.workspaceOfWindow(h.idOf("c")); ws != 1 {
		t.Fatalf("c dragged into workspace 1's group is on workspace %d", ws)
	}
	h.expectListed(0, h.idsOf("a", "b", "c"))
}

func TestTypingFiltersTheOverviewAndBackSpaceRestoresIt(t *testing.T) {
	t.Parallel()
	h := StartWith(t, plainOverview, overviewMode)
	c := testclient.Connect(t, h.Socket())
	windows := []*testclient.Window{
		appWindow(t, c, "Firefox one", "firefox"),
		appWindow(t, c, "Terminal", "foot"),
		appWindow(t, c, "Mozilla Firefox two", "org.mozilla.firefox"),
	}
	h.dispatch("overview show")
	every := h.idsOf("Firefox one", "Terminal", "Mozilla Firefox two")
	h.expectListed(0, every)
	h.tap(keyF, keyI, keyR)
	h.expectListed(0, h.idsOf("Firefox one", "Mozilla Firefox two"))
	h.tap(keyBackSpace, keyBackSpace, keyBackSpace)
	h.expectListed(0, every)
	h.tap(keyF, keyI, keyR, keyEsc)
	h.expectOverview(true)
	h.expectListed(0, every)
	h.withAlt(keyX)
	c.DispatchUntil("Alt + x to close the ringed window", func() bool {
		return slices.ContainsFunc(windows, func(w *testclient.Window) bool { return w.Closed })
	})
	h.tap(keyEsc)
	h.expectOverview(false)
}

func TestFilteringRedrawsNoThumbnailButACommitDoes(t *testing.T) {
	h := Start(t, overviewMode)
	c := testclient.Connect(t, h.Socket())
	fire := appWindow(t, c, "Firefox", "firefox")
	appWindow(t, c, "Terminal", "foot")
	h.dispatch("overview show")
	h.Rendered(0)
	h.Eventually("the thumbnail drawn and filtered", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return comp.OverviewThumbRedraws() >= 1 && !comp.OverviewThumbsStale()
	})
	redraws := func() uint64 {
		var n uint64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			n = comp.OverviewThumbRedraws()
			return nil
		})
		return n
	}
	frames := func() {
		h.t.Helper()
		h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
			for range 5 {
				if err := comp.Frame(b.Outputs()[0]); err != nil {
					return err
				}
			}
			return nil
		})
	}
	frames()
	first := redraws()
	h.tap(keyF, keyI, keyR)
	h.expectListed(0, h.idsOf("Firefox"))
	frames()
	h.tap(keyBackSpace)
	frames()
	if got := redraws(); got != first {
		t.Fatalf("filtering redrew %d thumbnails", got-first)
	}
	if err := errors.Join(fire.Redraw(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the committed window redrawing its thumbnail", func(b *headless.Backend, comp *compositor.Compositor) bool {
		if err := comp.Frame(b.Outputs()[0]); err != nil {
			t.Fatal(err)
		}
		return comp.OverviewThumbRedraws() > first
	})
}

func TestDownOnTheLowestWindowShowsItsAppAndUpGoesBack(t *testing.T) {
	t.Parallel()
	h := StartWith(t, plainOverview, overviewMode)
	c := testclient.Connect(t, h.Socket())
	appWindow(t, c, "fire1", "firefox")
	appWindow(t, c, "term", "foot")
	appWindow(t, c, "fire2", "firefox")
	h.dispatch("movetoworkspace empty")
	h.dispatch("workspace 1")
	h.dispatch(fmt.Sprintf("focuswindow %d", h.idOf("fire1")))
	h.dispatch("overview show")
	h.expectListed(0, h.idsOf("fire1", "term"))
	h.tap(keyDown)
	h.expectListed(0, h.idsOf("fire1", "fire2"))
	h.tap(keyUp)
	h.expectListed(0, h.idsOf("fire1", "term"))
}

func TestASwipeWithAScopeOpensThatScope(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		plainOverview(cfg)
		cfg.Gestures.Binds = []binds.GestureBind{{Kind: backend.Swipe, Fingers: 4, Direction: binds.SwipeDown, Drive: binds.DriveOverview, Scope: wm.OverviewApp}}
	}, overviewMode)
	c := testclient.Connect(t, h.Socket())
	appWindow(t, c, "fire", "firefox")
	appWindow(t, c, "term", "foot")
	h.dispatch(fmt.Sprintf("focuswindow %d", h.idOf("fire")))
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureBegin(backend.Swipe, 4) })
	h.swipeOverview(1000)
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureEnd(backend.Swipe, false) })
	h.expectOverview(true)
	h.expectListed(0, h.idsOf("fire"))
}
