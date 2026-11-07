package harness

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	ccm "github.com/stubbedev/lys-wm/internal/protocol/client/colormanagement"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
)

type outputLog struct{ events []string }

func (l *outputLog) Geometry(*cw.WlOutput, int32, int32, int32, int32, cw.WlOutputSubpixel, string, string, cw.WlOutputTransform) error {
	l.events = append(l.events, "geometry")
	return nil
}
func (*outputLog) Mode(*cw.WlOutput, cw.WlOutputMode, int32, int32, int32) error { return nil }
func (l *outputLog) Done(*cw.WlOutput) error {
	l.events = append(l.events, "done")
	return nil
}
func (*outputLog) Scale(*cw.WlOutput, int32) error        { return nil }
func (*outputLog) Name(*cw.WlOutput, string) error        { return nil }
func (*outputLog) Description(*cw.WlOutput, string) error { return nil }
func (l *outputLog) ImageDescriptionChanged(*ccm.WpColorManagementOutputV1) error {
	l.events = append(l.events, "image_description_changed")
	return nil
}

func TestAnOutputsImageDescriptionChangeIsFollowedByOneDone(t *testing.T) {
	t.Parallel()
	var cfg compositor.Config
	h := StartWith(t, func(c *compositor.Config) {
		c.ColorManagement = true
		c.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: true, SDRBrightness: 203}}
		cfg = *c
	}, small)
	c, m, _ := colorManager(t, h)
	log := &outputLog{}
	wl, err := cw.NewWlOutput(c.Display, 4, log)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(wl)
	if _, err := m.GetOutput(log, wl); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	change := func(what string, rule compositor.OutputRule) []string {
		t.Helper()
		log.events = nil
		cfg.Outputs = compositor.OutputRules{Fallback: rule}
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
		c.DispatchUntil(what, func() bool { return slices.Contains(log.events, "image_description_changed") })
		c.Sync()
		return log.events
	}
	if got := change("a brighter SDR white", compositor.OutputRule{HDR: true, SDRBrightness: 300}); !slices.Equal(got, []string{"image_description_changed", "done"}) {
		t.Fatalf("a colour-only change sent %v, want image_description_changed and one done", got)
	}
	got := change("the output leaving HDR", compositor.OutputRule{})
	idc := slices.Index(got, "image_description_changed")
	geometries, dones := 0, 0
	for _, e := range got {
		switch e {
		case "geometry":
			geometries++
		case "done":
			dones++
		}
	}
	if slices.Index(got[idc:], "done") < 0 || dones != geometries || got[len(got)-1] != "done" {
		t.Fatalf("a mode change sent %v, want a done after image_description_changed and none beyond the output's own", got)
	}
}
