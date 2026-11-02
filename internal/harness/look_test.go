package harness

import (
	"errors"
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cbe "github.com/stubbedev/lys-wm/internal/protocol/client/backgroundeffect"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func defaultLook(cfg *compositor.Config) {
	report, clock, rules := cfg.Report, cfg.Clock, cfg.Rules
	*cfg = compositor.DefaultConfig(cfg.Seat.Keymap, cfg.Font)
	cfg.Report, cfg.Clock, cfg.Rules = report, clock, rules
	cfg.Decor.Blur.Size, cfg.Decor.Blur.Passes = 3, 2
	for speed, e := range cfg.Slower {
		e.Decor.Blur.Size, e.Decor.Blur.Passes = 3, 2
		cfg.Slower[speed] = e
	}
}

func speedOf(h *Harness) render.Speed {
	var s render.Speed
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		s = b.Renderer().Speed()
		return nil
	})
	return s
}

func TestTheDefaultLookFitsTheRenderer(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, defaultLook, tall)
		c := testclient.Connect(t, h.Socket())
		white := [3]byte{0xff, 0xff, 0xff}
		w := c.Window("look", testclient.Fill(0xff, 0xff, 0xff))
		c.DispatchUntil("the window to map", w.Mapped)
		size := tall.Size
		gap, border := compositor.DefaultSettings().Gaps.Inner, compositor.DefaultDecor().BorderWidth
		inset := 2 * (gap + border)
		c.DispatchUntil("the window to fill its tile", func() bool { return w.Logical == image.Pt(size.X-inset, size.Y-inset) })
		pump(c, 2*time.Second)
		w.Stop()
		h.Eventually("the window settled in the middle of its tile", showing(size.X/4, size.Y/4, white))
		h.Eventually("the background in the outer gap", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return pixel(snapshot(b.Outputs()[0]), gap/2, size.Y/4) != white
		})
		corner := gap + border + 1
		h.Eventually("the rounded corner of the window", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return pixel(snapshot(b.Outputs()[0]), corner, corner) != white
		})
		h.Eventually("the edge of the window", showing(corner, size.Y/4, white))
	})
}

func TestBlurIsOfferedOnlyWhereItIsCheapUnlessAsked(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		var asked compositor.Config
		h, c, glass := startGlass(t, start, func(cfg *compositor.Config) {
			defaultLook(cfg)
			asked = *cfg
			asked.Slower = nil
			asked.Decor.Rounding, asked.Decor.BorderWidth = 0, 0
			asked.Decor.Shadow.Enabled = false
			asked.Animations = cfg.Slower[render.Software].Animations
			asked.Decor.Blur.Withhold = false
		})
		var flags cbe.ExtBackgroundEffectManagerV1Capability
		m, err := cbe.NewExtBackgroundEffectManagerV1(c.Display, 1, capabilities{&flags})
		if err != nil {
			t.Fatal(err)
		}
		c.Bind(m)
		if err := c.Display.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		cheap := speedOf(h) == render.GPU
		if flags.Has(cbe.ExtBackgroundEffectManagerV1CapabilityBlur) != cheap {
			t.Errorf("capabilities %v, a GPU %v", flags, cheap)
		}
		fx := effects{c, m}
		obj := fx.of(t, glass.Surface)
		fx.set(t, obj, image.Rect(0, 0, 60, 30))
		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		settle(t, h)
		if !cheap {
			h.Eventually("no blur from a region the renderer does not offer", sharpAt(75, 45))
		}

		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(asked) })
		if err := c.Display.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if !flags.Has(cbe.ExtBackgroundEffectManagerV1CapabilityBlur) {
			t.Errorf("capabilities %v once the config asks for blur", flags)
		}
		h.Eventually("the region blurs once the config asks", frostedAt(75, 45))
	})
}
