package session

import (
	"reflect"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
)

func fakeTablet(proximity, tip int32, changed tabletinput.Axis) *inputReader {
	in := &inputReader{}
	f := &in.fn.tablet
	id := func(ev uintptr) uintptr { return ev }
	value := func(v float64) func(uintptr) float64 { return func(uintptr) float64 { return v } }
	f.event, f.tool, f.toolRef = id, func(uintptr) uintptr { return 7 }, id
	f.toolUnref = id
	f.time = func(uintptr) uint64 { return 2000 }
	for i, a := range changedAxes {
		f.changed[i] = func(uintptr) int32 {
			if changed.Any(a.axis) {
				return 1
			}
			return 0
		}
	}
	f.x = func(uintptr, uint32) float64 { return 0.25 }
	f.y = func(uintptr, uint32) float64 { return 0.75 }
	f.pressure, f.distance, f.tiltX, f.tiltY = value(0.5), value(0.1), value(-30), value(40)
	f.rotation, f.slider, f.wheel = value(90), value(-0.5), value(15)
	f.wheelClicks = func(uintptr) int32 { return 1 }
	f.proximity = func(uintptr) int32 { return proximity }
	f.tip = func(uintptr) int32 { return tip }
	f.button = func(uintptr) uint32 { return 0x14b }
	f.buttonState = func(uintptr) int32 { return 1 }
	f.toolType = func(uintptr) int32 { return 2 }
	f.toolSerial = func(uintptr) uint64 { return 0xabc }
	f.toolID = func(uintptr) uint64 { return 0x802 }
	for i := range f.toolHas {
		f.toolHas[i] = func(uintptr) int32 { return int32(1 - i%2) }
	}
	return in
}

func TestTabletToolEventsCarryWhatLibinputSaid(t *testing.T) {
	ms := 2 * time.Millisecond
	axes := tabletinput.Axes{Changed: tabletinput.AxisX | tabletinput.AxisPressure, X: 0.25, Y: 0.75, Pressure: 0.5, Distance: 0.1, TiltX: -30, TiltY: 40, Rotation: 90, Slider: -0.5, Wheel: 15, WheelClicks: 1}
	tool := &tabletinput.Tool{Type: tabletinput.Eraser, Serial: 0xabc, HardwareID: 0x802, Caps: tabletinput.CanTilt | tabletinput.CanDistance | tabletinput.CanSlider}
	cases := map[int32]backend.Event{
		evTabletToolProximity: &tabletinput.Proximity{Time: ms, Tool: tool, In: true, Axes: axes},
		evTabletToolAxis:      &tabletinput.Motion{Time: ms, Tool: tool, Axes: axes},
		evTabletToolTip:       &tabletinput.Tip{Time: ms, Tool: tool, Down: true, Axes: axes},
		evTabletToolButton:    &tabletinput.Button{Time: ms, Tool: tool, Button: 0x14b, Pressed: true},
	}
	for typ, want := range cases {
		got := fakeTablet(1, 1, axes.Changed).translate(nil, &translated{}, &InputDevice{}, 1, typ)
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("event %d: %+v, want %+v", typ, got, want)
		}
	}
}

func TestATabletToolKeepsItsIdentityAcrossEvents(t *testing.T) {
	in := fakeTablet(1, 0, 0)
	first := in.translate(nil, &translated{}, &InputDevice{}, 1, evTabletToolProximity)[0].(*tabletinput.Proximity).Tool
	second := in.translate(nil, &translated{}, &InputDevice{}, 1, evTabletToolAxis)[0].(*tabletinput.Motion).Tool
	if first != second {
		t.Fatal("one libinput tool became two")
	}
	unrefs := 0
	in.fn.tablet.toolUnref = func(uintptr) uintptr { unrefs++; return 0 }
	in.dropTabletTools()
	if unrefs != 1 || len(in.tools) != 0 {
		t.Fatalf("%d unrefs, %d tools left", unrefs, len(in.tools))
	}
}

func TestTranslatingTabletToolMotionAllocatesNothing(t *testing.T) {
	in := fakeTablet(1, 1, tabletinput.AxisX|tabletinput.AxisY)
	d := &InputDevice{}
	var into translated
	for _, typ := range []int32{evTabletToolProximity, evTabletToolAxis, evTabletToolTip, evTabletToolButton} {
		in.out = in.translate(in.out[:0], &into, d, 1, typ)
		if n := testing.AllocsPerRun(100, func() {
			into.reset()
			in.out = in.translate(in.out[:0], &into, d, 1, typ)
		}); n != 0 {
			t.Errorf("event %d: %v allocations", typ, n)
		}
	}
}
