package drm

import (
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/render/soft"
)

var (
	interlaced1080  = kdrm.Mode{Clock: 74250, HDisplay: 1920, HTotal: 2200, VDisplay: 1080, VTotal: 1125, Flags: kdrm.ModeFlagInterlace, Type: kdrm.ModeTypePreferred}
	progressive1080 = kdrm.Mode{Clock: 148352, HDisplay: 1920, HTotal: 2200, VDisplay: 1080, VTotal: 1125}
)

func bootModes(t *testing.T, list ...kdrm.Mode) *rig {
	t.Helper()
	return boot(t, func(d *fakeDevice) allocator {
		d.modes = list
		return &fakeAlloc{soft: soft.New()}
	})
}

func TestInterlacedModesAreNeitherOfferedNorChosen(t *testing.T) {
	r := bootModes(t, interlaced1080, progressive1080, modes[1])
	o := r.b.Outputs()[0]
	offered := o.Modes()
	if len(offered) != 2 || offered[0] != modeOf(progressive1080) || offered[1] != modeOf(modes[1]) {
		t.Fatalf("modes %+v, want 1080p59.94 and 720p only", offered)
	}
	if o.mode != progressive1080 || o.State().Mode != modeOf(progressive1080) {
		t.Fatalf("lit in %+v, want the progressive 1080p", o.mode)
	}
	if err := o.Commit(o.State()); err != nil {
		t.Fatalf("selecting the offered mode: %v", err)
	}
}

func TestAConnectorWithOnlyInterlacedModesIsNotLit(t *testing.T) {
	r := bootModes(t, interlaced1080)
	if len(r.b.Outputs()) != 0 || len(r.rec.outputs) != 0 {
		t.Fatalf("outputs %v for a connector with only interlaced modes", r.rec.log)
	}
}
