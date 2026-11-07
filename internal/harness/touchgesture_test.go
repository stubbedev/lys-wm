package harness

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	bottomEdgeOverview  = binds.GestureBind{Kind: backend.Swipe, Fingers: 1, Edge: binds.EdgeBottom, Direction: binds.SwipeUp, Drive: binds.DriveOverview}
	threeFingerSwitches = binds.GestureBind{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeHorizontal, Drive: binds.DriveWorkspaces}
)

func touchLog(log testclient.Log) []string {
	var out []string
	for _, l := range log {
		for _, p := range []string{"down ", "touch_motion ", "up ", "cancel", "touch_frame"} {
			if strings.HasPrefix(l, p) {
				out = append(out, l)
			}
		}
	}
	return out
}

type fingers struct {
	h  *Harness
	at time.Duration
}

func (f *fingers) do(step time.Duration, do func(t *headless.Touch, o *headless.Output, at time.Duration) error) {
	f.h.t.Helper()
	f.at += step
	at := f.at
	f.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(do(b.Touch(), b.Outputs()[0], at), b.Touch().Frame())
	})
}

func (f *fingers) down(id int32, x, y float64) {
	f.h.t.Helper()
	f.do(time.Millisecond, func(t *headless.Touch, o *headless.Output, at time.Duration) error { return t.DownAt(at, id, o, x, y) })
}

func (f *fingers) move(step time.Duration, ids []int32, xs, ys []float64) {
	f.h.t.Helper()
	f.do(step, func(t *headless.Touch, _ *headless.Output, at time.Duration) error {
		var err error
		for i, id := range ids {
			err = errors.Join(err, t.MotionAt(at, id, xs[i], ys[i]))
		}
		return err
	})
}

func (f *fingers) up(ids ...int32) {
	f.h.t.Helper()
	f.do(time.Millisecond, func(t *headless.Touch, _ *headless.Output, at time.Duration) error {
		var err error
		for _, id := range ids {
			err = errors.Join(err, t.UpAt(at, id))
		}
		return err
	})
}

func TestABottomEdgeSwipeDrivesTheOverviewAndSettlesByVelocity(t *testing.T) {
	var now time.Duration
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = springTable(t)
		cfg.Clock = func() time.Duration { return now }
		cfg.Gestures.Touch = []binds.GestureBind{bottomEdgeOverview}
	}, small)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	progress := func() (p float64) {
		h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
			p = c.OverviewProgress(now)
			return nil
		})
		return p
	}
	f := &fingers{h: h, at: time.Hour}
	f.down(1, 0.5, 0.95)
	r.c.DispatchUntil("the touch down on the window", func() bool { return slices.Contains(r.in.Log, "down 1 win 80,95") })
	f.move(8*time.Millisecond, []int32{1}, []float64{0.5}, []float64{0.8})
	f.move(time.Second, []int32{1}, []float64{0.5}, []float64{0.7})
	half := progress()
	f.move(time.Second, []int32{1}, []float64{0.5}, []float64{0.6})
	if got := progress(); half <= 0 || got <= half {
		t.Fatalf("the overview does not follow the finger up: %v then %v", half, got)
	}
	f.move(time.Second, []int32{1}, []float64{0.5}, []float64{0.62})
	f.up(1)
	h.advanceClock(&now, 5*time.Second)
	if got := progress(); got != 0 {
		t.Fatalf("a slow swipe a third of the way settled at %v, not closed", got)
	}
	f.down(2, 0.5, 0.97)
	for _, y := range []float64{0.85, 0.75, 0.65} {
		f.move(8*time.Millisecond, []int32{2}, []float64{0.5}, []float64{y})
	}
	f.up(2)
	h.advanceClock(&now, 5*time.Second)
	if got := progress(); got != 1 {
		t.Fatalf("a quick flick a third of the way settled at %v, not open", got)
	}
	r.c.Sync()
	want := []string{"down 1 win 80,95", "touch_frame", "cancel", "down 2 win 80,97", "touch_frame", "cancel"}
	r.c.DispatchUntil("both cancels", func() bool { return len(touchLog(r.in.Log)) >= len(want) })
	if got := touchLog(r.in.Log); !slices.Equal(got, want) {
		t.Fatalf("the client saw %q, want %q", got, want)
	}
}

func TestThreeFingersOnTheScreenDriveWorkspaces(t *testing.T) {
	t.Parallel()
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, func(cfg *compositor.Config) {
		cfg.Gestures.Touch = []binds.GestureBind{threeFingerSwitches}
	})
	r.redBlue()
	f := &fingers{h: r.h, at: time.Hour}
	ids := []int32{1, 2, 3}
	ys := []float64{0.3, 0.5, 0.7}
	for i, id := range ids {
		f.down(id, 0.5, ys[i])
	}
	for _, x := range []float64{0.45, 0.375, 0.25} {
		f.move(8*time.Millisecond, ids, []float64{x, x, x}, ys)
	}
	r.h.Eventually("both workspaces a quarter along", both(showing(100, 30, swipeRed), showing(130, 30, swipeBlue)))
	for _, x := range []float64{0.1, -0.1} {
		f.move(8*time.Millisecond, ids, []float64{x, x, x}, ys)
	}
	f.up(ids...)
	if got := r.active(); got != 2 {
		t.Fatalf("three fingers past half left workspace %d active", got)
	}
}

func TestOneFingerAwayFromTheEdgesReachesTheClientInTheSameDispatch(t *testing.T) {
	t.Parallel()
	r := inputOn(t, StartUntracedWith(t, func(cfg *compositor.Config) {
		cfg.Gestures.Touch = []binds.GestureBind{bottomEdgeOverview, threeFingerSwitches}
	}, small))
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		if err := c.Input(b.Touch(), backend.TouchDown{Output: b.Outputs()[0], ID: 1, X: 0.5, Y: 0.5}); err != nil {
			return err
		}
		if c.Seat().TouchSurface(1) == nil {
			return errors.New("the touch down did not reach the client in the dispatch it arrived in")
		}
		return errors.Join(c.Input(b.Touch(), backend.TouchFrame{}),
			c.Input(b.Touch(), backend.TouchMotion{ID: 1, X: 0.25, Y: 0.5}), c.Input(b.Touch(), backend.TouchFrame{}),
			c.Input(b.Touch(), backend.TouchUp{ID: 1}), c.Input(b.Touch(), backend.TouchFrame{}))
	})
	r.expect("down 1 win 80,50", "touch_frame", "touch_motion 1 40,50", "touch_frame", "up 1", "touch_frame")
}

func TestTouchGestureTrackingAllocatesNothingPerMotion(t *testing.T) {
	r := startSwipeRig(t, StartUntracedWith, anim.Style{Kind: anim.Slide}, func(cfg *compositor.Config) {
		cfg.Gestures.Touch = []binds.GestureBind{threeFingerSwitches}
	})
	r.redBlue()
	r.h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		o := b.Outputs()[0]
		var errs error
		for i, y := range []float64{0.3, 0.5, 0.7} {
			errs = errors.Join(errs, c.Input(b.Touch(), backend.TouchDown{Output: o, ID: int32(i), X: 0.5, Y: y}))
		}
		motion := &backend.TouchMotion{ID: 0, X: 0.5, Y: 0.3}
		motion.X = 0.5
		for range 40 {
			motion.X -= 0.01
			errs = errors.Join(errs, c.Input(b.Touch(), motion))
		}
		sign := 1.0
		step := func() {
			sign = -sign
			motion.X += sign * 0.05
			errs = errors.Join(errs, c.Input(b.Touch(), motion))
		}
		if c.Seat().TouchSurface(0) != nil {
			return errors.New("three fingers moving together were not claimed")
		}
		if n := allocsPerRun(100, func() {}, step); n != 0 {
			return fmt.Errorf("a touch motion in a claimed gesture allocated %d times", n)
		}
		return errs
	})
}

func TestTouchpadDriveMoveDropsTheWindowUnderThePointerIntoTheTree(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.PlainDecor()
		cfg.Decor.BorderWidth = 0
		cfg.Gestures.Binds = []binds.GestureBind{{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeAny, Drive: binds.DriveMove}}
	}, tall)
	c := testclient.Connect(t, h.Socket())
	left := c.Window("left", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("right to map", right.Mapped)
	h.Eventually("left on the left, right on the right", both(showing(20, 60, swipeRed), showing(140, 60, swipeBlue)))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.125, 0.5), b.Pointer().Frame())
	})
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureBegin(backend.Swipe, 3) })
	h.Eventually("no drag before the fingers move", func(_ *headless.Backend, comp *compositor.Compositor) bool { return !comp.Dragging() })
	for range 12 {
		h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.Swipe(10, 0) })
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if !comp.Dragging() {
			return errors.New("a three-finger swipe bound to drive move started no move")
		}
		return nil
	})
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureEnd(backend.Swipe, false) })
	h.Eventually("the windows swapped in the tree", both(showing(20, 20, swipeBlue), showing(140, 20, swipeRed), func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return !comp.Dragging()
	}))
}

func TestAWindowWithTouchGesturesOffKeepsEveryTouch(t *testing.T) {
	t.Parallel()
	off := false
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Gestures.Touch = []binds.GestureBind{bottomEdgeOverview}
		cfg.Rules = []wm.Rule{{Match: wm.Match{Title: regexp.MustCompile(`^win$`)}, TouchGestures: &off}}
	}, small)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	f := &fingers{h: h, at: time.Hour}
	f.down(1, 0.5, 0.95)
	f.move(8*time.Millisecond, []int32{1}, []float64{0.5}, []float64{0.6})
	f.up(1)
	r.expect("down 1 win 80,95", "touch_frame", "touch_motion 1 80,60", "touch_frame", "up 1", "touch_frame")
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if p := c.OverviewProgress(c.Now()); p != 0 {
			return fmt.Errorf("the edge swipe moved the overview to %v over a window that keeps its touches", p)
		}
		return nil
	})
}

func touchWindowRig(t *testing.T, drive binds.Drive) (*Harness, *fingers) {
	t.Helper()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.PlainDecor()
		cfg.Decor.BorderWidth = 0
		cfg.Gestures.Touch = []binds.GestureBind{{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeAny, Drive: drive}}
	}, tall)
	c := testclient.Connect(t, h.Socket())
	left := c.Window("left", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("right to map", right.Mapped)
	h.Eventually("left on the left, right on the right", both(showing(20, 20, swipeRed), showing(140, 20, swipeBlue)))
	return h, &fingers{h: h, at: time.Hour}
}

func windowRects(h *Harness) map[string]ipc.Rect {
	out := map[string]ipc.Rect{}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		for _, w := range c.Snapshot().Windows {
			out[w.Title] = w.Rect
		}
		return nil
	})
	return out
}

func TestThreeFingersOnTheScreenMoveTheWindowUnderThemIntoTheTree(t *testing.T) {
	t.Parallel()
	h, f := touchWindowRig(t, binds.DriveMove)
	ids, ys := []int32{1, 2, 3}, []float64{0.3, 0.5, 0.7}
	for i, id := range ids {
		f.down(id, 0.125, ys[i])
	}
	for _, x := range []float64{0.25, 0.5, 0.75, 0.875} {
		f.move(8*time.Millisecond, ids, []float64{x, x, x}, ys)
	}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if !c.Dragging() {
			return errors.New("three fingers bound to drive move started no move")
		}
		return nil
	})
	f.up(ids...)
	h.Eventually("the windows swapped in the tree", both(showing(20, 20, swipeBlue), showing(140, 20, swipeRed), func(_ *headless.Backend, c *compositor.Compositor) bool {
		return !c.Dragging()
	}))
}

func TestThreeFingersOnTheScreenResizeTheWindowUnderThem(t *testing.T) {
	t.Parallel()
	h, f := touchWindowRig(t, binds.DriveResize)
	before := windowRects(h)
	ids, ys := []int32{1, 2, 3}, []float64{0.3, 0.5, 0.7}
	for i, id := range ids {
		f.down(id, 0.4, ys[i])
	}
	for _, x := range []float64{0.45, 0.5, 0.55, 0.6} {
		f.move(8*time.Millisecond, ids, []float64{x, x, x}, ys)
	}
	f.up(ids...)
	h.Eventually("the left window wider by the fingers' travel", func(_ *headless.Backend, c *compositor.Compositor) bool {
		after := map[string]ipc.Rect{}
		for _, w := range c.Snapshot().Windows {
			after[w.Title] = w.Rect
		}
		return !c.Dragging() && after["left"].Width > before["left"].Width+16 && after["right"].X > before["right"].X
	})
}
