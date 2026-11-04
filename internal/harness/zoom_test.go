package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwsc "github.com/stubbedev/lys-wm/internal/protocol/client/wlrscreencopy"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	zoomRed    = [3]byte{0xff, 0, 0}
	zoomGreen  = [3]byte{0, 0xff, 0}
	zoomBlue   = [3]byte{0, 0, 0xff}
	zoomYellow = [3]byte{0xff, 0xff, 0}
)

func zoomConfig(cfg *compositor.Config) {
	cfg.WM.Gaps = wm.Gaps{}
	cfg.Decor.BorderWidth = 0
}

func TestZoomMagnifiesAroundThePointerOnEveryRenderer(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, zoomConfig, small)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("quadrants", quadrants)
		c.DispatchUntil("the window to map", w.Mapped)
		h.Eventually("the window", showing(10, 90, zoomBlue))
		movePointer(h, 40, 25)
		h.Eventually("the pointer", showing(120, 75, zoomYellow))
		h.dispatch("zoom set 2")
		h.Eventually("the top left quadrant filling more of the output", func(b *headless.Backend, comp *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			return pixel(snap, 110, 60) == zoomRed && pixel(snap, 130, 60) == zoomGreen && pixel(snap, 20, 90) == zoomBlue && pixel(snap, 150, 90) == zoomYellow
		})
		h.dispatch("zoom reset")
		h.Eventually("the whole window back", func(b *headless.Backend, comp *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			return pixel(snap, 70, 40) == zoomRed && pixel(snap, 90, 40) == zoomGreen && pixel(snap, 70, 60) == zoomBlue && pixel(snap, 90, 60) == zoomYellow
		})
	})
}

func TestTheViewFollowsThePointerAndWhatItPointsAtIsWhatClientsSee(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		r := inputOn(t, start(t, zoomConfig, small))
		r.expect("kb_enter win", "enter win 80,50", "frame")
		r.h.dispatch("zoom set 4")
		r.inject(func(b *headless.Backend, o *headless.Output) error {
			return errors.Join(b.Pointer().MoveTo(o, 0.25, 0.25), b.Pointer().Frame())
		})
		r.expect("motion 40,25", "frame")
		r.h.Eventually("red under the pointer, four times as big", func(b *headless.Backend, _ *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			return pixel(snap, 10, 90) == zoomRed && pixel(snap, 150, 10) == zoomRed
		})
		r.inject(func(b *headless.Backend, o *headless.Output) error {
			return errors.Join(b.Pointer().MoveTo(o, 0.75, 0.75), b.Pointer().Frame())
		})
		r.expect("motion 120,75", "frame")
		r.h.Eventually("yellow under the pointer", func(b *headless.Backend, _ *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			return pixel(snap, 110, 65) == zoomYellow && pixel(snap, 10, 10) == zoomYellow
		})
	})
}

func TestZoomOnAFractionalOutputKeepsTheTipOverTheContent(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			zoomConfig(cfg)
		}, scaled)
		h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
			return comp.SetOutputScale(b.Outputs()[0], 180)
		})
		c := testclient.Connect(t, h.Socket())
		_, in := c.Seat()
		w := c.Window("quadrants", quadrants)
		c.DispatchUntil("the window to map", w.Mapped)
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.75, 0.25), b.Pointer().Frame())
		})
		c.DispatchUntil("the pointer to enter", func() bool { return in.Serial != 0 })
		h.dispatch("zoom set 3")
		h.Eventually("green around the tip and nothing else", func(b *headless.Backend, _ *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			return pixel(snap, 10, 140) == zoomGreen && pixel(snap, 230, 10) == zoomGreen
		})
	})
}

func TestUnderZoomAFingerTouchesWhatItLandsOn(t *testing.T) {
	t.Parallel()
	r := inputOn(t, StartWith(t, zoomConfig, small))
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.inject(func(b *headless.Backend, o *headless.Output) error {
		return errors.Join(b.Pointer().MoveTo(o, 0, 0), b.Pointer().Frame())
	})
	r.expect("motion 0,0", "frame")
	r.h.dispatch("zoom set 2")
	r.h.Eventually("the top left of the window across the output", showing(150, 90, zoomRed))
	r.inject(func(b *headless.Backend, o *headless.Output) error {
		return errors.Join(b.Touch().Down(1, o, 0.5, 0.5), b.Touch().Frame())
	})
	r.expect("down 1 win 40,25", "touch_frame")
}

func TestZoomSpringsAndSettlesThenCostsNoFrames(t *testing.T) {
	t.Parallel()
	spring, err := anim.NewSpring(40, 13, 1)
	if err != nil {
		t.Fatal(err)
	}
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Zoom: {Enabled: &on, Shape: spring}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		zoomConfig(cfg)
		cfg.Animations = table
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("quadrants", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	movePointer(h, 80, 50)
	h.Eventually("the window", showing(10, 90, zoomBlue))
	before := presents(h)
	h.dispatch("zoom set 8")
	h.Eventually("the view at eight times", showing(2, 2, zoomRed))
	settle(t, h)
	if n := presents(h); n-before < 3 {
		t.Errorf("the zoom took only %d frames, so it did not animate", n-before)
	}
	h.Eventually("the zoom spring at rest", func(_ *headless.Backend, comp *compositor.Compositor) bool { return !comp.Animating(comp.Outputs()[0]) })
	settle(t, h)
	settled := presents(h)
	settle(t, h)
	if n := presents(h); n != settled {
		t.Errorf("a settled zoom drew %d more frames with nothing moving", n-settled)
	}
	h.dispatch("zoom reset")
	h.Eventually("the whole window back", showing(10, 90, zoomBlue))
	settle(t, h)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if got := comp.Snapshot().Outputs[0].Zoom; got != 0 {
			t.Errorf("IPC says zoom %v after the reset", got)
		}
		return nil
	})
}

func TestReducedMotionZoomsAtOnce(t *testing.T) {
	t.Parallel()
	table, errs := anim.ResolveReduced(nil)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		zoomConfig(cfg)
		cfg.Animations = table
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("quadrants", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	movePointer(h, 80, 50)
	h.Eventually("the window", showing(10, 90, zoomBlue))
	settle(t, h)
	frames := presents(h)
	h.dispatch("zoom set 2")
	h.Eventually("the zoom", showing(2, 2, zoomRed))
	settle(t, h)
	if n := presents(h); n-frames > 2 {
		t.Errorf("reduced motion took %d frames to zoom", n-frames)
	}
}

func TestScanoutStopsWhileZoomedAndResumes(t *testing.T) {
	onEachDMABUFRenderer(t, func(t *testing.T, start gpuStarter) {
		s := fullscreenScanout(t, start, zoomConfig)
		h, c := s.h, s.c
		first := c.DMABUF(image.Pt(160, 100), testclient.Fill(0, 0, 0xff))
		s.show(t, "the dmabuf scanned out", first)
		movePointer(h, 80, 50)
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			return comp.Dispatch(wm.ZoomAction{Mode: wm.ZoomSet, Factor: 2})
		})
		settle(t, h)
		before := scanouts(h)
		b := c.DMABUF(image.Pt(160, 100), testclient.Fill(0, 0xff, 0))
		if err := errors.Join(s.w.Surface.Attach(b.WL, 0, 0), s.w.Surface.DamageBuffer(0, 0, 160, 100), s.w.Surface.Commit(), c.Display.Roundtrip()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the new buffer composited", showing(10, 10, [3]byte{0, 0xff, 0}))
		settle(t, h)
		if n := scanouts(h); n != before {
			t.Fatalf("a zoomed output scanned out %d frames", n-before)
		}
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			return comp.Dispatch(wm.ZoomAction{Mode: wm.ZoomReset})
		})
		h.Eventually("scanout again", func(b *headless.Backend, _ *compositor.Compositor) bool { return b.Outputs()[0].Scanouts() > before })
	})
}

func TestACursorPlaneHidesWhileZoomedAndComesBack(t *testing.T) {
	t.Parallel()
	h := StartWithCursorPlanes(t, plane, zoomConfig, small)
	h.Eventually("the arrow on the cursor plane", func(b *headless.Backend, c *compositor.Compositor) bool {
		return b.Outputs()[0].Cursor().Visible() && arrowAt(image.Pt(80, 50))(b, c)
	})
	settle(t, h)
	h.dispatch("zoom set 2")
	h.Eventually("the plane hidden and the arrow in the frame, twice as big", func(b *headless.Backend, _ *compositor.Compositor) bool {
		o := b.Outputs()[0]
		bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
		return !o.Cursor().Visible() && pixel(primary(o), 82, 56) != bg && pixel(primary(o), 84, 74) != bg
	})
	settle(t, h)
	moves := planeOf(h).moves
	movePointer(h, 40, 25)
	h.Eventually("the arrow at the pointer in the frame", func(b *headless.Backend, _ *compositor.Compositor) bool {
		o := b.Outputs()[0]
		return !o.Cursor().Visible() && pixel(primary(o), 42, 31) != [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	})
	if planeOf(h).moves != moves {
		t.Error("the hidden plane moved")
	}
	h.dispatch("zoom reset")
	h.Eventually("the arrow back on the plane", func(b *headless.Backend, c *compositor.Compositor) bool {
		return b.Outputs()[0].Cursor().Visible() && arrowAt(image.Pt(40, 25))(b, c)
	})
}

func TestCapturesShowTheUnzoomedScreen(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, zoomConfig, small)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("shot", quadrants)
		c.DispatchUntil("the window to map", w.Mapped)
		movePointer(h, 40, 25)
		h.dispatch("zoom set 4")
		h.Eventually("the zoom", showing(150, 10, zoomRed))
		settle(t, h)
		mgr, err := cwsc.NewZwlrScreencopyManagerV1(c.Display, 3)
		if err != nil {
			t.Fatal(err)
		}
		c.Bind(mgr)
		f, _, pix := screenshot(t, c, mgr, image.Rectangle{}, false)
		c.DispatchUntil("the capture", func() bool { return f.ready || f.failed })
		if f.failed {
			t.Fatal("the capture failed")
		}
		at := func(x, y int) [3]byte { return rgbAt(pix, int(f.stride), x, y) }
		if at(120, 25) != zoomGreen || at(40, 75) != zoomBlue || at(120, 75) != zoomYellow || at(10, 40) != zoomRed {
			t.Errorf("the capture shows %v %v %v %v, not the four quadrants", at(120, 25), at(40, 75), at(120, 75), at(10, 40))
		}
	})
}

func TestZoomOnAnHDROutputKeepsItsColour(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, fullWindowConfig(compositor.OutputRule{}), small)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("sdr", testclient.Fill(0xff, 0x40, 0x00))
		c.DispatchUntil("the window to map", w.Mapped)
		h.Eventually("the window on the PQ signal", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return pixel(snapshot(b.Outputs()[0]), 10, 10)[1] > 0x40
		})
		settle(t, h)
		want := pixel(h.Snapshot(0), 10, 10)
		h.dispatch("zoom set 2")
		settle(t, h)
		if got := pixel(h.Snapshot(0), 10, 10); !within(got, want, 2) {
			t.Errorf("zoomed %v, unzoomed %v", got, want)
		}
	})
}

func TestZoomAppliesUnderAScreenShader(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		invert := shaded(writeShader(t, invertShader))
		h := start(t, func(cfg *compositor.Config) {
			zoomConfig(cfg)
			invert(cfg)
		}, small)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("quadrants", quadrants)
		c.DispatchUntil("the window to map", w.Mapped)
		movePointer(h, 40, 25)
		h.dispatch("zoom set 4")
		h.Eventually("the red quadrant, inverted, across the output", func(b *headless.Backend, _ *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			want := [3]byte{0, 0xff, 0xff}
			return pixel(snap, 10, 90) == want && pixel(snap, 150, 10) == want
		})
	})
}

func TestAPinchWithItsModifierZoomsAndTheClientSeesNothing(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		zoomConfig(cfg)
		mods, err := binds.ModSuper.Mask(cfg.Seat.Keymap)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Gestures.Binds = []binds.GestureBind{{Kind: backend.Pinch, Fingers: 2, Direction: binds.PinchBoth, Drive: binds.DriveZoom, Modifiers: binds.ModSuper, Mods: mods}}
	}, small)
	c, log := gestureClient(t, h, "quadrants")
	movePointer(h, 80, 50)
	h.Eventually("the window", showing(10, 90, zoomBlue))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.GestureBegin(backend.Pinch, 2), p.Pinch(0, 0, 2, 0), p.GestureEnd(backend.Pinch, false))
	})
	c.DispatchUntil("the pinch at the client, which has no modifier to match", func() bool { return len(log.events) >= 3 })
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if z := comp.Snapshot().Outputs[0].Zoom; z != 0 {
			t.Errorf("a pinch with no modifier zoomed to %v", z)
		}
		return nil
	})
	seen := len(log.events)
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.GestureBegin(backend.Pinch, 2), p.Pinch(0, 0, 2, 0))
	})
	h.Eventually("the view following the fingers", showing(2, 2, zoomRed))
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		if err := b.Pointer().GestureEnd(backend.Pinch, false); err != nil {
			return err
		}
		if z := comp.Snapshot().Outputs[0].Zoom; z != 2 {
			t.Errorf("IPC says zoom %v after the pinch, want 2", z)
		}
		p := b.Pointer()
		return errors.Join(p.GestureBegin(backend.Pinch, 2), p.Pinch(0, 0, 0.25, 0), p.GestureEnd(backend.Pinch, false))
	})
	h.Eventually("back at one when the fingers pinch in", showing(10, 90, zoomBlue))
	h.key(keySuper, false)
	settle(t, h)
	c.Sync()
	if len(log.events) != seen {
		t.Errorf("the client saw the zoom pinch: %q", log.events[seen:])
	}
}
