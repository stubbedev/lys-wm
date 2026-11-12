package session

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
)

type ClickMethod uint32

const (
	ClickNone        ClickMethod = 0
	ClickButtonAreas ClickMethod = 1 << 0
	ClickFinger      ClickMethod = 1 << 1
)

type ScrollMethod uint32

const (
	ScrollNone         ScrollMethod = 0
	ScrollTwoFinger    ScrollMethod = 1 << 0
	ScrollEdge         ScrollMethod = 1 << 1
	ScrollOnButtonDown ScrollMethod = 1 << 2
)

type TapButtonMap uint32

const (
	TapLRM TapButtonMap = 0
	TapLMR TapButtonMap = 1
)

func (m TapButtonMap) String() string {
	if m == TapLMR {
		return "lmr"
	}
	return "lrm"
}

type accelKind uint32

const (
	accelFlat     accelKind = 1 << 0
	accelAdaptive accelKind = 1 << 1
	accelCustom   accelKind = 1 << 2
)

type accelCurve struct {
	step   float64
	points []float64
}

type AccelProfile struct {
	kind  accelKind
	curve *accelCurve
}

var (
	AccelFlat     = AccelProfile{kind: accelFlat}
	AccelAdaptive = AccelProfile{kind: accelAdaptive}
)

const (
	MinAccelPoints = 2
	MaxAccelPoints = 64
	MaxAccelStep   = 10000
	MaxAccelPoint  = 10000
)

func AccelCustom(step float64, points []float64) (AccelProfile, error) {
	if !(step > 0 && step <= MaxAccelStep) {
		return AccelProfile{}, fmt.Errorf("step %g is not above 0 and up to %d", step, MaxAccelStep)
	}
	if len(points) < MinAccelPoints || len(points) > MaxAccelPoints {
		return AccelProfile{}, fmt.Errorf("%d points, not %d to %d", len(points), MinAccelPoints, MaxAccelPoints)
	}
	for i, p := range points {
		if !(p >= 0 && p <= MaxAccelPoint) {
			return AccelProfile{}, fmt.Errorf("point %d is %g, not 0 to %d", i, p, MaxAccelPoint)
		}
	}
	return AccelProfile{kind: accelCustom, curve: &accelCurve{step: step, points: slices.Clone(points)}}, nil
}

func (p AccelProfile) String() string {
	switch {
	case p.curve != nil:
		return fmt.Sprintf("custom (step %g, points %v)", p.curve.step, p.curve.points)
	case p.kind == accelFlat:
		return "flat"
	case p.kind == accelAdaptive:
		return "adaptive"
	}
	return fmt.Sprintf("profile %d", p.kind)
}

type sendEvents uint32

const (
	sendEventsEnabled                 sendEvents = 0
	sendEventsDisabledOnExternalMouse sendEvents = 1 << 1
)

type Calibration [6]float32

func NewCalibration(m [6]float64) (Calibration, error) {
	var c Calibration
	for i, v := range m {
		c[i] = float32(v)
	}
	if slices.ContainsFunc(c[:], func(v float32) bool { return math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) }) {
		return Calibration{}, fmt.Errorf("%v is not six finite numbers", m)
	}
	return c, nil
}

type Area struct {
	X1, Y1, X2, Y2 float64
}

func NewArea(x, y, w, h float64) (Area, error) {
	if !(x >= 0 && y >= 0 && w > 0 && h > 0 && x+w <= 1 && y+h <= 1) {
		return Area{}, fmt.Errorf("[%g, %g, %g, %g] is not a rectangle of positive size inside 0 to 1", x, y, w, h)
	}
	return Area{X1: x, Y1: y, X2: x + w, Y2: y + h}, nil
}

type DeviceConfig struct {
	Tap, DragLock, NaturalScroll, DisableWhileTyping, MiddleEmulation *bool
	LeftHanded, ScrollButtonLock, DisabledOnExternalMouse             *bool
	ClickMethod                                                       *ClickMethod
	ScrollMethod                                                      *ScrollMethod
	ScrollButton                                                      *uint32
	TapButtonMap                                                      *TapButtonMap
	AccelProfile                                                      *AccelProfile
	AccelSpeed, ScrollFactor                                          *float64
}

type NamedDeviceConfig struct {
	DeviceConfig
	Calibration *Calibration
	Area        *Area
}

type InputConfig struct {
	Pointer, Touchpad DeviceConfig
	Devices           map[string]NamedDeviceConfig
}

type configFuncs struct {
	tapFingers          func(dev uintptr) int32
	tap                 func(dev uintptr, enable int32) int32
	tapDefault          func(dev uintptr) int32
	dragLock            func(dev uintptr, enable int32) int32
	dragDefault         func(dev uintptr) int32
	tapMap              func(dev uintptr, m TapButtonMap) int32
	tapMapDefault       func(dev uintptr) TapButtonMap
	hasNatural          func(dev uintptr) int32
	natural             func(dev uintptr, enable int32) int32
	naturalDefault      func(dev uintptr) int32
	hasDWT              func(dev uintptr) int32
	dwt                 func(dev uintptr, enable int32) int32
	dwtDefault          func(dev uintptr) int32
	hasMiddle           func(dev uintptr) int32
	middle              func(dev uintptr, enable int32) int32
	middleDefault       func(dev uintptr) int32
	hasLeftHanded       func(dev uintptr) int32
	leftHanded          func(dev uintptr, enable int32) int32
	leftHandedDefault   func(dev uintptr) int32
	clickMethods        func(dev uintptr) ClickMethod
	clickMethod         func(dev uintptr, method ClickMethod) int32
	clickDefault        func(dev uintptr) ClickMethod
	scrollMethods       func(dev uintptr) ScrollMethod
	scrollMethod        func(dev uintptr, method ScrollMethod) int32
	scrollDefault       func(dev uintptr) ScrollMethod
	scrollButton        func(dev uintptr, button uint32) int32
	scrollButtonDefault func(dev uintptr) uint32
	buttonLock          func(dev uintptr, enable int32) int32
	buttonLockDefault   func(dev uintptr) int32
	hasAccel            func(dev uintptr) int32
	accelProfile        func(dev uintptr, profile accelKind) int32
	profileDefault      func(dev uintptr) accelKind
	accelSpeed          func(dev uintptr, speed float64) int32
	speedDefault        func(dev uintptr) float64
	accelCreate         func(profile accelKind) uintptr
	accelPoints         func(accel uintptr, typ int32, step float64, n uintptr, points *float64) int32
	accelApply          func(dev uintptr, accel uintptr) int32
	accelDestroy        func(accel uintptr)
	sendModes           func(dev uintptr) sendEvents
	sendMode            func(dev uintptr, mode sendEvents) int32
	sendDefault         func(dev uintptr) sendEvents
	hasCalibration      func(dev uintptr) int32
	calibration         func(dev uintptr, m *float32) int32
	calibrationDefault  func(dev uintptr, m *float32) int32
	hasArea             func(dev uintptr) int32
	area                func(dev uintptr, a *Area) int32
	areaDefault         func(dev uintptr) Area
}

func (r *inputReader) bindConfig(path string) error {
	f := &r.fn.config
	return r.register(path, []libSymbol{
		{&f.tapFingers, "libinput_device_config_tap_get_finger_count"},
		{&f.tap, "libinput_device_config_tap_set_enabled"},
		{&f.tapDefault, "libinput_device_config_tap_get_default_enabled"},
		{&f.dragLock, "libinput_device_config_tap_set_drag_lock_enabled"},
		{&f.dragDefault, "libinput_device_config_tap_get_default_drag_lock_enabled"},
		{&f.tapMap, "libinput_device_config_tap_set_button_map"},
		{&f.tapMapDefault, "libinput_device_config_tap_get_default_button_map"},
		{&f.hasNatural, "libinput_device_config_scroll_has_natural_scroll"},
		{&f.natural, "libinput_device_config_scroll_set_natural_scroll_enabled"},
		{&f.naturalDefault, "libinput_device_config_scroll_get_default_natural_scroll_enabled"},
		{&f.hasDWT, "libinput_device_config_dwt_is_available"},
		{&f.dwt, "libinput_device_config_dwt_set_enabled"},
		{&f.dwtDefault, "libinput_device_config_dwt_get_default_enabled"},
		{&f.hasMiddle, "libinput_device_config_middle_emulation_is_available"},
		{&f.middle, "libinput_device_config_middle_emulation_set_enabled"},
		{&f.middleDefault, "libinput_device_config_middle_emulation_get_default_enabled"},
		{&f.hasLeftHanded, "libinput_device_config_left_handed_is_available"},
		{&f.leftHanded, "libinput_device_config_left_handed_set"},
		{&f.leftHandedDefault, "libinput_device_config_left_handed_get_default"},
		{&f.clickMethods, "libinput_device_config_click_get_methods"},
		{&f.clickMethod, "libinput_device_config_click_set_method"},
		{&f.clickDefault, "libinput_device_config_click_get_default_method"},
		{&f.scrollMethods, "libinput_device_config_scroll_get_methods"},
		{&f.scrollMethod, "libinput_device_config_scroll_set_method"},
		{&f.scrollDefault, "libinput_device_config_scroll_get_default_method"},
		{&f.scrollButton, "libinput_device_config_scroll_set_button"},
		{&f.scrollButtonDefault, "libinput_device_config_scroll_get_default_button"},
		{&f.buttonLock, "libinput_device_config_scroll_set_button_lock"},
		{&f.buttonLockDefault, "libinput_device_config_scroll_get_default_button_lock"},
		{&f.hasAccel, "libinput_device_config_accel_is_available"},
		{&f.accelProfile, "libinput_device_config_accel_set_profile"},
		{&f.profileDefault, "libinput_device_config_accel_get_default_profile"},
		{&f.accelSpeed, "libinput_device_config_accel_set_speed"},
		{&f.speedDefault, "libinput_device_config_accel_get_default_speed"},
		{&f.accelCreate, "libinput_config_accel_create"},
		{&f.accelPoints, "libinput_config_accel_set_points"},
		{&f.accelApply, "libinput_device_config_accel_apply"},
		{&f.accelDestroy, "libinput_config_accel_destroy"},
		{&f.sendModes, "libinput_device_config_send_events_get_modes"},
		{&f.sendMode, "libinput_device_config_send_events_set_mode"},
		{&f.sendDefault, "libinput_device_config_send_events_get_default_mode"},
		{&f.hasCalibration, "libinput_device_config_calibration_has_matrix"},
		{&f.calibration, "libinput_device_config_calibration_set_matrix"},
		{&f.calibrationDefault, "libinput_device_config_calibration_get_default_matrix"},
		{&f.hasArea, "libinput_device_config_area_has_rectangle"},
		{&f.area, "libinput_device_config_area_set_rectangle"},
		{&f.areaDefault, "libinput_device_config_area_get_default_rectangle"},
	})
}

type knob[T any] struct {
	name      string
	available bool
	def       func() T
	set       func(T) int32
}

func (k knob[T]) apply(device string, class, override *T) error {
	if !k.available {
		if override != nil {
			return fmt.Errorf("session: input device %q has no %s setting", device, k.name)
		}
		return nil
	}
	v := k.def()
	switch {
	case override != nil:
		v = *override
	case class != nil:
		v = *class
	}
	if status := k.set(v); status != configStatusSuccess {
		return fmt.Errorf("session: input device %q rejects %s %v (libinput status %d)", device, k.name, v, status)
	}
	return nil
}

func toggle(set func(uintptr, int32) int32, def func(uintptr) int32, h uintptr) (func() bool, func(bool) int32) {
	return func() bool { return def(h) != 0 }, func(on bool) int32 {
		if on {
			return set(h, 1)
		}
		return set(h, 0)
	}
}

func bind[T any](set func(uintptr, T) int32, def func(uintptr) T, h uintptr) (func() T, func(T) int32) {
	return func() T { return def(h) }, func(v T) int32 { return set(h, v) }
}

const accelTypeFallback = 0

func (f *configFuncs) setAccel(h uintptr, p AccelProfile) int32 {
	if p.curve == nil {
		return f.accelProfile(h, p.kind)
	}
	accel := f.accelCreate(accelCustom)
	if accel == 0 {
		return configStatusUnsupported
	}
	defer f.accelDestroy(accel)
	if status := f.accelPoints(accel, accelTypeFallback, p.curve.step, uintptr(len(p.curve.points)), &p.curve.points[0]); status != configStatusSuccess {
		return status
	}
	return f.accelApply(h, accel)
}

func externalMouse(off *bool) *sendEvents {
	if off == nil {
		return nil
	}
	mode := sendEventsEnabled
	if *off {
		mode = sendEventsDisabledOnExternalMouse
	}
	return &mode
}

func (r *inputReader) class(d *InputDevice) DeviceConfig {
	switch {
	case d.touchpad:
		return r.config.Touchpad
	case d.caps&backend.Pointer != 0:
		return r.config.Pointer
	}
	return DeviceConfig{}
}

func (r *inputReader) configure(d *InputDevice) error {
	f, h, c, o := &r.fn.config, d.h, r.class(d), r.config.Devices[d.name]
	tap, accel, buttonScroll := f.tapFingers(h) > 0, f.hasAccel(h) != 0, f.scrollMethods(h)&ScrollOnButtonDown != 0
	toggles := []struct {
		name          string
		available     bool
		set           func(uintptr, int32) int32
		def           func(uintptr) int32
		class, device *bool
	}{
		{"tap", tap, f.tap, f.tapDefault, c.Tap, o.Tap},
		{"drag_lock", tap, f.dragLock, f.dragDefault, c.DragLock, o.DragLock},
		{"natural_scroll", f.hasNatural(h) != 0, f.natural, f.naturalDefault, c.NaturalScroll, o.NaturalScroll},
		{"disable_while_typing", f.hasDWT(h) != 0, f.dwt, f.dwtDefault, c.DisableWhileTyping, o.DisableWhileTyping},
		{"middle_emulation", f.hasMiddle(h) != 0, f.middle, f.middleDefault, c.MiddleEmulation, o.MiddleEmulation},
		{"left_handed", f.hasLeftHanded(h) != 0, f.leftHanded, f.leftHandedDefault, c.LeftHanded, o.LeftHanded},
		{"scroll_button_lock", buttonScroll, f.buttonLock, f.buttonLockDefault, c.ScrollButtonLock, o.ScrollButtonLock},
	}
	errs := make([]error, 0, len(toggles)+10)
	for _, t := range toggles {
		k := knob[bool]{name: t.name, available: t.available}
		k.def, k.set = toggle(t.set, t.def, h)
		errs = append(errs, k.apply(d.name, t.class, t.device))
	}
	click := knob[ClickMethod]{name: "click_method", available: f.clickMethods(h) != ClickNone}
	click.def, click.set = bind(f.clickMethod, f.clickDefault, h)
	scroll := knob[ScrollMethod]{name: "scroll_method", available: f.scrollMethods(h) != ScrollNone}
	scroll.def, scroll.set = bind(f.scrollMethod, f.scrollDefault, h)
	button := knob[uint32]{name: "scroll_button", available: buttonScroll}
	button.def, button.set = bind(f.scrollButton, f.scrollButtonDefault, h)
	tapMap := knob[TapButtonMap]{name: "tap_button_map", available: tap}
	tapMap.def, tapMap.set = bind(f.tapMap, f.tapMapDefault, h)
	profile := knob[AccelProfile]{
		name: "accel_profile", available: accel,
		def: func() AccelProfile { return AccelProfile{kind: f.profileDefault(h)} },
		set: func(p AccelProfile) int32 { return f.setAccel(h, p) },
	}
	speed := knob[float64]{name: "accel_speed", available: accel}
	speed.def, speed.set = bind(f.accelSpeed, f.speedDefault, h)
	send := knob[sendEvents]{name: "disabled_on_external_mouse", available: f.sendModes(h)&sendEventsDisabledOnExternalMouse != 0}
	send.def, send.set = bind(f.sendMode, f.sendDefault, h)
	calibration := knob[Calibration]{
		name: "calibration", available: f.hasCalibration(h) != 0,
		def: func() Calibration {
			var m Calibration
			f.calibrationDefault(h, &m[0])
			return m
		},
		set: func(m Calibration) int32 { return f.calibration(h, &m[0]) },
	}
	area := knob[Area]{
		name: "region", available: f.hasArea(h) != 0,
		def: func() Area { return f.areaDefault(h) },
		set: func(a Area) int32 { return f.area(h, &a) },
	}
	factor := knob[float64]{name: "scroll_factor", available: d.caps&backend.Pointer != 0, def: func() float64 { return 1 }, set: func(v float64) int32 {
		d.scrollFactor = v
		return configStatusSuccess
	}}
	return errors.Join(append(errs,
		click.apply(d.name, c.ClickMethod, o.ClickMethod),
		scroll.apply(d.name, c.ScrollMethod, o.ScrollMethod),
		button.apply(d.name, c.ScrollButton, o.ScrollButton),
		tapMap.apply(d.name, c.TapButtonMap, o.TapButtonMap),
		profile.apply(d.name, c.AccelProfile, o.AccelProfile),
		speed.apply(d.name, c.AccelSpeed, o.AccelSpeed),
		send.apply(d.name, externalMouse(c.DisabledOnExternalMouse), externalMouse(o.DisabledOnExternalMouse)),
		calibration.apply(d.name, nil, o.Calibration),
		area.apply(d.name, nil, o.Area),
		factor.apply(d.name, c.ScrollFactor, o.ScrollFactor),
	)...)
}

func (r *inputReader) reconfigure(config InputConfig) error {
	r.config = config
	devices := slices.SortedFunc(maps.Values(r.devices), func(a, b *InputDevice) int { return cmp.Compare(a.name, b.name) })
	errs := make([]error, 0, len(devices))
	for _, d := range devices {
		errs = append(errs, r.configure(d))
	}
	return errors.Join(errs...)
}
