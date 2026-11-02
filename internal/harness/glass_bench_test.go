package harness

import (
	"errors"
	"image"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	benchMode = backend.Mode{Size: image.Pt(1920, 1080), RefreshMHz: 60000, Preferred: true}
	benchFull = wm.Look{
		Tint: glassTint, SaturationShift: 0.5, BrightnessShift: 0.05, ContrastShift: 0.1, Grain: 0.05,
		Refraction: wm.Refraction{Bezel: 16, Amount: 8}, Specular: wm.Specular{Strength: 0.6, Width: 1},
	}
)

func benchHarness(b *testing.B, open func(testing.TB) setup, look wm.Look, cursor bool) *Harness {
	b.Helper()
	half := float32(0.5)
	s := open(b)
	s.configure = func(cfg *compositor.Config) {
		if cursor {
			cfg.Animations = animated(b)
		}
		cfg.Decor.Rounding = 12
		cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 8, Passes: 2, Look: look}
		cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^glass$`), Mode: wm.RuleFloat, Size: image.Pt(1000, 600), Opacity: &half}}
	}
	if cursor {
		s.open = withCursorPlanes(s.open, image.Pt(64, 64))
	}
	h := start(b, s, benchMode)
	c := testclient.Connect(b, h.Socket())
	bg := c.Window("background", quadrants)
	c.DispatchUntil("the background to map", bg.Mapped)
	bg.Stop()
	glass := c.Window("glass", testclient.Fill(0, 0, 0))
	c.DispatchUntil("the glass to map", glass.Mapped)
	glass.Stop()
	h.Rendered(0)
	return h
}

func benchRenderers(b *testing.B, run func(b *testing.B, open func(testing.TB) setup)) {
	for _, r := range []struct {
		name string
		open func(testing.TB) setup
	}{
		{"soft", func(testing.TB) setup { return setup{open: softBackend} }},
		{"gles", func(tb testing.TB) setup {
			testenv.GPU(tb)
			return setup{open: glesBackend}
		}},
		{"vulkan", func(tb testing.TB) setup {
			testenv.Vulkan(tb)
			return setup{open: vulkanBackend}
		}},
	} {
		b.Run(r.name, func(b *testing.B) { run(b, r.open) })
	}
}

func BenchmarkGlassIdleFrame(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		for name, look := range map[string]wm.Look{"off": {}, "on": benchFull} {
			b.Run(name, func(b *testing.B) {
				h := benchHarness(b, open, look, false)
				h.Do(func(bk *headless.Backend, c *compositor.Compositor) error {
					o := bk.Outputs()[0]
					for b.Loop() {
						if err := c.Frame(o); err != nil {
							return err
						}
					}
					return nil
				})
			})
		}
	})
}

func BenchmarkGlassRimStep(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		h := benchHarness(b, open, wm.Look{Specular: wm.Specular{Strength: 0.6, Width: 1}}, true)
		h.Do(func(bk *headless.Backend, c *compositor.Compositor) error {
			o := bk.Outputs()[0]
			corners := [2]float64{0.02, 0.98}
			i := 0
			for b.Loop() {
				i++
				if err := errors.Join(bk.Pointer().MoveTo(o, corners[i%2], 0.5), bk.Pointer().Frame()); err != nil {
					return err
				}
				if err := c.Frame(o); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func benchOverview(b *testing.B, open func(testing.TB) setup, windows int, moving bool) {
	b.Helper()
	now := time.Hour
	s := open(b)
	s.configure = func(cfg *compositor.Config) {
		cfg.Animations = animated(b)
		cfg.Clock = func() time.Duration { return now }
		cfg.Decor.Rounding = 12
		cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^pile$`), Mode: wm.RuleFloat, Size: image.Pt(1000, 700)}}
	}
	h := start(b, s, benchMode)
	c := testclient.Connect(b, h.Socket())
	for range windows {
		w := c.Window("pile", testclient.Fill(0x40, 0x60, 0x80))
		c.DispatchUntil("a window to map", w.Mapped)
		w.Stop()
	}
	h.dispatch("overview show")
	h.Do(func(bk *headless.Backend, comp *compositor.Compositor) error {
		o := bk.Outputs()[0]
		now += 10 * time.Second
		if err := comp.Frame(o); err != nil {
			return err
		}
		frame := 0
		for b.Loop() {
			if moving {
				frame++
				now += 4 * time.Millisecond
				if frame%40 == 0 {
					if err := comp.Dispatch(wm.OverviewAction{Mode: wm.OverviewToggle}); err != nil {
						return err
					}
				}
			}
			if err := comp.Frame(o); err != nil {
				return err
			}
		}
		return nil
	})
}

func BenchmarkOverviewFrame(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		for _, n := range []int{10, 30} {
			for name, moving := range map[string]bool{"settled": false, "moving": true} {
				b.Run(strconv.Itoa(n)+"/"+name, func(b *testing.B) { benchOverview(b, open, n, moving) })
			}
		}
	})
}

func TestASettledOverviewFrameAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates")
	}
	s := setup{open: softBackend}
	s.configure = func(cfg *compositor.Config) {
		cfg.Decor.Rounding = 12
		cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^pile$`), Mode: wm.RuleFloat, Size: image.Pt(40, 30)}}
	}
	h := start(t, s, small)
	c := testclient.Connect(t, h.Socket())
	for range 3 {
		w := c.Window("pile", testclient.Fill(0x40, 0x60, 0x80))
		c.DispatchUntil("a window to map", w.Mapped)
		w.Stop()
	}
	h.dispatch("overview show")
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		if err := comp.Frame(o); err != nil {
			return err
		}
		var failed error
		if got := testing.AllocsPerRun(50, func() {
			if err := comp.Frame(o); err != nil {
				failed = err
			}
		}); got != 0 {
			t.Errorf("a settled overview frame allocates %v times", got)
		}
		return failed
	})
}
