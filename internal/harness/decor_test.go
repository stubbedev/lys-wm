package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/golden"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestBordersAndTabBars(t *testing.T) {
	decor := compositor.DefaultDecor()
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Decor = decor }, tall)
	c := testclient.Connect(t, h.Socket())
	gray := testclient.Fill(0x60, 0x60, 0x60)
	left := c.Window("left", gray)
	c.DispatchUntil("left to map", left.Mapped)
	if left.Logical != image.Pt(158, 118) {
		t.Fatalf("a lone window is %v, want the output less a 1px border", left.Logical)
	}
	right := c.Window("right", gray)
	c.DispatchUntil("right to map", right.Mapped)
	active := [3]byte{decor.ActiveBorder.R, decor.ActiveBorder.G, decor.ActiveBorder.B}
	inactive := [3]byte{decor.InactiveBorder.R, decor.InactiveBorder.G, decor.InactiveBorder.B}
	h.Eventually("the focused window's border", showing(80, 100, active))
	h.Eventually("the other window's border", showing(79, 100, inactive))

	h.dispatch("makegroup tab")
	third := c.Window("third", gray)
	c.DispatchUntil("third to map", third.Mapped)
	if want := image.Pt(78, 98); third.Logical != want {
		t.Fatalf("a tabbed window is %v, want %v below the 20px tab strip", third.Logical, want)
	}
	tabActive := [3]byte{decor.Tab.Active.Background.R, decor.Tab.Active.Background.G, decor.Tab.Active.Background.B}
	h.Eventually("the active tab on the right half", showing(158, 2, tabActive))
	golden.Assert(t, "decor", h.Rendered(0))
}

var tall = backend.Mode{Size: image.Pt(160, 120), RefreshMHz: 60000, Preferred: true}

func TestGapsSeparateTiledWindowsAndReloadRearranges(t *testing.T) {
	var next compositor.Config
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.DefaultDecor()
		cfg.WM.Gaps = wm.Gaps{Inner: 6, Outer: wm.Insets{Top: 4, Right: 4, Bottom: 4, Left: 4}}
		next = *cfg
		next.WM.Gaps = wm.Gaps{}
	}, tall)
	c := testclient.Connect(t, h.Socket())
	gray := testclient.Fill(0x60, 0x60, 0x60)
	left := c.Window("left", gray)
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", gray)
	c.DispatchUntil("right to map", right.Mapped)
	c.DispatchUntil("both windows tiled with gaps", func() bool {
		return left.Logical == image.Pt(71, 110) && right.Logical == image.Pt(71, 110)
	})
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	h.Eventually("the inner gap shows the background", showing(79, 60, bg))
	h.Eventually("the outer gap shows the background", showing(2, 60, bg))
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(next) })
	c.DispatchUntil("the reload to drop the gaps", func() bool {
		return left.Logical == image.Pt(78, 118) && right.Logical == image.Pt(78, 118)
	})
}

func TestRoundedWindowsAndBorders(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.DefaultDecor()
		cfg.Decor.BorderWidth, cfg.Decor.Rounding = 2, 8
		cfg.WM.Gaps = wm.Gaps{Inner: 6, Outer: wm.Insets{Top: 6, Right: 6, Bottom: 6, Left: 6}}
	}, tall)
	c := testclient.Connect(t, h.Socket())
	left := c.Window("left", testclient.Fill(0x60, 0x60, 0x60))
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", testclient.Fill(0x30, 0x80, 0x30))
	c.DispatchUntil("right to map", right.Mapped)
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	h.Eventually("the right window's rounded corner shows the background", showing(83, 6, bg))
	h.Eventually("the right window's border along its straight edge", showing(83, 60, [3]byte{0xcb, 0xa6, 0xf7}))
	golden.Assert(t, "rounded", h.Rendered(0))
}

func TestARoundedWindowThatFillsTheOutputIsComposited(t *testing.T) {
	h := StartGLES(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.DefaultDecor()
		cfg.Decor.BorderWidth, cfg.Decor.Rounding = 0, 6
	}, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[0].AllowScanout(primaryPlane)
		return nil
	})
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("tiled", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	c.DispatchUntil("the pointer to enter", func() bool { return in.Serial != 0 })
	if err := errors.Join(in.Pointer.SetCursor(in.Serial, nil, 0, 0), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the window to fill the output", func() bool { return w.Logical == small.Size })
	quiesce(t, h, c, w)
	s := &scanoutScene{h: h, c: c, w: w}
	before := scanouts(h)
	b := c.DMABUF(small.Size, testclient.Fill(0, 0, 0xff))
	if err := errors.Join(w.Surface.Attach(b.WL, 0, 0), w.Surface.DamageBuffer(0, 0, 160, 100), w.Surface.Commit(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	settle(t, h)
	if n := scanouts(h); n != before {
		t.Fatalf("a rounded window scanned out %d frames, which would square its corners", n-before)
	}
	h.dispatch("fullscreen")
	c.DispatchUntil("the fullscreen configure", func() bool { return w.Logical == small.Size })
	s.show(t, "the same buffer once fullscreen, which is never rounded", c.DMABUF(small.Size, testclient.Fill(0, 0xff, 0)))
}
