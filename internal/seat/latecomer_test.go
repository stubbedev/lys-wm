package seat

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	cpg "github.com/stubbedev/lys-wm/internal/protocol/client/pointergestures"
	ctab "github.com/stubbedev/lys-wm/internal/protocol/client/tablet"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type pointerEvents struct {
	cw.WlPointerHandler
	log     []string
	serials []uint32
}

func (p *pointerEvents) Enter(_ *cw.WlPointer, serial uint32, _ *cw.WlSurface, x, y wire.Fixed) error {
	p.serials = append(p.serials, serial)
	p.log = append(p.log, fmt.Sprintf("enter %v %v", x.Float(), y.Float()))
	return nil
}

func (p *pointerEvents) Motion(_ *cw.WlPointer, _ uint32, x, y wire.Fixed) error {
	p.log = append(p.log, fmt.Sprintf("motion %v %v", x.Float(), y.Float()))
	return nil
}

func (p *pointerEvents) Frame(*cw.WlPointer) error {
	p.log = append(p.log, "frame")
	return nil
}

type touchEvents struct {
	cw.WlTouchHandler
	log []string
}

func (l *touchEvents) Down(_ *cw.WlTouch, _, at uint32, _ *cw.WlSurface, id int32, x, y wire.Fixed) error {
	l.log = append(l.log, fmt.Sprintf("down %d at %d %v %v", id, at, x.Float(), y.Float()))
	return nil
}

func (l *touchEvents) Motion(_ *cw.WlTouch, _ uint32, id int32, x, y wire.Fixed) error {
	l.log = append(l.log, fmt.Sprintf("motion %d %v %v", id, x.Float(), y.Float()))
	return nil
}

func (l *touchEvents) Up(_ *cw.WlTouch, _, _ uint32, id int32) error {
	l.log = append(l.log, fmt.Sprintf("up %d", id))
	return nil
}

func (l *touchEvents) Frame(*cw.WlTouch) error {
	l.log = append(l.log, "frame")
	return nil
}

type gestureEvents struct {
	log []string
}

func (g *gestureEvents) begin(fingers uint32) error {
	g.log = append(g.log, fmt.Sprintf("begin %d", fingers))
	return nil
}

func (g *gestureEvents) note(event string) error {
	g.log = append(g.log, event)
	return nil
}

type swipeEvents struct{ *gestureEvents }

func (e swipeEvents) Begin(_ *cpg.ZwpPointerGestureSwipeV1, _, _ uint32, _ *cw.WlSurface, fingers uint32) error {
	return e.begin(fingers)
}

func (e swipeEvents) Update(*cpg.ZwpPointerGestureSwipeV1, uint32, wire.Fixed, wire.Fixed) error {
	return e.note("update")
}

func (e swipeEvents) End(*cpg.ZwpPointerGestureSwipeV1, uint32, uint32, int32) error {
	return e.note("end")
}

type pinchEvents struct{ *gestureEvents }

func (e pinchEvents) Begin(_ *cpg.ZwpPointerGesturePinchV1, _, _ uint32, _ *cw.WlSurface, fingers uint32) error {
	return e.begin(fingers)
}

func (e pinchEvents) Update(*cpg.ZwpPointerGesturePinchV1, uint32, wire.Fixed, wire.Fixed, wire.Fixed, wire.Fixed) error {
	return e.note("update")
}

func (e pinchEvents) End(*cpg.ZwpPointerGesturePinchV1, uint32, uint32, int32) error {
	return e.note("end")
}

type tabletSeatEvents struct {
	ctab.ZwpTabletSeatV2Handler
	log []string
}

func (l *tabletSeatEvents) TabletAdded(*ctab.ZwpTabletSeatV2, *ctab.ZwpTabletV2) (ctab.ZwpTabletV2Handler, error) {
	return tabletEvents{}, nil
}

func (l *tabletSeatEvents) ToolAdded(*ctab.ZwpTabletSeatV2, *ctab.ZwpTabletToolV2) (ctab.ZwpTabletToolV2Handler, error) {
	return toolEvents{l}, nil
}

type tabletEvents struct{ ctab.ZwpTabletV2Handler }

func (tabletEvents) Name(*ctab.ZwpTabletV2, string) error { return nil }
func (tabletEvents) Done(*ctab.ZwpTabletV2) error         { return nil }

type toolEvents struct{ l *tabletSeatEvents }

func (e toolEvents) note(event string) error {
	e.l.log = append(e.l.log, event)
	return nil
}

func (toolEvents) Type(*ctab.ZwpTabletToolV2, ctab.ZwpTabletToolV2Type) error { return nil }
func (toolEvents) HardwareSerial(*ctab.ZwpTabletToolV2, uint32, uint32) error { return nil }
func (toolEvents) HardwareIDWacom(*ctab.ZwpTabletToolV2, uint32, uint32) error {
	return nil
}

func (toolEvents) Capability(*ctab.ZwpTabletToolV2, ctab.ZwpTabletToolV2Capability) error {
	return nil
}
func (toolEvents) Done(*ctab.ZwpTabletToolV2) error      { return nil }
func (e toolEvents) Removed(*ctab.ZwpTabletToolV2) error { return e.note("removed") }

func (e toolEvents) ProximityIn(*ctab.ZwpTabletToolV2, uint32, *ctab.ZwpTabletV2, *cw.WlSurface) error {
	return e.note("proximity_in")
}
func (e toolEvents) ProximityOut(*ctab.ZwpTabletToolV2) error { return e.note("proximity_out") }
func (e toolEvents) Down(*ctab.ZwpTabletToolV2, uint32) error { return e.note("down") }
func (e toolEvents) Up(*ctab.ZwpTabletToolV2) error           { return e.note("up") }

func (e toolEvents) Motion(_ *ctab.ZwpTabletToolV2, x, y wire.Fixed) error {
	return e.note(fmt.Sprintf("motion %v %v", x.Float(), y.Float()))
}
func (toolEvents) Pressure(*ctab.ZwpTabletToolV2, uint32) error             { return nil }
func (toolEvents) Distance(*ctab.ZwpTabletToolV2, uint32) error             { return nil }
func (toolEvents) Tilt(*ctab.ZwpTabletToolV2, wire.Fixed, wire.Fixed) error { return nil }
func (toolEvents) Rotation(*ctab.ZwpTabletToolV2, wire.Fixed) error         { return nil }
func (toolEvents) Slider(*ctab.ZwpTabletToolV2, int32) error                { return nil }
func (toolEvents) Wheel(*ctab.ZwpTabletToolV2, wire.Fixed, int32) error     { return nil }
func (e toolEvents) Frame(_ *ctab.ZwpTabletToolV2, at uint32) error {
	return e.note(fmt.Sprintf("frame %d", at))
}

func (e toolEvents) Button(_ *ctab.ZwpTabletToolV2, _, button uint32, state ctab.ZwpTabletToolV2ButtonState) error {
	return e.note(fmt.Sprintf("button %d %d", button, state))
}

func (e *env) do(steps ...func() error) {
	e.t.Helper()
	e.on(func() {
		for _, step := range steps {
			if err := step(); err != nil {
				e.t.Error(err)
			}
		}
	})
}

func (c *client) pointer() *pointerEvents {
	c.t.Helper()
	p := &pointerEvents{}
	if _, err := c.seat.GetPointer(p); err != nil {
		c.t.Fatal(err)
	}
	c.roundtrip()
	return p
}

func (c *client) touch() *touchEvents {
	c.t.Helper()
	l := &touchEvents{}
	if _, err := c.seat.GetTouch(l); err != nil {
		c.t.Fatal(err)
	}
	c.roundtrip()
	return l
}

func (c *client) tabletSeat(m *ctab.ZwpTabletManagerV2) *tabletSeatEvents {
	c.t.Helper()
	l := &tabletSeatEvents{}
	if _, err := m.GetTabletSeat(l, c.seat); err != nil {
		c.t.Fatal(err)
	}
	c.roundtrip()
	return l
}

func TestAPointerMadeOverItsClientsSurfaceIsEntered(t *testing.T) {
	e := setup(t)
	e.plug(sw.WlSeatCapabilityPointer)
	over, away := e.connect(), e.connect()
	first := over.pointer()
	over.surface()
	e.do(
		func() error { return e.seat.SetPointerFocus(e.surf.list[0], 3, 4) },
		func() error { return e.seat.PointerMotion(time.Second, 5, 6) },
		e.seat.PointerFrame,
	)
	over.roundtrip()

	late, elsewhere := over.pointer(), away.pointer()
	if want := []string{"enter 5 6", "frame"}; !slices.Equal(late.log, want) {
		t.Fatalf("a wl_pointer made over its client's surface saw %v, want %v", late.log, want)
	}
	if !slices.Equal(late.serials, first.serials) {
		t.Fatalf("the late wl_pointer entered with serial %v, the first with %v: set_cursor honours one enter serial", late.serials, first.serials)
	}
	if len(elsewhere.log) != 0 {
		t.Fatalf("a wl_pointer of a client the pointer is not over saw %v", elsewhere.log)
	}

	e.do(func() error { return e.seat.PointerMotion(time.Second, 7, 8) }, e.seat.PointerFrame)
	over.roundtrip()
	if want := []string{"enter 5 6", "frame", "motion 7 8", "frame"}; !slices.Equal(late.log, want) {
		t.Fatalf("the late wl_pointer saw %v, want %v", late.log, want)
	}
}

func TestATouchMadeWhileItsClientIsTouchedSeesThePointsDown(t *testing.T) {
	e := setup(t)
	e.plug(sw.WlSeatCapabilityTouch)
	held, away := e.connect(), e.connect()
	held.surface()
	away.surface()
	e.do(
		func() error { return e.seat.TouchDown(time.Second, 7, e.surf.list[0], 1, 2) },
		func() error { return e.seat.TouchDown(time.Second, 8, e.surf.list[1], 1, 2) },
		e.seat.TouchFrame,
		func() error { return e.seat.TouchMotion(2*time.Second, 7, 5, 6) },
		e.seat.TouchFrame,
	)

	late := held.touch()
	if want := []string{"down 7 at 2000 5 6", "frame"}; !slices.Equal(late.log, want) {
		t.Fatalf("a wl_touch made while its client holds a touch point saw %v, want %v", late.log, want)
	}

	e.do(func() error { return e.seat.TouchUp(3*time.Second, 7) }, e.seat.TouchFrame)
	held.roundtrip()
	if want := []string{"down 7 at 2000 5 6", "frame", "up 7", "frame"}; !slices.Equal(late.log, want) {
		t.Fatalf("the late wl_touch saw %v, want %v", late.log, want)
	}
	if after := held.touch(); len(after.log) != 0 {
		t.Fatalf("a wl_touch made after its client's touch point lifted saw %v", after.log)
	}
}

func TestAGestureObjectMadeDuringItsGestureSeesItBegin(t *testing.T) {
	e := setup(t)
	e.plug(sw.WlSeatCapabilityPointer)
	c := e.connect()
	gestures, err := cpg.NewZwpPointerGesturesV1(c.d, gesturesVersion)
	if err != nil {
		t.Fatal(err)
	}
	c.bind("zwp_pointer_gestures_v1", gestures)
	ptr, err := c.seat.GetPointer(&pointerEvents{})
	if err != nil {
		t.Fatal(err)
	}
	c.surface()
	e.do(
		func() error { return e.seat.SetPointerFocus(e.surf.list[0], 1, 1) },
		func() error { return e.seat.GestureBegin(time.Second, backend.Pinch, 2) },
	)

	pinch, swipe := &gestureEvents{}, &gestureEvents{}
	if _, err := gestures.GetPinchGesture(pinchEvents{pinch}, ptr); err != nil {
		t.Fatal(err)
	}
	if _, err := gestures.GetSwipeGesture(swipeEvents{swipe}, ptr); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	e.do(
		func() error { return e.seat.PinchUpdate(time.Second, 1, 1, 1.5, 0) },
		func() error { return e.seat.GestureEnd(time.Second, backend.Pinch, false) },
	)
	c.roundtrip()
	if want := []string{"begin 2", "update", "end"}; !slices.Equal(pinch.log, want) {
		t.Fatalf("a pinch object made during a pinch saw %v, want %v", pinch.log, want)
	}
	if len(swipe.log) != 0 {
		t.Fatalf("a swipe object made during a pinch saw %v", swipe.log)
	}

	after := &gestureEvents{}
	if _, err := gestures.GetPinchGesture(pinchEvents{after}, ptr); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	if len(after.log) != 0 {
		t.Fatalf("a pinch object made after the pinch ended saw %v", after.log)
	}
}

func TestATabletSeatMadeWhileAToolIsOverItsClientSeesTheToolIn(t *testing.T) {
	e := setup(t)
	c := e.connect()
	manager, err := ctab.NewZwpTabletManagerV2(c.d, tabletVersion)
	if err != nil {
		t.Fatal(err)
	}
	c.bind("zwp_tablet_manager_v2", manager)
	c.surface()
	device, tool := "tablet", &tabletinput.Tool{Type: tabletinput.Pen}
	e.do(
		func() error { return e.seat.AddTablet(device, 1, "tablet", tabletinput.ID{}) },
		func() error { return e.seat.TabletTool(tool) },
	)
	first := c.tabletSeat(manager)
	e.do(
		func() error { return e.seat.TabletToolMotion(time.Second, device, tool, e.surf.list[0], 1, 2) },
		func() error { return e.seat.TabletToolTip(tool, true) },
		func() error { return e.seat.TabletToolFrame(time.Second, tool) },
	)

	late := c.tabletSeat(manager)
	if want := []string{"proximity_in", "motion 1 2", "down", "frame 1000"}; !slices.Equal(first.log, want) {
		t.Fatalf("the tool of the first tablet seat saw %v, want %v", first.log, want)
	}
	if want := []string{"proximity_in", "down", "frame 1000"}; !slices.Equal(late.log, want) {
		t.Fatalf("the tool of a tablet seat made while it is over its client saw %v, want %v", late.log, want)
	}

	e.do(
		func() error { return e.seat.TabletToolMotion(2*time.Second, device, tool, e.surf.list[0], 3, 4) },
		func() error { return e.seat.TabletToolFrame(2*time.Second, tool) },
	)
	c.roundtrip()
	if want := []string{"proximity_in", "down", "frame 1000", "motion 3 4", "frame 2000"}; !slices.Equal(late.log, want) {
		t.Fatalf("the late tool saw %v, want %v", late.log, want)
	}
}
