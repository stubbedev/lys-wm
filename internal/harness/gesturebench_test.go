package harness

import (
	"image"
	"strconv"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

var swipeBenchMode = backend.Mode{Size: image.Pt(320, 200), RefreshMHz: 60000, Preferred: true}

func swipeBench(b *testing.B, windows int) *Harness {
	b.Helper()
	h := start(b, setup{open: softBackend, configure: func(cfg *compositor.Config) {
		cfg.Gestures.Distance = float64(swipeBenchMode.Size.X)
	}}, swipeBenchMode)
	c := testclient.Connect(b, h.Socket())
	for _, ws := range []string{"1", "2"} {
		h.dispatch("workspace " + ws)
		for i := range windows {
			w := c.Window(ws+"-"+strconv.Itoa(i), testclient.Fill(0, 0, 0xff))
			c.DispatchUntil("the window to map", w.Mapped)
			w.Stop()
		}
	}
	h.dispatch("workspace 1")
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error {
		if err := p.GestureBegin(backend.Swipe, 3); err != nil {
			return err
		}
		return p.SwipeAt(time.Hour, -64, 0)
	})
	h.Rendered(0)
	return h
}

func BenchmarkSwipeUpdate(b *testing.B) {
	h := swipeBench(b, 4)
	b.ReportAllocs()
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error {
		at := time.Hour
		dx := 1.0
		b.ResetTimer()
		for i := range b.N {
			at += 8 * time.Millisecond
			if i%64 == 0 {
				dx = -dx
			}
			if err := p.SwipeAt(at, dx, 0); err != nil {
				return err
			}
		}
		b.StopTimer()
		return nil
	})
}

func BenchmarkSwipeFrame(b *testing.B) {
	h := swipeBench(b, 4)
	b.ReportAllocs()
	h.Do(func(x *headless.Backend, c *compositor.Compositor) error {
		o, p := x.Outputs()[0], x.Pointer()
		at := time.Hour
		dx := 1.0
		b.ResetTimer()
		for i := range b.N {
			at += 8 * time.Millisecond
			if i%64 == 0 {
				dx = -dx
			}
			if err := p.SwipeAt(at, dx, 0); err != nil {
				return err
			}
			if err := c.Frame(o); err != nil {
				return err
			}
		}
		b.StopTimer()
		return nil
	})
}

func BenchmarkSwipeGesture(b *testing.B) {
	h := swipeBench(b, 4)
	b.ReportAllocs()
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error {
		at := time.Hour
		if err := p.GestureEndAt(at, backend.Swipe, true); err != nil {
			return err
		}
		b.ResetTimer()
		for range b.N {
			if err := p.GestureBegin(backend.Swipe, 3); err != nil {
				return err
			}
			for range 2 {
				at += 8 * time.Millisecond
				if err := p.SwipeAt(at, -20, 0); err != nil {
					return err
				}
			}
			if err := p.GestureEndAt(at, backend.Swipe, true); err != nil {
				return err
			}
		}
		b.StopTimer()
		return nil
	})
}
