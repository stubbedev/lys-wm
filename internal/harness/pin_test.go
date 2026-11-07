package harness

import (
	"fmt"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type pinRun struct {
	rest, mid int
	pipAt     [3]byte
	stayed    bool
}

func runPinnedSwitch(t *testing.T, withPip bool) pinRun {
	t.Helper()
	now := time.Hour
	d := time.Second
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Workspaces: {Enabled: &on, Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
		cfg.Decor.BorderWidth = 0
		cfg.WM.Gaps = wm.Gaps{}
		cfg.Rules = []wm.Rule{{Match: wm.Match{Title: regexp.MustCompile(`^pip$`)}, Mode: wm.RuleFloat, Size: wm.Px(40, 30)}}
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	c := testclient.Connect(t, h.Socket())
	r := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", r.Mapped)
	if withPip {
		p := c.Window("pip", testclient.Fill(0, 0xff, 0))
		c.DispatchUntil("pip to map", p.Mapped)
		h.dispatch("pin")
		h.settled(c, "pip pinned", func(w ipc.Window) bool { return w.Pinned }, "pip")
	}
	h.dispatch("focuswindow 1")
	h.Eventually("red drawn", showing(10, 10, [3]byte{0xff, 0, 0}))
	var run pinRun
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		run.rest, _ = comp.FrameCost(comp.Outputs()[0])
		return nil
	})
	at(2 * time.Hour)
	h.dispatch("workspace 2")
	at(2*time.Hour + 500*time.Millisecond)
	h.Eventually("the switch halfway", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(10, 10, [3]byte{0xff, 0, 0})(b, comp) && showing(120, 10, [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B})(b, comp)
	})
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		run.mid, _ = comp.FrameCost(comp.Outputs()[0])
		run.pipAt = pixel(snapshot(b.Outputs()[0]), 70, 50)
		return nil
	})
	at(3 * time.Hour)
	h.Eventually("workspace 2 shown", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		s := comp.WM()
		shown, _ := s.ActiveOn(s.FocusedOutput())
		return shown == 2 && !comp.Animating(comp.Outputs()[0])
	})
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		run.stayed = pixel(snapshot(b.Outputs()[0]), 70, 50) == [3]byte{0, 0xff, 0}
		if withPip {
			w, _ := windowNamed(comp, "pip")
			run.stayed = run.stayed && w.Workspace == 2 && !w.Focused
		}
		return nil
	})
	return run
}

func TestAPinnedWindowStaysPutThroughAWorkspaceSwitchAndCostsOnlyItsOwnDraw(t *testing.T) {
	t.Parallel()
	plain, pinned := runPinnedSwitch(t, false), runPinnedSwitch(t, true)
	green := [3]byte{0, 0xff, 0}
	if pinned.pipAt != green || !pinned.stayed {
		t.Fatalf("the pinned window mid-switch shows %v, and after it stayed %t; want it green in place, on workspace 2, unfocused", pinned.pipAt, pinned.stayed)
	}
	if own := pinned.rest - plain.rest; pinned.mid-plain.mid != own {
		t.Fatalf("mid-switch the pinned window adds %d draws, at rest %d", pinned.mid-plain.mid, own)
	}
}

func TestAPinnedWindowStaysPutWhileASwipeMovesTheWorkspaces(t *testing.T) {
	t.Parallel()
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, func(cfg *compositor.Config) {
		cfg.Rules = []wm.Rule{{Match: wm.Match{Title: regexp.MustCompile(`^pip$`)}, Mode: wm.RuleFloat, Size: wm.Px(40, 30)}}
	})
	r.redBlue()
	pip := r.c.Window("pip", testclient.Fill(0, 0xff, 0))
	r.c.DispatchUntil("pip to map", pip.Mapped)
	r.h.dispatch("pin")
	green := [3]byte{0, 0xff, 0}
	r.h.Eventually("pip drawn at rest", showing(70, 50, green))
	r.begin()
	r.swipe(-80, 0)
	r.h.Eventually("the workspaces halfway with pip in place", both(showing(40, 30, swipeRed), showing(120, 30, swipeBlue), showing(70, 50, green)))
	r.end(false)
	r.settle()
	r.h.Eventually("workspace 2 shown with pip", both(showingWorkspace(2), showing(70, 50, green)))
}

func TestAPinnedWindowShowsOnEveryOverviewThumbnailOfItsOutput(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Rules = []wm.Rule{{Match: wm.Match{Title: regexp.MustCompile(`^pip$`)}, Mode: wm.RuleFloat, Size: wm.Px(40, 30)}}
	}, tall)
	c := testclient.Connect(t, h.Socket())
	r := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", r.Mapped)
	p := c.Window("pip", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("pip to map", p.Mapped)
	h.dispatch("pin")
	h.settled(c, "pip pinned", func(w ipc.Window) bool { return w.Pinned }, "pip")
	h.dispatch("workspace 2")
	b := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", b.Mapped)
	h.dispatch("workspace 1")
	h.dispatch("overview show")
	ids := map[string]wm.WindowID{}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		for _, name := range []string{"red", "pip", "blue"} {
			w, ok := windowNamed(comp, name)
			if !ok {
				return fmt.Errorf("no window %s", name)
			}
			ids[name] = w.ID
		}
		return nil
	})
	want := map[wm.WorkspaceID][]wm.WindowID{1: {ids["red"], ids["pip"]}, 2: {ids["blue"], ids["pip"]}}
	h.Eventually("the pinned window on both thumbnails", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		o := comp.Outputs()[0]
		for id, members := range want {
			if !slices.Equal(comp.OverviewThumbWindows(o, id), members) {
				return false
			}
		}
		return true
	})
}
