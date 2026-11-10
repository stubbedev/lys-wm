package seat

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	cpg "github.com/stubbedev/lys-wm/internal/protocol/client/pointergestures"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const (
	leftButton   = 0x110
	measuredRuns = 100
)

type received struct{ events int }

func (r *received) note() error {
	r.events++
	return nil
}

func (r *received) count() int { return r.events }

type countedPointer struct{ *received }

func (p countedPointer) Enter(*cw.WlPointer, uint32, *cw.WlSurface, wire.Fixed, wire.Fixed) error {
	return p.note()
}

func (p countedPointer) Leave(*cw.WlPointer, uint32, *cw.WlSurface) error { return p.note() }

func (p countedPointer) Motion(*cw.WlPointer, uint32, wire.Fixed, wire.Fixed) error { return p.note() }

func (p countedPointer) Button(*cw.WlPointer, uint32, uint32, uint32, cw.WlPointerButtonState) error {
	return p.note()
}

func (p countedPointer) Axis(*cw.WlPointer, uint32, cw.WlPointerAxis, wire.Fixed) error {
	return p.note()
}
func (p countedPointer) Frame(*cw.WlPointer) error                              { return p.note() }
func (p countedPointer) AxisSource(*cw.WlPointer, cw.WlPointerAxisSource) error { return p.note() }

func (p countedPointer) AxisStop(*cw.WlPointer, uint32, cw.WlPointerAxis) error {
	return p.note()
}

func (p countedPointer) AxisDiscrete(*cw.WlPointer, cw.WlPointerAxis, int32) error {
	return p.note()
}

func (p countedPointer) AxisValue120(*cw.WlPointer, cw.WlPointerAxis, int32) error {
	return p.note()
}

func (p countedPointer) AxisRelativeDirection(*cw.WlPointer, cw.WlPointerAxis, cw.WlPointerAxisRelativeDirection) error {
	return p.note()
}
func (p countedPointer) Warp(*cw.WlPointer, wire.Fixed, wire.Fixed) error { return p.note() }

type countedTouch struct{ *received }

func (l countedTouch) Down(*cw.WlTouch, uint32, uint32, *cw.WlSurface, int32, wire.Fixed, wire.Fixed) error {
	return l.note()
}
func (l countedTouch) Up(*cw.WlTouch, uint32, uint32, int32) error { return l.note() }

func (l countedTouch) Motion(*cw.WlTouch, uint32, int32, wire.Fixed, wire.Fixed) error {
	return l.note()
}
func (l countedTouch) Frame(*cw.WlTouch) error                                { return l.note() }
func (l countedTouch) Cancel(*cw.WlTouch) error                               { return l.note() }
func (l countedTouch) Shape(*cw.WlTouch, int32, wire.Fixed, wire.Fixed) error { return l.note() }
func (l countedTouch) Orientation(*cw.WlTouch, int32, wire.Fixed) error       { return l.note() }

type countedSwipe struct{ *received }

func (g countedSwipe) Begin(*cpg.ZwpPointerGestureSwipeV1, uint32, uint32, *cw.WlSurface, uint32) error {
	return g.note()
}

func (g countedSwipe) Update(*cpg.ZwpPointerGestureSwipeV1, uint32, wire.Fixed, wire.Fixed) error {
	return g.note()
}

func (g countedSwipe) End(*cpg.ZwpPointerGestureSwipeV1, uint32, uint32, int32) error {
	return g.note()
}

type countedPinch struct{ *received }

func (g countedPinch) Begin(*cpg.ZwpPointerGesturePinchV1, uint32, uint32, *cw.WlSurface, uint32) error {
	return g.note()
}

func (g countedPinch) Update(*cpg.ZwpPointerGesturePinchV1, uint32, wire.Fixed, wire.Fixed, wire.Fixed, wire.Fixed) error {
	return g.note()
}

func (g countedPinch) End(*cpg.ZwpPointerGesturePinchV1, uint32, uint32, int32) error {
	return g.note()
}

type countedHold struct{ *received }

func (g countedHold) Begin(*cpg.ZwpPointerGestureHoldV1, uint32, uint32, *cw.WlSurface, uint32) error {
	return g.note()
}

func (g countedHold) End(*cpg.ZwpPointerGestureHoldV1, uint32, uint32, int32) error {
	return g.note()
}

func (e *env) sendsWithoutAllocating(c *client, seen func() int, events int, what string, run func() error) {
	e.t.Helper()
	c.roundtrip()
	before := seen()
	var allocs float64
	e.on(func() {
		allocs = testing.AllocsPerRun(measuredRuns, func() {
			if err := run(); err != nil {
				e.t.Error(err)
			}
		})
	})
	c.roundtrip()
	if allocs != 0 {
		e.t.Errorf("%s allocated %v times", what, allocs)
	}
	if got, want := seen()-before, events*(measuredRuns+1); got != want {
		e.t.Errorf("%s reached the client as %d events, want %d: what was measured must have been sent", what, got, want)
	}
}

func TestPointerButtonsAndAxesAllocateNothing(t *testing.T) {
	e := setup(t)
	e.plug(sw.WlSeatCapabilityPointer)
	c := e.connect()
	got := &received{}
	if _, err := c.seat.GetPointer(countedPointer{got}); err != nil {
		t.Fatal(err)
	}
	c.surface()
	c.surface()
	wheel := Axis{Orientation: sw.WlPointerAxisVerticalScroll, Source: sw.WlPointerAxisSourceWheel, Value: 15, Value120: 120}
	finger := Axis{Orientation: sw.WlPointerAxisVerticalScroll, Source: sw.WlPointerAxisSourceFinger, Value: 3}
	stop := Axis{Orientation: sw.WlPointerAxisVerticalScroll, Source: sw.WlPointerAxisSourceFinger}
	e.do(func() error { return e.seat.SetPointerFocus(e.surf.list[0], 1, 1) }, e.seat.PointerFrame)

	const click, notch, scroll, lift = 4, 5, 4, 3
	e.sendsWithoutAllocating(c, got.count, click+notch+scroll+lift, "a click, a wheel notch and a finger scroll that stops", func() error {
		return errors.Join(
			e.seat.PointerButton(time.Second, leftButton, true), e.seat.PointerFrame(),
			e.seat.PointerButton(time.Second, leftButton, false), e.seat.PointerFrame(),
			e.seat.PointerAxis(time.Second, wheel), e.seat.PointerFrame(),
			e.seat.PointerAxis(time.Second, finger), e.seat.PointerFrame(),
			e.seat.PointerAxis(time.Second, stop), e.seat.PointerFrame(),
		)
	})

	const crossing = 4
	e.sendsWithoutAllocating(c, got.count, 2*crossing, "the pointer crossing to another surface with a button going down and up", func() error {
		return errors.Join(
			e.seat.SetPointerFocus(e.surf.list[1], 1, 1), e.seat.PointerButton(time.Second, leftButton, true), e.seat.PointerFrame(),
			e.seat.SetPointerFocus(e.surf.list[0], 1, 1), e.seat.PointerButton(time.Second, leftButton, false), e.seat.PointerFrame(),
		)
	})
}

func TestKeysAllocateNothing(t *testing.T) {
	e := setup(t)
	c := e.connect()
	k := c.keyboard()
	c.surface()
	e.focus(0)
	seen := func() int { return len(k.log) }

	const modifier, key = 2, 1
	e.sendsWithoutAllocating(c, seen, 2*modifier+2*key, "a key pressed and released under a modifier", func() error {
		return errors.Join(
			e.seat.Key(keyLeftShift, true, time.Second), e.seat.Key(keyA, true, time.Second),
			e.seat.Key(keyA, false, time.Second), e.seat.Key(keyLeftShift, false, time.Second),
		)
	})

	fd, err := unix.Dup(e.keymap.FD())
	if err != nil {
		t.Fatal(err)
	}
	v := NewVirtualKeyboard()
	if err := v.SetKeymap(fd, e.keymap.Size()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := v.Close(); err != nil {
			t.Error(err)
		}
	})
	e.do(func() error { return e.seat.VirtualKey(v, keyA, true, time.Second) }, func() error { return e.seat.VirtualKey(v, keyA, false, time.Second) })
	e.sendsWithoutAllocating(c, seen, 2+2*key, "a virtual key pressed and released under a modifier", func() error {
		return errors.Join(
			e.seat.VirtualModifiers(v, 1, 0, 0, 0), e.seat.VirtualKey(v, keyA, true, time.Second),
			e.seat.VirtualKey(v, keyA, false, time.Second), e.seat.VirtualModifiers(v, 0, 0, 0, 0),
		)
	})
}

func TestTouchesAllocateNothing(t *testing.T) {
	e := setup(t)
	e.plug(sw.WlSeatCapabilityTouch)
	c := e.connect()
	got := &received{}
	if _, err := c.seat.GetTouch(countedTouch{got}); err != nil {
		t.Fatal(err)
	}
	c.surface()

	const down, moved, lifted, cancelled = 3, 3, 2, 1
	e.sendsWithoutAllocating(c, got.count, down+moved+lifted+cancelled, "two touch points going down, moving, lifting and being cancelled", func() error {
		target := e.surf.list[0]
		return errors.Join(
			e.seat.TouchDown(time.Second, 1, target, 1, 1), e.seat.TouchDown(time.Second, 2, target, 2, 2), e.seat.TouchFrame(),
			e.seat.TouchMotion(time.Second, 1, 3, 3), e.seat.TouchMotion(time.Second, 2, 4, 4), e.seat.TouchFrame(),
			e.seat.TouchUp(time.Second, 1), e.seat.TouchFrame(),
			e.seat.TouchCancel(),
		)
	})

	ids := []int32{1}
	e.sendsWithoutAllocating(c, got.count, 3, "a touch point taken from its client", func() error {
		return errors.Join(e.seat.TouchDown(time.Second, 1, e.surf.list[0], 1, 1), e.seat.TouchFrame(), e.seat.CancelTouches(ids))
	})
}

func TestGesturesAllocateNothing(t *testing.T) {
	e := setup(t)
	e.plug(sw.WlSeatCapabilityPointer)
	c := e.connect()
	gestures, err := cpg.NewZwpPointerGesturesV1(c.d, gesturesVersion)
	if err != nil {
		t.Fatal(err)
	}
	c.bind("zwp_pointer_gestures_v1", gestures)
	ptr, err := c.seat.GetPointer(countedPointer{&received{}})
	if err != nil {
		t.Fatal(err)
	}
	got := &received{}
	if _, err := gestures.GetSwipeGesture(countedSwipe{got}, ptr); err != nil {
		t.Fatal(err)
	}
	if _, err := gestures.GetPinchGesture(countedPinch{got}, ptr); err != nil {
		t.Fatal(err)
	}
	if _, err := gestures.GetHoldGesture(countedHold{got}, ptr); err != nil {
		t.Fatal(err)
	}
	c.surface()
	e.do(func() error { return e.seat.SetPointerFocus(e.surf.list[0], 1, 1) })

	const swipe, pinch, hold = 3, 3, 2
	e.sendsWithoutAllocating(c, got.count, swipe+pinch+hold, "a swipe, a pinch and a hold", func() error {
		return errors.Join(
			e.seat.GestureBegin(time.Second, backend.Swipe, 3), e.seat.SwipeUpdate(time.Second, 1, 1), e.seat.GestureEnd(time.Second, backend.Swipe, false),
			e.seat.GestureBegin(time.Second, backend.Pinch, 2), e.seat.PinchUpdate(time.Second, 1, 1, 1.5, 10), e.seat.GestureEnd(time.Second, backend.Pinch, false),
			e.seat.GestureBegin(time.Second, backend.Hold, 4), e.seat.GestureEnd(time.Second, backend.Hold, true),
		)
	})
}
