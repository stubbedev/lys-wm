package harness

import (
	"errors"
	"image"
	"runtime"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func area(r render.Region) int {
	n := 0
	for _, rect := range r {
		n += rect.Dx() * rect.Dy()
	}
	return n
}

func presentCount(h *Harness) uint64 {
	var n uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		n = b.Outputs()[0].Presents()
		return nil
	})
	return n
}

func settle(t testing.TB, h *Harness) {
	t.Helper()
	h.Eventually("labels and cursors loaded, no transaction held and the output at rest", func(b *headless.Backend, c *compositor.Compositor) bool {
		return !c.LabelsPending() && !c.CursorsLoading() && !c.Holding() && atRest(b, c)
	})
}

func atRest(b *headless.Backend, c *compositor.Compositor) bool {
	return !b.Outputs()[0].FrameOwed() && !c.Animating(c.Outputs()[0])
}

func quiesce(t *testing.T, h *Harness, c *testclient.Client, w *testclient.Window) {
	t.Helper()
	w.Stop()
	for range 5 {
		settle(t, h)
		before := presentCount(h)
		c.Sync()
		c.Sync()
		settle(t, h)
		if presentCount(h) == before {
			return
		}
	}
	t.Fatal("the client kept redrawing")
}

type frameDone struct{ done *bool }

func (f frameDone) Done(*cw.WlCallback, uint32) error {
	*f.done = true
	return nil
}

func TestOnlyDamageIsRepainted(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("damage", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.MoveTo(b.Outputs()[0], 0.9, 0.9), p.Frame())
	})
	settle(t, h)

	presents := presentCount(h)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Move(40, 0), p.Frame())
	})
	h.Eventually("the cursor move presented", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > presents
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if d := b.Outputs()[0].LastDamage(); area(d) == 0 || area(d) > 2*compositor.DefaultCursorSize*compositor.DefaultCursorSize {
			t.Errorf("a cursor move repainted %v", d)
		}
		return nil
	})
	settle(t, h)
	presents = presentCount(h)
	update := c.Buffer(w.Size, 1, testclient.Fill(0x10, 0x20, 0x30))
	for _, err := range []error{w.Surface.Attach(update, 0, 0), w.Surface.DamageBuffer(100, 50, 10, 20), w.Surface.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.Sync()
	h.Eventually("the partial update presented", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > presents
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		if d := o.LastDamage(); len(d) != 1 || d[0] != image.Rect(100, 50, 110, 70) {
			t.Errorf("a 10x20 update at (100,50) repainted %v", d)
		}
		if got := pixel(snapshot(o), 105, 60); got != [3]byte{0x30, 0x20, 0x10} {
			t.Errorf("the damaged area shows %v", got)
		}
		if got := pixel(snapshot(o), 300, 300); got == [3]byte{0x30, 0x20, 0x10} {
			t.Error("undamaged pixels changed")
		}
		presents = o.Presents()
		return nil
	})

	done := false
	if _, err := w.Surface.Frame(frameDone{&done}); err != nil {
		t.Fatal(err)
	}
	if err := w.Surface.Commit(); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the frame callback of an unchanged commit", func() bool { return done })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if n := b.Outputs()[0].Presents(); n != presents {
			t.Errorf("an unchanged commit presented %d frames", n-presents)
		}
		return nil
	})
}

func TestIdleDesktopNeverWakes(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("idle", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	settle(t, h)
	before := h.Wakeups()
	h.Advance(300 * time.Millisecond)
	if woke := h.Wakeups() - before; woke > 2 {
		t.Fatalf("an idle desktop woke the loop %d times in 300ms", woke-2)
	}
}

func TestSteadyStateFramesDoNotAllocate(t *testing.T) {
	h := StartUntraced(t, desktop)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	w := c.Window("steady", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		move := func() {
			if err := errors.Join(b.Pointer().Move(2, 1), b.Pointer().Frame()); err != nil {
				t.Fatal(err)
			}
		}
		frame := func() {
			if err := comp.Frame(o); err != nil {
				t.Fatal(err)
			}
		}
		for range 100 {
			move()
			frame()
			frame()
		}
		moving, idle := allocsPerRun(100, move, frame), allocsPerRun(100, func() {}, frame)
		if moving != 0 || idle != 0 {
			t.Errorf("cursor frames allocated %d times each and unchanged frames %d times", moving, idle)
		}
		presented := func() {
			if err := comp.Presented(o, backend.Presentation{Time: h.loop.Now(), Refresh: desktop.Interval(), Seq: 1}); err != nil {
				t.Fatal(err)
			}
		}
		if n := allocsPerRun(100, func() {}, presented); n != 0 {
			t.Errorf("a presentation allocated %d times", n)
		}
		return nil
	})
}

func TestPointerMotionDoesNotAllocate(t *testing.T) {
	h := StartUntraced(t, desktop)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	w := c.Window("steady", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		motions := [2]backend.Event{&backend.PointerMotion{DX: 2, DY: 1}, &backend.PointerMotion{DX: -2, DY: -1}}
		var frame backend.Event = &backend.PointerFrame{}
		next := 0
		move := func() {
			if err := errors.Join(comp.Input(b.Pointer(), motions[next]), comp.Input(b.Pointer(), frame)); err != nil {
				t.Fatal(err)
			}
			next ^= 1
		}
		for range 100 {
			move()
		}
		if n := allocsPerRun(100, func() {}, move); n != 0 {
			t.Errorf("a pointer motion allocated %d times", n)
		}
		return nil
	})
}

func allocsPerRun(runs uint64, prepare, frame func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	var before, after runtime.MemStats
	var total uint64
	for range runs {
		prepare()
		runtime.ReadMemStats(&before)
		frame()
		runtime.ReadMemStats(&after)
		total += after.Mallocs - before.Mallocs
	}
	return total / runs
}

func TestUndamagedFrameCallbacksArePacedByRefresh(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("idle", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	var times frameTimes
	for len(times) < 12 {
		asked := len(times)
		if _, err := w.Surface.Frame(&times); err != nil {
			t.Fatal(err)
		}
		if err := w.Surface.Commit(); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the frame callback", func() bool { return len(times) > asked })
	}
	for i := 1; i < len(times); i++ {
		if gap := time.Duration(times[i]-times[i-1]) * time.Millisecond; gap < small.Interval()-time.Millisecond {
			t.Fatalf("frame callbacks without damage came %v apart by the compositor's clock, want a refresh of %v between them: %v", gap, small.Interval(), times)
		}
	}
}

func TestAPartlyOpaqueLayerHidesWhatItsOpaqueRegionCovers(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.WM = compositor.DefaultSettings()
	}, backend.Mode{Size: image.Pt(800, 600), RefreshMHz: 60000, Preferred: true})
	c := testclient.Connect(t, h.Socket())
	w := c.Window("under", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	layer := c.Layer(testclient.LayerSpec{
		Layer:  cwlr.ZwlrLayerShellV1LayerOverlay,
		Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight,
		Size:   image.Pt(800, 600),
	}, testclient.Fill(0x10, 0x20, 0x30))
	layer.Translucent = true
	c.DispatchUntil("the layer to map", layer.Mapped)
	layer.Stop()
	settle(t, h)
	cost := func() (draws, pixels int) {
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			draws, pixels = comp.FrameCost(comp.Outputs()[0])
			return nil
		})
		return draws, pixels
	}
	beforeDraws, beforePixels := cost()
	region, err := c.Compositor.CreateRegion()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(region.Add(0, 1, 800, 599), layer.Surface.SetOpaqueRegion(region), region.Destroy(), layer.Redraw()); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	settle(t, h)
	draws, pixels := cost()
	t.Logf("draws %d -> %d, pixels %d -> %d", beforeDraws, draws, beforePixels, pixels)
	if layerPixels := 800 * 600; pixels > layerPixels+3*800 {
		t.Fatalf("the frame draws %d px with all but one row of the layer opaque, want at most the layer's %d and a row of each op below", pixels, layerPixels)
	}
}

func TestAWindowDrawnBeforeTheHiddenFrameIntervalLeavesNoHiddenFrame(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) { cfg.HiddenFrame = 200 * time.Millisecond }, desktop)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("drawn", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	settle(t, h)
	h.Advance(250 * time.Millisecond)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if n := comp.HiddenFrames(); n != 0 {
			t.Errorf("a window drawn as it mapped left %d hidden frames to fire", n)
		}
		return nil
	})
}
