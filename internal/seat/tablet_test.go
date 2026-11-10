package seat

import (
	"errors"
	"slices"
	"testing"
	"time"

	ctab "github.com/stubbedev/lys-wm/internal/protocol/client/tablet"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const (
	everyToolCapability = tabletinput.CanTilt | tabletinput.CanPressure | tabletinput.CanDistance |
		tabletinput.CanRotation | tabletinput.CanSlider | tabletinput.CanWheel
	everyToolAxis = tabletinput.AxisPressure | tabletinput.AxisDistance | tabletinput.AxisTiltX | tabletinput.AxisTiltY |
		tabletinput.AxisRotation | tabletinput.AxisSlider | tabletinput.AxisWheel
	stylusButton = 0x14b
)

type padCounts struct {
	buttons, switches, rings, strips, dials int
}

type padSeatEvents struct {
	*tabletSeatEvents
	pad *padCounts
}

func (e padSeatEvents) PadAdded(*ctab.ZwpTabletSeatV2, *ctab.ZwpTabletPadV2) (ctab.ZwpTabletPadV2Handler, error) {
	return padEvents{e.pad}, nil
}

type padEvents struct{ *padCounts }

func (e padEvents) Group(*ctab.ZwpTabletPadV2, *ctab.ZwpTabletPadGroupV2) (ctab.ZwpTabletPadGroupV2Handler, error) {
	return padGroupEvents(e), nil
}
func (padEvents) Path(*ctab.ZwpTabletPadV2, string) error    { return nil }
func (padEvents) Buttons(*ctab.ZwpTabletPadV2, uint32) error { return nil }
func (padEvents) Done(*ctab.ZwpTabletPadV2) error            { return nil }

func (e padEvents) Button(*ctab.ZwpTabletPadV2, uint32, uint32, ctab.ZwpTabletPadV2ButtonState) error {
	e.buttons++
	return nil
}

func (padEvents) Enter(*ctab.ZwpTabletPadV2, uint32, *ctab.ZwpTabletV2, *cw.WlSurface) error {
	return nil
}
func (padEvents) Leave(*ctab.ZwpTabletPadV2, uint32, *cw.WlSurface) error { return nil }
func (padEvents) Removed(*ctab.ZwpTabletPadV2) error                      { return nil }

type padGroupEvents struct{ *padCounts }

func (padGroupEvents) Buttons(*ctab.ZwpTabletPadGroupV2, []byte) error { return nil }

func (e padGroupEvents) Ring(*ctab.ZwpTabletPadGroupV2, *ctab.ZwpTabletPadRingV2) (ctab.ZwpTabletPadRingV2Handler, error) {
	return ringEvents(e), nil
}

func (e padGroupEvents) Strip(*ctab.ZwpTabletPadGroupV2, *ctab.ZwpTabletPadStripV2) (ctab.ZwpTabletPadStripV2Handler, error) {
	return stripEvents(e), nil
}

func (e padGroupEvents) Dial(*ctab.ZwpTabletPadGroupV2, *ctab.ZwpTabletPadDialV2) (ctab.ZwpTabletPadDialV2Handler, error) {
	return dialEvents(e), nil
}
func (padGroupEvents) Modes(*ctab.ZwpTabletPadGroupV2, uint32) error { return nil }
func (padGroupEvents) Done(*ctab.ZwpTabletPadGroupV2) error          { return nil }

func (e padGroupEvents) ModeSwitch(*ctab.ZwpTabletPadGroupV2, uint32, uint32, uint32) error {
	e.switches++
	return nil
}

type ringEvents struct{ *padCounts }

func (ringEvents) Source(*ctab.ZwpTabletPadRingV2, ctab.ZwpTabletPadRingV2Source) error { return nil }

func (ringEvents) Angle(*ctab.ZwpTabletPadRingV2, wire.Fixed) error { return nil }

func (ringEvents) Stop(*ctab.ZwpTabletPadRingV2) error { return nil }

func (e ringEvents) Frame(*ctab.ZwpTabletPadRingV2, uint32) error {
	e.rings++
	return nil
}

type stripEvents struct{ *padCounts }

func (stripEvents) Source(*ctab.ZwpTabletPadStripV2, ctab.ZwpTabletPadStripV2Source) error {
	return nil
}
func (stripEvents) Position(*ctab.ZwpTabletPadStripV2, uint32) error { return nil }
func (stripEvents) Stop(*ctab.ZwpTabletPadStripV2) error             { return nil }

func (e stripEvents) Frame(*ctab.ZwpTabletPadStripV2, uint32) error {
	e.strips++
	return nil
}

type dialEvents struct{ *padCounts }

func (dialEvents) Delta(*ctab.ZwpTabletPadDialV2, int32) error { return nil }

func (e dialEvents) Frame(*ctab.ZwpTabletPadDialV2, uint32) error {
	e.dials++
	return nil
}

func (c *client) tabletManager() *ctab.ZwpTabletManagerV2 {
	c.t.Helper()
	manager, err := ctab.NewZwpTabletManagerV2(c.d, tabletVersion)
	if err != nil {
		c.t.Fatal(err)
	}
	c.bind("zwp_tablet_manager_v2", manager)
	return manager
}

func TestTabletToolAndPadEventsAllocateNothing(t *testing.T) {
	e := setup(t)
	c := e.connect()
	manager := c.tabletManager()
	c.surface()
	device, pad := new(int), new(int)
	tool := &tabletinput.Tool{Type: tabletinput.Pen, Caps: everyToolCapability}
	layout := tabletinput.PadLayout{Buttons: 1, Groups: []tabletinput.PadGroup{
		{Buttons: []uint32{0}, Rings: []uint32{0}, Strips: []uint32{0}, Dials: []uint32{0}, Modes: 2},
	}}
	e.do(
		func() error { return e.seat.AddTablet(device, 1, "tablet", tabletinput.ID{}) },
		func() error { return e.seat.AddPad(pad, 1, layout) },
		func() error { return e.seat.TabletTool(tool) },
	)
	tools, pads := &tabletSeatEvents{}, &padCounts{}
	if _, err := manager.GetTabletSeat(padSeatEvents{tools, pads}, c.seat); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	e.focus(0)

	axes := tabletinput.Axes{Changed: everyToolAxis, Pressure: 0.5, Distance: 0.5, TiltX: 1, TiltY: 1, Rotation: 1, Slider: 0.5, Wheel: 1, WheelClicks: 1}
	button := &tabletinput.PadButton{Time: time.Second, Pressed: true}
	ring := &tabletinput.PadRing{Time: time.Second, Degrees: 90, Finger: true}
	strip := &tabletinput.PadStrip{Time: time.Second, Position: 0.5, Finger: true}
	dial := &tabletinput.PadDial{Time: time.Second, Delta: 120}
	const runs = 100
	var toolAllocs, padAllocs float64
	e.on(func() {
		target := e.surf.list[0]
		check := func(errs ...error) {
			if err := errors.Join(errs...); err != nil {
				t.Error(err)
			}
		}
		toolAllocs = testing.AllocsPerRun(runs, func() {
			check(
				e.seat.TabletToolMotion(time.Second, device, tool, target, 1, 2),
				e.seat.TabletToolAxes(tool, axes),
				e.seat.TabletToolTip(tool, true),
				e.seat.TabletToolButton(tool, stylusButton, true),
				e.seat.TabletToolFrame(time.Second, tool),
				e.seat.TabletToolButton(tool, stylusButton, false),
				e.seat.TabletToolTip(tool, false),
				e.seat.TabletToolFrame(time.Second, tool),
			)
		})
		mode := uint32(0)
		padAllocs = testing.AllocsPerRun(runs, func() {
			mode ^= 1
			button.Mode, ring.Mode, strip.Mode, dial.Mode = mode, mode, mode, mode
			check(e.seat.PadButton(pad, button), e.seat.PadRing(pad, ring), e.seat.PadStrip(pad, strip), e.seat.PadDial(pad, dial))
		})
	})
	if toolAllocs != 0 {
		t.Errorf("a tablet tool's motion, axes, tip, button and frames allocated %v times", toolAllocs)
	}
	if padAllocs != 0 {
		t.Errorf("a pad's button, ring, strip and dial with a mode switch allocated %v times", padAllocs)
	}

	c.roundtrip()
	const sent = runs + 1
	seen := func(event string) int {
		n := 0
		for _, l := range tools.log {
			if l == event {
				n++
			}
		}
		return n
	}
	for _, w := range []struct {
		event string
		times int
	}{{"proximity_in", 1}, {"motion 1 2", sent}, {"down", sent}, {"up", sent}, {"button 331 1", sent}, {"button 331 0", sent}, {"frame 1000", 2 * sent}} {
		if got := seen(w.event); got != w.times {
			t.Errorf("the tool's client saw %q %d times, want %d: what was measured must have been sent", w.event, got, w.times)
		}
	}
	if want := (padCounts{buttons: sent, switches: sent + 1, rings: sent, strips: sent, dials: sent}); *pads != want {
		t.Errorf("the pad's client saw %+v, want %+v: what was measured must have been sent", *pads, want)
	}
}

func TestAToolStaysOutOfProximityOfATabletItsClientDestroyed(t *testing.T) {
	e := setup(t)
	c := e.connect()
	manager := c.tabletManager()
	c.surface()
	device, tool := "tablet", &tabletinput.Tool{Type: tabletinput.Pen}
	e.do(
		func() error { return e.seat.AddTablet(device, 1, "tablet", tabletinput.ID{}) },
		func() error { return e.seat.TabletTool(tool) },
	)
	without := c.tabletSeat(manager)
	if len(without.tablets) != 1 {
		t.Fatalf("the tablet seat was told of %d tablets, want 1", len(without.tablets))
	}
	if err := without.tablets[0].Destroy(); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	var focused bool
	stroke := func(at time.Duration, x, y float64) {
		t.Helper()
		e.do(
			func() error { return e.seat.TabletToolMotion(at, device, tool, e.surf.list[0], x, y) },
			func() error { return e.seat.TabletToolFrame(at, tool) },
		)
		e.on(func() { focused = e.seat.TabletToolFocus(tool) != nil })
		c.roundtrip()
	}
	stroke(time.Second, 1, 2)
	if len(without.log) != 0 {
		t.Fatalf("a tool that cannot be told which tablet it is near saw %v: proximity_in names the tablet, and nothing may come before it", without.log)
	}
	if focused {
		t.Fatal("the tool is focused on a surface whose client holds no tablet for it: the compositor would send it no pointer either")
	}

	with := c.tabletSeat(manager)
	stroke(2*time.Second, 3, 4)
	if want := []string{"proximity_in", "motion 3 4", "frame 2000"}; !slices.Equal(with.log, want) {
		t.Fatalf("the tool of a tablet seat that still holds the tablet saw %v, want %v", with.log, want)
	}
	if len(without.log) != 0 {
		t.Fatalf("the tool of the tablet seat without the tablet saw %v beside one that is near", without.log)
	}
	if !focused {
		t.Fatal("the tool is not focused on the surface it is in proximity of")
	}
}

func TestAToolWhoseLastResourceInProximityIsDestroyedLosesItsFocus(t *testing.T) {
	e := setup(t)
	c := e.connect()
	manager := c.tabletManager()
	c.surface()
	device, tool := "tablet", &tabletinput.Tool{Type: tabletinput.Pen}
	e.do(
		func() error { return e.seat.AddTablet(device, 1, "tablet", tabletinput.ID{}) },
		func() error { return e.seat.TabletTool(tool) },
	)
	seat := c.tabletSeat(manager)
	e.do(
		func() error { return e.seat.TabletToolMotion(time.Second, device, tool, e.surf.list[0], 1, 2) },
		func() error { return e.seat.TabletToolFrame(time.Second, tool) },
	)
	c.roundtrip()
	if want := []string{"proximity_in", "motion 1 2", "frame 1000"}; !slices.Equal(seat.log, want) {
		t.Fatalf("the tool saw %v, want %v", seat.log, want)
	}
	if err := seat.tools[0].Destroy(); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	var focus bool
	e.on(func() { focus = e.seat.TabletToolFocus(tool) != nil })
	if focus {
		t.Fatal("the tool is still focused on a surface whose client destroyed the only object it was in proximity through")
	}
}
