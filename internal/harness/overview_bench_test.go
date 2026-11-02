package harness

import (
	"errors"
	"image"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func overviewBenchHarness(b *testing.B, open func(testing.TB) setup, now *time.Duration, windows int, tiled bool) (*Harness, []*testclient.Window) {
	b.Helper()
	s := open(b)
	s.configure = func(cfg *compositor.Config) {
		cfg.Animations = springTable(b)
		cfg.Clock = func() time.Duration { return *now }
		cfg.Decor.Rounding = 12
		if !tiled {
			cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^float`), Mode: wm.RuleFloat, Size: image.Pt(1000, 700)}}
		}
	}
	h := start(b, s, benchMode)
	c := testclient.Connect(b, h.Socket())
	all := make([]*testclient.Window, 0, windows)
	for i := range windows {
		w := c.Window("float"+strconv.Itoa(i), quadrants)
		c.DispatchUntil("a window to map", w.Mapped)
		w.Stop()
		all = append(all, w)
	}
	h.Rendered(0)
	return h, all
}

func BenchmarkFrameClosed30Windows(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		var now time.Duration
		h, _ := overviewBenchHarness(b, open, &now, 30, false)
		h.Do(func(bk *headless.Backend, c *compositor.Compositor) error {
			o := bk.Outputs()[0]
			corners := [2]float64{0.02, 0.98}
			i := 0
			for b.Loop() {
				i++
				if err := errors.Join(bk.Pointer().MoveTo(o, corners[i%2], 0.5), bk.Pointer().Frame(), c.Frame(o)); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func BenchmarkOverviewMoving(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		b.Run("float", func(b *testing.B) { overviewMoving(b, open, false) })
		b.Run("tiled", func(b *testing.B) { overviewMoving(b, open, true) })
	})
}

func overviewMoving(b *testing.B, open func(testing.TB) setup, tiled bool) {
	var now time.Duration
	h, _ := overviewBenchHarness(b, open, &now, 30, tiled)
	h.Do(func(bk *headless.Backend, c *compositor.Compositor) error {
		o := bk.Outputs()[0]
		toggle, err := wm.ParseAction("overview toggle")
		if err != nil {
			return err
		}
		if err := c.Dispatch(toggle); err != nil {
			return err
		}
		for b.Loop() {
			now += 8 * time.Millisecond
			if p := c.OverviewProgress(now); p > 0.98 || p < 0.02 {
				if err := c.Dispatch(toggle); err != nil {
					return err
				}
			}
			if err := c.Frame(o); err != nil {
				return err
			}
			if _, err := o.Snapshot(); err != nil {
				return err
			}
		}
		return nil
	})
}

func BenchmarkOverviewSettledOneWindowDamages(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		var now time.Duration
		h, _ := overviewBenchHarness(b, open, &now, 30, false)
		h.dispatch("overview show")
		now += time.Minute
		h.Rendered(0)
		h.Do(func(bk *headless.Backend, c *compositor.Compositor) error {
			o := bk.Outputs()[0]
			corners := [2]float64{0.02, 0.98}
			i := 0
			for b.Loop() {
				i++
				if err := errors.Join(bk.Pointer().MoveTo(o, corners[i%2], 0.5), bk.Pointer().Frame(), c.Frame(o)); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func BenchmarkReadback(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		var now time.Duration
		h, _ := overviewBenchHarness(b, open, &now, 1, true)
		h.Do(func(bk *headless.Backend, _ *compositor.Compositor) error {
			o := bk.Outputs()[0]
			for b.Loop() {
				if _, err := o.Snapshot(); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func BenchmarkOverviewFrameAllocs(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		var now time.Duration
		h, _ := overviewBenchHarness(b, open, &now, 30, true)
		h.dispatch("overview show")
		h.Rendered(0)
		h.Do(func(bk *headless.Backend, c *compositor.Compositor) error {
			o := bk.Outputs()[0]
			b.ReportAllocs()
			for b.Loop() {
				now += time.Millisecond
				if err := c.Frame(o); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func BenchmarkThumbRedraw(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		var now time.Duration
		h, _ := overviewBenchHarness(b, open, &now, 10, true)
		h.dispatch("overview show")
		now += time.Minute
		h.Rendered(0)
		h.Do(func(bk *headless.Backend, c *compositor.Compositor) error {
			o := bk.Outputs()[0]
			for b.Loop() {
				c.DirtyOverviewThumbs()
				if err := c.Frame(o); err != nil {
					return err
				}
				if _, err := o.Snapshot(); err != nil {
					return err
				}
			}
			return nil
		})
	})
}
