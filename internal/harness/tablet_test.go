package harness

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	ccs "github.com/stubbedev/lys-wm/internal/protocol/client/cursorshape"
	ctab "github.com/stubbedev/lys-wm/internal/protocol/client/tablet"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type fakeTablet struct{}

func (fakeTablet) Name() string                     { return "Wacom Intuos" }
func (fakeTablet) Capabilities() backend.Capability { return 0 }
func (fakeTablet) Tablet() (tabletinput.ID, bool) {
	return tabletinput.ID{Vendor: 0x56a, Product: 0x374}, true
}

type tabletLog struct {
	log   testclient.Log
	in    *testclient.Input
	tools []*ctab.ZwpTabletToolV2
	enter uint32
}

func (l *tabletLog) TabletAdded(*ctab.ZwpTabletSeatV2, *ctab.ZwpTabletV2) (ctab.ZwpTabletV2Handler, error) {
	return tabletEvents{l}, l.log.Note("tablet_added")
}

func (l *tabletLog) ToolAdded(_ *ctab.ZwpTabletSeatV2, tool *ctab.ZwpTabletToolV2) (ctab.ZwpTabletToolV2Handler, error) {
	l.tools = append(l.tools, tool)
	return toolEvents{l}, l.log.Note("tool_added")
}

func (*tabletLog) PadAdded(*ctab.ZwpTabletSeatV2, *ctab.ZwpTabletPadV2) (ctab.ZwpTabletPadV2Handler, error) {
	return nil, errors.New("a pad was added")
}

type tabletEvents struct{ l *tabletLog }

func (e tabletEvents) Name(_ *ctab.ZwpTabletV2, name string) error {
	return e.l.log.Note("name %s", name)
}
func (e tabletEvents) ID(_ *ctab.ZwpTabletV2, vid, pid uint32) error {
	return e.l.log.Note("id %x:%x", vid, pid)
}
func (tabletEvents) Path(*ctab.ZwpTabletV2, string) error                     { return nil }
func (e tabletEvents) Done(*ctab.ZwpTabletV2) error                           { return e.l.log.Note("tablet_done") }
func (e tabletEvents) Removed(*ctab.ZwpTabletV2) error                        { return e.l.log.Note("tablet_removed") }
func (tabletEvents) Bustype(*ctab.ZwpTabletV2, ctab.ZwpTabletV2Bustype) error { return nil }

type toolEvents struct{ l *tabletLog }

func (e toolEvents) Type(_ *ctab.ZwpTabletToolV2, t ctab.ZwpTabletToolV2Type) error {
	return e.l.log.Note("type %#x", uint32(t))
}

func (e toolEvents) HardwareSerial(_ *ctab.ZwpTabletToolV2, hi, lo uint32) error {
	return e.l.log.Note("serial %d %d", hi, lo)
}

func (toolEvents) HardwareIDWacom(*ctab.ZwpTabletToolV2, uint32, uint32) error { return nil }

func (e toolEvents) Capability(_ *ctab.ZwpTabletToolV2, c ctab.ZwpTabletToolV2Capability) error {
	return e.l.log.Note("capability %d", c)
}

func (e toolEvents) Done(*ctab.ZwpTabletToolV2) error    { return e.l.log.Note("tool_done") }
func (e toolEvents) Removed(*ctab.ZwpTabletToolV2) error { return e.l.log.Note("tool_removed") }

func (e toolEvents) ProximityIn(_ *ctab.ZwpTabletToolV2, serial uint32, _ *ctab.ZwpTabletV2, s *cw.WlSurface) error {
	e.l.enter = serial
	return e.l.log.Note("proximity_in %s", e.l.in.Surfaces[s.Base().ID()])
}

func (e toolEvents) ProximityOut(*ctab.ZwpTabletToolV2) error { return e.l.log.Note("proximity_out") }
func (e toolEvents) Down(*ctab.ZwpTabletToolV2, uint32) error { return e.l.log.Note("down") }
func (e toolEvents) Up(*ctab.ZwpTabletToolV2) error           { return e.l.log.Note("up") }

func (e toolEvents) Motion(_ *ctab.ZwpTabletToolV2, x, y wire.Fixed) error {
	return e.l.log.Note("motion %g,%g", x.Float(), y.Float())
}

func (e toolEvents) Pressure(_ *ctab.ZwpTabletToolV2, p uint32) error {
	return e.l.log.Note("pressure %d", p)
}

func (e toolEvents) Distance(_ *ctab.ZwpTabletToolV2, d uint32) error {
	return e.l.log.Note("distance %d", d)
}

func (e toolEvents) Tilt(_ *ctab.ZwpTabletToolV2, x, y wire.Fixed) error {
	return e.l.log.Note("tilt %g,%g", x.Float(), y.Float())
}

func (toolEvents) Rotation(*ctab.ZwpTabletToolV2, wire.Fixed) error     { return nil }
func (toolEvents) Slider(*ctab.ZwpTabletToolV2, int32) error            { return nil }
func (toolEvents) Wheel(*ctab.ZwpTabletToolV2, wire.Fixed, int32) error { return nil }

func (e toolEvents) Button(_ *ctab.ZwpTabletToolV2, _, button uint32, state ctab.ZwpTabletToolV2ButtonState) error {
	return e.l.log.Note("button %#x %d", button, state)
}

func (e toolEvents) Frame(*ctab.ZwpTabletToolV2, uint32) error { return e.l.log.Note("frame") }

func tabletSeat(t *testing.T, p *party) *tabletLog {
	t.Helper()
	m, err := ctab.NewZwpTabletManagerV2(p.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.c.Bind(m)
	l := &tabletLog{in: p.in}
	if _, err := m.GetTabletSeat(l, p.seat); err != nil {
		t.Fatal(err)
	}
	p.c.Sync()
	return l
}

var pen = &tabletinput.Tool{Type: tabletinput.Pen, Serial: 7, Caps: tabletinput.CanPressure | tabletinput.CanTilt}

func at(x, y float64) tabletinput.Axes {
	return tabletinput.Axes{Changed: tabletinput.AxisX | tabletinput.AxisY, X: x, Y: y}
}

func pressed(x, y, pressure float64) tabletinput.Axes {
	a := at(x, y)
	a.Changed |= tabletinput.AxisPressure | tabletinput.AxisTiltX
	a.Pressure, a.TiltX, a.TiltY = pressure, 10, -5
	return a
}

func tablet(h *Harness, events ...backend.Event) {
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		for _, e := range events {
			if err := c.Input(fakeTablet{}, e); err != nil {
				return err
			}
		}
		return nil
	})
}

func addTablet(h *Harness) {
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.DeviceAdded(fakeTablet{}) })
}

func TestATabletToolDrawsIntoAClient(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	p := join(t, h, "canvas")
	l := tabletSeat(t, p)
	addTablet(h)
	until(t, p.c, "the tablet announced", &l.log, "tablet_added", "name Wacom Intuos", "id 56a:374", "tablet_done")

	tablet(h, &tabletinput.Proximity{Time: time.Millisecond, Tool: pen, In: true, Axes: at(0.5, 0.5)})
	until(t, p.c, "the pen announced and in proximity", &l.log,
		"tool_added", "type 0x140", "serial 0 7", "capability 1", "capability 2", "tool_done", "proximity_in canvas")
	p.c.DispatchUntil("the pen's first frame", func() bool { return slices.Contains(l.log, "frame") })
	if !slices.ContainsFunc(l.log, func(s string) bool { return len(s) > 7 && s[:7] == "motion " }) {
		t.Fatalf("no motion with proximity: %q", l.log)
	}
	l.log = nil

	tablet(h, &tabletinput.Tip{Time: 2 * time.Millisecond, Tool: pen, Down: true, Axes: pressed(0.5, 0.5, 0.5)})
	p.c.DispatchUntil("the tip down", func() bool { return slices.Contains(l.log, "frame") })
	for _, want := range []string{"down", "pressure 32768", "tilt 10,-5"} {
		if !slices.Contains(l.log, want) {
			t.Fatalf("tip down gave %q, missing %q", l.log, want)
		}
	}
	l.log = nil

	tablet(h, &tabletinput.Button{Time: 3 * time.Millisecond, Tool: pen, Button: 0x14b, Pressed: true},
		&tabletinput.Tip{Time: 4 * time.Millisecond, Tool: pen, Axes: at(0.5, 0.5)},
		&tabletinput.Proximity{Time: 5 * time.Millisecond, Tool: pen, Axes: at(0.5, 0.5)})
	p.c.DispatchUntil("the pen out of proximity", func() bool { return slices.Contains(l.log, "proximity_out") })
	for _, want := range []string{"button 0x14b 1", "up", "button 0x14b 0"} {
		if !slices.Contains(l.log, want) {
			t.Fatalf("the pen leaving gave %q, missing %q", l.log, want)
		}
	}
	if slices.Contains(p.in.Log, "button 272 1") {
		t.Fatalf("a client with the tablet protocol got a pointer click too: %q", p.in.Log)
	}
}

func TestADownPenStaysOnItsSurface(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	left := join(t, h, "left")
	right := join(t, h, "right")
	ll, rl := tabletSeat(t, left), tabletSeat(t, right)
	addTablet(h)
	tablet(h, &tabletinput.Proximity{Time: time.Millisecond, Tool: pen, In: true, Axes: at(0.1, 0.5)},
		&tabletinput.Tip{Time: 2 * time.Millisecond, Tool: pen, Down: true, Axes: pressed(0.1, 0.5, 1)},
		&tabletinput.Motion{Time: 3 * time.Millisecond, Tool: pen, Axes: at(0.9, 0.5)})
	left.c.DispatchUntil("the pen down on left", func() bool { return slices.Contains(ll.log, "down") })
	right.c.Sync()
	if slices.ContainsFunc(rl.log, func(s string) bool { return s == "proximity_in right" }) {
		t.Fatalf("a down pen crossed into right: %q", rl.log)
	}
	tablet(h, &tabletinput.Tip{Time: 4 * time.Millisecond, Tool: pen, Axes: at(0.9, 0.5)},
		&tabletinput.Motion{Time: 5 * time.Millisecond, Tool: pen, Axes: at(0.91, 0.5)})
	left.c.DispatchUntil("the lifted pen left left", func() bool { return slices.Contains(ll.log, "proximity_out") })
	right.c.DispatchUntil("the lifted pen entered right", func() bool { return slices.Contains(rl.log, "proximity_in right") })
}

func TestAPenClicksForAClientWithoutTheTabletProtocol(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	p := join(t, h, "plain")
	addTablet(h)
	tablet(h, &tabletinput.Proximity{Time: time.Millisecond, Tool: pen, In: true, Axes: at(0.5, 0.5)},
		&tabletinput.Tip{Time: 2 * time.Millisecond, Tool: pen, Down: true, Axes: pressed(0.5, 0.5, 1)},
		&tabletinput.Button{Time: 3 * time.Millisecond, Tool: pen, Button: 0x14b, Pressed: true})
	p.seen(t, "the tip and the barrel button as clicks", func() bool {
		return slices.Contains(p.in.Log, "button 272 1") && slices.Contains(p.in.Log, "button 273 1")
	})
	tablet(h, &tabletinput.Proximity{Time: 4 * time.Millisecond, Tool: pen, Axes: at(0.5, 0.5)})
	p.seen(t, "leaving proximity releases them", func() bool {
		return slices.Contains(p.in.Log, "button 272 0") && slices.Contains(p.in.Log, "button 273 0")
	})
}

func TestARemovedTabletTakesItsToolsAlong(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	p := join(t, h, "canvas")
	addTablet(h)
	tablet(h, &tabletinput.Proximity{Time: time.Millisecond, Tool: pen, In: true, Axes: at(0.5, 0.5)})
	l := tabletSeat(t, p)
	if !slices.Contains(l.log, "tablet_added") || !slices.Contains(l.log, "tool_added") {
		t.Fatalf("a late tablet seat missed the tablet or its tool: %q", l.log)
	}
	l.log = nil
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.DeviceRemoved(fakeTablet{}) })
	until(t, p.c, "the tablet and the pen removed", &l.log, "tablet_removed", "tool_removed")
}

func TestAPenInProximitySetsTheCursor(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	p := join(t, h, "canvas")
	l := tabletSeat(t, p)
	addTablet(h)
	tablet(h, &tabletinput.Proximity{Time: time.Millisecond, Tool: pen, In: true, Axes: at(0.5, 0.5)})
	p.c.DispatchUntil("the pen in proximity", func() bool { return l.enter != 0 })
	device, err := p.c.CursorShape.GetTabletToolV2(l.tools[0])
	if err != nil {
		t.Fatal(err)
	}
	send(t, func() error { return device.SetShape(l.enter+1000, ccs.WpCursorShapeDeviceV1ShapeCrosshair) }, p.c.Display.Flush)
	p.c.Sync()
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if got := c.Seat().Cursor(); got == (seat.ShapeCursor{Shape: "crosshair"}) {
			return errors.New("a stale serial set the pen's cursor")
		}
		return nil
	})
	send(t, func() error { return device.SetShape(l.enter, ccs.WpCursorShapeDeviceV1ShapeCrosshair) }, p.c.Display.Flush)
	h.Eventually("the pen's shape", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Seat().Cursor() == seat.ShapeCursor{Shape: "crosshair"}
	})
	send(t, func() error { return l.tools[0].SetCursor(l.enter, nil, 0, 0) }, p.c.Display.Flush)
	h.Eventually("the pen's cursor hidden", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Seat().Cursor() == seat.HiddenCursor{}
	})
}
