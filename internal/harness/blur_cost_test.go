package harness

import (
	"errors"
	"image"
	"regexp"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var glassRect = image.Rect(50, 35, 110, 65)

func clearWindowRule(title string) wm.Rule {
	zero := float32(0)
	return wm.Rule{Title: regexp.MustCompile("^" + title + "$"), Mode: wm.RuleFloat, Size: glassRect.Size(), Opacity: &zero}
}

func TestAHiddenBlurCostsNothing(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 2, Passes: 1}
			cfg.Rules = []wm.Rule{
				clearWindowRule("glass"),
				{Title: regexp.MustCompile(`^cover$`), Mode: wm.RuleFloat, Size: image.Pt(80, 50)},
				{Title: regexp.MustCompile(`^behind$`), Mode: wm.RuleFloat, Size: image.Pt(80, 50)},
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
		cover.Stop()
		h.Eventually("the cover shown over the glass", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return pixel(snapshot(b.Outputs()[0]), 100, 60) == [3]byte{0, 0xff, 0}
		})
		f0, r0 := frames(h), blurRedraws(h)
		pump(c, 200*time.Millisecond)
		f1, r1 := frames(h), blurRedraws(h)
		if f1 == f0 {
			t.Fatal("the animated window behind presented no frame")
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
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 2, Passes: 1}
			cfg.Rules = []wm.Rule{
				clearWindowRule("glass"),
				{Title: regexp.MustCompile(`^behind$`), Mode: wm.RuleFloat, Size: image.Pt(80, 50)},
				{Title: regexp.MustCompile(`^wall$`), Mode: wm.RuleFloat, Size: image.Pt(120, 80)},
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
		glass.Stop()
		h.Eventually("the glass over the wall", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return near(pixel(snapshot(b.Outputs()[0]), 100, 60), [3]byte{0xff, 0, 0})
		})
		pump(c, 100*time.Millisecond)
		r0 := blurRedraws(h)
		f0 := frames(h)
		pump(c, 100*time.Millisecond)
		if frames(h) == f0 {
			t.Fatal("the window behind presented no frame")
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

func pump(c *testclient.Client, d time.Duration) {
	until := time.Now().Add(d)
	c.DispatchUntil("the time to pass", func() bool { return time.Now().After(until) })
}
