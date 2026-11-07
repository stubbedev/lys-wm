package harness

import (
	"math"
	"os"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func displayP3(t *testing.T) *colorspace.Description {
	t.Helper()
	b, err := os.ReadFile("testdata/display-p3.icc")
	if err != nil {
		t.Fatal(err)
	}
	d, err := colorspace.ParseICC(b)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func profiled(d *colorspace.Description) func(*compositor.Config) {
	return func(cfg *compositor.Config) {
		cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{ICC: d, ICCPath: "display-p3.icc"}}
		cfg.WM.Gaps = wm.Gaps{}
	}
}

func TestAProfileOnAnOutputWithoutCRTCLUTsCorrectsEveryDraw(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		p3 := displayP3(t)
		h := start(t, profiled(p3), small)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("orange", testclient.Fill(0x20, 0x80, 0xe0))
		c.DispatchUntil("the window to map", w.Mapped)
		px := colorspace.Plan(colorspace.SDR, colorspace.ProfileTarget(p3)).Apply([4]float32{0xe0 / 255.0, 0x80 / 255.0, 0x20 / 255.0, 1})
		var want [3]byte
		for i := range want {
			want[i] = byte(math.Round(float64(px[i]) * 255))
		}
		h.Eventually("the window drawn through the profile", showingAbout(10, 10, want))
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			if got := comp.OutputDescription(comp.Outputs()[0]); got != p3 {
				t.Errorf("the output describes itself as %+v, want the profile", got)
			}
			return nil
		})
	})
}

func TestAProfileOnAnOutputWithCRTCLUTsSetsThemOnceAndLeavesTheFramesAlone(t *testing.T) {
	p3 := displayP3(t)
	var cfg compositor.Config
	h := StartWith(t, func(c *compositor.Config) {
		c.WM.Gaps = wm.Gaps{}
		cfg = *c
	}, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		o.SetGammaSize(256)
		o.SetDegammaSize(256)
		o.EnableColorTransform()
		return nil
	})
	c := testclient.Connect(t, h.Socket())
	w := c.Window("orange", testclient.Fill(0x20, 0x80, 0xe0))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window drawn", showingAbout(10, 10, [3]byte{0xe0, 0x80, 0x20}))
	var before headless.ColorSets
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		before = b.Outputs()[0].ColorSets()
		return nil
	})
	profiled(p3)(&cfg)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
	settle(t, h)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		sets := o.ColorSets()
		if d := (headless.ColorSets{Gamma: sets.Gamma - before.Gamma, Degamma: sets.Degamma - before.Degamma, CTM: sets.CTM - before.CTM}); d != (headless.ColorSets{Gamma: 1, Degamma: 1, CTM: 1}) {
			t.Errorf("the profile set the CRTC's LUTs and matrix %+v times, want each once", d)
		}
		if o.ColorTransform() == backend.Identity || len(o.Degamma()) != 3*256 || len(o.Gamma()) != 3*256 {
			t.Errorf("the profile left the CTM %v, a %d-entry degamma and a %d-entry gamma", o.ColorTransform(), len(o.Degamma()), len(o.Gamma()))
		}
		if got := pixel(snapshot(o), 10, 10); got != [3]byte{0xe0, 0x80, 0x20} {
			t.Errorf("with the profile in the CRTC the frame still corrected the window to %v", got)
		}
		return nil
	})
}
