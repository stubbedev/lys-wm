package harness

import (
	"errors"
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func linearMoves(t *testing.T, d time.Duration) anim.Table {
	t.Helper()
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.WindowsMove: {Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return table
}

func TestAMovedWindowAnimatesOnTheFrameClock(t *testing.T) {
	now := time.Hour
	var next compositor.Config
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.DefaultDecor()
		cfg.Animations = linearMoves(t, time.Second)
		cfg.Clock = func() time.Duration { return now }
		next = *cfg
		next.WM.Gaps = wm.Gaps{Outer: wm.Insets{Top: 20, Right: 20, Bottom: 20, Left: 20}}
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = time.Hour + d
			return nil
		})
	}
	c := testclient.Connect(t, h.Socket())
	gray := [3]byte{0x60, 0x60, 0x60}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	w := c.Window("moved", testclient.Fill(gray[0], gray[1], gray[2]))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window over the whole output", showing(5, 5, gray))

	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(next) })
	c.DispatchUntil("the goal size, configured at once", func() bool { return w.Logical == image.Pt(118, 78) })
	h.Eventually("the window still covering its start", showing(5, 5, gray))

	at(500 * time.Millisecond)
	h.Eventually("the window halfway in", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(5, 5, bg)(b, comp) && showing(12, 12, gray)(b, comp)
	})
	var damage image.Rectangle
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		for _, r := range b.Outputs()[0].LastDamage() {
			damage = damage.Union(r)
		}
		return nil
	})
	if damage.Empty() || !damage.In(image.Rect(0, 0, 160, 120)) || !image.Pt(5, 5).In(damage) {
		t.Fatalf("the halfway frame repainted %v, want the strip the window left", damage)
	}

	at(time.Second)
	h.Eventually("the window at its goal", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(12, 12, bg)(b, comp) && showing(22, 22, gray)(b, comp)
	})
	var before uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		before = b.Outputs()[0].Frames()
		return nil
	})
	time.Sleep(200 * time.Millisecond)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if after := b.Outputs()[0].Frames(); after != before {
			t.Errorf("%d frames ran after the animation ended", after-before)
		}
		return nil
	})
}

func TestADraggedWindowFollowsThePointerWithoutAnimating(t *testing.T) {
	now := time.Hour
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = linearMoves(t, time.Hour)
		cfg.Clock = func() time.Duration { return now }
		withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"})(cfg)
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("dragged", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("a floating window", func() bool { return w.Logical == image.Pt(80, 50) })
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		now = 3 * time.Hour
		return nil
	})
	h.Eventually("the floating window settled in the middle", showing(45, 30, blueColor))
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.Button(btnLeft, true), p.Frame(), p.MoveTo(o, 100.0/160, 0.6), p.Frame())
	})
	h.Eventually("the window under the pointer while the clock stands still", showing(135, 80, blueColor))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Button(btnLeft, false), p.Frame())
	})
	h.key(keySuper, false)
}
