package drm

import (
	"slices"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/backend"
)

func (r *rig) twoOutputsOneFlipping() (flipping, other *Output) {
	r.t.Helper()
	flipping = r.flipFirstFrame()
	r.dev.connectors = []uint32{20, 21}
	if err := r.hotplug(); err != nil {
		r.t.Fatal(err)
	}
	if len(r.b.Outputs()) != 2 {
		r.t.Fatalf("outputs %v, reports %v", r.rec.log, r.reports)
	}
	other = r.b.Outputs()[1]
	if err := flipping.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.committedSince("DP-1's frame in flight", flipping, 0)
	r.dev.commits, r.rec.log = nil, nil
	return flipping, other
}

func TestAModesetRefusedBusyWaitsForTheDevicesFlips(t *testing.T) {
	r := start(t)
	first, second := r.twoOutputsOneFlipping()
	r.dev.busyUntilFlip = true
	if err := second.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("DP-2's modeset refused busy", func() bool { return second.busy != nil })
	r.settle()
	if second.flipping || len(r.dev.commits) != 2 || slices.Contains(r.rec.log, "damaged DP-2") {
		t.Fatalf("DP-2 flipping %v after %d commits, log %v", second.flipping, len(r.dev.commits), r.rec.log)
	}
	r.dev.flip(t, 10, 2)
	r.until("DP-2's modeset after DP-1's flip", func() bool { return second.flipping })
	modeset := r.dev.commits[len(r.dev.commits)-1]
	if modeset.userData != 11 || modeset.flags != planeFlags|kdrm.AtomicAllowModeset || modeset.values[[2]uint32{11, propActive}] != 1 {
		t.Fatalf("DP-2's modeset %+v", modeset)
	}
	frames := 0
	for _, l := range r.rec.log {
		switch l {
		case "frame":
			frames++
		case "damaged DP-2":
			t.Fatalf("DP-2 dropped a frame: log %v", r.rec.log)
		}
	}
	if frames != 1 || len(r.reports) != 0 || first.flipping {
		t.Fatalf("%d renders, reports %v, log %v", frames, r.reports, r.rec.log)
	}
}

func TestAModesetRefusedBusyWithNoFlipPendingCommitsBlocking(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	r.dev.busyNonblock = true
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the blocking modeset", o, 0)
	got := r.dev.commits[len(r.dev.commits)-1]
	if got.flags != kdrm.AtomicPageFlipEvent|kdrm.AtomicAllowModeset || o.busy != nil {
		t.Fatalf("the modeset after EBUSY committed %+v", got)
	}
}

func TestAPowerOffRefusedBusyWaitsForTheDevicesFlips(t *testing.T) {
	r := start(t)
	_, second := r.twoOutputsOneFlipping()
	if err := second.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("DP-2's first frame", second, 0)
	r.dev.flip(t, 11, 1)
	r.until("DP-2's flip", func() bool { return !second.flipping })
	r.dev.busyUntilFlip = true
	n := len(r.dev.commits)
	state := second.State()
	state.Power = backend.PoweredOff
	if err := second.Commit(state); err != nil {
		t.Fatal(err)
	}
	if !second.offBusy || second.State().Power != backend.PoweredOff || len(r.dev.commits) != n+1 {
		t.Fatalf("a busy power off: held %v, state %+v, commits %+v", second.offBusy, second.State(), r.dev.commits[n:])
	}
	r.dev.flip(t, 10, 2)
	r.until("DP-2's power off after DP-1's flip", func() bool { return !second.offBusy })
	off := r.dev.commits[len(r.dev.commits)-1]
	if off.flags != kdrm.AtomicAllowModeset|kdrm.AtomicNonblock || off.values[[2]uint32{11, propActive}] != 0 || len(r.reports) != 0 {
		t.Fatalf("power off commit %+v, reports %v", off, r.reports)
	}
}

func TestAPowerOffRefusedBusyWithNoFlipPendingCommitsBlocking(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.dev.busyNonblock = true
	n := len(r.dev.commits)
	state := o.State()
	state.Power = backend.PoweredOff
	if err := o.Commit(state); err != nil {
		t.Fatal(err)
	}
	if got := r.dev.commits[n:]; len(got) != 2 || got[1].flags != kdrm.AtomicAllowModeset || o.offBusy {
		t.Fatalf("a busy power off with nothing flipping committed %+v", got)
	}
}
