package session

import (
	"reflect"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
)

func fakePad(ring, strip float64) *inputReader {
	in := &inputReader{}
	f := &in.fn.pad
	f.event = func(ev uintptr) uintptr { return ev }
	f.time = func(uintptr) uint64 { return 3000 }
	f.button = func(uintptr) uint32 { return 5 }
	f.buttonState = func(uintptr) int32 { return 1 }
	f.ringPosition = func(uintptr) float64 { return ring }
	f.ringNumber = func(uintptr) uint32 { return 1 }
	f.ringSource = func(uintptr) int32 { return padSourceFinger }
	f.stripPos = func(uintptr) float64 { return strip }
	f.stripNumber = func(uintptr) uint32 { return 0 }
	f.stripSource = func(uintptr) int32 { return 1 }
	f.dialDelta = func(uintptr) float64 { return -119.6 }
	f.dialNumber = func(uintptr) uint32 { return 0 }
	f.mode = func(uintptr) uint32 { return 2 }
	f.modeGroup = func(uintptr) uintptr { return 40 }
	f.groupIndex = func(g uintptr) uint32 { return uint32(g / 40) }
	f.numButtons = func(uintptr) int32 { return 4 }
	f.numRings = func(uintptr) int32 { return 2 }
	f.numStrips = func(uintptr) int32 { return 1 }
	f.numDials = func(uintptr) int32 { return -1 }
	f.numGroups = func(uintptr) int32 { return 2 }
	f.group = func(_ uintptr, i uint32) uintptr { return uintptr(i) * 40 }
	f.groupModes = func(g uintptr) uint32 { return uint32(g/40) + 1 }
	f.groupMode = func(uintptr) uint32 { return 0 }
	f.hasButton = func(g uintptr, n uint32) int32 { return b2i(n%2 == uint32(g/40)) }
	f.hasRing = func(g uintptr, n uint32) int32 { return b2i(n == uint32(g/40)) }
	f.hasStrip = func(g uintptr, _ uint32) int32 { return b2i(g == 0) }
	f.hasDial = func(uintptr, uint32) int32 { return 1 }
	return in
}

func b2i(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func TestAPadsLayoutComesFromItsModeGroups(t *testing.T) {
	got := fakePad(0, 0).padLayout(9)
	want := tabletinput.PadLayout{Buttons: 4, Groups: []tabletinput.PadGroup{
		{Buttons: []uint32{0, 2}, Rings: []uint32{0}, Strips: []uint32{0}, Modes: 1},
		{Buttons: []uint32{1, 3}, Rings: []uint32{1}, Modes: 2},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("layout %+v, want %+v", got, want)
	}
}

func TestTabletPadEventsCarryWhatLibinputSaid(t *testing.T) {
	ms := 3 * time.Millisecond
	cases := []struct {
		typ         int32
		ring, strip float64
		want        backend.Event
	}{
		{evTabletPadButton, 0, 0, &tabletinput.PadButton{Time: ms, Button: 5, Pressed: true, Group: 1, Mode: 2}},
		{evTabletPadRing, 45, 0, &tabletinput.PadRing{Time: ms, Ring: 1, Degrees: 45, Finger: true, Group: 1, Mode: 2}},
		{evTabletPadRing, -1, 0, &tabletinput.PadRing{Time: ms, Ring: 1, Stopped: true, Finger: true, Group: 1, Mode: 2}},
		{evTabletPadStrip, 0, 0.25, &tabletinput.PadStrip{Time: ms, Position: 0.25, Group: 1, Mode: 2}},
		{evTabletPadStrip, 0, -1, &tabletinput.PadStrip{Time: ms, Stopped: true, Group: 1, Mode: 2}},
		{evTabletPadDial, 0, 0, &tabletinput.PadDial{Time: ms, Delta: -120, Group: 1, Mode: 2}},
	}
	for _, tc := range cases {
		got := fakePad(tc.ring, tc.strip).translate(nil, &translated{}, &InputDevice{}, 1, tc.typ)
		if len(got) != 1 || !reflect.DeepEqual(got[0], tc.want) {
			t.Errorf("event %d: %+v, want %+v", tc.typ, got, tc.want)
		}
	}
}

func TestTranslatingTabletPadEventsAllocatesNothing(t *testing.T) {
	in := fakePad(10, 0.5)
	d := &InputDevice{}
	var into translated
	for _, typ := range []int32{evTabletPadButton, evTabletPadRing, evTabletPadStrip, evTabletPadDial} {
		in.out = in.translate(in.out[:0], &into, d, 1, typ)
		if n := testing.AllocsPerRun(100, func() {
			into.reset()
			in.out = in.translate(in.out[:0], &into, d, 1, typ)
		}); n != 0 {
			t.Errorf("event %d: %v allocations", typ, n)
		}
	}
}
