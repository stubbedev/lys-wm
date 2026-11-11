package drm

import (
	"errors"
	"slices"
	"strings"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"

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

func TestAModesetRefusedAloneIsRetriedWithEveryLitOutputRestated(t *testing.T) {
	r := start(t)
	first := r.flipFirstFrame()
	r.dev.refuseLone = true
	r.dev.connectors = []uint32{20, 21}
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	if len(r.b.Outputs()) != 2 {
		t.Fatalf("outputs %v, reports %v", r.rec.log, r.reports)
	}
	second := r.b.Outputs()[1]
	if !second.restate {
		t.Fatalf("DP-2 lit without restating DP-1; commits %+v", r.dev.commits)
	}
	r.dev.commits = nil
	if err := second.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("DP-2's first frame", second, 0)
	modeset := r.dev.commits[len(r.dev.commits)-1]
	restated, ok := modeset.values[[2]uint32{10, propModeID}]
	if modeset.flags&kdrm.AtomicAllowModeset == 0 || modeset.userData != 11 || !ok || restated == uint64(first.modeBlob) || modeset.values[[2]uint32{20, propConnCRTC}] != 10 || modeset.values[[2]uint32{10, propActive}] != 1 {
		t.Fatalf("DP-2's modeset %+v does not restate DP-1 with a fresh mode blob", modeset)
	}
	if m, err := kdrm.ParseMode(r.dev.blobData[uint32(restated)]); err != nil || m != first.mode || !slices.Contains(r.dev.destroyed, uint32(restated)) {
		t.Fatalf("restated DP-1 in %+v (%v), blob destroyed %v", m, err, r.dev.destroyed)
	}
	r.dev.flip(t, 11, 1)
	r.settle()
	if !second.flipping || len(r.rec.presents) != 1 {
		t.Fatalf("the first of the restated commit's two flip events ended DP-2's flip: flipping %v, presents %+v", second.flipping, r.rec.presents)
	}
	r.dev.flip(t, 11, 2)
	r.until("DP-2's flip", func() bool { return !second.flipping })
	if second.restate || len(r.rec.presents) != 2 || r.rec.presents[1].Seq != 2 {
		t.Fatalf("restate %v, presents %+v", second.restate, r.rec.presents)
	}
	r.dev.commits = nil
	if err := second.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("DP-2's second frame", second, 0)
	if _, again := r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{10, propModeID}]; again {
		t.Fatal("a flip after the restated modeset restated DP-1 again")
	}
}

func TestAModesetThatPassesAloneRestatesNothing(t *testing.T) {
	r := start(t)
	r.flipFirstFrame()
	r.dev.connectors = []uint32{20, 21}
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	second := r.b.Outputs()[1]
	if second.restate {
		t.Fatal("DP-2 restates DP-1 though its modeset passed alone")
	}
	for _, c := range r.dev.commits {
		if _, ok := c.values[[2]uint32{10, propModeID}]; ok {
			t.Fatalf("DP-2's test touched DP-1's CRTC: %+v", c)
		}
	}
}

func TestAnOutputWhosePreferredModeIsRefusedLightsTheNextBest(t *testing.T) {
	fifty1080 := kdrm.Mode{Clock: 148500, HDisplay: 1920, HTotal: 2640, VDisplay: 1080, VTotal: 1125}
	fifty720 := kdrm.Mode{Clock: 74250, HDisplay: 1280, HTotal: 1980, VDisplay: 720, VTotal: 750}
	r := boot(t, func(d *fakeDevice) allocator {
		d.modes = []kdrm.Mode{modes[0], fifty720, fifty1080, modes[1]}
		d.refuseMode = func(m kdrm.Mode) bool { return m.HDisplay == 1920 }
		return &fakeAlloc{soft: soft.New()}
	})
	if len(r.b.Outputs()) != 1 {
		t.Fatalf("outputs %v, reports %v", r.rec.log, r.reports)
	}
	o := r.b.Outputs()[0]
	if o.mode != modes[1] {
		t.Fatalf("lit in %+v, want 1280x720@60", modeOf(o.mode))
	}
	if len(r.reports) != 1 || !strings.Contains(r.reports[0].Error(), "lights in") || strings.Count(r.reports[0].Error(), "1920") != 2 {
		t.Fatalf("reports %v, want the mode taken and both refusals", r.reports)
	}
	r.dev.refuseMode = func(kdrm.Mode) bool { return true }
	r.dev.connectors = []uint32{20, 21}
	if err := r.hotplug(); err != nil {
		t.Fatalf("a connector the device takes in no mode: %v", err)
	}
	if len(r.b.Outputs()) != 1 || !strings.Contains(r.reports[len(r.reports)-1].Error(), "stays dark") {
		t.Fatalf("outputs %v, reports %v", r.rec.log, r.reports)
	}
}

func TestEveryModesetIsTestedBeforeItIsAdopted(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	r.dev.refuseModesets = true
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[1])}); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("an 8-bit modeset the device refuses: %v", err)
	}
	if o.mode != modes[0] {
		t.Fatalf("the refused mode was adopted: %+v", modeOf(o.mode))
	}
}

func TestANonDesktopConnectorIsNotLit(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.connectors = []uint32{20, 21}
		d.nonDesktop = map[uint32]bool{21: true}
		return &fakeAlloc{soft: soft.New()}
	})
	if len(r.b.Outputs()) != 1 || r.b.Outputs()[0].Name() != "DP-1" || !slices.Equal(r.rec.log, []string{"added DP-1"}) {
		t.Fatalf("outputs %v beside a non-desktop DP-2", r.rec.log)
	}
	if len(r.reports) != 1 || !strings.Contains(r.reports[0].Error(), "DP-2 is a non-desktop display") {
		t.Fatalf("reports %v, want DP-2 named once", r.reports)
	}
}

func bootLinks(t *testing.T) *rig {
	t.Helper()
	return boot(t, func(d *fakeDevice) allocator {
		d.links = true
		return &fakeAlloc{soft: soft.New()}
	})
}

func modesetsWithGoodLink(commits []commit) int {
	n := 0
	for _, c := range commits {
		if v, ok := c.values[[2]uint32{20, propLinkStatusID}]; ok && v == linkStatusGood && c.flags&kdrm.AtomicAllowModeset != 0 && c.flags&kdrm.AtomicTestOnly == 0 {
			n++
		}
	}
	return n
}

func TestALinkThatGoesBadIsModesetAgainWithAGoodLink(t *testing.T) {
	r := bootLinks(t)
	o := r.flipFirstFrame()
	r.dev.linkBad = true
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the modeset after the bad link", o, 0)
	if n := modesetsWithGoodLink(r.dev.commits); n != 1 || o.mode != modes[0] || r.dev.linkBad {
		t.Fatalf("%d modesets with a good link in %+v, link still bad %v", n, modeOf(o.mode), r.dev.linkBad)
	}
	r.dev.flip(t, 10, 2)
	r.until("the flip", func() bool { return !o.flipping })
	r.dev.commits = nil
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != 0 {
		t.Fatalf("a probe with a good link committed %+v", r.dev.commits)
	}
}

func TestALinkThatGoesBadTakesAModeTheSinkStillOffers(t *testing.T) {
	r := bootLinks(t)
	o := r.flipFirstFrame()
	r.dev.linkBad = true
	r.dev.modes = []kdrm.Mode{modes[1]}
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the modeset after the bad link", o, 0)
	if n := modesetsWithGoodLink(r.dev.commits); n != 1 || o.mode != modes[1] || len(o.Modes()) != 1 {
		t.Fatalf("%d modesets with a good link in %+v, modes %+v", n, modeOf(o.mode), o.Modes())
	}
}

func riggedOnAFileWithoutMaster(t *testing.T, master Master) *rig {
	t.Helper()
	return rigged(t, func(d *fakeDevice) allocator {
		d.noMaster = true
		return &fakeAlloc{soft: soft.New()}
	}, master)
}

func TestABackendThatIsNotMasterLightsOutputsWithoutTestingTheirModes(t *testing.T) {
	r := riggedOnAFileWithoutMaster(t, WithoutMaster)
	if err := r.b.Start(r.rec); err != nil {
		t.Fatalf("starting without master: %v", err)
	}
	if len(r.b.Outputs()) != 1 || len(r.reports) != 0 {
		t.Fatalf("outputs %v, reports %v", r.rec.log, r.reports)
	}
	o := r.b.Outputs()[0]
	if !o.State().Enabled() || len(o.buffers) != buffersPerOut || o.mode != modes[0] {
		t.Fatalf("%s: state %+v, %d buffers, mode %+v", o.Name(), o.State(), len(o.buffers), o.mode)
	}
	if !r.b.Paused() || len(r.dev.commits) != 0 {
		t.Fatalf("paused %v, a file without master committed %+v", r.b.Paused(), r.dev.commits)
	}
}

func TestABackendHoldingMasterTestsTheModeOfEveryOutputItAdds(t *testing.T) {
	r := rigged(t, func(*fakeDevice) allocator { return &fakeAlloc{soft: soft.New()} }, HoldsMaster)
	if err := r.b.Start(r.rec); err != nil {
		t.Fatal(err)
	}
	if r.b.Paused() || !slices.ContainsFunc(r.dev.commits, func(c commit) bool {
		return c.flags&kdrm.AtomicTestOnly != 0 && c.flags&kdrm.AtomicAllowModeset != 0
	}) {
		t.Fatalf("paused %v, no modeset tested at add: %+v", r.b.Paused(), r.dev.commits)
	}
}

func TestABackendClaimingMasterItLacksFailsToStart(t *testing.T) {
	r := riggedOnAFileWithoutMaster(t, HoldsMaster)
	if err := r.b.Start(r.rec); !errors.Is(err, unix.EACCES) {
		t.Fatalf("starting as master on a file without it: %v, want EACCES", err)
	}
}

func TestAnOutputLitInItsModeTestsNoModeset(t *testing.T) {
	lit := modes[0]
	lit.Type, lit.Name = 0, [32]byte{'s', 'p', 'l', 'a', 's', 'h'}
	r := bootLit(t, &splash{crtc: 10, plane: 40, mode: lit}, false)
	if n := tests(r.dev.commits, kdrm.AtomicAllowModeset); n != 0 {
		t.Fatalf("%d modeset tests adding an output its CRTC already shows: %+v", n, r.dev.commits)
	}
	if !r.b.Outputs()[0].Inherited() {
		t.Fatal("the output does not report the frame it inherited")
	}
}
