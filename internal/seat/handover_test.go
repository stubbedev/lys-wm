package seat

import (
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/handover"
	"github.com/stubbedev/lys-wm/internal/loop"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type pointerLog struct {
	cw.WlPointerHandler
	log []string
}

func (p *pointerLog) Enter(*cw.WlPointer, uint32, *cw.WlSurface, wire.Fixed, wire.Fixed) error {
	p.log = append(p.log, "enter")
	return nil
}

func (p *pointerLog) Leave(*cw.WlPointer, uint32, *cw.WlSurface) error {
	p.log = append(p.log, "leave")
	return nil
}

func (*pointerLog) Frame(*cw.WlPointer) error { return nil }

type touchLog struct {
	cw.WlTouchHandler
	log []string
}

func (l *touchLog) Down(*cw.WlTouch, uint32, uint32, *cw.WlSurface, int32, wire.Fixed, wire.Fixed) error {
	l.log = append(l.log, "down")
	return nil
}

func (l *touchLog) Cancel(*cw.WlTouch) error {
	l.log = append(l.log, "cancel")
	return nil
}

func (*touchLog) Frame(*cw.WlTouch) error { return nil }

func (e *env) handOver() *env {
	e.t.Helper()
	fds := &handover.FDs{}
	var display *server.State
	var seat HandoverState
	var err error
	e.on(func() {
		if err = e.disp.Park(); err != nil {
			return
		}
		seat = e.seat.HandoverState()
		display, _, err = e.disp.Snapshot(fds, func(*server.Client) bool { return false })
	})
	if err != nil {
		e.t.Fatal(err)
	}
	l, err := loop.New()
	if err != nil {
		e.t.Fatal(err)
	}
	next := &env{t: e.t, loop: l, dir: e.dir, keymap: e.keymap, surf: &surfaces{}}
	if next.disp, err = server.Adopt(l, server.Options{Report: func(err error) { e.t.Error(err) }}, display, fds); err != nil {
		e.t.Fatal(err)
	}
	if _, err := surface.New(next.disp, next.surf); err != nil {
		e.t.Fatal(err)
	}
	if next.seat, err = New(next.disp, Config{Keymap: e.keymap, RepeatRate: DefaultRepeatRate, RepeatDelay: DefaultRepeatDelay}, quietListener{}); err != nil {
		e.t.Fatal(err)
	}
	next.surf.seat = next.seat
	next.serve()
	next.on(func() {
		if err = next.seat.AdoptHandover(seat); err != nil {
			return
		}
		var release func() error
		if release, err = next.disp.Restore(display, fds); err == nil {
			err = release()
		}
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return next
}

func (e *env) plug(caps ...sw.WlSeatCapability) {
	e.t.Helper()
	e.on(func() {
		for _, c := range caps {
			if err := e.seat.AddDevice(c); err != nil {
				e.t.Error(err)
			}
		}
	})
}

const everyCapability = cw.WlSeatCapabilityKeyboard | cw.WlSeatCapabilityPointer | cw.WlSeatCapabilityTouch

func TestAHandoverToASeatWhoseDevicesComeLaterKeepsEveryInputObjectWorking(t *testing.T) {
	old := setup(t)
	old.on(func() { old.disp.KeepHandoverJournal() })
	old.plug(sw.WlSeatCapabilityPointer, sw.WlSeatCapabilityTouch)
	c := old.connect()
	kb, ptr, touch := c.keyboard(), &pointerLog{}, &touchLog{}
	if _, err := c.seat.GetPointer(ptr); err != nil {
		t.Fatal(err)
	}
	if _, err := c.seat.GetTouch(touch); err != nil {
		t.Fatal(err)
	}
	c.surface()

	next := old.handOver()
	c.roundtrip()
	next.plug(sw.WlSeatCapabilityKeyboard, sw.WlSeatCapabilityPointer, sw.WlSeatCapabilityTouch)
	c.roundtrip()
	if slices.ContainsFunc(c.seatE.caps, func(caps cw.WlSeatCapability) bool { return caps != everyCapability }) {
		t.Fatalf("capabilities %v: the client was told of a loss though every device came back", c.seatE.caps)
	}
	next.focus(0)
	next.key(30, true)
	next.on(func() {
		target := next.surf.list[0]
		for _, err := range []error{
			next.seat.SetPointerFocus(target, 1, 1), next.seat.PointerFrame(),
			next.seat.TouchDown(time.Second, 7, target, 1, 1), next.seat.TouchFrame(),
		} {
			if err != nil {
				t.Error(err)
			}
		}
	})
	c.roundtrip()
	if want := []string{"enter", "modifiers", "press"}; !slices.Equal(kb.log, want) {
		t.Errorf("the keyboard made before the handover saw %v, want %v", kb.log, want)
	}
	if want := []string{"enter"}; !slices.Equal(ptr.log, want) {
		t.Errorf("the pointer made before the handover saw %v, want %v", ptr.log, want)
	}
	if want := []string{"cancel", "down"}; !slices.Equal(touch.log, want) {
		t.Errorf("the touch made before the handover saw %v, want %v", touch.log, want)
	}
}

func TestAHandoverKeepsTheInputObjectOfADeviceUnpluggedBeforeIt(t *testing.T) {
	old := setup(t)
	old.on(func() { old.disp.KeepHandoverJournal() })
	old.plug(sw.WlSeatCapabilityPointer, sw.WlSeatCapabilityTouch)
	c := old.connect()
	ptr, touch := &pointerLog{}, &touchLog{}
	if _, err := c.seat.GetPointer(ptr); err != nil {
		t.Fatal(err)
	}
	if _, err := c.seat.GetTouch(touch); err != nil {
		t.Fatal(err)
	}
	c.surface()
	old.on(func() {
		if err := old.seat.RemoveDevice(sw.WlSeatCapabilityPointer | sw.WlSeatCapabilityTouch); err != nil {
			t.Error(err)
		}
	})
	c.roundtrip()

	next := old.handOver()
	c.roundtrip()
	if want := []cw.WlSeatCapability{everyCapability, cw.WlSeatCapabilityKeyboard, cw.WlSeatCapabilityKeyboard}; !slices.Equal(c.seatE.caps, want) {
		t.Fatalf("capabilities %v, want %v: the new seat repeats what the old one last said", c.seatE.caps, want)
	}
	next.plug(sw.WlSeatCapabilityPointer)
	next.on(func() {
		if err := next.seat.SetPointerFocus(next.surf.list[0], 1, 1); err != nil {
			t.Error(err)
		}
	})
	c.roundtrip()
	if c.seatE.caps[len(c.seatE.caps)-1] != cw.WlSeatCapabilityKeyboard|cw.WlSeatCapabilityPointer || !slices.Equal(ptr.log, []string{"enter"}) {
		t.Fatalf("after a pointer was plugged in again: capabilities %v, the old wl_pointer saw %v", c.seatE.caps, ptr.log)
	}
	if _, err := c.seat.GetTouch(&touchLog{}); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
}
