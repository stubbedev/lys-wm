package session

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

func probe(available bool, def float64, status int32) (*knob[float64], *[]float64) {
	var set []float64
	return &knob[float64]{name: "accel_speed", available: available, def: func() float64 { return def }, set: func(v float64) int32 {
		set = append(set, v)
		return status
	}}, &set
}

func TestKnobLayers(t *testing.T) {
	for _, c := range []struct {
		name            string
		class, override *float64
		want            float64
	}{
		{"default without config", nil, nil, -0.25},
		{"class over default", new(0.5), nil, 0.5},
		{"device over class", new(0.5), new(-1.0), -1},
		{"device alone", nil, new(1.0), 1},
	} {
		k, set := probe(true, -0.25, configStatusSuccess)
		if err := k.apply("mouse", c.class, c.override); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(*set) != 1 || (*set)[0] != c.want {
			t.Fatalf("%s: set %v, want [%g]", c.name, *set, c.want)
		}
	}
}

func TestKnobUnavailable(t *testing.T) {
	k, set := probe(false, 0, configStatusSuccess)
	if err := k.apply("keyboard", new(0.5), nil); err != nil {
		t.Fatalf("a class setting on a device without it: %v", err)
	}
	err := k.apply("keyboard", nil, new(0.5))
	if err == nil || !strings.Contains(err.Error(), `"keyboard" has no accel_speed`) {
		t.Fatalf("a device setting the device lacks: %v", err)
	}
	if len(*set) != 0 {
		t.Fatalf("set %v on a device without the setting", *set)
	}
}

func TestKnobRejected(t *testing.T) {
	k, _ := probe(true, 0, 1)
	err := k.apply("touchpad", new(2.0), nil)
	if err == nil || !strings.Contains(err.Error(), `"touchpad" rejects accel_speed 2`) {
		t.Fatalf("a rejected value: %v", err)
	}
}

type fakeSettings struct {
	tap, drag, natural, dwt, middle, left, lock int32
	tapMap                                      TapButtonMap
	click                                       ClickMethod
	scroll                                      ScrollMethod
	button                                      uint32
	profile                                     accelKind
	step                                        float64
	points                                      []float64
	speed                                       float64
	send                                        sendEvents
	calibration                                 Calibration
	area                                        Area
}

const fakeDevice = "fake tablet touchpad"

func fakeConfigurable(def fakeSettings) (*inputReader, *fakeSettings) {
	now := def
	now.points = slices.Clone(def.points)
	yes := func(uintptr) int32 { return 1 }
	flag := func(field *int32) func(uintptr, int32) int32 {
		return func(_ uintptr, v int32) int32 {
			*field = v
			return configStatusSuccess
		}
	}
	var pending fakeSettings
	in := &inputReader{devices: map[uintptr]*InputDevice{}, filling: &inputBatch{}, queue: &inputQueue{}, fn: libinputFuncs{
		eventDevice:  func(ev uintptr) uintptr { return ev },
		eventType:    func(uintptr) int32 { return evDeviceAdded },
		deviceName:   func(uintptr) string { return fakeDevice },
		deviceRef:    func(dev uintptr) uintptr { return dev },
		deviceHasCap: func(_ uintptr, c int32) int32 { return b2i(c == capPointer) },
		pad:          padFuncs{deviceGroup: func(uintptr) uintptr { return 0 }},
		config: configFuncs{
			tapFingers: func(uintptr) int32 { return 3 },
			tap:        flag(&now.tap), tapDefault: func(uintptr) int32 { return def.tap },
			dragLock: flag(&now.drag), dragDefault: func(uintptr) int32 { return def.drag },
			tapMap: func(_ uintptr, m TapButtonMap) int32 {
				now.tapMap = m
				return configStatusSuccess
			},
			tapMapDefault: func(uintptr) TapButtonMap { return def.tapMap },
			hasNatural:    yes, natural: flag(&now.natural), naturalDefault: func(uintptr) int32 { return def.natural },
			hasDWT: yes, dwt: flag(&now.dwt), dwtDefault: func(uintptr) int32 { return def.dwt },
			hasMiddle: yes, middle: flag(&now.middle), middleDefault: func(uintptr) int32 { return def.middle },
			hasLeftHanded: yes, leftHanded: flag(&now.left), leftHandedDefault: func(uintptr) int32 { return def.left },
			clickMethods: func(uintptr) ClickMethod { return ClickButtonAreas | ClickFinger },
			clickMethod: func(_ uintptr, m ClickMethod) int32 {
				now.click = m
				return configStatusSuccess
			},
			clickDefault:  func(uintptr) ClickMethod { return def.click },
			scrollMethods: func(uintptr) ScrollMethod { return ScrollTwoFinger | ScrollEdge | ScrollOnButtonDown },
			scrollMethod: func(_ uintptr, m ScrollMethod) int32 {
				now.scroll = m
				return configStatusSuccess
			},
			scrollDefault: func(uintptr) ScrollMethod { return def.scroll },
			scrollButton: func(_ uintptr, b uint32) int32 {
				now.button = b
				return configStatusSuccess
			},
			scrollButtonDefault: func(uintptr) uint32 { return def.button },
			buttonLock:          flag(&now.lock), buttonLockDefault: func(uintptr) int32 { return def.lock },
			hasAccel: yes,
			accelProfile: func(_ uintptr, k accelKind) int32 {
				now.profile, now.step, now.points = k, 0, nil
				return configStatusSuccess
			},
			profileDefault: func(uintptr) accelKind { return def.profile },
			accelSpeed: func(_ uintptr, v float64) int32 {
				now.speed = v
				return configStatusSuccess
			},
			speedDefault: func(uintptr) float64 { return def.speed },
			accelCreate: func(k accelKind) uintptr {
				pending = fakeSettings{profile: k}
				return 1
			},
			accelPoints: func(_ uintptr, typ int32, step float64, n uintptr, points *float64) int32 {
				if typ != accelTypeFallback {
					return 2
				}
				pending.step, pending.points = step, slices.Clone(unsafe.Slice(points, n))
				return configStatusSuccess
			},
			accelApply: func(uintptr, uintptr) int32 {
				now.profile, now.step, now.points = pending.profile, pending.step, pending.points
				return configStatusSuccess
			},
			accelDestroy: func(uintptr) { pending = fakeSettings{} },
			sendModes:    func(uintptr) sendEvents { return sendEventsDisabledOnExternalMouse | 1 },
			sendMode: func(_ uintptr, m sendEvents) int32 {
				now.send = m
				return configStatusSuccess
			},
			sendDefault:    func(uintptr) sendEvents { return def.send },
			hasCalibration: yes,
			calibration: func(_ uintptr, m *float32) int32 {
				now.calibration = Calibration(unsafe.Slice(m, 6))
				return configStatusSuccess
			},
			calibrationDefault: func(_ uintptr, m *float32) int32 {
				copy(unsafe.Slice(m, 6), def.calibration[:])
				return 0
			},
			hasArea: yes,
			area: func(_ uintptr, a *Area) int32 {
				now.area = *a
				return configStatusSuccess
			},
			areaDefault: func(uintptr) Area { return def.area },
		},
	}}
	return in, &now
}

func TestEveryLibinputSettingAppliesOnHotplugAndRevertsWhenRemoved(t *testing.T) {
	def := fakeSettings{
		tap: 0, drag: 0, natural: 0, dwt: 1, middle: 0, left: 0, lock: 0,
		tapMap: TapLRM, click: ClickButtonAreas, scroll: ScrollTwoFinger, button: 0x112,
		profile: accelAdaptive, speed: 0, send: sendEventsEnabled,
		calibration: Calibration{1, 0, 0, 0, 1, 0}, area: Area{X2: 1, Y2: 1},
	}
	in, now := fakeConfigurable(def)
	custom, err := AccelCustom(0.5, []float64{0, 1, 2.5})
	if err != nil {
		t.Fatal(err)
	}
	area, err := NewArea(0.25, 0, 0.5, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	calibration, err := NewCalibration([6]float64{0, 1, 0, -1, 0, 1})
	if err != nil {
		t.Fatal(err)
	}
	in.config = InputConfig{
		Touchpad: DeviceConfig{Tap: new(true), TapButtonMap: new(TapLMR), DisabledOnExternalMouse: new(true)},
		Devices: map[string]NamedDeviceConfig{fakeDevice: {
			DeviceConfig: DeviceConfig{
				DragLock: new(true), NaturalScroll: new(true), DisableWhileTyping: new(false), MiddleEmulation: new(true),
				LeftHanded: new(true), ScrollMethod: new(ScrollOnButtonDown), ScrollButton: new(uint32(0x113)), ScrollButtonLock: new(true),
				ClickMethod: new(ClickFinger), AccelProfile: &custom, AccelSpeed: new(0.5),
			},
			Calibration: &calibration,
			Area:        &area,
		}},
	}
	in.event(7)
	if r := in.filling.records; len(r) != 1 || r[0].kind != deviceAdded || r[0].dev.Name() != fakeDevice {
		t.Fatalf("hotplug recorded %+v, want one added %s and no configuration error", r, fakeDevice)
	}
	want := fakeSettings{
		tap: 1, drag: 1, natural: 1, dwt: 0, middle: 1, left: 1, lock: 1,
		tapMap: TapLMR, click: ClickFinger, scroll: ScrollOnButtonDown, button: 0x113,
		profile: accelCustom, step: 0.5, points: []float64{0, 1, 2.5}, speed: 0.5, send: sendEventsDisabledOnExternalMouse,
		calibration: calibration, area: Area{X1: 0.25, X2: 0.75, Y2: 0.5},
	}
	if !reflect.DeepEqual(*now, want) {
		t.Fatalf("after hotplug the device holds\n%+v\nwant\n%+v", *now, want)
	}
	if err := in.reconfigure(InputConfig{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*now, def) {
		t.Fatalf("after the settings left the config the device holds\n%+v\nwant its defaults\n%+v", *now, def)
	}
	flat := AccelFlat
	if err := in.reconfigure(InputConfig{Pointer: DeviceConfig{AccelProfile: &flat}, Touchpad: DeviceConfig{AccelProfile: &custom}}); err != nil {
		t.Fatal(err)
	}
	if now.profile != accelCustom || now.step != 0.5 {
		t.Fatalf("a touchpad takes input.touchpad over input.pointer: %+v", *now)
	}
}

func TestCustomAccelerationIsValidatedBeforeLibinputSeesIt(t *testing.T) {
	for _, c := range []struct {
		step   float64
		points []float64
		want   string
	}{
		{0, []float64{0, 1}, "step 0"},
		{1, []float64{0}, "1 points"},
		{1, make([]float64, 65), "65 points"},
		{1, []float64{0, -1}, "point 1 is -1"},
		{1, []float64{0, math.NaN()}, "point 1 is NaN"},
	} {
		if _, err := AccelCustom(c.step, c.points); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("step %g points %v: %v, want %q", c.step, c.points, err, c.want)
		}
	}
	if _, err := NewArea(0.5, 0, 0.75, 1); err == nil {
		t.Error("a region past the right edge was accepted")
	}
	if _, err := NewCalibration([6]float64{1, 0, math.Inf(1), 0, 1, 0}); err == nil {
		t.Error("an infinite calibration was accepted")
	}
}
