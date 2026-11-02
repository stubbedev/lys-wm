package harness

import (
	"errors"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestAClosingWindowWithItsMotionOffOnlyFades(t *testing.T) {
	d := 10 * time.Second
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{
		anim.Global:  {Enabled: &off},
		anim.FadeOut: {Enabled: &on, Shape: anim.Linear, Duration: &d},
	})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	now := time.Hour
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
		cfg.WM.Gaps = wm.Gaps{}
		cfg.Decor.BorderWidth = 0
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("closing", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window", showing(2, 2, [3]byte{0xff, 0xff, 0xff}))
	if err := errors.Join(w.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		now += d / 2
		return nil
	})
	img := h.Rendered(0)
	corner, inside := pixel(img, 2, 2), pixel(img, small.Size.X-30, small.Size.Y-30)
	if corner == previewBackground {
		t.Errorf("the corner of the window is gone at once, so it shrank: %v", corner)
	}
	if corner != inside {
		t.Errorf("corner %v and inside %v differ, so this is not a plain fade", corner, inside)
	}
}

func TestUnderReducedMotionAReleasedSwipeOnlyCrossFades(t *testing.T) {
	off := false
	table, errs := anim.ResolveReduced(map[anim.Event]anim.Setting{anim.Fade: {Enabled: &off}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	r := startSwipeRig(t, StartWith, anim.Style{Kind: anim.Slide}, func(cfg *compositor.Config) { cfg.Animations = table })
	r.redBlue()
	r.begin()
	for range 3 {
		r.swipe(-16, 0)
	}
	r.end(false)
	if got := r.active(); got != 2 {
		t.Fatalf("a flick left workspace %d showing", got)
	}
	last := swipeRed
	for step := 0; last != swipeBlue; step++ {
		if step > 400 {
			t.Fatalf("still at %v after %d frames", last, step)
		}
		r.at(*r.now + 8*time.Millisecond)
		img := r.h.Rendered(0)
		left, right := pixel(img, 5, 30), pixel(img, tall.Size.X-5, 30)
		if left != right {
			t.Fatalf("frame %d: left %v and right %v differ, so the workspaces travel", step, left, right)
		}
		if left[0] > last[0] || left[2] < last[2] {
			t.Fatalf("frame %d: %v after %v, so the fade turned back or overshot", step, left, last)
		}
		last = left
	}
	r.settle()
	r.h.Rendered(0)
	r.expectNoMoreFrames()
}
