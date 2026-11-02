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
