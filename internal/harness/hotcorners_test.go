package harness

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cpc "github.com/stubbedev/lys-wm/internal/protocol/client/pointerconstraints"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func withHotCorner(t testing.TB, k compositor.Corner, action string, mods binds.ModifierSet) func(*compositor.Config) {
	return func(cfg *compositor.Config) {
		a, err := wm.ParseAction(action)
		if err != nil {
			t.Fatal(err)
		}
		cfg.HotCorners.Corners[k] = compositor.HotCorner{Action: a, Modifiers: mods}
		if cfg.HotCorners.Corners, err = cfg.HotCorners.Corners.Resolve(cfg.Seat.Keymap); err != nil {
			t.Fatal(err)
		}
	}
}

func overviewCorner(t testing.TB) func(*compositor.Config) {
	return withHotCorner(t, compositor.TopLeft, "overview toggle", 0)
}

func (h *Harness) pointAt(output int, x, y float64) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[output]
		size := o.State().Mode.Size
		return errors.Join(b.Pointer().MoveTo(o, x/float64(size.X), y/float64(size.Y)), b.Pointer().Frame())
	})
}

func (h *Harness) push(dx, dy float64, times int) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		var err error
		for range times {
			err = errors.Join(err, b.Pointer().Move(dx, dy), b.Pointer().Frame())
		}
		return err
	})
}

func (h *Harness) activeWorkspace() wm.WorkspaceID {
	var id wm.WorkspaceID
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		id = c.Snapshot().Outputs[0].Workspace
		return nil
	})
	return id
}

func TestPushingIntoAHotCornerFiresOncePerVisit(t *testing.T) {
	t.Parallel()
	h := StartWith(t, overviewCorner(t), small)
	h.pointAt(0, 0, 0)
	h.push(-30, 0, 2)
	h.expectOverview(false)
	h.push(0, -30, 1)
	h.expectOverview(true)
	h.push(-30, -30, 5)
	h.expectOverview(true)
	h.push(50, 50, 1)
	h.pointAt(0, 0, 0)
	h.push(-30, 0, 2)
	h.expectOverview(true)
	h.push(-30, 0, 1)
	h.expectOverview(false)
}

func TestAFastMotionThroughAHotCornerDoesNotFire(t *testing.T) {
	t.Parallel()
	h := StartWith(t, overviewCorner(t), small)
	h.pointAt(0, 80, 50)
	h.push(-5000, -5000, 1)
	h.expectOverview(false)
	h.push(40, 40, 1)
	h.pointAt(0, 80, 0)
	h.push(-5000, 0, 1)
	h.push(0, 20, 1)
	h.expectOverview(false)
	if x, y := h.cursorAt(); x != 0 || y != 20 {
		t.Fatalf("the pointer at %v,%v, want 0,20 after passing the corner", x, y)
	}
}

func (h *Harness) cursorAt() (float64, float64) {
	var x, y float64
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		x, y = c.Cursor()
		return nil
	})
	return x, y
}

func TestAHotCornerSharedWithAnotherOutputDoesNotFire(t *testing.T) {
	t.Parallel()
	every := func(cfg *compositor.Config) {
		for _, k := range []compositor.Corner{compositor.TopLeft, compositor.TopRight} {
			withHotCorner(t, k, "overview toggle", 0)(cfg)
		}
	}
	h := StartWith(t, every, small, small)
	h.pointAt(0, 159, 0)
	h.push(0, -30, 5)
	h.expectOverview(false)
	h.pointAt(1, 0, 0)
	h.push(0, -30, 5)
	h.expectOverview(false)
	h.pointAt(1, 159, 0)
	h.push(30, 0, 3)
	h.expectOverview(true)
}

func TestAnOutputCanTurnItsHotCornersOff(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		overviewCorner(t)(cfg)
		cfg.Outputs.Fallback.HotCorners = &compositor.HotCornerSet{}
	}, small)
	h.pointAt(0, 0, 0)
	h.push(-30, -30, 5)
	h.expectOverview(false)
}

func TestAConstrainedPointerDoesNotFireAHotCorner(t *testing.T) {
	t.Parallel()
	r := inputOn(t, StartWith(t, overviewCorner(t), small))
	r.expect("kb_enter win", "enter win 80,50", "frame")
	l := &constraintLog{}
	if _, err := r.constraints().ConfinePointer(l, r.w.Surface, r.in.Pointer, nil, cpc.ZwpPointerConstraintsV1LifetimePersistent); err != nil {
		t.Fatal(err)
	}
	r.await(l, "confined")
	r.h.pointAt(0, 0, 0)
	r.h.push(-30, -30, 5)
	if x, y := r.cursor(); x != 0 || y != 0 {
		t.Fatalf("the confined pointer at %v,%v, not in the corner", x, y)
	}
	r.h.expectOverview(false)
}

func TestAFullscreenFocusDoesNotFireAHotCornerUnlessAllowed(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{false, true} {
		h := StartWith(t, func(cfg *compositor.Config) {
			overviewCorner(t)(cfg)
			cfg.HotCorners.Fullscreen = allowed
		}, small)
		r := inputOn(t, h)
		r.expect("kb_enter win", "enter win 80,50", "frame")
		h.dispatch("fullscreen on")
		h.pointAt(0, 0, 0)
		h.push(-30, -30, 5)
		h.expectOverview(allowed)
	}
}

func TestALockedSessionDoesNotFireAHotCorner(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withHotCorner(t, compositor.TopLeft, "workspace 2", 0), small)
	l := lockSession(t, h)
	l.seen("locked")
	h.pointAt(0, 0, 0)
	h.push(-30, -30, 5)
	if got := h.activeWorkspace(); got != 1 {
		t.Fatalf("the locked session switched to workspace %d", got)
	}
}

func TestAHotCornerWithModifiersFiresOnlyWhileTheyAreHeld(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withHotCorner(t, compositor.BottomRight, "overview toggle", binds.ModSuper), small)
	h.pointAt(0, 159, 99)
	h.push(30, 30, 5)
	h.expectOverview(false)
	h.key(keySuper, true)
	h.push(30, 30, 3)
	h.key(keySuper, false)
	h.expectOverview(true)
}

func TestAHotCornerFiresDuringAMoveWindowDrag(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		dragConfig(t, cfg)
		withHotCorner(t, compositor.TopLeft, "workspace 2", 0)(cfg)
	}, small)
	_, _ = floatingForDrag(t, h)
	holdDrag(t, h, image.Pt(80, 50), image.Pt(40, 20))
	if !h.dragging() {
		t.Fatal("no window drag")
	}
	dragTo(h, image.Pt(0, 0))
	h.push(-30, -30, 3)
	if got := h.activeWorkspace(); got != 2 {
		t.Fatalf("workspace %d during the drag, want 2", got)
	}
	releaseDrag(h)
}

func TestAHotCornerFiresDuringADataDeviceDrag(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withHotCorner(t, compositor.TopLeft, "workspace 2", 0), small)
	a := join(t, h, "a")
	r := &inputRig{t: t, h: h, c: a.c, in: a.in, w: a.w}
	r.moveOn(0, 20, 50)
	a.seen(t, "the pointer over a", func() bool { return slices.Contains(a.in.Log, "motion 20,50") })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame())
	})
	a.seen(t, "the press", func() bool { return a.in.Press != 0 })
	dragged := a.c.DataSource(map[string]string{"text/plain": "x"})
	if err := errors.Join(dragged.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy), a.cb.Device.StartDrag(dragged.Data, a.w.Surface, nil, a.in.Press), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the drag entering a", func() bool { return a.cb.Drag != nil })
	r.moveOn(0, 0, 0)
	h.push(-30, -30, 3)
	if got := h.activeWorkspace(); got != 2 {
		t.Fatalf("workspace %d during the drag, want 2", got)
	}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, false), b.Pointer().Frame())
	})
}

func TestPointerMotionAwayFromHotCornersAndDragsDoesNoEdgeWorkAndAllocatesNothing(t *testing.T) {
	r := inputOn(t, start(t, setup{open: softBackend, configure: func(cfg *compositor.Config) {
		for k := range compositor.CornerCount {
			withHotCorner(t, k, "overview toggle", 0)(cfg)
		}
	}}, small))
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		there, back := &backend.PointerMotion{DX: 7, DY: 3, UnaccelDX: 7, UnaccelDY: 3}, &backend.PointerMotion{DX: -7, DY: -3, UnaccelDX: -7, UnaccelDY: -3}
		frame := &backend.PointerFrame{}
		cycle := func() {
			if err := errors.Join(comp.Input(b.Pointer(), there), comp.Input(b.Pointer(), frame), comp.Input(b.Pointer(), back), comp.Input(b.Pointer(), frame)); err != nil {
				t.Fatal(err)
			}
		}
		for range 20 {
			cycle()
		}
		scans, looks := comp.HotCornerScans(), comp.DragHoldLooks()
		if n := allocsPerRun(100, func() {}, cycle); n != 0 {
			t.Errorf("pointer motion away from the corners allocated %d times a cycle", n)
		}
		if got := comp.HotCornerScans(); got != scans {
			t.Errorf("pointer motion away from the corners scanned for a corner %d times", got-scans)
		}
		if got := comp.DragHoldLooks(); got != looks {
			t.Errorf("pointer motion outside a drag looked for an edge, tab or tile %d times", got-looks)
		}
		return nil
	})
}
