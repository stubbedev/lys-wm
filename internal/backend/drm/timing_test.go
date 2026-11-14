package drm

import (
	"image"
	"testing"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
)

func TestACursorOnlyCommitLeadsTheVblankByThePlaneLead(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	for range 32 {
		o.budget.Sample(interval)
	}
	if o.budget.Budget(interval) != interval {
		t.Fatalf("the budget is %v, want a whole refresh", o.budget.Budget(interval))
	}
	r.at(o.lastFlip + interval - backend.PlaneLead)
	r.cursorCommit("the cursor's first commit", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	vblank := o.lastFlip + interval
	r.at(vblank - interval/2)
	r.dev.commits = nil
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(20, 30)); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 0 || o.planesWait() != interval/2-backend.PlaneLead {
		t.Fatalf("half a refresh before the vblank a cursor move committed %+v and waits %v, want it held until the plane lead", r.dev.commits, o.planesWait())
	}
	r.at(vblank - backend.PlaneLead)
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

func TestAPlaneLatchFiringPastItsVblankCommitsInThatRefresh(t *testing.T) {
	r := start(t)
	o := r.lit()
	c := r.shownCursor(o, image.Pt(4, 6))
	interval := modeOf(o.mode).Interval()
	r.dev.commits = nil
	const refreshes = 6
	slipped := 0
	for i := range refreshes {
		vblank := r.refreshStart(o)
		r.at(vblank - interval/2)
		at := image.Pt(100+3*i, 50)
		if err := c.MoveCursor(at); err != nil {
			t.Fatal(err)
		}
		if !o.planesWaiting {
			t.Fatalf("refresh %d: a move half a refresh before the vblank did not wait for the plane latch: %d commits", i, len(r.dev.commits))
		}
		r.at(vblank + time.Millisecond)
		n := len(r.dev.commits)
		if err := o.planesDue(); err != nil {
			t.Fatal(err)
		}
		if len(r.dev.commits) == n {
			slipped++
			r.at(vblank + interval - backend.PlaneLead)
			r.until("the slipped latch", func() bool { return len(r.dev.commits) > n })
		}
		if _, got, _ := cursorAt(r.dev.commits[len(r.dev.commits)-1]); got != at.Sub(image.Pt(4, 6)) {
			t.Fatalf("refresh %d: the late latch put the plane at %v", i, got)
		}
		r.at(vblank + interval)
		r.dev.flip(t, 10, uint32(len(r.dev.commits)+1))
		r.until("the late latch's flip", func() bool { return !o.flipping })
	}
	if slipped != 0 || len(r.dev.commits) != refreshes {
		t.Fatalf("%d plane latches firing 1 ms past their vblank slipped %d refreshes and issued %d atomic requests, want none slipped and one each", refreshes, slipped, len(r.dev.commits))
	}
}

func TestAFrameCommitCarriesTheCursorAsOfThePlaneLead(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	r.at(o.lastFlip + interval - backend.PlaneLead)
	r.cursorCommit("the cursor's first commit", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	vblank := o.lastFlip + interval
	r.at(vblank - interval/2)
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
	r.at(vblank - backend.PlaneLead)
	r.until("the frame's commit at the plane lead", func() bool { return o.flipping && o.late == nil })
	frame := r.dev.commits[len(r.dev.commits)-1]
	if _, at, _ := cursorAt(frame); frame.values[[2]uint32{40, propFB}] == 0 || frame.values[[2]uint32{40, propInFence}] != uint64(r.alloc.fences[len(r.alloc.fences)-1]) || at != image.Pt(16, 24) {
		t.Fatalf("the frame committed %+v, want its buffer, its fence and the cursor at (16,24)", frame)
	}
}

func TestAFrameWithoutACursorCommitsAtOnce(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	r.at(o.lastFlip + modeOf(o.mode).Interval()/2)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("the frame", func() bool { return o.flipping })
	if o.late != nil || len(r.dev.commits) == 0 {
		t.Fatalf("a frame with no cursor on screen waited for the plane lead: %d commits", len(r.dev.commits))
	}
}

func (r *rig) frames() int {
	n := 0
	for _, l := range r.rec.log {
		if l == "frame" {
			n++
		}
	}
	return n
}

func TestTheNextFrameStartsWhileOneFlipsWhenRendersOverrun(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	for range 32 {
		o.budget.Sample(interval * 3 / 2)
	}
	r.rec.log = nil
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first slow frame", o, 0)
	first := r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{40, propFB}]
	n := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("the next frame drawn while the first flips", func() bool { return o.late != nil })
	if len(r.dev.commits) != n || r.frames() != 2 || len(o.buffers) != buffersPerOut+1 {
		t.Fatalf("%d commits, %d frames and %d buffers while the first frame flips", len(r.dev.commits)-n, r.frames(), len(o.buffers))
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.frames() != 2 {
		t.Fatalf("%d frames started with one flipping and one waiting, want no third", r.frames())
	}
	r.dev.flip(t, 10, 2)
	r.until("the waiting frame's commit on the flip", func() bool { return o.flipping && o.late == nil })
	second := r.dev.commits[len(r.dev.commits)-1]
	if fb := second.values[[2]uint32{40, propFB}]; fb == 0 || fb == first || second.flags&kdrm.AtomicTestOnly != 0 {
		t.Fatalf("the commit on the flip %+v, want the waiting frame's buffer", second)
	}
	if len(r.rec.presents) != 2 || r.rec.presents[1].Seq != 2 {
		t.Fatalf("presents %+v", r.rec.presents)
	}
	r.until("the third frame drawn while the second flips", func() bool { return o.late != nil })
	r.dev.flip(t, 10, 3)
	r.until("the third frame's commit on the flip", func() bool { return o.flipping && o.late == nil })
	if len(r.rec.presents) != 3 || r.rec.presents[2].Seq != 3 {
		t.Fatalf("presents %+v, want a frame on each consecutive flip", r.rec.presents)
	}
}

func TestFramesThatFitTheRefreshWaitForTheFlip(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	for range 32 {
		o.budget.Sample(time.Millisecond)
	}
	r.rec.log = nil
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if o.late != nil || r.frames() != 1 || len(o.buffers) != buffersPerOut {
		t.Fatalf("a fast frame started while one flips: %d frames, %d buffers", r.frames(), len(o.buffers))
	}
}

func TestAFlipEventTimesItsPresentationInHardware(t *testing.T) {
	r := start(t)
	r.flipFirstFrame()
	if p := r.rec.presents[len(r.rec.presents)-1]; !p.Hardware || p.Seq != 1 {
		t.Fatalf("a flip at sequence 1 presented %+v, want it timed in hardware", p)
	}
}

func TestAFlipAtSequenceZeroIsNotTimedInHardware(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	r.dev.flip(t, 10, 0)
	r.until("the first frame's flip", func() bool { return !o.flipping })
	if p := r.rec.presents[len(r.rec.presents)-1]; p.Hardware {
		t.Fatalf("a flip at sequence 0 presented %+v, want it not timed in hardware", p)
	}
}

func (r *rig) idleUntilTheCRTCDrifts(o *Output) time.Duration {
	r.t.Helper()
	interval := modeOf(o.mode).Interval()
	drifted := o.lastFlip + 1000*interval + 3*time.Millisecond
	r.dev.vblank = func(crtc uint32) (kdrm.Vblank, error) {
		if crtc != o.crtc.id {
			r.t.Fatalf("read the vblank of CRTC %d, want %d", crtc, o.crtc.id)
		}
		return kdrm.Vblank{Sequence: 1001, Time: drifted, Active: true}, nil
	}
	r.at(drifted + time.Millisecond)
	return drifted
}

func TestTheFirstFrameAfterIdleAimsAtTheCRTCsVblank(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	vblank := r.idleUntilTheCRTCDrifts(o)
	if wait := o.planesWait(); wait != interval-time.Millisecond-backend.PlaneLead {
		t.Fatalf("after idle the planes wait %v, want the plane lead before the CRTC's next vblank, %v", wait, interval-time.Millisecond-backend.PlaneLead)
	}
	if wait, want := o.frameWait(), o.budget.Latch(backend.Pacing{Vblank: vblank, Refresh: interval, Paced: interval}, r.now())-r.now(); wait != want {
		t.Fatalf("after idle the frame waits %v, want %v before the CRTC's next vblank", wait, want)
	}
	r.at(vblank + interval/2 + time.Millisecond)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame after idle", o, 0)
	if o.aimedAt != vblank+interval {
		t.Fatalf("the first frame after idle aimed at %v, want the CRTC's next vblank at %v", o.aimedAt, vblank+interval)
	}
	if r.dev.vblankReads != 1 {
		t.Fatalf("read the CRTC's vblank %d times on one wake from idle, want once", r.dev.vblankReads)
	}
}

func TestFramesWithinARefreshOfTheirFlipReadNoVblank(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	r.dev.vblank = func(uint32) (kdrm.Vblank, error) {
		return kdrm.Vblank{Sequence: 1, Time: o.lastFlip, Active: true}, nil
	}
	r.at(o.lastFlip + interval/2 + time.Millisecond)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the next frame", o, 0)
	if r.dev.vblankReads != 0 || o.aimedAt != o.lastFlip+interval {
		t.Fatalf("a frame half a refresh after its flip read the vblank %d times and aimed at %v, want no read and %v", r.dev.vblankReads, o.aimedAt, o.lastFlip+interval)
	}
}

func TestADriverWithoutAVblankSequenceExtrapolatesAndIsAskedOnce(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	flip := o.lastFlip
	r.at(flip + 1000*interval + 4*time.Millisecond)
	if wait := o.planesWait(); wait != interval-4*time.Millisecond-backend.PlaneLead {
		t.Fatalf("without a vblank sequence the planes wait %v, want the extrapolated %v", wait, interval-4*time.Millisecond-backend.PlaneLead)
	}
	r.advance(10 * interval)
	o.planesWait()
	if r.dev.vblankReads != 1 || r.reports != nil || o.lastVblank != flip {
		t.Fatalf("a driver without a vblank sequence was asked %d times, reports %v, its vblank anchor moved %v", r.dev.vblankReads, r.reports, o.lastVblank-flip)
	}
}

func TestAPoweredOnCRTCIsNotAskedForItsVblankBeforeItsModeset(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	r.dev.vblank = func(uint32) (kdrm.Vblank, error) {
		return kdrm.Vblank{}, unix.EINVAL
	}
	mode := modeOf(o.mode)
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOff, Mode: mode}); err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: mode}); err != nil {
		t.Fatal(err)
	}
	r.at(o.lastFlip + 100*interval)
	o.frameWait()
	o.planesWait()
	if r.dev.vblankReads != 0 || r.reports != nil || o.unanchored {
		t.Fatalf("a CRTC powered on but not yet modeset was asked for its vblank %d times, reports %v, unanchored %v", r.dev.vblankReads, r.reports, o.unanchored)
	}
}

func (r *rig) idlePresentation(o *Output) backend.Presentation {
	r.t.Helper()
	r.rec.idle = true
	frames := r.frames()
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.until("the empty frame", func() bool { return r.frames() > frames })
	r.at(o.idleAt)
	presents := len(r.rec.presents)
	if err := o.paceElapsed(); err != nil {
		r.t.Fatal(err)
	}
	got := r.rec.presents[presents:]
	if len(got) != 1 || !got[0].Idle {
		r.t.Fatalf("the empty frame's refresh presented %+v, want one idle presentation", got)
	}
	return got[0]
}

func TestAnIdleRefreshIsPresentedAtItsVblanksSequenceAndTheRefreshPeriod(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	vblank := r.idleUntilTheCRTCDrifts(o)
	p := r.idlePresentation(o)
	if want := (backend.Presentation{Time: vblank + interval, Refresh: interval, Seq: 1002, Idle: true}); p != want {
		t.Fatalf("an idle refresh a refresh after the CRTC's vblank 1001 presented %+v, want %+v", p, want)
	}
}

func TestAnIdleRefreshAfterAFlipWithoutASequenceHasNone(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	r.dev.flip(t, 10, 0)
	r.until("the first frame's flip", func() bool { return !o.flipping })
	r.idleUntilTheCRTCDrifts(o)
	if p := r.idlePresentation(o); p.Seq != 0 || p.Refresh != modeOf(o.mode).Interval() {
		t.Fatalf("an idle refresh after a flip at sequence 0 presented %+v, want sequence 0 like the flip's and the refresh period", p)
	}
}
