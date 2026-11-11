package drm

import (
	"image"
	"testing"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
)

func modesets(commits []commit) int {
	n := 0
	for _, c := range commits {
		if c.flags&kdrm.AtomicAllowModeset != 0 {
			n++
		}
	}
	return n
}

func (r *rig) pacedFrame(o *Output) (aimed time.Duration) {
	r.t.Helper()
	n := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.committedSince("a frame", o, n)
	aimed = o.aimedAt
	r.at(aimed)
	r.dev.flip(r.t, 10, uint32(len(r.dev.commits)))
	r.until("the flip", func() bool { return !o.flipping })
	return aimed
}

func TestACappedOutputAimsEachFrameAWholeNumberOfRefreshesOn(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	o.CapFrameRate(30)
	for range 4 {
		flip := o.lastFlip
		r.at(flip + time.Millisecond)
		if wait := o.frameWait(); wait <= interval/2 {
			t.Fatalf("a 60 Hz output capped at 30 Hz waits %v after a flip, want longer than an uncapped frame's %v", wait, interval/2-time.Millisecond)
		}
		r.at(flip + interval)
		if aimed := r.pacedFrame(o); aimed != flip+2*interval {
			t.Fatalf("a capped frame aimed at %v after the last flip, want two refreshes, %v", aimed-flip, 2*interval)
		}
		if p := r.rec.presents[len(r.rec.presents)-1]; p.Refresh != 2*interval {
			t.Fatalf("a capped flip presented a refresh of %v, want %v", p.Refresh, 2*interval)
		}
	}
	o.CapFrameRate(0)
	flip := o.lastFlip
	r.at(flip + interval/2)
	if aimed := r.pacedFrame(o); aimed != flip+interval {
		t.Fatalf("uncapped, a frame aimed at %v after the last flip, want the next refresh, %v", aimed-flip, interval)
	}
	if p := r.rec.presents[len(r.rec.presents)-1]; p.Refresh != interval {
		t.Fatalf("uncapped, a flip presented a refresh of %v, want %v", p.Refresh, interval)
	}
	if n := modesets(r.dev.commits); n != 0 {
		t.Fatalf("capping and uncapping the frame rate made %d modesets, want 0", n)
	}
}

func TestACappedOutputStillMovesTheCursorEveryRefresh(t *testing.T) {
	r := start(t)
	o := r.lit()
	c := r.shownCursor(o, image.Pt(4, 6))
	o.CapFrameRate(30)
	r.dev.commits = nil
	const moves = 6
	for i := range moves {
		r.elapse(o.planesWait())
		r.cursorCommit("the move", o, func() error { return c.MoveCursor(image.Pt(100+i, 50)) })
	}
	if len(r.dev.commits) != moves || modesets(r.dev.commits) != 0 {
		t.Fatalf("%d cursor moves on a capped output made %d commits, %d of them modesets; want one plane commit each", moves, len(r.dev.commits), modesets(r.dev.commits))
	}
}

func TestUnderVRRACapIsAMinimumFrameInterval(t *testing.T) {
	r := bootVRR(t, true)
	o := r.b.Outputs()[0]
	if err := o.SetAdaptiveSync(true); err != nil {
		t.Fatal(err)
	}
	r.nextFrame(o)
	r.nextFrame(o)
	o.CapFrameRate(30)
	r.at(o.lastFlip + time.Millisecond)
	if wait, want := o.frameWait(), time.Second/30-time.Millisecond; wait != want {
		t.Fatalf("under VRR a 30 Hz cap waits %v a millisecond after a flip, want %v", wait, want)
	}
	o.CapFrameRate(0)
	if wait := o.frameWait(); wait != 0 {
		t.Fatalf("under VRR uncapped a frame waits %v, want none", wait)
	}
}
