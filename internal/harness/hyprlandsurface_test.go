package harness

import (
	"errors"
	"image"
	"math"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cam "github.com/stubbedev/lys-wm/internal/protocol/client/alphamodifier"
	chs "github.com/stubbedev/lys-wm/internal/protocol/client/hyprlandsurface"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wire"
)

var greenPixel = [3]byte{0, 0xff, 0}

func hyprlandSurfaces(t *testing.T, c *testclient.Client) *chs.HyprlandSurfaceManagerV1 {
	t.Helper()
	m, err := chs.NewHyprlandSurfaceManagerV1(c.Display, 2)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	return m
}

func hyprlandSurfaceOf(t *testing.T, m *chs.HyprlandSurfaceManagerV1, s *cw.WlSurface) *chs.HyprlandSurfaceV1 {
	t.Helper()
	hs, err := m.GetHyprlandSurface(s)
	if err != nil {
		t.Fatal(err)
	}
	return hs
}

func redNear(want float64) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		p := pixel(snapshot(b.Outputs()[0]), 30, 30)
		expected := float64(backgroundPixel[0]) + want*float64(0xff-int(backgroundPixel[0]))
		return math.Abs(float64(p[0])-expected) <= 6 && p[1] <= backgroundPixel[1]+6
	}
}

func TestHyprlandOpacityMultipliesLikeAnAlphaModifier(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("hyprland opacity", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window opaque", showing(30, 30, redPixel))
	hs := hyprlandSurfaceOf(t, hyprlandSurfaces(t, c), w.Surface)
	commit := func(errs ...error) {
		t.Helper()
		if err := errors.Join(append(errs, w.Surface.Commit(), c.Display.Flush())...); err != nil {
			t.Fatal(err)
		}
	}

	commit(hs.SetOpacity(wire.FixedFromFloat(0)))
	h.Eventually("the window fully faded", showing(30, 30, backgroundPixel))
	commit(hs.SetOpacity(wire.FixedFromFloat(0.5)))
	h.Eventually("the window half faded", redNear(0.5))

	am, err := cam.NewWpAlphaModifierV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(am)
	fade, err := am.GetSurface(w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	commit(fade.SetMultiplier(math.MaxUint32 / 2))
	h.Eventually("both factors multiplied to a quarter", redNear(0.25))
	commit(hs.Destroy())
	h.Eventually("the alpha modifier alone once the hyprland surface is gone", redNear(0.5))
	commit(fade.Destroy())
	h.Eventually("the window opaque again", showing(30, 30, redPixel))
}

func TestHyprlandOpacityOutsideZeroToOneIsAnError(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	hs := hyprlandSurfaceOf(t, hyprlandSurfaces(t, c), c.Surface())
	if err := hs.SetOpacity(wire.FixedFromFloat(1.5)); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(chs.HyprlandSurfaceV1ErrorOutOfRange))
}

func TestASecondHyprlandSurfaceIsAnError(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := hyprlandSurfaces(t, c)
	s := c.Surface()
	hyprlandSurfaceOf(t, m, s)
	hyprlandSurfaceOf(t, m, s)
	c.ExpectProtocolError(uint32(chs.HyprlandSurfaceManagerV1ErrorAlreadyConstructed))
}

func TestHyprlandSurfaceRequestsAfterTheSurfaceIsGoneAreAnError(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	s := c.Surface()
	hs := hyprlandSurfaceOf(t, hyprlandSurfaces(t, c), s)
	if err := errors.Join(s.Destroy(), hs.SetVisibleRegion(nil)); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(chs.HyprlandSurfaceV1ErrorNoSurface))
}

func paintRects(red ...image.Rectangle) testclient.Painter {
	return func(pix []byte, stride int, size image.Point, _ int) {
		for y := range size.Y {
			for x := range size.X {
				c := [3]byte{0, 0xff, 0}
				for _, r := range red {
					if image.Pt(x, y).In(r) {
						c = [3]byte{0xff, 0, 0}
					}
				}
				copy(pix[y*stride+x*4:], []byte{c[2], c[1], c[0], 0xff})
			}
		}
	}
}

func outputColors(b *headless.Backend, r image.Rectangle) map[[3]byte]int {
	snap := snapshot(b.Outputs()[0])
	seen := map[[3]byte]int{}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			seen[pixel(snap, x, y)]++
		}
	}
	return seen
}

func TestAHyprlandVisibleRegionClipsDrawingToItsBufferRects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		transform cw.WlOutputTransform
		scale     int32
	}{
		{"normal", cw.WlOutputTransformNormal, 1},
		{"rotated", cw.WlOutputTransform90, 1},
		{"flipped", cw.WlOutputTransformFlipped270, 1},
		{"scaled", cw.WlOutputTransformNormal, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := Start(t, small)
			c := testclient.Connect(t, h.Socket())
			w := c.Window("visible region", testclient.Fill(0, 0xff, 0))
			c.DispatchUntil("the window to map", w.Mapped)
			w.Stop()
			size := image.Pt(w.Size.X*int(tc.scale), w.Size.Y*int(tc.scale))
			if tc.transform == cw.WlOutputTransform90 || tc.transform == cw.WlOutputTransformFlipped270 {
				size = image.Pt(size.Y, size.X)
			}
			half := size.Div(2)
			red := []image.Rectangle{
				image.Rect(0, 0, half.X, half.Y),
				image.Rect(half.X, half.Y, size.X, size.Y),
			}
			g, err := c.Compositor.CreateRegion()
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range red {
				if err := g.Add(int32(r.Min.X), int32(r.Min.Y), int32(r.Dx()), int32(r.Dy())); err != nil {
					t.Fatal(err)
				}
			}
			hs := hyprlandSurfaceOf(t, hyprlandSurfaces(t, c), w.Surface)
			buf := c.Buffer(size, 0, paintRects(red...))
			if err := errors.Join(
				w.Surface.SetBufferTransform(tc.transform), w.Surface.SetBufferScale(tc.scale),
				w.Surface.Attach(buf, 0, 0), w.Surface.DamageBuffer(0, 0, int32(size.X), int32(size.Y)),
				hs.SetVisibleRegion(g), g.Destroy(), w.Surface.Commit(), c.Display.Flush(),
			); err != nil {
				t.Fatal(err)
			}
			inner := image.Rect(8, 8, small.Size.X-8, small.Size.Y-8)
			var last map[[3]byte]int
			h.Eventually("only the red rects drawn", func(b *headless.Backend, _ *compositor.Compositor) bool {
				last = outputColors(b, inner)
				return last[greenPixel] == 0 && last[redPixel] > 0 && last[backgroundPixel] > 0
			})
			if quarter := inner.Dx() * inner.Dy() / 4; last[redPixel] < quarter || last[backgroundPixel] < quarter {
				t.Fatalf("the visible region showed %v inside %v", last, inner)
			}

			if err := errors.Join(hs.SetVisibleRegion(nil), w.Surface.Commit(), c.Display.Flush()); err != nil {
				t.Fatal(err)
			}
			h.Eventually("the whole buffer drawn once the region is empty", func(b *headless.Backend, _ *compositor.Compositor) bool {
				last = outputColors(b, inner)
				return last[greenPixel] > 0 && last[backgroundPixel] == 0
			})
		})
	}
}

func TestAHyprlandVisibleRegionClipsDamage(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("visible damage", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window drawn whole", showing(130, 80, greenPixel))
	w.Stop()
	visible := image.Rect(10, 10, 50, 40)
	g, err := c.Compositor.CreateRegion()
	if err != nil {
		t.Fatal(err)
	}
	hs := hyprlandSurfaceOf(t, hyprlandSurfaces(t, c), w.Surface)
	if err := errors.Join(g.Add(int32(visible.Min.X), int32(visible.Min.Y), int32(visible.Dx()), int32(visible.Dy())), hs.SetVisibleRegion(g), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the region clipped", showing(130, 80, backgroundPixel))
	settle(t, h)

	presents := presentCount(h)
	update := c.Buffer(w.Size, 1, testclient.Fill(0, 0, 0xff))
	if err := errors.Join(w.Surface.Attach(update, 0, 0), w.Surface.DamageBuffer(0, 0, int32(w.Size.X), int32(w.Size.Y)), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the update presented", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > presents
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		for _, d := range o.LastDamage() {
			if !d.In(visible) {
				t.Errorf("a whole-buffer update under a %v visible region repainted %v", visible, o.LastDamage())
			}
		}
		if got := pixel(snapshot(o), 20, 20); got != redPixel {
			t.Errorf("the visible part shows %v after the update", got)
		}
		if got := pixel(snapshot(o), 130, 80); got != backgroundPixel {
			t.Errorf("the hidden part shows %v after the update", got)
		}
		return nil
	})
}
