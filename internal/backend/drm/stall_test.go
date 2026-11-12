package drm

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
)

func (r *rig) framesFailing(err error) *Output {
	r.t.Helper()
	o := r.lit()
	r.rec.retry = true
	r.dev.failCommits = err
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.until("the first refused frame", func() bool { return r.rec.dropped > 0 })
	return o
}

func TestAnOutputWhoseCommitsKeepFailingStopsCountingAsDisplayingUntilTheyTakeForTheBound(t *testing.T) {
	r := start(t)
	o := r.framesFailing(unix.EBUSY)
	failing := r.now()
	early := false
	r.until("the output to stall", func() bool {
		early = early || o.Stalled() && r.now()-failing < backend.SustainedFor
		return o.Stalled()
	})
	r.until("the stall to reach the handler", func() bool { return r.rec.changed > 0 })
	r.settle()
	switch {
	case early:
		t.Fatalf("%s stalled before its commits had failed for %v", o.Name(), backend.SustainedFor)
	case r.rec.dropped < 2:
		t.Fatalf("%s stalled after %d refused frames", o.Name(), r.rec.dropped)
	case backend.Displays(o) || r.rec.changed != 1:
		t.Fatalf("a stalled output displays %v, the handler heard %d changes, want one", backend.Displays(o), r.rec.changed)
	case len(r.reports) != 1 || !errors.Is(r.reports[0], unix.EBUSY) || !strings.Contains(r.reports[0].Error(), "DP-1 has taken no commit") || !strings.Contains(r.reports[0].Error(), "re-reading its connector"):
		t.Fatalf("reported %v, want the stall once with its cause", r.reports)
	case slices.Contains(r.rec.log, "removed DP-1"):
		t.Fatalf("a connector the re-probe found connected was removed: %v", r.rec.log)
	}
	r.dev.failCommits = nil
	taking := r.now()
	for seq := uint32(2); o.Stalled(); seq++ {
		if r.now()-taking > 2*backend.SustainedFor {
			t.Fatalf("%s still stalled after taking commits for %v", o.Name(), r.now()-taking)
		}
		r.tookFrame(o, seq)
	}
	if took := r.now() - taking; took < backend.SustainedFor {
		t.Fatalf("%s recovered after taking commits for %v, want %v", o.Name(), took, backend.SustainedFor)
	}
	r.until("the recovery to reach the handler", func() bool { return r.rec.changed > 1 })
	if !backend.Displays(o) || r.rec.changed != 2 || len(r.reports) != 2 || !strings.Contains(r.reports[1].Error(), "DP-1 has taken every commit for") {
		t.Fatalf("after commits took for %v: displays %v, %d changes, reports %v", backend.SustainedFor, backend.Displays(o), r.rec.changed, r.reports)
	}
}

func (r *rig) tookFrame(o *Output, seq uint32) {
	r.t.Helper()
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.until("the frame to commit", func() bool { return o.flipping })
	r.elapse(o.paceInterval())
	r.dev.flip(r.t, 10, seq)
	r.until("its flip", func() bool { return !o.flipping })
}

func TestATransientRefusedCommitDoesNotStallTheOutput(t *testing.T) {
	r := start(t)
	o := r.framesFailing(unix.EBUSY)
	took := func(seq uint32) {
		t.Helper()
		r.dev.failCommits = nil
		r.until("the retried frame", func() bool { return o.flipping })
		r.dev.flip(t, 10, seq)
		r.until("its flip", func() bool { return !o.flipping })
	}
	took(2)
	r.elapse(2 * backend.SustainedFor)
	r.dev.failCommits = unix.EBUSY
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("the second refused frame", func() bool { return r.rec.dropped > 1 })
	took(3)
	r.settle()
	if o.Stalled() || r.rec.changed != 0 || len(r.reports) != 0 {
		t.Fatalf("two refused commits %v apart, each followed by one that took: stalled %v, %d changes, reports %v", 2*backend.SustainedFor, o.Stalled(), r.rec.changed, r.reports)
	}
}

func TestAConnectorGoneWithoutAHotplugIsFoundByTheStallsReprobe(t *testing.T) {
	r := start(t)
	r.dev.connected = false
	r.framesFailing(unix.EBUSY)
	r.until("the stalled output's re-probe to remove it", func() bool { return slices.Contains(r.rec.log, "removed DP-1") })
	gone := slices.ContainsFunc(r.reports, func(err error) bool {
		return errors.Is(err, unix.EBUSY) && strings.Contains(err.Error(), "disable DP-1") && strings.Contains(err.Error(), "it is gone")
	})
	if len(r.b.Outputs()) != 0 || !gone {
		t.Fatalf("outputs left after the re-probe: %d, reports %v, want the refused disable of the gone output", len(r.b.Outputs()), r.reports)
	}
}
