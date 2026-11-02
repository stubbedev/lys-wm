package harness

import (
	"image"
	"regexp"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestAPiledOverviewClosingDoesNotDrawWhatIsCovered(t *testing.T) {
	var now time.Duration
	const screens, most = 5, 500
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = springTable(t)
		cfg.Clock = func() time.Duration { return now }
		cfg.Decor.Rounding = 12
		cfg.Decor.Shadow = compositor.Shadow{Enabled: true, Range: 24, Power: 3, Scale: 1, Offset: image.Pt(0, 8), Active: render.Color{A: 0x60}, Inactive: render.Color{A: 0x60}}
		cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^pile$`), Mode: wm.RuleFloat, Size: image.Pt(1000, 700)}}
	}, benchMode)
	c := testclient.Connect(t, h.Socket())
	for range 30 {
		w := c.Window("pile", testclient.Fill(0x30, 0x60, 0x90))
		c.DispatchUntil("the window to map", w.Mapped)
		w.Stop()
	}
	h.Rendered(0)
	h.dispatch("overview show")
	h.advanceClock(&now, time.Minute)
	h.Rendered(0)
	h.dispatch("overview hide")
	peak, peakDraws := 0, 0
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		for range 30 {
			now += 16 * time.Millisecond
			if err := comp.Frame(o); err != nil {
				return err
			}
			draws, pixels := comp.FrameCost(comp.Outputs()[0])
			if pixels > screens*1920*1080 || draws > most {
				t.Errorf("at %v a close frame draws %d ops over %.1f screens of pixels", now, draws, float64(pixels)/float64(1920*1080))
			}
			peak, peakDraws = max(peak, pixels), max(peakDraws, draws)
		}
		return nil
	})
	t.Logf("the worst close frame: %.1f screens, %d draws", float64(peak)/float64(1920*1080), peakDraws)
}
