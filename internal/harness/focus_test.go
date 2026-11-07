package harness

import (
	"errors"
	"image"
	"regexp"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cpc "github.com/stubbedev/lys-wm/internal/protocol/client/pointerconstraints"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type focusRig struct {
	t *testing.T
	h *Harness
	c *testclient.Client
}

func startFocus(t *testing.T, focus wm.FocusSettings, configure ...func(*compositor.Config)) *focusRig {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.WM.Focus = focus
		withBinds(t, bindSpec{chord: "SUPER + H", action: "focus left"}, bindSpec{chord: "SUPER + L", action: "focus right"})(cfg)
		for _, f := range configure {
			f(cfg)
		}
	}, small)
	return &focusRig{t: t, h: h, c: testclient.Connect(t, h.Socket())}
}

func (r *focusRig) window(title string) *testclient.Window {
	r.t.Helper()
	w := r.c.Window(title, quadrants)
	r.c.DispatchUntil(title+" to map", w.Mapped)
	return w
}

func (r *focusRig) windows() map[string]ipc.Window {
	out := map[string]ipc.Window{}
	r.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		for _, w := range comp.Snapshot().Windows {
			out[w.Title] = w
		}
		return nil
	})
	return out
}

func (r *focusRig) rect(title string) image.Rectangle {
	r.t.Helper()
	w, ok := r.windows()[title]
	if !ok {
		r.t.Fatalf("no window %q", title)
	}
	return image.Rect(w.Rect.X, w.Rect.Y, w.Rect.X+w.Rect.Width, w.Rect.Y+w.Rect.Height)
}

func (r *focusRig) focused() string {
	for title, w := range r.windows() {
		if w.Focused {
			return title
		}
	}
	return ""
}

func (r *focusRig) wantFocus(title, why string) {
	r.t.Helper()
	r.h.Eventually(title+" focused "+why, func(_ *headless.Backend, comp *compositor.Compositor) bool {
		for _, w := range comp.Snapshot().Windows {
			if w.Focused {
				return w.Title == title
			}
		}
		return false
	})
}

func (r *focusRig) keepFocus(title, why string) {
	r.t.Helper()
	if got := r.focused(); got != title {
		r.t.Fatalf("%s: %s is focused, want %s", why, got, title)
	}
}

func (r *focusRig) moveTo(p image.Point) {
	r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		return errors.Join(b.Pointer().MoveTo(o, float64(p.X)/float64(small.Size.X), float64(p.Y)/float64(small.Size.Y)), b.Pointer().Frame())
	})
}

func (r *focusRig) chord(key uint32) {
	r.h.key(keySuper, true)
	r.h.tap(key)
	r.h.key(keySuper, false)
}

func (r *focusRig) updates() uint64 {
	var n uint64
	r.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		n = comp.StateUpdates()
		return nil
	})
	return n
}

func (r *focusRig) cursor() image.Point {
	var p image.Point
	r.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		x, y := comp.Cursor()
		p = image.Pt(int(x), int(y))
		return nil
	})
	return p
}

func (r *focusRig) warps() uint64 {
	var n uint64
	r.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		n = comp.PointerWarps()
		return nil
	})
	return n
}

func (r *focusRig) motionIsFree(why string) {
	r.t.Helper()
	before := r.updates()
	r.h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		motions := [2]backend.Event{&backend.PointerMotion{DX: 1, DY: 1}, &backend.PointerMotion{DX: -1, DY: -1}}
		var frame backend.Event = &backend.PointerFrame{}
		next := 0
		move := func() {
			if err := errors.Join(comp.Input(b.Pointer(), motions[next]), comp.Input(b.Pointer(), frame)); err != nil {
				r.t.Fatal(err)
			}
			next ^= 1
		}
		for range 20 {
			move()
		}
		if n := allocsPerRun(50, func() {}, move); n != 0 {
			r.t.Errorf("%s: a pointer motion allocated %d times", why, n)
		}
		return nil
	})
	if n := r.updates() - before; n != 0 {
		r.t.Errorf("%s: 140 pointer motions inside one window made %d window manager updates", why, n)
	}
}

func TestFocusFollowsMouseModes(t *testing.T) {
	t.Parallel()
	t.Run("off", func(t *testing.T) {
		t.Parallel()
		r := startFocus(t, wm.FocusSettings{FollowsMouse: wm.FollowsMouseOff})
		r.window("a")
		r.window("b")
		r.wantFocus("b", "after it opened")
		r.moveTo(centre(r.rect("a")))
		r.motionIsFree("pointer over a with focus on b")
		r.keepFocus("b", "the pointer moved over a")
		p := centre(r.rect("a"))
		r.h.click(0, float64(p.X), float64(p.Y), btnLeft)
		r.wantFocus("a", "after a click on it")
	})
	t.Run("loose", func(t *testing.T) {
		t.Parallel()
		r := startFocus(t, wm.FocusSettings{FollowsMouse: wm.FollowsMouseLoose})
		r.window("a")
		r.window("b")
		r.moveTo(centre(r.rect("a")))
		r.wantFocus("a", "once the pointer entered it")
		r.chord(keyL)
		r.wantFocus("b", "after focus right")
		r.moveTo(centre(r.rect("a")).Add(image.Pt(3, 3)))
		r.motionIsFree("pointer inside a after focus right")
		r.keepFocus("b", "the pointer moved inside a after focus right")
		r.moveTo(centre(r.rect("b")))
		r.wantFocus("b", "with the pointer over it")
		r.moveTo(centre(r.rect("a")))
		r.wantFocus("a", "once the pointer crossed into it again")
		r.motionIsFree("pointer inside the focused window")
	})
	t.Run("always", func(t *testing.T) {
		t.Parallel()
		r := startFocus(t, wm.FocusSettings{FollowsMouse: wm.FollowsMouseAlways})
		r.window("a")
		r.window("b")
		r.moveTo(centre(r.rect("a")))
		r.wantFocus("a", "once the pointer entered it")
		r.chord(keyL)
		r.wantFocus("b", "after focus right")
		r.moveTo(centre(r.rect("a")).Add(image.Pt(3, 3)))
		r.wantFocus("a", "as soon as the pointer moved over it")
		r.motionIsFree("pointer inside the focused window")
	})
}

func floatsAt(rects map[string]image.Rectangle) func(*compositor.Config) {
	return func(cfg *compositor.Config) {
		for title, r := range rects {
			cfg.Rules = append(cfg.Rules, wm.Rule{
				Match:    wm.Match{Title: regexp.MustCompile("^" + title + "$")},
				Mode:     wm.RuleFloat,
				Size:     wm.Px(r.Dx(), r.Dy()),
				Position: wm.Position{Kind: wm.PositionAt, X: wm.Length{N: r.Min.X}, Y: wm.Length{N: r.Min.Y}},
			})
		}
	}
}

func (r *focusRig) stacks() map[string]int {
	out := map[string]int{}
	for title, w := range r.windows() {
		if w.Stack != nil {
			out[title] = *w.Stack
		}
	}
	return out
}

func TestHoveringAFloatingWindowFocusesItWithoutRaising(t *testing.T) {
	t.Parallel()
	r := startFocus(t, wm.FocusSettings{}, floatsAt(map[string]image.Rectangle{
		"low":  image.Rect(10, 10, 70, 50),
		"high": image.Rect(40, 30, 100, 70),
	}))
	r.window("tiled")
	r.window("low")
	r.window("high")
	low, high := r.rect("low"), r.rect("high")
	if !low.Overlaps(high) || low.Min.In(high) {
		t.Fatalf("low %v and high %v must overlap with low's corner bare", low, high)
	}
	before := r.stacks()
	if before["high"] >= before["low"] {
		t.Fatalf("the newer floating window is not on top: %v", before)
	}
	corner := low.Min.Add(image.Pt(3, 3))
	r.moveTo(corner)
	r.wantFocus("low", "under the pointer")
	if after := r.stacks(); after["high"] != before["high"] || after["low"] != before["low"] {
		t.Fatalf("hovering low restacked %v into %v", before, after)
	}
	r.h.click(0, float64(corner.X), float64(corner.Y), btnLeft)
	r.h.Eventually("low raised by the click", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		s := map[string]int{}
		for _, w := range comp.Snapshot().Windows {
			if w.Stack != nil {
				s[w.Title] = *w.Stack
			}
		}
		return s["low"] < s["high"]
	})
}

func TestRaiseOnHoverRaisesAFloatingWindow(t *testing.T) {
	t.Parallel()
	r := startFocus(t, wm.FocusSettings{RaiseOnHover: true}, floatsAt(map[string]image.Rectangle{
		"low":  image.Rect(10, 10, 70, 50),
		"high": image.Rect(40, 30, 100, 70),
	}))
	r.window("low")
	r.window("high")
	r.moveTo(r.rect("low").Min.Add(image.Pt(3, 3)))
	r.wantFocus("low", "under the pointer")
	if s := r.stacks(); s["low"] > s["high"] {
		t.Fatalf("raise_on_hover left low under high: %v", s)
	}
}

func TestKeyboardFocusWarpsThePointerOncePerChange(t *testing.T) {
	t.Parallel()
	r := startFocus(t, wm.FocusSettings{WarpPointer: wm.WarpOnKeyboardFocus})
	_, in := r.c.Seat()
	a := r.window("a")
	r.window("b")
	ra, rb := r.rect("a"), r.rect("b")
	r.moveTo(ra.Min.Add(image.Pt(2, 2)))
	r.wantFocus("a", "under the pointer")
	if n := r.warps(); n != 0 {
		t.Fatalf("pointer focus warped the pointer %d times", n)
	}
	r.chord(keyL)
	r.wantFocus("b", "after focus right")
	if n, at := r.warps(), r.cursor(); n != 1 || at != centre(rb) {
		t.Fatalf("focus right: %d warps, cursor at %v, want 1 to %v", n, at, centre(rb))
	}
	r.chord(keyL)
	r.keepFocus("b", "focus right at the edge")
	if n := r.warps(); n != 1 {
		t.Fatalf("a focus action that moved no focus warped: %d warps", n)
	}
	r.chord(keyH)
	r.wantFocus("a", "after focus left")
	if n, at := r.warps(), r.cursor(); n != 2 || at != centre(ra) {
		t.Fatalf("focus left: %d warps, cursor at %v, want 2 to %v", n, at, centre(ra))
	}

	r.moveTo(centre(ra).Add(image.Pt(1, 1)))
	m, err := cpc.NewZwpPointerConstraintsV1(r.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.c.Bind(m)
	l := &constraintLog{}
	if _, err := m.LockPointer(l, a.Surface, in.Pointer, nil, cpc.ZwpPointerConstraintsV1LifetimePersistent); err != nil {
		t.Fatal(err)
	}
	r.c.DispatchUntil("the pointer to lock", func() bool { return len(l.events) == 1 })
	held := r.cursor()
	r.chord(keyL)
	r.wantFocus("b", "after focus right with the pointer locked")
	if n, at := r.warps(), r.cursor(); n != 2 || at != held {
		t.Fatalf("focus right under a pointer lock: %d warps, cursor at %v, want 2 and %v", n, at, held)
	}
}

func TestClosingTheFocusedWindowFocusesTheOneUnderThePointer(t *testing.T) {
	t.Parallel()
	r := startFocus(t, wm.FocusSettings{FollowsMouse: wm.FollowsMouseOff, OnClose: wm.CloseFocusesUnderPointer})
	r.window("a")
	r.window("b")
	c := r.window("c")
	r.moveTo(centre(r.rect("a")))
	r.wantFocus("c", "with hover focus off")
	if err := errors.Join(c.Toplevel.Destroy(), c.Surface.Destroy()); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	r.wantFocus("a", "under the pointer once c closed")
}
