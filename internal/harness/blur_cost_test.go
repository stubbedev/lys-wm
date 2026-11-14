package harness

import (
	"errors"
	"image"
	"regexp"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var glassRect = image.Rect(50, 35, 110, 65)

func clearWindowRule(title string) wm.Rule {
	zero := float32(0)
	return wm.Rule{Match: wm.Match{Title: regexp.MustCompile("^" + title + "$")}, Mode: wm.RuleFloat, Size: wm.Px(glassRect.Dx(), glassRect.Dy()), Opacity: &zero}
}

func TestAHiddenBlurCostsNothing(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 2, Passes: 1}
			cfg.Rules = []wm.Rule{
				clearWindowRule("glass"),
				{Match: wm.Match{Title: regexp.MustCompile(`^cover$`)}, Mode: wm.RuleFloat, Size: wm.Px(80, 50)},
				{Match: wm.Match{Title: regexp.MustCompile(`^behind$`)}, Mode: wm.RuleFloat, Size: wm.Px(80, 50)},
			}
		}, small)
		c := testclient.Connect(t, h.Socket())
		behind := c.Window("behind", fineChecker)
		c.DispatchUntil("the window behind to map", behind.Mapped)
		glass := c.Window("glass", testclient.Fill(0, 0, 0))
		c.DispatchUntil("the glass to map", glass.Mapped)
		glass.Stop()
		cover := c.Window("cover", testclient.Fill(0, 0xff, 0))
		c.DispatchUntil("the cover to map", cover.Mapped)
		h.Eventually("the cover shown over the glass", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return pixel(snapshot(b.Outputs()[0]), 100, 60) == [3]byte{0, 0xff, 0}
		})
		f0, r0 := framesAndBlurs(h)
		pumpFrames(h, c, 12)
		f1, r1 := framesAndBlurs(h)
		if f1 == f0 {
			t.Fatal("the animated cover presented no frame")
		}
		if r1 != r0 {
			t.Errorf("%d blur redraws over %d frames behind an opaque window", r1-r0, f1-f0)
		}
		if err := errors.Join(cover.Destroy(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the glass frosted again as soon as it shows", frostedAt(100, 60))
	})
}

func TestABlurBehindAnOpaqueWindowIgnoresWhatThatWindowHides(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 2, Passes: 1}
			cfg.Rules = []wm.Rule{
				clearWindowRule("glass"),
				{Match: wm.Match{Title: regexp.MustCompile(`^behind$`)}, Mode: wm.RuleFloat, Size: wm.Px(80, 50)},
				{Match: wm.Match{Title: regexp.MustCompile(`^wall$`)}, Mode: wm.RuleFloat, Size: wm.Px(120, 80)},
			}
		}, small)
		c := testclient.Connect(t, h.Socket())
		behind := c.Window("behind", fineChecker)
		c.DispatchUntil("the window behind to map", behind.Mapped)
		wall := c.Window("wall", testclient.Fill(0, 0, 0xff))
		c.DispatchUntil("the wall to map", wall.Mapped)
		wall.Stop()
		glass := c.Window("glass", testclient.Fill(0, 0, 0))
		c.DispatchUntil("the glass to map", glass.Mapped)
		h.Eventually("the glass over the wall", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return near(pixel(snapshot(b.Outputs()[0]), 100, 60), [3]byte{0xff, 0, 0})
		})
		pumpFrames(h, c, 6)
		r0 := blurRedraws(h)
		f0 := frames(h)
		pumpFrames(h, c, 6)
		if frames(h) == f0 {
			t.Fatal("the animated glass presented no frame")
		}
		if r := blurRedraws(h); r != r0 {
			t.Errorf("%d blur redraws for a window hidden behind the wall the glass blurs", r-r0)
		}
	})
}

func blurRedraws(h *Harness) uint64 {
	var n uint64
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		n = c.BlurRedraws()
		return nil
	})
	return n
}

func frames(h *Harness) uint64 {
	var n uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		n = b.Outputs()[0].Frames()
		return nil
	})
	return n
}

func pumpFrames(h *Harness, c *testclient.Client, n uint64) {
	h.t.Helper()
	until := frames(h) + n
	c.DispatchUntil("frames to pass", func() bool { return frames(h) >= until })
}

func tiledGlassRedraws(t *testing.T, live bool) uint64 {
	t.Helper()
	zero := float32(0)
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 2, Passes: 1, LiveTiled: live}
		cfg.Rules = []wm.Rule{{Match: wm.Match{Title: regexp.MustCompile(`^glass$`)}, Opacity: &zero}}
	}, small)
	c := testclient.Connect(t, h.Socket())
	for range 2 {
		w := c.Window("glass", testclient.Fill(0, 0, 0))
		c.DispatchUntil("a glass window to map", w.Mapped)
		w.Stop()
	}
	settle(t, h)
	before := blurRedraws(h)
	for range 6 {
		h.dispatch("resizeactive 6 0")
		if err := c.Display.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		settle(t, h)
	}
	return blurRedraws(h) - before
}

func TestTiledGlassBlursOverTheSharedBackgroundChain(t *testing.T) {
	t.Parallel()
	if n := tiledGlassRedraws(t, false); n != 0 {
		t.Errorf("resizing two tiled glass windows redrew %d blur chains, want none over an unchanged background", n)
	}
	if n := tiledGlassRedraws(t, true); n == 0 {
		t.Error("with tiled windows blurring live, resizing them redrew no blur chain, so this test could not tell the two apart")
	}
}
