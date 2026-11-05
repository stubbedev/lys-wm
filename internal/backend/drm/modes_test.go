package drm

import (
	"errors"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/backend"
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

func TestADisabledOutputReleasesItsCRTCForAnother(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.connectors = []uint32{20, 21}
		return &fakeAlloc{soft: soft.New()}
	})
	r.dev.connectors = []uint32{20, 21, 22}
	if err := r.hotplug(); err != nil {
		t.Fatalf("a third connector on a device with two CRTCs: %v", err)
	}
	if len(r.b.Outputs()) != 2 || len(r.reports) != 1 || !errors.Is(r.reports[0], errNoCRTC) {
		t.Fatalf("outputs %v, reports %v, want DP-3 reported waiting for a CRTC", r.rec.log, r.reports)
	}
	first := r.b.Outputs()[0]
	if err := first.Commit(backend.OutputState{}); err != nil {
		t.Fatal(err)
	}
	r.until("DP-3 lighting on the freed CRTC", func() bool { return len(r.b.Outputs()) == 3 })
	third := r.b.Outputs()[2]
	if third.Name() != "DP-3" || third.crtc.id != 10 || third.primary.id != 40 || third.cursor == nil || third.cursor.plane.id != 43 || !third.State().Enabled() {
		t.Fatalf("%s on CRTC %d plane %d, cursor %v, state %+v", third.Name(), third.crtc.id, third.primary.id, third.cursor, third.State())
	}
	if err := first.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[0])}); !errors.Is(err, errNoCRTC) {
		t.Fatalf("enabling DP-1 with every CRTC taken: %v", err)
	}
	if err := third.Commit(backend.OutputState{}); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[0])}); err != nil {
		t.Fatalf("enabling DP-1 once DP-3 freed its CRTC: %v", err)
	}
	if !first.bound() || first.crtc.id != 10 || third.bound() || first.cursor == nil || first.cursor.plane.id != 43 || !first.cursor.plane.taken {
		t.Fatalf("DP-1 bound %v on CRTC %d, DP-3 bound %v", first.bound(), first.crtc.id, third.bound())
	}
	r.committedSince("DP-1's modeset on the CRTC it took back", first, 0)
}
