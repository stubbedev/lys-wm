package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

var greenColor = [3]byte{0, 0xff, 0}

func framesOn(h *Harness, i int) uint64 {
	var n uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		n = b.Outputs()[i].Frames()
		return nil
	})
	return n
}

func showingOn(i, x, y int, want [3]byte) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[i]), x, y) == want
	}
}

func replace(t *testing.T, c *testclient.Client, s *cw.WlSurface, size image.Point, paint testclient.Painter) {
	t.Helper()
	if err := errors.Join(
		s.Attach(c.Buffer(size, 0, paint), 0, 0),
		s.DamageBuffer(0, 0, int32(size.X), int32(size.Y)),
		s.Commit(),
		c.Display.Flush(),
	); err != nil {
		t.Fatal(err)
	}
}

func quietOutputs(t *testing.T, h *Harness, c *testclient.Client, windows ...*testclient.Window) {
	t.Helper()
	for _, w := range windows {
		w.Stop()
	}
	c.Sync()
	settle(t, h)
	var last [2]uint64
	h.Eventually("both outputs to stop framing", func(b *headless.Backend, _ *compositor.Compositor) bool {
		now := [2]uint64{b.Outputs()[0].Frames(), b.Outputs()[1].Frames()}
		same := now == last
		last = now
		return same
	})
}

func straddling(t *testing.T, h *Harness, c *testclient.Client) *testclient.Window {
	t.Helper()
	w := c.Window("float", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("the window to float", func() bool { return w.Logical == image.Pt(80, 50) })
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.Button(btnLeft, true), p.Frame(),
			p.MoveTo(o, 150.0/160, 0.5), p.Frame(), p.Button(btnLeft, false), p.Frame())
	})
	h.key(keySuper, false)
	h.Eventually("the window across both outputs", showingOn(1, 5, 50, redColor))
	return w
}

func TestACommitWakesOnlyTheOutputsShowingTheSurface(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("animating", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("a few frames", func() bool { return w.Frames >= 5 })
	before := framesOn(h, 1)
	shown := framesOn(h, 0)
	frames := w.Frames
	c.DispatchUntil("many more frames", func() bool { return w.Frames >= frames+30 })
	if woke := framesOn(h, 1) - before; woke != 0 {
		t.Fatalf("%d client frames ran %d frames on the output not showing it", w.Frames-frames, woke)
	}
	if framesOn(h, 0) == shown {
		t.Fatal("the output showing the surface ran no frames")
	}
}

func TestALayerSurfaceCommitWakesOnlyItsOutput(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight, Size: image.Pt(0, 20)}, testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("a few frames", func() bool { return w.Frames >= 5 })
	before := framesOn(h, 1)
	frames := w.Frames
	c.DispatchUntil("many more frames", func() bool { return w.Frames >= frames+30 })
	if woke := framesOn(h, 1) - before; woke != 0 {
		t.Fatalf("%d layer frames ran %d frames on the other output", w.Frames-frames, woke)
	}
}

func TestACommitOnAHiddenSurfaceWakesNothingAndShowsWhenRevealed(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("hidden", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window drawn", showingOn(0, 40, 30, redColor))
	h.dispatch("workspace 3")
	h.Eventually("the window hidden", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[0]), 40, 30) != redColor
	})
	quietOutputs(t, h, c, w)
	first, second := framesOn(h, 0), framesOn(h, 1)
	replace(t, c, w.Surface, w.Logical, testclient.Fill(0xff, 0, 0))
	c.Sync()
	if framesOn(h, 0) != first || framesOn(h, 1) != second {
		t.Fatal("a commit on a surface no output shows ran a frame")
	}
	h.dispatch("workspace 1")
	h.Eventually("the newest buffer when revealed", showingOn(0, 40, 30, blueColor))
}

func TestAPopupGrowingOntoAnotherOutputIsDrawnThere(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("win", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	menu := c.Popup(w, testclient.Placement{Size: image.Pt(40, 30), Anchor: image.Rect(100, 10, 101, 11), Edge: cxdg.XdgPositionerAnchorBottomRight, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the menu to map", menu.Mapped)
	h.Eventually("the menu on the first output", showingOn(0, 120, 20, greenColor))
	quietOutputs(t, h, c, w, menu)
	replace(t, c, menu.Surface, image.Pt(120, 30), testclient.Fill(0, 0xff, 0))
	h.Eventually("the grown menu on the second output", showingOn(1, 30, 20, greenColor))
}

func TestAPopupRepositionedOntoAnotherOutputIsDrawnThere(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("win", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	placed := func(x int) testclient.Placement {
		return testclient.Placement{Size: image.Pt(40, 30), Anchor: image.Rect(x, 10, x+1, 11), Edge: cxdg.XdgPositionerAnchorBottomRight, Gravity: cxdg.XdgPositionerGravityBottomRight}
	}
	menu := c.Popup(w, placed(100), nil, testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the menu to map", menu.Mapped)
	h.Eventually("the menu on the first output", showingOn(0, 120, 20, greenColor))
	quietOutputs(t, h, c, w, menu)
	pos := c.Positioner(placed(170))
	if err := errors.Join(menu.Popup.Reposition(pos, 1), pos.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the menu configured at its new place", func() bool { return menu.Placed.Min.X == 171 })
	replace(t, c, menu.Surface, image.Pt(40, 30), testclient.Fill(0, 0xff, 0))
	h.Eventually("the moved menu on the second output", showingOn(1, 30, 20, greenColor))
	h.Eventually("the moved menu gone from the first output", showingOn(0, 120, 20, redColor))
}

func TestAPopupsWindowGeometryCarryingItOntoAnotherOutputIsDrawnThere(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("win", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	menu := c.Popup(w, testclient.Placement{Size: image.Pt(40, 30), Anchor: image.Rect(100, 10, 101, 11), Edge: cxdg.XdgPositionerAnchorBottomRight, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the menu to map", menu.Mapped)
	h.Eventually("the menu on the first output", showingOn(0, 120, 20, greenColor))
	quietOutputs(t, h, c, w, menu)
	if err := errors.Join(menu.XdgSurface.SetWindowGeometry(-60, 0, 40, 30), menu.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the shifted menu on the second output", showingOn(1, 10, 20, greenColor))
}

func TestANewWindowOnAnIdleOutputIsDrawnWhileAnotherAnimates(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	busy := c.Window("busy", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the busy window to map", busy.Mapped)
	h.dispatch("workspace 2")
	fresh := c.Window("fresh", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the new window to map", fresh.Mapped)
	h.Eventually("the new window on the second output", showingOn(1, 40, 30, greenColor))
}

func TestAFloatingWindowOnBothOutputsGrowsOnBoth(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"}), small, small)
	c := testclient.Connect(t, h.Socket())
	w := straddling(t, h, c)
	quietOutputs(t, h, c, w)
	replace(t, c, w.Surface, image.Pt(140, 50), testclient.Fill(0, 0xff, 0))
	h.Eventually("the new buffer on the first output", showingOn(0, 130, 30, greenColor))
	h.Eventually("the new buffer on the second output", showingOn(1, 5, 30, greenColor))
}

func TestAFloatingWindowGrowingWakesTheOtherOutput(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("float", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("the window to float", func() bool { return w.Logical == image.Pt(80, 50) })
	quietOutputs(t, h, c, w)
	before := framesOn(h, 1)
	replace(t, c, w.Surface, image.Pt(200, 50), testclient.Fill(0, 0xff, 0))
	h.Eventually("the other output to look again", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[1].Frames() > before
	})
}

func TestASubsurfaceAddedToAWindowOnBothOutputsIsDrawnOnBoth(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"}), small, small)
	c := testclient.Connect(t, h.Socket())
	w := straddling(t, h, c)
	quietOutputs(t, h, c, w)
	child := c.Surface()
	sub, err := c.Subcompositor.GetSubsurface(child, w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(sub.SetPosition(0, 0), sub.SetDesync(), child.Attach(c.Buffer(image.Pt(80, 20), 0, testclient.Fill(0, 0xff, 0)), 0, 0), child.Commit(), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the subsurface on the second output", showingOn(1, 5, 30, greenColor))
	h.Eventually("the subsurface on the first output", showingOn(0, 130, 30, greenColor))
}

func TestPartialDamageReachesEveryOutputShowingTheSurface(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"}), small, small)
	c := testclient.Connect(t, h.Socket())
	w := straddling(t, h, c)
	quietOutputs(t, h, c, w)
	if err := errors.Join(
		w.Surface.Attach(c.Buffer(w.Logical, 0, testclient.Fill(0, 0xff, 0)), 0, 0),
		w.Surface.DamageBuffer(70, 0, 10, 40),
		w.Surface.Commit(),
		c.Display.Flush(),
	); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the damaged corner on the second output", func(b *headless.Backend, _ *compositor.Compositor) bool {
		snap := snapshot(b.Outputs()[1])
		for x := range small.Size.X {
			if pixel(snap, x, 30) == greenColor {
				return true
			}
		}
		return false
	})
}

func TestAClientCursorGrowingIsDrawn(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("win", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.MoveTo(b.Outputs()[0], 150.0/160, 0.5), p.Frame())
	})
	c.DispatchUntil("the pointer on the window", func() bool { return in.Serial != 0 })
	cursor := c.Surface()
	if err := errors.Join(cursor.Attach(c.Buffer(image.Pt(4, 4), 0, testclient.Fill(0, 0xff, 0)), 0, 0), cursor.Commit(), in.Pointer.SetCursor(in.Serial, cursor, 0, 0), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the small cursor", showingOn(0, 151, 51, greenColor))
	quietOutputs(t, h, c, w)
	replace(t, c, cursor, image.Pt(40, 4), testclient.Fill(0, 0xff, 0))
	h.Eventually("the grown cursor", showingOn(0, 158, 51, greenColor))
}

func TestALayerMovingItsPopupOntoAnotherOutputIsDrawnThere(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	layer := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorRight, Size: image.Pt(20, 20)}, testclient.Fill(0, 0, 0xff))
	if err := errors.Join(layer.Layer.SetMargin(0, 50, 0, 0), layer.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the layer to map", layer.Mapped)
	h.Eventually("the layer on the first output", showingOn(0, 100, 10, redColor))
	menu := c.Popup(layer, testclient.Placement{Size: image.Pt(40, 20), Anchor: image.Rect(19, 0, 20, 1), Edge: cxdg.XdgPositionerAnchorTopRight, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the menu to map", menu.Mapped)
	h.Eventually("the menu on the first output", showingOn(0, 130, 10, greenColor))
	quietOutputs(t, h, c, layer, menu)
	if err := errors.Join(layer.Layer.SetMargin(0, 0, 0, 0), layer.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the moved menu on the second output", showingOn(1, 20, 10, greenColor))
}
