package harness

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	ccs "github.com/stubbedev/lys-wm/internal/protocol/client/cursorshape"
)

func cursorState(r *inputRig) (hidden, visible bool, commits uint64) {
	r.h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		cur := b.Outputs()[0].Cursor()
		hidden, visible, commits = c.CursorHidden(), cur.Visible(), cur.Commits()
		return nil
	})
	return hidden, visible, commits
}

func TestTypingHidesTheCursorAndMotionShowsItWithOnePlaneCommit(t *testing.T) {
	t.Parallel()
	const keyA, keyShift = 30, 42
	r := inputOn(t, StartWithCursorPlanes(t, plane, func(c *compositor.Config) {
		c.CursorBehavior.HideWhileTyping = true
	}, small))
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.h.Eventually("the arrow on the cursor plane", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Cursor().Visible()
	})
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Keyboard().Key(keyShift, true), b.Keyboard().Key(keyShift, false))
	})
	r.expect("key 42 1", "key 42 0")
	if hidden, visible, _ := cursorState(r); hidden || !visible {
		t.Fatalf("a lone modifier hid the cursor: hidden %v, plane visible %v", hidden, visible)
	}
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Keyboard().Key(keyA, true), b.Keyboard().Key(keyA, false))
	})
	r.expect("key 30 1", "key 30 0")
	hidden, visible, before := cursorState(r)
	if !hidden || visible {
		t.Fatalf("typing left the cursor up: hidden %v, plane visible %v", hidden, visible)
	}
	device, err := r.c.CursorShape.GetPointer(r.in.Pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err := device.SetShape(r.in.Serial, ccs.WpCursorShapeDeviceV1ShapeText); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	r.h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if c.Seat().Cursor() == nil {
			return errors.New("the shape did not land")
		}
		return nil
	})
	if hidden, visible, commits := cursorState(r); !hidden || visible || commits != before {
		t.Fatalf("a new shape while hidden: hidden %v, plane visible %v, %d plane commits", hidden, visible, commits-before)
	}
	r.moveTo(40, 50)
	r.expect("motion 40,50", "frame")
	if hidden, visible, commits := cursorState(r); hidden || !visible || commits != before+1 {
		t.Fatalf("motion after typing: hidden %v, plane visible %v, %d plane commits, want 1", hidden, visible, commits-before)
	}
}

func TestHideAfterWaitsForTheClockAndNeverHidesWhileAButtonIsHeld(t *testing.T) {
	t.Parallel()
	now := time.Hour
	h := StartWith(t, func(c *compositor.Config) {
		c.Clock = func() time.Duration { return now }
		c.CursorBehavior.HideAfter = time.Millisecond
	}, small)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	advance := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now += d
			return nil
		})
	}
	checked := func(what string) {
		t.Helper()
		var from uint64
		h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
			from = c.CursorIdleChecks()
			return nil
		})
		h.Eventually(what, func(_ *headless.Backend, c *compositor.Compositor) bool { return c.CursorIdleChecks() >= from+2 })
		h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
			if c.CursorHidden() {
				return fmt.Errorf("%s: the cursor hid", what)
			}
			return nil
		})
	}
	r.moveTo(40, 50)
	r.expect("motion 40,50", "frame")
	checked("idle checks before the clock moves")
	advance(time.Millisecond)
	h.Eventually("the cursor hidden once hide_after passed", func(_ *headless.Backend, c *compositor.Compositor) bool { return c.CursorHidden() })
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame())
	})
	r.expect("button 272 1", "frame")
	advance(time.Hour)
	checked("idle checks while the button is held")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Button(btnLeft, false), b.Pointer().Frame())
	})
	r.expect("button 272 0", "frame")
	checked("idle checks right after the release")
	advance(time.Millisecond)
	h.Eventually("the cursor hidden after the release", func(_ *headless.Backend, c *compositor.Compositor) bool { return c.CursorHidden() })
}

func TestAShakeGrowsTheCursorAndAStraightDashDoesNot(t *testing.T) {
	t.Parallel()
	now := time.Hour
	h := StartWith(t, func(c *compositor.Config) {
		c.Clock = func() time.Duration { return now }
		c.CursorBehavior.ShakeToFind = true
	}, small, small)
	growthAfter := func(d time.Duration) (g float64) {
		h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
			now += d
			g = c.CursorGrowth()
			return nil
		})
		return g
	}
	move := func(dxs ...float64) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			for _, dx := range dxs {
				if err := errors.Join(b.Pointer().Move(dx, 0), b.Pointer().Frame()); err != nil {
					return err
				}
			}
			return nil
		})
	}
	move(-1000)
	move(40, 40, 40, 40, 40, 40, 40, 40)
	if g := growthAfter(100 * time.Millisecond); g != 1 {
		t.Fatalf("a straight dash grew the cursor to %g", g)
	}
	move(-200, -20, 20, -20, 20, -20, 20)
	if g := growthAfter(100 * time.Millisecond); g != 1 {
		t.Fatalf("a jitter too small to be a shake grew the cursor to %g", g)
	}
	move(-60, 60, -60, 60, -60)
	if g := growthAfter(150 * time.Millisecond); g <= 1.5 {
		t.Fatalf("a shake grew the cursor only to %g", g)
	}
	h.Eventually("the grown cursor drawn larger than the arrow", func(b *headless.Backend, c *compositor.Compositor) bool {
		snap := snapshot(b.Outputs()[0])
		x, y := c.Cursor()
		tip := [2]int{int(x), int(y)}
		return tip[0]+30 < snap.W && pixel(snap, tip[0]+2, tip[1]+30) != [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	})
	h.Eventually("the cursor settled back to its size", func(_ *headless.Backend, c *compositor.Compositor) bool {
		now += time.Second
		return c.CursorGrowth() == 1 && !c.Animating(c.Outputs()[0])
	})
}
