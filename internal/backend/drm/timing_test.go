package drm

import (
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
)

func TestACursorOnlyCommitLeadsTheVblankByThePlaneLead(t *testing.T) {
	r := start(t)
	r.pinned = backend.Now()
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	for range 32 {
		o.budget.Sample(interval)
	}
	if o.budget.Budget(interval) != interval {
		t.Fatalf("the budget is %v, want a whole refresh", o.budget.Budget(interval))
	}
	r.pinned = o.lastFlip + interval - backend.PlaneLead
	r.cursorCommit("the cursor's first commit", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	vblank := o.lastFlip + interval
	r.pinned = vblank - interval/2
	r.dev.commits = nil
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(20, 30)); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 0 || o.planesWait() != interval/2-backend.PlaneLead {
		t.Fatalf("half a refresh before the vblank a cursor move committed %+v and waits %v, want it held until the plane lead", r.dev.commits, o.planesWait())
	}
	r.pinned = vblank - backend.PlaneLead
	r.until("the cursor move at the plane lead", func() bool { return o.flipping })
	if _, at, _ := cursorAt(r.dev.commits[len(r.dev.commits)-1]); at != image.Pt(16, 24) {
		t.Fatalf("the move committed the cursor at %v", at)
	}
	r.dev.flip(t, 10, 9)
	r.until("the move's flip", func() bool { return !o.flipping })
	r.dev.commits = nil
	if err := cp.MoveCursor(image.Pt(40, 50)); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 0 || o.planesWait() < interval-backend.PlaneLead-time.Millisecond {
		t.Fatalf("a move right after the flip committed %+v and waits %v, want it to make the next vblank at its plane lead", r.dev.commits, o.planesWait())
	}
}

func TestAFrameCommitCarriesTheCursorAsOfThePlaneLead(t *testing.T) {
	r := start(t)
	r.pinned = backend.Now()
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	r.pinned = o.lastFlip + interval - backend.PlaneLead
	r.cursorCommit("the cursor's first commit", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	vblank := o.lastFlip + interval
	r.pinned = vblank - interval/2
	r.dev.commits = nil
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("the frame to be recorded", func() bool { return o.late != nil })
	if len(r.dev.commits) != 0 {
		t.Fatalf("a frame recorded half a refresh before the vblank committed at once: %+v", r.dev.commits)
	}
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(20, 30)); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 0 {
		t.Fatalf("a cursor move during a recorded frame committed alone: %+v", r.dev.commits)
	}
	r.pinned = vblank - backend.PlaneLead
	r.until("the frame's commit at the plane lead", func() bool { return o.flipping && o.late == nil })
	frame := r.dev.commits[len(r.dev.commits)-1]
	if _, at, _ := cursorAt(frame); frame.values[[2]uint32{40, propFB}] == 0 || frame.values[[2]uint32{40, propInFence}] != uint64(r.alloc.fences[len(r.alloc.fences)-1]) || at != image.Pt(16, 24) {
		t.Fatalf("the frame committed %+v, want its buffer, its fence and the cursor at (16,24)", frame)
	}
}

func TestAFrameWithoutACursorCommitsAtOnce(t *testing.T) {
	r := start(t)
	r.pinned = backend.Now()
	o := r.flipFirstFrame()
	r.pinned = o.lastFlip + modeOf(o.mode).Interval()/2
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("the frame", func() bool { return o.flipping })
	if o.late != nil || len(r.dev.commits) == 0 {
		t.Fatalf("a frame with no cursor on screen waited for the plane lead: %d commits", len(r.dev.commits))
	}
}
