package harness

import (
	"errors"
	"fmt"
	"image"
	"regexp"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestATranslucentWindowBlursWhatIsBehindIt(t *testing.T) {
	half := float32(0.5)
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 4, Passes: 2}
		cfg.Rules = []wm.Rule{
			{Title: regexp.MustCompile(`^glass$`), Mode: wm.RuleFloat, Size: image.Pt(60, 30), Opacity: &half},
			{Title: regexp.MustCompile(`^spot$`), Mode: wm.RuleFloat, Size: image.Pt(10, 10)},
		}
	}, small)
	c := testclient.Connect(t, h.Socket())
	bg := c.Window("background", quadrants)
	c.DispatchUntil("the background to map", bg.Mapped)
	spot := c.Window("spot", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("the spot to map", spot.Mapped)
	glass := c.Window("glass", testclient.Fill(0, 0, 0))
	c.DispatchUntil("the glass to map", glass.Mapped)
	settle(t, h)
	if err := errors.Join(spot.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	glassRect := image.Rect(50, 35, 110, 65)
	h.Eventually("the glass re-blurred where the spot below it left", func(b *headless.Backend, _ *compositor.Compositor) bool {
		var damage image.Rectangle
		for _, r := range b.Outputs()[0].LastDamage() {
			damage = damage.Union(r)
		}
		return glassRect.In(damage)
	})
	h.Eventually("red and green mixed under the glass at their border", func(b *headless.Backend, _ *compositor.Compositor) bool {
		px := pixel(snapshot(b.Outputs()[0]), 79, 40)
		return px[0] > 0x10 && px[1] > 0x10 && px[0] < 0x80 && px[1] < 0x80
	})
	h.Eventually("the glass corner far from any border keeps its quadrant's colour", func(b *headless.Backend, _ *compositor.Compositor) bool {
		px := pixel(snapshot(b.Outputs()[0]), 52, 37)
		return px[0] > 0x60 && px[1] < 0x40
	})
}

func TestXRayBlurShowsTheWallpaperNotTheWindowsBelow(t *testing.T) {
	half := float32(0.5)
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 2, Passes: 1, XRay: true}
		cfg.Rules = []wm.Rule{
			{Title: regexp.MustCompile(`^glass$`), Mode: wm.RuleFloat, Size: image.Pt(60, 30), Opacity: &half},
			{Title: regexp.MustCompile(`^spot$`), Mode: wm.RuleFloat, Size: image.Pt(10, 10)},
		}
	}, small)
	shell := testclient.Connect(t, h.Socket())
	wallpaper := shell.Layer(testclient.LayerSpec{
		Layer:  cwlr.ZwlrLayerShellV1LayerBackground,
		Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight,
	}, quadrants)
	shell.DispatchUntil("the wallpaper to map", wallpaper.Mapped)
	wallpaper.Stop()
	c := testclient.Connect(t, h.Socket())
	under := c.Window("under", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("the window below to map", under.Mapped)
	under.Stop()
	spot := c.Window("spot", testclient.Fill(0, 0, 0))
	c.DispatchUntil("the black spot to map", spot.Mapped)
	glass := c.Window("glass", testclient.Fill(0, 0, 0))
	c.DispatchUntil("the glass to map", glass.Mapped)
	blue := func(b *headless.Backend, _ *compositor.Compositor) bool {
		px := pixel(snapshot(b.Outputs()[0]), 55, 60)
		return px[0] < 0x20 && px[2] > 0x40
	}
	h.Eventually("the glass showing the blurred blue wallpaper quadrant, not the white window below", blue)
	var with [3]byte
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		with = pixel(snapshot(b.Outputs()[0]), 80, 50)
		return nil
	})
	if err := errors.Join(spot.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	settle(t, h)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if without := pixel(snapshot(b.Outputs()[0]), 80, 50); without != with {
			return fmt.Errorf("the glass over where the black window was changed from %v to %v when it left, but xray blurs only the background", with, without)
		}
		return nil
	})
}
