package harness

import (
	"errors"
	"image"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwft "github.com/stubbedev/lys-wm/internal/protocol/client/wlrforeigntoplevel"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func genieTable(t *testing.T, d time.Duration) anim.Table {
	t.Helper()
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.WindowsMinimize: {Enabled: &on, Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return table
}

func snapshotWindow(h *Harness, id int) (minimized, found bool) {
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		for _, w := range comp.Snapshot().Windows {
			if int(w.ID) == id {
				minimized, found = w.Minimized, true
			}
		}
		return nil
	})
	return minimized, found
}

func TestAMinimizedWindowHidesAndComesBack(t *testing.T) {
	h := Start(t, backend.Mode{Size: image.Pt(800, 400), RefreshMHz: 60000, Preferred: true})
	c := testclient.Connect(t, h.Socket())
	red, blue := [3]byte{0xff, 0, 0}, [3]byte{0, 0, 0xff}
	a := c.Window("a", testclient.Fill(red[2], red[1], red[0]))
	c.DispatchUntil("a to map", a.Mapped)
	b := c.Window("b", testclient.Fill(blue[2], blue[1], blue[0]))
	c.DispatchUntil("b to map", b.Mapped)
	c.DispatchUntil("a at half the output", func() bool { return a.Logical.X == 400 })
	h.Eventually("both windows", func(bk *headless.Backend, comp *compositor.Compositor) bool {
		return showing(20, 50, red)(bk, comp) && showing(700, 50, blue)(bk, comp)
	})
	h.dispatch("minimize")
	c.DispatchUntil("a over the output", func() bool { return a.Logical.X == 800 })
	h.Eventually("a alone over the output", func(bk *headless.Backend, comp *compositor.Compositor) bool {
		return showing(20, 50, red)(bk, comp) && showing(700, 50, red)(bk, comp)
	})
	if minimized, found := snapshotWindow(h, 2); !found || !minimized {
		t.Errorf("IPC says minimized %v, found %v", minimized, found)
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if f, ok := comp.WM().Focused(); !ok || f != 1 {
			t.Errorf("focus on %d (%v), want the remaining window", f, ok)
		}
		return nil
	})
	h.dispatch("restore")
	c.DispatchUntil("a at half the output again", func() bool { return a.Logical.X == 400 })
	h.Eventually("both windows again", func(bk *headless.Backend, comp *compositor.Compositor) bool {
		return showing(20, 50, red)(bk, comp) && showing(700, 50, blue)(bk, comp)
	})
	if minimized, _ := snapshotWindow(h, 2); minimized {
		t.Error("IPC still says minimized")
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if f, ok := comp.WM().Focused(); !ok || f != 2 {
			t.Errorf("focus on %d (%v), want the restored window", f, ok)
		}
		return nil
	})
}

func TestAClientMinimizesItself(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	gray := [3]byte{0x60, 0x60, 0x60}
	w := c.Window("self", testclient.Fill(gray[0], gray[1], gray[2]))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window", showing(50, 50, gray))
	if err := errors.Join(w.Toplevel.SetMinimized(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	h.Eventually("the background", showing(50, 50, bg))
	h.dispatch("focuswindow 1")
	h.Eventually("the window back from focuswindow", showing(50, 50, gray))
}

func TestForeignToplevelsSeeAndChangeMinimized(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	gray := [3]byte{0x60, 0x60, 0x60}
	win := c.Window("taskbar", testclient.Fill(gray[0], gray[1], gray[2]))
	c.DispatchUntil("the window to map", win.Mapped)
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	h.Eventually("the window", showing(50, 50, gray))
	watcher := testclient.Connect(t, h.Socket())
	mgr, err := cwft.NewZwlrForeignToplevelManagerV1(watcher.Display, 3, &wlrWatcher{toplevels: map[*cwft.ZwlrForeignToplevelHandleV1]*wlrToplevel{}})
	if err != nil {
		t.Fatal(err)
	}
	wlr := mgr.Handler().(*wlrWatcher)
	watcher.Bind(mgr)
	watcher.DispatchUntil("the toplevel", func() bool { return len(wlr.order) == 1 })
	handle := wlr.order[0]
	if err := errors.Join(handle.SetMinimized(), watcher.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the background", showing(50, 50, bg))
	watcher.DispatchUntil("the minimized state", func() bool {
		return slices.Contains(wlr.toplevels[handle].states, cwft.ZwlrForeignToplevelHandleV1StateMinimized) && !wlr.toplevels[handle].activated()
	})
	seat, _ := watcher.Seat()
	if err := errors.Join(handle.Activate(seat), watcher.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the window back from activate", showing(50, 50, gray))
	watcher.DispatchUntil("the minimized state gone", func() bool {
		return !slices.Contains(wlr.toplevels[handle].states, cwft.ZwlrForeignToplevelHandleV1StateMinimized) && wlr.toplevels[handle].activated()
	})
}

func TestAGenieHeadsForTheTaskbarRectangle(t *testing.T) {
	now := time.Hour
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = genieTable(t, time.Second)
		cfg.Clock = func() time.Duration { return now }
	}, small)
	bar := testclient.Connect(t, h.Socket())
	panel := bar.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom, Size: image.Pt(10, 0)}, testclient.Fill(0, 0xff, 0))
	bar.DispatchUntil("the panel to map", panel.Mapped)
	mgr, err := cwft.NewZwlrForeignToplevelManagerV1(bar.Display, 3, &wlrWatcher{toplevels: map[*cwft.ZwlrForeignToplevelHandleV1]*wlrToplevel{}})
	if err != nil {
		t.Fatal(err)
	}
	wlr := mgr.Handler().(*wlrWatcher)
	bar.Bind(mgr)
	c := testclient.Connect(t, h.Socket())
	gray := [3]byte{0x60, 0x60, 0x60}
	w := c.Window("to the panel", testclient.Fill(gray[0], gray[1], gray[2]))
	c.DispatchUntil("the window to map", w.Mapped)
	bar.DispatchUntil("the toplevel", func() bool { return len(wlr.order) == 1 })
	h.Eventually("the window", showing(100, 50, gray))
	if err := errors.Join(wlr.order[0].SetRectangle(panel.Surface, 0, 80, 10, 10), bar.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	bar.Sync()
	h.dispatch("minimize")
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		now += 999 * time.Millisecond
		return nil
	})
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	h.Eventually("the window down to the corner the panel named", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(100, 50, bg)(b, comp) && showing(30, 20, bg)(b, comp) && showing(12, 90, bg)(b, comp)
	})
	if err := wlr.order[0].SetRectangle(panel.Surface, 0, 0, -1, 1); err != nil {
		t.Fatal(err)
	}
	bar.ExpectProtocolError(uint32(cwft.ZwlrForeignToplevelHandleV1ErrorInvalidRectangle))
}

func TestAGenieCollapsesTowardTheNearestEdgeAndComesBack(t *testing.T) {
	onEachConfiguredRenderer(t, testAGenieCollapsesTowardTheNearestEdgeAndComesBack)
}

func testAGenieCollapsesTowardTheNearestEdgeAndComesBack(t *testing.T, start gpuStarter) {
	now := time.Hour
	at := func(h *Harness, d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = time.Hour + d
			return nil
		})
	}
	h := start(t, func(cfg *compositor.Config) {
		cfg.Animations = genieTable(t, time.Second)
		cfg.Clock = func() time.Duration { return now }
	}, small)
	c := testclient.Connect(t, h.Socket())
	gray := [3]byte{0x60, 0x60, 0x60}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	w := c.Window("genie", testclient.Fill(gray[0], gray[1], gray[2]))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("a floating window", func() bool { return w.Logical == image.Pt(80, 50) })
	h.Eventually("the window in the middle", showing(80, 45, gray))
	h.dispatch("minimize")
	h.Eventually("the window whole as the genie starts", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(45, 30, gray)(b, comp) && showing(115, 70, gray)(b, comp)
	})
	at(h, 700*time.Millisecond)
	h.Eventually("the far corners gone, the tail on its way", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(45, 30, bg)(b, comp) && showing(115, 30, bg)(b, comp)
	})
	at(h, 2*time.Second)
	h.Eventually("nothing left", showing(80, 45, bg))
	time.Sleep(100 * time.Millisecond)
	var frames uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		frames = b.Outputs()[0].Frames()
		return nil
	})
	time.Sleep(200 * time.Millisecond)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if n := b.Outputs()[0].Frames(); n != frames {
			t.Errorf("%d frames ran after the genie ended", n-frames)
		}
		return nil
	})
	h.dispatch("restore")
	h.Eventually("the restored window starts collapsed", showing(45, 30, bg))
	at(h, 2*time.Second+300*time.Millisecond)
	at(h, 4*time.Second)
	h.Eventually("the window whole again", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(45, 30, gray)(b, comp) && showing(115, 70, gray)(b, comp)
	})
}

func TestReversingAGenieMidwayKeepsItsPlace(t *testing.T) {
	now := time.Hour
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = genieTable(t, time.Second)
		cfg.Clock = func() time.Duration { return now }
	}, small)
	c := testclient.Connect(t, h.Socket())
	gray := [3]byte{0x60, 0x60, 0x60}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	w := c.Window("genie", testclient.Fill(gray[0], gray[1], gray[2]))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window", showing(5, 5, gray))
	h.dispatch("minimize")
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		now += 600 * time.Millisecond
		return nil
	})
	h.Eventually("the top corner gone", showing(5, 5, bg))
	h.dispatch("restore")
	h.Eventually("the top corner still gone just after the reversal", showing(5, 5, bg))
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		now += 2 * time.Second
		return nil
	})
	h.Eventually("the window whole", showing(5, 5, gray))
}

func TestUnderReducedMotionAWindowMinimizesAsAFade(t *testing.T) {
	d := 10 * time.Second
	table, errs := anim.ResolveReduced(map[anim.Event]anim.Setting{anim.Global: {Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.WM.Gaps = wm.Gaps{}
		cfg.Decor.BorderWidth = 0
	}, small)
	c := testclient.Connect(t, h.Socket())
	gray := [3]byte{0xf0, 0xf0, 0xf0}
	w := c.Window("fading", testclient.Fill(gray[0], gray[1], gray[2]))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window at full strength, its fade in done", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[0]), 2, 2)[0] > 0x10
	})
	h.dispatch("minimize")
	h.Eventually("a fade across the whole rectangle, not a funnel", func(b *headless.Backend, _ *compositor.Compositor) bool {
		img := snapshot(b.Outputs()[0])
		corner, far := pixel(img, 2, 2), pixel(img, small.Size.X-3, small.Size.Y-3)
		return corner == far && corner[0] > 0 && corner[0] < 0xf0
	})
}
