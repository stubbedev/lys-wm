package harness

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/outputmanagement"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

var (
	barColor  = [3]byte{0x10, 0x80, 0x10}
	noteColor = [3]byte{0x80, 0x10, 0x80}
)

func topBar(interactivity cwlr.ZwlrLayerSurfaceV1KeyboardInteractivity) testclient.LayerSpec {
	return testclient.LayerSpec{
		Layer:         cwlr.ZwlrLayerShellV1LayerTop,
		Anchor:        cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight,
		Size:          image.Pt(0, 20),
		ExclusiveZone: 20,
		Interactivity: interactivity,
	}
}

func TestBarReservesSpaceForWindows(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	if bar.Logical != image.Pt(160, 20) {
		t.Fatalf("bar configured to %v, want the output's width", bar.Logical)
	}
	w := c.Window("win", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	if w.Logical != image.Pt(160, 80) {
		t.Fatalf("window configured to %v, want what the bar leaves", w.Logical)
	}
	h.Eventually("the bar above the window", showing(5, 5, barColor))
	h.Eventually("the window below the bar", showing(1, 21, [3]byte{0xff, 0, 0}))

	menu := c.Popup(bar, menuAt(image.Pt(10, 10), image.Pt(30, 30)), nil, testclient.Fill(0x80, 0x80, 0x80))
	c.DispatchUntil("the bar's popup to map", menu.Mapped)
	if menu.Placed != image.Rect(11, 11, 41, 41) {
		t.Fatalf("bar popup placed at %v", menu.Placed)
	}
	h.Eventually("the bar's popup over the window", showing(20, 30, gray))

	if err := errors.Join(menu.Destroy(), bar.Destroy()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the window to grow back", func() bool { return w.Logical == image.Pt(160, 100) })
	h.Eventually("the window at the top", showing(1, 1, [3]byte{0xff, 0, 0}))
}

func TestBackgroundLayersStayBelowWindows(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	wallpaper := c.Layer(testclient.LayerSpec{
		Layer:         cwlr.ZwlrLayerShellV1LayerBackground,
		Anchor:        cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight,
		ExclusiveZone: -1,
	}, testclient.Fill(noteColor[2], noteColor[1], noteColor[0]))
	c.DispatchUntil("the wallpaper to map", wallpaper.Mapped)
	h.Eventually("the wallpaper", showing(40, 30, noteColor))
	w := c.Window("win", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window over the wallpaper", showing(1, 1, [3]byte{0xff, 0, 0}))
}

func TestLayerKeyboardInteractivity(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	r := &inputRig{t: t, h: h, c: c, in: in, w: w}
	r.expect("kb_enter win", "enter win 80,50", "frame")

	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	in.Name(bar.Surface, "bar")
	c.DispatchUntil("the bar to map", bar.Mapped)
	r.expect("motion 80,30", "frame")
	r.moveOn(0, 10, 5)
	r.expect("leave win", "enter bar 10,5", "frame")
	r.click()
	r.expect("kb_leave win", "kb_enter bar", "button 272 1", "frame", "button 272 0", "frame")
	r.moveOn(0, 10, 50)
	r.expect("leave bar", "enter win 10,30", "kb_leave bar", "kb_enter win", "frame")

	launcher := c.Layer(testclient.LayerSpec{
		Layer:         cwlr.ZwlrLayerShellV1LayerOverlay,
		Size:          image.Pt(60, 30),
		Interactivity: cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityExclusive,
	}, testclient.Fill(noteColor[2], noteColor[1], noteColor[0]))
	in.Name(launcher.Surface, "launcher")
	c.DispatchUntil("the launcher to map", launcher.Mapped)
	r.expect("kb_leave win", "kb_enter launcher")
	h.Eventually("the launcher centred", showing(80, 50, noteColor))
	r.moveOn(0, 10, 90)
	r.expect("motion 10,70", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Keyboard().Key(30, true), b.Keyboard().Key(30, false))
	})
	r.expect("key 30 1", "key 30 0")
	if err := launcher.Destroy(); err != nil {
		t.Fatal(err)
	}
	r.expect("kb_leave nil", "kb_enter win")
}

func TestLayersCloseWithTheirOutput(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return b.RemoveOutput(b.Outputs()[0])
	})
	c.DispatchUntil("the bar closed", func() bool { return bar.Closed })
}

func TestALayerDroppingItsKeyboardGivesItBack(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	r := &inputRig{t: t, h: h, c: c, in: in, w: w}
	r.expect("kb_enter win", "enter win 80,50", "frame")

	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	in.Name(bar.Surface, "bar")
	c.DispatchUntil("the bar to map", bar.Mapped)
	r.expect("motion 80,30", "frame")
	r.moveOn(0, 10, 5)
	r.expect("leave win", "enter bar 10,5", "frame")
	r.click()
	r.expect("kb_leave win", "kb_enter bar", "button 272 1", "frame", "button 272 0", "frame")

	setInteractivity := func(i cwlr.ZwlrLayerSurfaceV1KeyboardInteractivity) {
		t.Helper()
		if err := errors.Join(bar.Layer.SetKeyboardInteractivity(i), bar.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
	}
	setInteractivity(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone)
	r.expect("kb_leave bar", "kb_enter win")

	setInteractivity(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand)
	c.Sync()
	r.h.tap(30)
	r.expect("key 30 1", "key 30 0")
}

func dockedLaptop(t *testing.T) *Harness {
	t.Helper()
	h := StartWith(t, func(*compositor.Config) {})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddNamedOutput("eDP-1", small)
		if err != nil {
			return err
		}
		_, err = b.AddNamedOutput("DP-1", small)
		return err
	})
	return h
}

func outputIndex(t *testing.T, h *Harness, name string) int {
	t.Helper()
	at := -1
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		at = slices.IndexFunc(b.Outputs(), func(o *headless.Output) bool { return o.Name() == name })
		return nil
	})
	if at < 0 {
		t.Fatalf("no output %s", name)
	}
	return at
}

func onOutput(o *cw.WlOutput, spec testclient.LayerSpec) testclient.LayerSpec {
	spec.Output = o
	return spec
}

func expectBarAcross(t *testing.T, h *Harness, name, when string) {
	t.Helper()
	shot := h.Rendered(outputIndex(t, h, name))
	for _, x := range []int{5, small.Size.X - 5} {
		if got := pixel(shot, x, 5); got != barColor {
			t.Errorf("%s: %s shows %v at %d,5, want the bar", when, name, got, x)
		}
	}
}

func TestALayerStaysOnItsOutputWhenTheOutputMoves(t *testing.T) {
	t.Parallel()
	moved := image.Pt(500, 0)
	for _, tc := range []struct {
		name string
		move func(*headless.Backend, *compositor.Compositor) error
	}{
		{"the lid closing", func(b *headless.Backend, _ *compositor.Compositor) error {
			return b.Switches().Toggle(backend.SwitchLid, true)
		}},
		{"the panel unplugged", func(b *headless.Backend, _ *compositor.Compositor) error {
			return b.RemoveOutput(b.Outputs()[0])
		}},
		{"a configuration placing it", func(b *headless.Backend, c *compositor.Compositor) error {
			return c.Configure([]outputmanagement.HeadConfig{{Output: b.Outputs()[1], Enabled: true, Position: &moved}}, true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := dockedLaptop(t)
			c := testclient.Connect(t, h.Socket())
			bar := c.Layer(onOutput(c.Outputs[1], topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone)), testclient.Fill(barColor[2], barColor[1], barColor[0]))
			c.DispatchUntil("the bar to map", bar.Mapped)
			h.Eventually("the bar on the external output", showingOn(1, 5, 5, barColor))

			h.Do(tc.move)
			c.Sync()

			expectBarAcross(t, h, "DP-1", tc.name)
			if bar.Closed || bar.Logical != image.Pt(small.Size.X, 20) {
				t.Errorf("after %s the bar is closed %v, configured to %v", tc.name, bar.Closed, bar.Logical)
			}
		})
	}
}

func TestLayersAfterTheLidOpensAgain(t *testing.T) {
	t.Parallel()
	h := dockedLaptop(t)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(onOutput(c.Outputs[1], topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone)), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	lid := func(closed bool) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return b.Switches().Toggle(backend.SwitchLid, closed)
		})
		c.Sync()
	}
	lid(true)
	lid(false)
	expectBarAcross(t, h, "DP-1", "the lid opened again")

	back := testclient.Connect(t, h.Socket())
	panel := back.Layer(onOutput(back.Outputs[1], topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone)), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	back.DispatchUntil("the panel's bar to map", panel.Mapped)
	expectBarAcross(t, h, "eDP-1", "a bar opened on the panel coming back")
}

func TestALayerOnAnOutputStartedWithTheLidClosed(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(*compositor.Config) {})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if err := b.Switches().Toggle(backend.SwitchLid, true); err != nil {
			return err
		}
		if _, err := b.AddNamedOutput("eDP-1", small); err != nil {
			return err
		}
		_, err := b.AddNamedOutput("DP-1", small)
		return err
	})
	c := testclient.Connect(t, h.Socket())
	if len(c.Outputs) != 1 {
		t.Fatalf("%d outputs advertised with the lid closed, want the external one", len(c.Outputs))
	}
	bar := c.Layer(onOutput(c.Outputs[0], topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone)), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	expectBarAcross(t, h, "DP-1", "started with the lid closed")
}
