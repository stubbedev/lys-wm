package harness

import (
	"errors"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestAClosingWindowKeepsItsFrost(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		now := time.Hour
		d := time.Second
		table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Shape: anim.Linear, Duration: &d}})
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		h := start(t, func(cfg *compositor.Config) {
			cfg.Animations = table
			cfg.Clock = func() time.Duration { return now }
			cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 4, Passes: 2, Look: wm.Look{Tint: glassTint}}
			cfg.Rules = []wm.Rule{clearWindowRule("glass")}
		}, small)
		at := func(off time.Duration) {
			h.Do(func(*headless.Backend, *compositor.Compositor) error {
				now = time.Hour + off
				return nil
			})
		}
		c := testclient.Connect(t, h.Socket())
		bg := c.Window("background", fineChecker)
		c.DispatchUntil("the background to map", bg.Mapped)
		bg.Stop()
		at(5 * time.Second)
		glass := c.Window("glass", testclient.Fill(0, 0, 0))
		c.DispatchUntil("the glass to map", glass.Mapped)
		glass.Stop()
		at(10 * time.Second)
		h.Eventually("the glass frosted", frostedAt(100, 60))
		settle(t, h)
		open := h.Rendered(0)
		if err := errors.Join(glass.Destroy(), c.Display.Roundtrip()); err != nil {
			t.Fatal(err)
		}
		closed := h.Rendered(0)
		if d := maxDifference(open, closed, glassRect.Inset(1)); d > 3 {
			t.Errorf("the closing window's first frame differs by %d from the window it replaced", d)
		}
		at(10*time.Second + 500*time.Millisecond)
		half := h.Rendered(0)
		v := pixel(half, 100, 60)[0]
		if v < 0x20 || v > 0xe0 {
			t.Errorf("half way through closing the middle is %#x: the frost is gone", v)
		}
	})
}
