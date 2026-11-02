package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func benchPointerFrames(b *testing.B, h *Harness) {
	b.Helper()
	busy := h.Busy()
	frames := func() (n uint64) {
		h.Do(func(bk *headless.Backend, _ *compositor.Compositor) error {
			n = bk.Outputs()[0].Frames()
			return nil
		})
		return n
	}
	b.ResetTimer()
	for i := range b.N {
		before := frames()
		h.Do(func(bk *headless.Backend, _ *compositor.Compositor) error {
			x := 0.25 + 0.5*float64(i%2)
			return errors.Join(bk.Pointer().MoveTo(bk.Outputs()[0], x, 0.5), bk.Pointer().Frame())
		})
		for frames() == before {
			continue
		}
	}
	b.StopTimer()
	b.ReportMetric(float64((h.Busy()-busy).Nanoseconds())/float64(b.N), "busy-ns/frame")
}

func benchZoomScene(b *testing.B, open func(testing.TB) setup) *Harness {
	b.Helper()
	s := open(b)
	s.configure = func(*compositor.Config) {}
	h := start(b, s, benchMode)
	c := testclient.Connect(b, h.Socket())
	w := c.Window("background", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	settle(b, h)
	return h
}

func BenchmarkPointerFramesWithoutZoom(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		benchPointerFrames(b, benchZoomScene(b, open))
	})
}

func BenchmarkPointerFramesAfterAZoom(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		h := benchZoomScene(b, open)
		h.dispatch("zoom set 2")
		settle(b, h)
		h.dispatch("zoom reset")
		settle(b, h)
		benchPointerFrames(b, h)
	})
}

func BenchmarkPointerFramesZoomed(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		h := benchZoomScene(b, open)
		h.dispatch("zoom set 2")
		settle(b, h)
		benchPointerFrames(b, h)
	})
}

func BenchmarkAnIdleFrameWithoutZoom(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		h := benchZoomScene(b, open)
		b.ReportAllocs()
		h.Do(func(bk *headless.Backend, comp *compositor.Compositor) error {
			o := bk.Outputs()[0]
			b.ResetTimer()
			for range b.N {
				if err := comp.Frame(o); err != nil {
					return err
				}
			}
			b.StopTimer()
			return nil
		})
	})
}

func benchMovingFrames(b *testing.B, h *Harness, at func(i int) float64, y float64) {
	b.Helper()
	b.ReportAllocs()
	h.Do(func(bk *headless.Backend, comp *compositor.Compositor) error {
		o := bk.Outputs()[0]
		p := bk.Pointer()
		b.ResetTimer()
		for i := range b.N {
			if err := errors.Join(p.MoveTo(o, at(i), y), p.Frame()); err != nil {
				return err
			}
			if err := comp.Frame(o); err != nil {
				return err
			}
		}
		b.StopTimer()
		return nil
	})
}

func alternating(i int) float64 { return 0.25 + 0.5*float64(i%2) }

func BenchmarkMovingFrameUnzoomed(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		benchMovingFrames(b, benchZoomScene(b, open), alternating, 0.5)
	})
}

func BenchmarkMovingFrameZoomed(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		h := benchZoomScene(b, open)
		h.dispatch("zoom set 2")
		settle(b, h)
		benchMovingFrames(b, h, alternating, 0.5)
	})
}

func BenchmarkMovingFrameDragging(b *testing.B) {
	for name, floating := range map[string]bool{"tiled": false, "floating": true} {
		b.Run(name, func(b *testing.B) {
			s := setup{open: softBackend, configure: func(cfg *compositor.Config) {
				withBinds(b, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"})(cfg)
			}}
			h := start(b, s, benchMode)
			c := testclient.Connect(b, h.Socket())
			for _, name := range []string{"a", "b", "c", "d"} {
				w := c.Window(name, testclient.Fill(0x40, 0x40, 0x40))
				c.DispatchUntil(name+" to map", w.Mapped)
				w.Stop()
			}
			if floating {
				h.dispatch("togglefloating")
			}
			settle(b, h)
			h.key(keySuper, true)
			h.Do(func(bk *headless.Backend, _ *compositor.Compositor) error {
				o, p := bk.Outputs()[0], bk.Pointer()
				return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.Button(btnLeft, true), p.Frame())
			})
			at := func(i int) float64 { return 0.002 + 0.001*float64(i%2) }
			if !floating {
				at = func(i int) float64 { return 0.9 + 0.01*float64(i%2) }
			}
			benchMovingFrames(b, h, at, 0.5)
			h.Do(func(bk *headless.Backend, _ *compositor.Compositor) error {
				p := bk.Pointer()
				return errors.Join(p.Button(btnLeft, false), p.Frame())
			})
		})
	}
}
