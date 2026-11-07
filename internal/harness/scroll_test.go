package harness

import (
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

func scrolled(cfg *compositor.Config) {
	cfg.WM.Layout = wm.ScrollLayout
	cfg.WM.Gaps = wm.Gaps{}
	cfg.Decor = compositor.PlainDecor()
	cfg.Decor.BorderWidth = 0
}

type strip struct {
	h       *Harness
	c       *testclient.Client
	windows []*testclient.Window
}

func openStrip(t *testing.T, h *Harness) strip {
	t.Helper()
	s := strip{h: h, c: testclient.Connect(t, h.Socket())}
	for _, w := range []struct {
		title string
		paint testclient.Painter
	}{{"red", testclient.Fill(0, 0, 0xff)}, {"blue", testclient.Fill(0xff, 0, 0)}, {"green", testclient.Fill(0, 0xff, 0)}} {
		win := s.c.Window(w.title, w.paint)
		s.c.DispatchUntil(w.title+" to map", win.Mapped)
		s.windows = append(s.windows, win)
	}
	s.settle()
	return s
}

func (s strip) settle() {
	s.h.t.Helper()
	for _, w := range s.windows {
		s.c.DispatchUntil("a window to answer its configure", func() bool { return w.Logical == w.Size })
	}
	s.c.Sync()
	s.h.Eventually("the change to land", func(_ *headless.Backend, comp *compositor.Compositor) bool { return !comp.Holding() })
}

func (s strip) configures() [3]int {
	return [3]int{s.windows[0].Configures, s.windows[1].Configures, s.windows[2].Configures}
}

func TestAnOffScreenColumnGetsFramesAtTheHiddenRateAndScrollingConfiguresNothing(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		scrolled(cfg)
		cfg.HiddenFrame = hiddenFrame
	}, tall)
	s := openStrip(t, h)
	h.Eventually("blue and green in view, red scrolled off", both(showing(20, 60, swipeBlue), showing(140, 60, swipeGreen)))
	if s.windows[0].Size != s.windows[1].Size {
		t.Fatalf("the columns are %v and %v, want equal widths", s.windows[0].Size, s.windows[1].Size)
	}
	pump(s.c, 200*time.Millisecond)
	from := s.windows[0].Frames
	pump(s.c, hiddenWindow)
	atTheHiddenRate(t, "the column scrolled off", s.windows[0].Frames-from)

	before := s.configures()
	h.ctl("center")
	h.Eventually("green in the middle", both(showing(60, 60, swipeGreen), showing(20, 60, swipeBlue)))
	s.settle()
	h.ctl("center")
	if after := s.configures(); after != before {
		t.Fatalf("scrolling the view sent configures: %v then %v", before, after)
	}
}

func TestAViewSwipeTracksTheFingersAndSettlesOnAColumnEdge(t *testing.T) {
	t.Parallel()
	now := time.Hour
	off, on := false, true
	spring, err := anim.NewSpring(300, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.WindowsMove: {Enabled: &on, Shape: spring}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	r := &swipeRig{t: t, now: &now, events: 5 * time.Hour}
	r.h = StartWith(t, func(cfg *compositor.Config) {
		scrolled(cfg)
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return *r.now }
		cfg.Gestures.Distance = float64(tall.Size.X)
		cfg.Gestures.Binds = []binds.GestureBind{{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeHorizontal, Drive: binds.DriveView}}
	}, tall)
	s := openStrip(t, r.h)
	r.c = s.c
	r.h.ctl("focuswindow " + idOf(r.h, "red"))
	r.settle()
	r.h.Eventually("red and blue in view", both(showing(20, 60, swipeRed), showing(140, 60, swipeBlue)))
	s.settle()
	before := s.configures()

	r.begin()
	r.swipe(-60, 0)
	r.h.Eventually("the strip moved with the fingers at once", both(showing(10, 60, swipeRed), showing(50, 60, swipeBlue), showing(150, 60, swipeGreen)))
	if after := s.configures(); after != before {
		t.Fatalf("tracking the fingers sent configures: %v then %v", before, after)
	}
	r.rest()
	r.end(false)
	r.h.Eventually("the spring starting where the fingers left the strip", showing(10, 60, swipeRed))
	r.settle()
	r.h.Eventually("blue settled on the left edge, green beside it", both(showing(5, 60, swipeBlue), showing(150, 60, swipeGreen)))
	if w, _ := windowTitled(r.h, "blue"); !w.Focused {
		t.Fatalf("the column the view settled on is not focused: %+v", w)
	}
	r.expectNoMoreFrames()
}

func TestAFixedPixelColumnWidthConfiguresTheClientToThosePixels(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		scrolled(cfg)
		cfg.WM.Scroll.Presets = []wm.ColumnWidth{wm.Share(0.5), wm.Pixels(40)}
	}, tall)
	s := openStrip(t, h)
	green := s.windows[2]
	for _, step := range []struct {
		action string
		width  int
	}{
		{"columnwidth 100px", 100},
		{"columnwidth -30px", 70},
		{"columnwidth preset next", 80},
		{"columnwidth preset next", 40},
	} {
		h.ctl(step.action)
		s.c.DispatchUntil(step.action+" to configure green", func() bool { return green.Size.X == step.width })
		s.settle()
	}
}
