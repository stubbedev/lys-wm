package drm

import (
	"errors"
	"image"
	"testing"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func testCommits(commits []commit) int {
	n := 0
	for _, c := range commits {
		if c.flags&kdrm.AtomicTestOnly != 0 {
			n++
		}
	}
	return n
}

func (r *rig) shownCursor(o *Output, hotspot image.Point) *cursor {
	r.t.Helper()
	r.cursorCommit("the cursor's first commit", o, func() error { return r.setCursor(o, hotspot) })
	r.until("the latch after the first cursor", func() bool { return o.planesWait() <= 0 })
	return o.cursor
}

func (r *rig) frameWithCursorAt(o *Output, at image.Point) commit {
	r.t.Helper()
	defer func(clock time.Duration) { r.pinned = clock }(r.pinned)
	interval := modeOf(o.mode).Interval()
	vblank := o.lastFlip + interval
	o.budget = backend.RenderBudget{}
	r.pinned = o.lastFlip + time.Millisecond
	return r.cursorCommit("the frame", o, func() error {
		err := errors.Join(o.ScheduleFrame(), o.cursor.MoveCursor(at))
		r.pinned = vblank - backend.PlaneLead
		return err
	})
}

func TestCursorMotionIssuesOneAtomicRequestPerRefresh(t *testing.T) {
	r := start(t)
	o := r.lit()
	c := r.shownCursor(o, image.Pt(4, 6))
	r.dev.commits = nil
	const refreshes = 8
	for i := range refreshes {
		at := image.Pt(100+3*i, 50)
		r.until("the latch", func() bool { return o.planesWait() <= 0 })
		shown := r.cursorCommit("the move", o, func() error { return c.MoveCursor(at) })
		if _, got, _ := cursorAt(shown); got != at.Sub(image.Pt(4, 6)) {
			t.Fatalf("the move to %v put the plane at %v", at, got)
		}
	}
	if len(r.dev.commits) != refreshes {
		t.Fatalf("%d refreshes of cursor motion issued %d atomic requests, %d of them test commits", refreshes, len(r.dev.commits), testCommits(r.dev.commits))
	}
}

func TestFramesCarryingAMovingCursorSkipTheTestCommit(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.shownCursor(o, image.Pt(4, 6))
	const warm, frames = 6, 6
	for i := range warm {
		r.frameWithCursorAt(o, image.Pt(100+i, 50))
	}
	r.dev.commits = nil
	for i := range frames {
		r.frameWithCursorAt(o, image.Pt(200+i, 60))
	}
	if len(r.dev.commits) != frames {
		t.Fatalf("%d frames with a moving cursor issued %d atomic requests, %d of them test commits", frames, len(r.dev.commits), testCommits(r.dev.commits))
	}
	r.dev.commits = nil
	r.frameWithCursorAt(o, image.Point{})
	if testCommits(r.dev.commits) == 0 {
		t.Fatal("a cursor newly clipped at the screen edge went out on a frame without a test commit")
	}
	for i := range warm {
		r.frameWithCursorAt(o, image.Pt(i, 1))
	}
	r.dev.commits = nil
	for i := range frames {
		r.frameWithCursorAt(o, image.Pt(2+i, 2))
	}
	if testCommits(r.dev.commits) != 0 {
		t.Fatalf("frames with a cursor still clipped at the edge issued %d test commits", testCommits(r.dev.commits))
	}
}

func TestARealCommitRefusingTheCursorMoveFallsBack(t *testing.T) {
	for _, refusal := range []error{unix.EINVAL, unix.ERANGE} {
		t.Run(refusal.Error(), func(t *testing.T) {
			r := start(t)
			o := r.lit()
			c := r.shownCursor(o, image.Pt(4, 6))
			r.dev.refuseCursorAt = func(image.Point) error { return refusal }
			r.dev.commits = nil
			if err := c.MoveCursor(image.Pt(30, 20)); !errors.Is(err, refusal) {
				t.Fatalf("the refused move returned %v, want %v", err, refusal)
			}
			if len(r.dev.commits) != 1 || r.dev.commits[0].flags&kdrm.AtomicTestOnly != 0 {
				t.Fatalf("the refused move issued %+v, want the real commit alone", r.dev.commits)
			}
			if _, err := c.BeginCursor(); !errors.Is(err, refusal) {
				t.Fatalf("the refused plane took another image: %v", err)
			}
			if len(r.reports) != 0 {
				t.Fatalf("a refusal the compositor falls back from reported %v", r.reports)
			}
			r.dev.commits = nil
			if err := o.ScheduleFrame(); err != nil {
				t.Fatal(err)
			}
			r.committedSince("the frame after the refusal", o, 0)
			last := r.dev.commits[len(r.dev.commits)-1]
			if fb, _, touched := cursorAt(last); !touched || fb != 0 || last.values[[2]uint32{40, propFB}] == 0 {
				t.Fatalf("the frame after the refusal left the plane showing: %+v", last)
			}
		})
	}
}

func TestABusyCursorCommitKeepsThePlane(t *testing.T) {
	r := start(t)
	o := r.lit()
	c := r.shownCursor(o, image.Pt(4, 6))
	r.dev.refuseCursorAt = func(image.Point) error { return unix.EBUSY }
	if err := c.MoveCursor(image.Pt(30, 20)); err != nil {
		t.Fatalf("a busy commit lost the plane: %v", err)
	}
	if c.refused != nil || !c.visible || !c.dirty {
		t.Fatalf("a busy commit left the cursor refused %v, visible %t, dirty %t", c.refused, c.visible, c.dirty)
	}
}

func TestARefusedColourDoesNotCostTheCursorItsPlane(t *testing.T) {
	r := start(t)
	o := r.lit()
	if err := r.setCursor(o, image.Pt(4, 6)); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the cursor", o, 0)
	c := o.cursor
	if err := errors.Join(c.MoveCursor(image.Pt(30, 20)), o.SetGamma([]uint16{0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23})); err != nil {
		t.Fatal(err)
	}
	r.dev.refuseGamma = true
	r.dev.flip(t, 10, 2)
	r.until("the refused commit's report", func() bool { return len(r.reports) > 0 })
	if c.refused != nil || !c.visible {
		t.Fatalf("the colour's refusal blamed the cursor: refused %v, visible %t", c.refused, c.visible)
	}
	if !errors.Is(r.reports[0], unix.EINVAL) {
		t.Fatalf("reported %v, want the refused commit", r.reports)
	}
}

func TestAFrameDropsACursorItsRealCommitRefuses(t *testing.T) {
	r := start(t)
	o := r.lit()
	c := r.shownCursor(o, image.Pt(4, 6))
	for i := range 6 {
		r.frameWithCursorAt(o, image.Pt(100+i, 50))
	}
	r.dev.refuseCursorAt = func(at image.Point) error {
		if at.X > 500 {
			return unix.EINVAL
		}
		return nil
	}
	r.dev.commits = nil
	shown := r.frameWithCursorAt(o, image.Pt(600, 50))
	if fb, _, touched := cursorAt(shown); !touched || fb != 0 || shown.values[[2]uint32{40, propFB}] == 0 {
		t.Fatalf("the frame kept the refused cursor: %+v", shown)
	}
	if err := c.MoveCursor(image.Pt(1, 1)); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("the plane stayed in use after the frame dropped it: %v", err)
	}
}

func TestAScanoutTheKernelRefusesForTheCursorIsComposited(t *testing.T) {
	r := start(t)
	o := r.lit()
	c := r.shownCursor(o, image.Pt(4, 6))
	r.cursorCommit("the move", o, func() error { return c.MoveCursor(image.Pt(50, 50)) })
	client := gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: 7, Stride: 1920 * 4}}}
	key := &fakeBuffer{client}
	if ok, err := o.Scanout(key, client, backend.Whole(client.Size), func() error { return nil }, false); err != nil || !ok {
		t.Fatalf("Scanout = %t, %v", ok, err)
	}
	r.dev.flip(t, 10, uint32(len(r.dev.commits)))
	r.until("the client buffer's flip", func() bool { return !o.flipping })
	r.dev.refuseCursorAt = func(image.Point) error { return unix.EINVAL }
	if err := errors.Join(o.ScheduleFrame(), c.MoveCursor(image.Pt(30, 20))); err != nil {
		t.Fatal(err)
	}
	if ok, err := o.Scanout(key, client, backend.Whole(client.Size), func() error { return nil }, false); err != nil || ok {
		t.Fatalf("Scanout with a refused cursor = %t, %v, want the frame composited", ok, err)
	}
	if len(o.tested) != 0 {
		t.Fatalf("the refused state stayed cached: %v", o.tested)
	}
}

func TestACursorDrawnOnTheCPURendersIntoItsBufferWithoutAReadback(t *testing.T) {
	r := start(t)
	o := r.lit()
	for i, hotspot := range []image.Point{{}, {2, 3}, {1, 1}} {
		if err := r.setCursor(o, hotspot); err != nil {
			t.Fatalf("cursor change %d: %v", i, err)
		}
		if pix := o.cursor.pending.dumb.Pix; [4]byte(pix[:4]) != [4]byte{0, 0, 0xff, 0xff} || pix[4] != 0 {
			t.Fatalf("cursor change %d: the buffer starts %v", i, pix[:8])
		}
	}
	if got := r.b.SyncReads(); got != 0 {
		t.Fatalf("%d synchronous reads through three cursor changes", got)
	}
}

func TestAGPURendererWithoutAsyncReadsLeavesADrawnCursorToTheFrame(t *testing.T) {
	gpu := struct{ render.Renderer }{soft.New()}
	r := boot(t, func(dev *fakeDevice) allocator { return &copyAllocator{dev: dev, primary: gpu} })
	o := r.lit()
	before := r.b.SyncReads()
	if err := r.setCursor(o, image.Point{}); !errors.Is(err, backend.ErrCursorReadback) {
		t.Fatalf("a cursor drawn by a renderer that reads back synchronously: %v, want ErrCursorReadback", err)
	}
	if got := r.b.SyncReads() - before; got != 0 {
		t.Fatalf("%d synchronous reads for a cursor change", got)
	}
	cp, _ := o.CursorPlane()
	if err := cp.LoadCursor(make([]byte, 4*4*4), 16, image.Pt(4, 4), image.Point{}); err != nil {
		t.Fatalf("a CPU image after the refusal: %v", err)
	}
}
