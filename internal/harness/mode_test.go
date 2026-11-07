package harness

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func gapped(cfg *compositor.Config) {
	cfg.WM.Gaps = wm.Gaps{Inner: 6, Outer: wm.Insets{Top: 4, Right: 8, Bottom: 4, Left: 8}}
	cfg.Decor.BorderWidth = 2
}

func windowNamed(c *compositor.Compositor, title string) (ipc.Window, bool) {
	ws := windowsOf(c)
	i := slices.IndexFunc(ws, func(w ipc.Window) bool { return w.Title == title })
	if i < 0 {
		return ipc.Window{}, false
	}
	return ws[i], true
}

func (h *Harness) settled(c *testclient.Client, what string, cond func(ipc.Window) bool, title string) {
	h.t.Helper()
	h.Eventually(what, func(_ *headless.Backend, comp *compositor.Compositor) bool {
		w, ok := windowNamed(comp, title)
		return ok && cond(w) && !comp.Holding()
	})
	c.Sync()
}

func TestAClientMaximizeFillsTheUsableAreaAndUnmaximizeReturnsItToItsTile(t *testing.T) {
	t.Parallel()
	h := StartWith(t, gapped, desktop)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("first to map", c.Window("first", fillGreen).Mapped)
	w := c.Window("second", fillGreen)
	c.DispatchUntil("second to map", w.Mapped)
	h.settled(c, "both tiled", func(ipc.Window) bool { return true }, "second")
	tile := w.Size
	before := w.Configures

	if err := errors.Join(w.Toplevel.SetMaximized(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.settled(c, "second maximized", func(x ipc.Window) bool { return x.Maximized && !x.Fullscreen }, "second")
	if want := image.Pt(1280-16-4, 800-8-4); w.Size != want || !slices.Contains(w.States, cxdg.XdgToplevelStateMaximized) || slices.Contains(w.States, cxdg.XdgToplevelStateTiledLeft) {
		t.Fatalf("maximized to %v with %v, want %v with the maximized state and no tiled ones", w.Size, w.States, want)
	}
	if n := w.Configures - before; n != 1 {
		t.Fatalf("maximizing sent %d configures, want 1", n)
	}

	before = w.Configures
	if err := errors.Join(w.Toplevel.UnsetMaximized(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.settled(c, "second back in its tile", func(x ipc.Window) bool { return !x.Maximized }, "second")
	if w.Size != tile || slices.Contains(w.States, cxdg.XdgToplevelStateMaximized) {
		t.Fatalf("unmaximized to %v with %v, want its tile %v", w.Size, w.States, tile)
	}
	if n := w.Configures - before; n != 1 {
		t.Fatalf("unmaximizing sent %d configures, want 1", n)
	}

	h.dispatch("maximize")
	h.settled(c, "second maximized by the action", func(x ipc.Window) bool { return x.Maximized }, "second")
	h.dispatch("maximize off")
	h.settled(c, "second unmaximized by the action", func(x ipc.Window) bool { return !x.Maximized }, "second")
	if w.Size != tile {
		t.Fatalf("maximize off left %v, want %v", w.Size, tile)
	}
}

func TestAWindowThatAsksToMaximizeBeforeItMapsOpensMaximized(t *testing.T) {
	t.Parallel()
	h := StartWith(t, gapped, desktop)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("first to map", c.Window("first", fillGreen).Mapped)
	w := c.WindowWith("second", fillGreen, func(w *testclient.Window) error { return w.Toplevel.SetMaximized() })
	c.DispatchUntil("second to map", w.Mapped)
	h.settled(c, "second maximized", func(x ipc.Window) bool { return x.Maximized }, "second")
	if want := image.Pt(1280-16-4, 800-8-4); w.Size != want {
		t.Fatalf("opened at %v, want %v", w.Size, want)
	}
}

func TestFakeFullscreenTellsTheWindowItIsFullscreenAndKeepsItInItsTile(t *testing.T) {
	t.Parallel()
	h := StartWith(t, gapped, desktop)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("first to map", c.Window("first", fillGreen).Mapped)
	w := c.Window("player", fillGreen)
	c.DispatchUntil("player to map", w.Mapped)
	h.settled(c, "both tiled", func(ipc.Window) bool { return true }, "player")
	tile, before := w.Size, w.Configures

	h.dispatch("fullscreen toggle fake")
	h.settled(c, "the player told it is fullscreen", func(x ipc.Window) bool { return x.ClientFull && !x.Fullscreen }, "player")
	if w.Size != tile || !slices.Contains(w.States, cxdg.XdgToplevelStateFullscreen) {
		t.Fatalf("fake fullscreen configured %v with %v, want its tile %v with the fullscreen state", w.Size, w.States, tile)
	}
	if n := w.Configures - before; n != 1 {
		t.Fatalf("fake fullscreen sent %d configures, want 1", n)
	}
	if err := errors.Join(w.Toplevel.SetFullscreen(nil), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.settled(c, "the player's own set_fullscreen keeps it tiled", func(x ipc.Window) bool { return x.ClientFull && !x.Fullscreen }, "player")
	if w.Size != tile {
		t.Fatalf("set_fullscreen under fake fullscreen resized to %v", w.Size)
	}
	if err := errors.Join(w.Toplevel.UnsetFullscreen(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.settled(c, "the player's unset_fullscreen ends fake fullscreen", func(x ipc.Window) bool { return !x.ClientFull }, "player")
	if slices.Contains(w.States, cxdg.XdgToplevelStateFullscreen) || w.Size != tile {
		t.Fatalf("after unset_fullscreen: %v with %v", w.Size, w.States)
	}
}

func TestANewWindowOverAFullscreenOneFollowsFullscreenNewWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                string
		policy              wm.CoverPolicy
		fullscreen, focused string
	}{
		{"behind", wm.CoverBehind, "video", "video"},
		{"take_over", wm.CoverTakeOver, "new", "new"},
		{"exit", wm.CoverExit, "", "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := StartWith(t, func(cfg *compositor.Config) { cfg.WM.NewOverCover = tc.policy }, desktop)
			c := testclient.Connect(t, h.Socket())
			c.DispatchUntil("video to map", c.Window("video", fillGreen).Mapped)
			h.dispatch("fullscreen on")
			h.settled(c, "video fullscreen", func(x ipc.Window) bool { return x.Fullscreen }, "video")
			c.DispatchUntil("new to map", c.Window("new", fillGreen).Mapped)
			h.Eventually("the policy applied", func(_ *headless.Backend, comp *compositor.Compositor) bool {
				fullscreen, focused := "", ""
				for _, w := range windowsOf(comp) {
					if w.Fullscreen {
						fullscreen = w.Title
					}
					if w.Focused {
						focused = w.Title
					}
				}
				return len(windowsOf(comp)) == 2 && fullscreen == tc.fullscreen && focused == tc.focused
			})
		})
	}
}
