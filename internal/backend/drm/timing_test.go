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
