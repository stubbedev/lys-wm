package harness

import (
	"errors"
	"image"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	swipeRed   = [3]byte{0xff, 0, 0}
	swipeBlue  = [3]byte{0, 0, 0xff}
	swipeGreen = [3]byte{0, 0xff, 0}
)

const swipeSettled = 30 * time.Second

type swipeRig struct {
	t      *testing.T
	h      *Harness
	c      *testclient.Client
	now    *time.Duration
	events time.Duration
}

func swipeConfig(t *testing.T, now *time.Duration, style anim.Style, extra func(*compositor.Config)) func(*compositor.Config) {
	t.Helper()
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Workspaces: {Enabled: &on, Shape: anim.Smooth, Style: &style}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return *now }
		cfg.Decor = compositor.DefaultDecor()
		cfg.Decor.BorderWidth = 0
		cfg.Gestures.Distance = float64(tall.Size.X)
		if extra != nil {
			extra(cfg)
		}
	}
}

func startSwipeRig(t *testing.T, start gpuStarter, style anim.Style, extra func(*compositor.Config)) *swipeRig {
	t.Helper()
	now := time.Hour
	r := &swipeRig{t: t, now: &now, events: 5 * time.Hour}
	r.h = start(t, swipeConfig(t, &now, style, extra), tall)
	r.c = testclient.Connect(t, r.h.Socket())
	return r
}

func (r *swipeRig) at(d time.Duration) {
	r.h.Do(func(*headless.Backend, *compositor.Compositor) error {
		*r.now = d
		return nil
	})
}

func (r *swipeRig) settle() {
	r.at(*r.now + swipeSettled)
}

func (r *swipeRig) workspace(id int, name string, fill func(*testclient.Client) *testclient.Window) {
	r.t.Helper()
	r.h.dispatch("workspace " + name)
	r.settle()
	w := fill(r.c)
	r.c.DispatchUntil(name+" to map", w.Mapped)
	r.h.Eventually("workspace "+name+" shown", showingWorkspace(wm.WorkspaceID(id)))
}

func (r *swipeRig) redBlue() {
	r.t.Helper()
	red := r.c.Window("red", testclient.Fill(0, 0, 0xff))
	r.c.DispatchUntil("red to map", red.Mapped)
	r.h.Eventually("red shown", showing(80, 30, swipeRed))
	r.workspace(2, "2", func(c *testclient.Client) *testclient.Window { return c.Window("blue", testclient.Fill(0xff, 0, 0)) })
	r.h.Eventually("blue shown", showing(80, 30, swipeBlue))
	r.h.dispatch("workspace 1")
	r.settle()
	r.h.Eventually("red shown again", showing(80, 30, swipeRed))
}

func (r *swipeRig) begin() {
	r.t.Helper()
	r.h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureBegin(backend.Swipe, 3) })
}

func (r *swipeRig) swipe(dx, dy float64) {
	r.t.Helper()
	r.events += 8 * time.Millisecond
	at := r.events
	r.h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.SwipeAt(at, dx, dy) })
}

func (r *swipeRig) rest() {
	r.events += time.Second
}

func (r *swipeRig) end(cancelled bool) {
	r.t.Helper()
	r.events += time.Millisecond
	at := r.events
	r.h.gesture(func(_ *headless.Output, p *headless.Pointer) error {
		return p.GestureEndAt(at, backend.Swipe, cancelled)
	})
}

func both(conds ...func(*headless.Backend, *compositor.Compositor) bool) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, c *compositor.Compositor) bool {
		for _, cond := range conds {
			if !cond(b, c) {
				return false
			}
		}
		return true
	}
}

func (r *swipeRig) active() wm.WorkspaceID {
	var id wm.WorkspaceID
	r.h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		s := c.WM()
		id, _ = s.ActiveOn(s.FocusedOutput())
		return nil
	})
	return id
}

func (r *swipeRig) expectNoMoreFrames() {
	r.t.Helper()
	time.Sleep(50 * time.Millisecond)
	var before uint64
	r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		before = b.Outputs()[0].Frames()
		return nil
	})
	time.Sleep(100 * time.Millisecond)
	r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if n := b.Outputs()[0].Frames(); n != before {
			r.t.Errorf("%d frames ran with nothing moving", n-before)
		}
		return nil
	})
}

func TestASwipeTracksTheFingersAndCommits(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		r := startSwipeRig(t, start, anim.Style{Kind: anim.Slide}, nil)
		r.redBlue()
		r.begin()
		r.swipe(-40, 0)
		r.h.Eventually("both workspaces a quarter along", both(showing(100, 30, swipeRed), showing(130, 30, swipeBlue)))
		r.expectNoMoreFrames()
		if got := r.active(); got != 1 {
			t.Fatalf("tracking switched to workspace %d", got)
		}
		r.at(*r.now + time.Hour)
		r.swipe(-60, 0)
		r.h.Eventually("the same place however late the frame", both(showing(50, 30, swipeRed), showing(70, 30, swipeBlue)))
		r.rest()
		r.end(false)
		if got := r.active(); got != 2 {
			t.Fatalf("past half stays on workspace %d", got)
		}
		r.at(*r.now + 50*time.Millisecond)
		r.h.Eventually("the spring under way, both still drawn", both(showing(5, 30, swipeRed), showing(100, 30, swipeBlue)))
		r.settle()
		r.h.Eventually("the second workspace in place", showing(5, 30, swipeBlue))
		r.expectNoMoreFrames()
	})
}

func TestASwipeShortOfHalfGoesBack(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		r := startSwipeRig(t, start, anim.Style{Kind: anim.Slide}, nil)
		r.redBlue()
		r.begin()
		r.swipe(-60, 0)
		r.h.Eventually("tracking", showing(110, 30, swipeBlue))
		r.rest()
		r.end(false)
		if got := r.active(); got != 1 {
			t.Fatalf("a short swipe went to workspace %d", got)
		}
		r.settle()
		r.h.Eventually("the first workspace back in place", showing(155, 30, swipeRed))
		r.expectNoMoreFrames()
	})
}

func TestAFlickCommitsFromAShortDistance(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, nil)
	r.redBlue()
	r.begin()
	for range 3 {
		r.swipe(-16, 0)
	}
	r.end(false)
	if got := r.active(); got != 2 {
		t.Fatalf("a flick left workspace %d showing", got)
	}
}

func TestAFlickBackCancelsFromFarAlong(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, nil)
	r.redBlue()
	r.begin()
	r.swipe(-120, 0)
	r.rest()
	for range 3 {
		r.swipe(16, 0)
	}
	r.end(false)
	if got := r.active(); got != 1 {
		t.Fatalf("a flick back ended on workspace %d", got)
	}
}

func TestACancelledSwipeGoesBack(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, nil)
	r.redBlue()
	r.begin()
	r.swipe(-140, 0)
	r.end(true)
	if got := r.active(); got != 1 {
		t.Fatalf("a cancelled swipe ended on workspace %d", got)
	}
	r.settle()
	r.h.Eventually("back in place", showing(155, 30, swipeRed))
}

func TestASwipeRubberBandsAtTheEnds(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		r := startSwipeRig(t, start, anim.Style{Kind: anim.Slide}, nil)
		r.redBlue()
		r.begin()
		for range 20 {
			r.swipe(20, 0)
		}
		bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
		r.h.Eventually("some of the way against the resistance", both(showing(5, 30, bg), showing(100, 30, swipeRed)))
		r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			if got := pixel(snapshot(b.Outputs()[0]), 40, 30); got != swipeRed {
				t.Errorf("two whole workspaces of travel moved the window past a fifth of the output, showing %v", got)
			}
			return nil
		})
		r.rest()
		r.end(false)
		if got := r.active(); got != 1 {
			t.Fatalf("the first workspace rubber-banded to %d", got)
		}
		r.settle()
		r.h.Eventually("sprung back", showing(5, 30, swipeRed))
		r.expectNoMoreFrames()
	})
}

func TestASwipeAcrossAWholeWorkspaceWalksOn(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, nil)
	r.redBlue()
	r.workspace(3, "3", func(c *testclient.Client) *testclient.Window { return c.Window("green", testclient.Fill(0, 0xff, 0)) })
	r.h.dispatch("workspace 1")
	r.settle()
	r.h.Eventually("red", showing(80, 30, swipeRed))
	r.begin()
	r.swipe(-200, 0)
	r.h.Eventually("the second and third workspaces", both(showing(50, 30, swipeBlue), showing(150, 30, swipeGreen)))
	if got := r.active(); got != 2 {
		t.Fatalf("crossing the second workspace left %d active", got)
	}
	r.swipe(-40, 0)
	r.rest()
	r.end(false)
	if got := r.active(); got != 3 {
		t.Fatalf("ended on %d", got)
	}
}

func TestANewSwipeGrabsASettlingOneWhereItIs(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		r := startSwipeRig(t, start, anim.Style{Kind: anim.Slide}, nil)
		r.redBlue()
		r.begin()
		r.swipe(-100, 0)
		r.rest()
		r.end(false)
		r.at(*r.now + 50*time.Millisecond)
		r.h.Eventually("the settle under way", both(showing(5, 30, swipeRed), showing(100, 30, swipeBlue)))
		var held *[3]byte
		r.begin()
		r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			got := pixel(snapshot(b.Outputs()[0]), 5, 30)
			held = &got
			return nil
		})
		r.at(*r.now + time.Hour)
		r.h.Eventually("held there", showing(5, 30, *held))
		r.expectNoMoreFrames()
		for range 6 {
			r.swipe(20, 0)
		}
		r.h.Eventually("the fingers bringing the first workspace back", showing(40, 30, swipeRed))
		r.rest()
		r.end(false)
		if got := r.active(); got != 1 {
			t.Fatalf("swiping back mid-settle ended on workspace %d", got)
		}
		r.settle()
		r.h.Eventually("the first workspace in place", showing(155, 30, swipeRed))
		r.expectNoMoreFrames()
	})
}

func TestASwipeThatGrabsThenGoesNowhereSettlesWhereItWasGoing(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, nil)
	r.redBlue()
	r.begin()
	r.swipe(-100, 0)
	r.rest()
	r.end(false)
	r.at(*r.now + 50*time.Millisecond)
	r.begin()
	r.end(false)
	if got := r.active(); got != 2 {
		t.Fatalf("on workspace %d", got)
	}
	r.settle()
	r.h.Eventually("the second workspace in place", showing(5, 30, swipeBlue))
	r.expectNoMoreFrames()
}

func TestASwipeRespectsNaturalScrollOff(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, func(cfg *compositor.Config) { cfg.Gestures.Natural = false })
	r.redBlue()
	r.begin()
	r.swipe(100, 0)
	r.h.Eventually("fingers right moving the content left", showing(100, 30, swipeBlue))
	r.rest()
	r.end(false)
	if got := r.active(); got != 2 {
		t.Fatalf("on workspace %d", got)
	}
}

func TestASwipeDrivesSlidevert(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		r := startSwipeRig(t, start, anim.Style{Kind: anim.SlideVert}, func(cfg *compositor.Config) {
			cfg.Gestures.Binds = compositor.DefaultGestureBinds(anim.Style{Kind: anim.SlideVert})
			cfg.Gestures.Distance = float64(tall.Size.Y)
		})
		r.redBlue()
		r.begin()
		r.swipe(0, -30)
		r.h.Eventually("both workspaces a quarter up", both(showing(20, 80, swipeRed), showing(20, 100, swipeBlue)))
		r.swipe(0, -50)
		r.rest()
		r.end(false)
		if got := r.active(); got != 2 {
			t.Fatalf("on workspace %d", got)
		}
		r.settle()
		r.h.Eventually("the second workspace in place", showing(20, 5, swipeBlue))
		r.expectNoMoreFrames()
	})
}

func TestAKeySwitchWhileTrackingEndsTheGesture(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, nil)
	r.redBlue()
	r.workspace(3, "3", func(c *testclient.Client) *testclient.Window { return c.Window("green", testclient.Fill(0, 0xff, 0)) })
	r.h.dispatch("workspace 1")
	r.settle()
	r.h.Eventually("red", showing(80, 30, swipeRed))
	r.begin()
	r.swipe(-40, 0)
	r.h.Eventually("tracking", showing(130, 30, swipeBlue))
	r.h.dispatch("workspace 3")
	r.settle()
	r.h.Eventually("the third workspace in place", showing(80, 30, swipeGreen))
	r.expectNoMoreFrames()
	r.swipe(-500, 0)
	r.swipe(300, 0)
	r.end(false)
	if got := r.active(); got != 3 {
		t.Fatalf("the rest of the gesture moved to workspace %d", got)
	}
	r.h.Eventually("still the third workspace", showing(80, 30, swipeGreen))
	r.expectNoMoreFrames()
	r.h.dispatch("workspace 1")
	r.settle()
	r.h.Eventually("keys still work afterwards", showing(80, 30, swipeRed))
	r.begin()
	r.swipe(-100, 0)
	r.rest()
	r.end(false)
	if got := r.active(); got != 2 {
		t.Fatalf("the next swipe ended on %d", got)
	}
}

func TestAKeySwitchToTheWorkspaceBeingSwipedToCarriesOnFromThere(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, nil)
	r.redBlue()
	r.begin()
	r.swipe(-40, 0)
	r.h.Eventually("tracking", showing(130, 30, swipeBlue))
	r.h.dispatch("workspace 2")
	r.h.Eventually("still where the fingers left it", showing(130, 30, swipeBlue))
	r.settle()
	r.h.Eventually("the second workspace in place", showing(5, 30, swipeBlue))
	r.swipe(100, 0)
	r.end(false)
	r.h.Eventually("the late fingers ignored", showing(5, 30, swipeBlue))
}

func TestAKeyboardActionWhileTrackingRuns(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, nil)
	r.redBlue()
	r.begin()
	r.swipe(-40, 0)
	r.h.dispatch("togglefloating")
	r.swipe(-60, 0)
	r.rest()
	r.end(false)
	if got := r.active(); got != 2 {
		t.Fatalf("on %d", got)
	}
}

func TestAClaimedSwipeCancelsOnTheClient(t *testing.T) {
	h := Start(t, tall)
	c, log := gestureClient(t, h, "gestures")
	h.gesture(func(o *headless.Output, p *headless.Pointer) error {
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.GestureBegin(backend.Swipe, 3), p.Swipe(-2, 0), p.Swipe(-200, 0), p.Swipe(-50, 0), p.GestureEnd(backend.Swipe, false))
	})
	want := []string{"swipe begin 3", "swipe update -2 0", "swipe end 1"}
	c.DispatchUntil("the cancel", func() bool { return len(log.events) >= len(want) })
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(log.events, want) {
		t.Fatalf("events %q, want %q", log.events, want)
	}
}

func TestASwipeOnAnUnboundAxisStaysWithTheClient(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Gestures.Binds = []binds.GestureBind{{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeHorizontal, Drive: binds.DriveWorkspaces}}
	}, tall)
	c, log := gestureClient(t, h, "gestures")
	h.gesture(func(o *headless.Output, p *headless.Pointer) error {
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.GestureBegin(backend.Swipe, 3), p.Swipe(0, 50), p.Swipe(300, 0), p.GestureEnd(backend.Swipe, false))
	})
	want := []string{"swipe begin 3", "swipe update 0 50", "swipe update 300 0", "swipe end 0"}
	c.DispatchUntil("the swipe", func() bool { return len(log.events) >= len(want) })
	if !slices.Equal(log.events, want) {
		t.Fatalf("events %q, want %q", log.events, want)
	}
}

func TestGestureActionsRunAndStayFromTheClient(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Gestures.Binds = []binds.GestureBind{
			{Kind: backend.Pinch, Fingers: 2, Direction: binds.PinchIn, Action: wm.WorkspaceAction{Target: wm.WorkspaceID(2)}},
			{Kind: backend.Hold, Fingers: 4, Action: wm.WorkspaceAction{Target: wm.WorkspaceID(3)}},
			{Kind: backend.Swipe, Fingers: 4, Direction: binds.SwipeUp, Action: wm.WorkspaceAction{Target: wm.WorkspaceID(4)}},
		}
	}, tall)
	c, log := gestureClient(t, h, "gestures")
	h.gesture(func(o *headless.Output, p *headless.Pointer) error {
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.GestureBegin(backend.Pinch, 2), p.Pinch(0, 0, 0.96875, 0), p.Pinch(0, 0, 0.7, 0), p.Pinch(0, 0, 0.5, 0), p.GestureEnd(backend.Pinch, false))
	})
	h.Eventually("the pinch switching", showingWorkspace(2))
	want := []string{"pinch begin 2", "pinch update 0 0 0.96875 0", "pinch end 1"}
	c.DispatchUntil("the pinch cancelled", func() bool { return len(log.events) >= len(want) })
	if !slices.Equal(log.events, want) {
		t.Fatalf("events %q, want %q", log.events, want)
	}
	h.dispatch("workspace 1")
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error {
		return errors.Join(p.GestureBegin(backend.Hold, 4), p.GestureEnd(backend.Hold, false))
	})
	h.Eventually("the hold switching", showingWorkspace(3))
	h.dispatch("workspace 1")
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error {
		return errors.Join(p.GestureBegin(backend.Swipe, 4), p.Swipe(0, -150), p.GestureEnd(backend.Swipe, false))
	})
	h.Eventually("the swipe switching", showingWorkspace(4))
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if n := len(log.events); n != len(want)+2 {
		t.Fatalf("the client saw %q", log.events)
	}
}

func TestLockedGesturesStayWithClients(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Gestures.Binds = []binds.GestureBind{{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeHorizontal, Drive: binds.DriveWorkspaces}}
	}, tall)
	lockSession(t, h)
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error {
		return errors.Join(p.GestureBegin(backend.Swipe, 3), p.Swipe(-200, 0), p.GestureEnd(backend.Swipe, false))
	})
	h.Eventually("no switch", showingWorkspace(1))
}

func TestAHiddenTabIsNotDrawnWhileSwiping(t *testing.T) {
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, func(cfg *compositor.Config) { cfg.WM.Autotile.Enabled = false })
	red := r.c.Window("red", testclient.Fill(0, 0, 0xff))
	r.c.DispatchUntil("red to map", red.Mapped)
	r.h.dispatch("workspace 2")
	r.settle()
	green := r.c.Window("green", testclient.Fill(0, 0xff, 0))
	r.c.DispatchUntil("green to map", green.Mapped)
	r.h.dispatch("makegroup tab")
	blue := r.c.Window("blue", testclient.Fill(0xff, 0, 0))
	r.c.DispatchUntil("blue to map", blue.Mapped)
	r.c.DispatchUntil("blue in the tab", func() bool { return blue.Logical != image.Point{} })
	r.h.Eventually("the blue tab on top", showing(80, 30, swipeBlue))
	r.h.dispatch("focus left")
	r.h.Eventually("the green tab on top", showing(80, 30, swipeGreen))
	r.h.dispatch("workspace 1")
	r.settle()
	r.h.Eventually("red", showing(80, 30, swipeRed))
	r.begin()
	r.swipe(-80, 0)
	r.h.Eventually("only the focused tab peeking in", both(showing(40, 30, swipeRed), showing(120, 30, swipeGreen)))
}
