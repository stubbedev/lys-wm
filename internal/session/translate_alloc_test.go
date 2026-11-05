package session

import (
	"reflect"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
)

func fakeInput() *Libinput {
	id := func(ev uintptr) uintptr { return ev }
	at := func(uintptr) uint64 { return 1000 }
	one := func(uintptr) int32 { return 1 }
	half := func(uintptr, uint32) float64 { return 0.5 }
	return &Libinput{fn: libinputFuncs{
		keyboardEvent: id, keyTime: at, key: func(uintptr) uint32 { return 30 }, keyState: one,
		pointerEvent: id, pointerTime: at, absX: half, absY: half,
		dx: func(uintptr) float64 { return 1 }, dy: func(uintptr) float64 { return 2 },
		dxRaw: func(uintptr) float64 { return 1 }, dyRaw: func(uintptr) float64 { return 2 },
		button: func(uintptr) uint32 { return 0x110 }, buttonState: one,
		hasAxis:     func(uintptr, int32) int32 { return 1 },
		scrollValue: func(uintptr, int32) float64 { return 15 },
		scrollV120:  func(uintptr, int32) float64 { return 120 },
		touchEvent:  id, touchTime: at, touchSlot: func(uintptr) int32 { return 3 }, touchX: half, touchY: half,
	}}
}

var inputKinds = map[string]int32{
	"key": evKeyboardKey, "motion": evPointerMotion, "absolute": evPointerAbsolute, "button": evPointerButton,
	"wheel": evScrollWheel, "finger scroll": evScrollFinger, "continuous scroll": evScrollContinuous,
	"touch down": evTouchDown, "touch motion": evTouchMotion, "touch up": evTouchUp, "touch cancel": evTouchCancel, "touch frame": evTouchFrame,
}

func TestTranslatingKeyButtonAxisAndTouchEventsAllocatesNothing(t *testing.T) {
	in := fakeInput()
	d := &InputDevice{scrollFactor: 1}
	for name, typ := range inputKinds {
		in.out = in.translate(in.out[:0], d, 1, typ)
		if n := testing.AllocsPerRun(100, func() { in.out = in.translate(in.out[:0], d, 1, typ) }); n != 0 {
			t.Errorf("%s: %v allocations per event", name, n)
		}
	}
}

func TestTranslatedEventsCarryWhatLibinputSaid(t *testing.T) {
	in := fakeInput()
	ms := time.Millisecond
	cases := map[int32][]backend.Event{
		evKeyboardKey:     {&backend.Key{Time: ms, Code: 30, Pressed: true}},
		evPointerAbsolute: {&backend.PointerMotionAbsolute{Time: ms, X: 0.5, Y: 0.5}, &backend.PointerFrame{Time: ms}},
		evPointerButton:   {&backend.PointerButton{Time: ms, Button: 0x110, Pressed: true}, &backend.PointerFrame{Time: ms}},
		evScrollWheel: {
			&backend.PointerAxis{Time: ms, Orientation: backend.Vertical, Source: backend.Wheel, Value: 30, Value120: 240},
			&backend.PointerAxis{Time: ms, Orientation: backend.Horizontal, Source: backend.Wheel, Value: 30, Value120: 240},
			&backend.PointerFrame{Time: ms},
		},
		evScrollFinger: {
			&backend.PointerAxis{Time: ms, Orientation: backend.Vertical, Source: backend.Finger, Value: 30},
			&backend.PointerAxis{Time: ms, Orientation: backend.Horizontal, Source: backend.Finger, Value: 30},
			&backend.PointerFrame{Time: ms},
		},
		evTouchDown:   {&backend.TouchDown{Time: ms, ID: 3, X: 0.5, Y: 0.5}},
		evTouchMotion: {&backend.TouchMotion{Time: ms, ID: 3, X: 0.5, Y: 0.5}},
		evTouchUp:     {&backend.TouchUp{Time: ms, ID: 3}},
		evTouchCancel: {&backend.TouchCancel{Time: ms, ID: 3}},
		evTouchFrame:  {&backend.TouchFrame{Time: ms}},
	}
	for typ, want := range cases {
		if got := in.translate(nil, &InputDevice{scrollFactor: 2}, 1, typ); !reflect.DeepEqual(got, want) {
			t.Errorf("event %d: %+v, want %+v", typ, got, want)
		}
	}
}
