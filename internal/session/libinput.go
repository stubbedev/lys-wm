package session

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

const LibinputEnv = "LYS_WM_LIBINPUT"

var defaultLibinput = "libinput.so.10"

func LibinputPath(getenv func(string) string) string {
	if p := getenv(LibinputEnv); p != "" {
		return p
	}
	return defaultLibinput
}

const (
	evDeviceAdded       = 1
	evDeviceRemoved     = 2
	evKeyboardKey       = 300
	evPointerMotion     = 400
	evPointerAbsolute   = 401
	evPointerButton     = 402
	evScrollWheel       = 404
	evScrollFinger      = 405
	evScrollContinuous  = 406
	evTouchDown         = 500
	evTouchUp           = 501
	evTouchMotion       = 502
	evTouchCancel       = 503
	evTouchFrame        = 504
	evSwipeBegin        = 800
	evSwipeUpdate       = 801
	evSwipeEnd          = 802
	evPinchBegin        = 803
	evPinchUpdate       = 804
	evPinchEnd          = 805
	evHoldBegin         = 806
	evHoldEnd           = 807
	evSwitchToggle      = 900
	capKeyboard         = 0
	capPointer          = 1
	capTouch            = 2
	axisVertical        = 0
	axisHorizontal      = 1
	switchLid           = 1
	switchTabletMode    = 2
	configStatusSuccess = 0
)

var switchKinds = map[int32]backend.SwitchKind{switchLid: backend.SwitchLid, switchTabletMode: backend.SwitchTabletMode}

type libinputFuncs struct {
	args             [1]uintptr
	createContext    func(iface unsafe.Pointer, userData uintptr) uintptr
	unref            func(li uintptr) uintptr
	addDevice        func(li uintptr, path string) uintptr
	removeDevice     func(dev uintptr)
	getFD            func(li uintptr) int32
	dispatch         func(li uintptr) int32
	getEvent         func(li uintptr) uintptr
	eventType        func(ev uintptr) int32
	eventDevice      func(ev uintptr) uintptr
	destroyEvent     func(ev uintptr)
	suspend          func(li uintptr)
	resume           func(li uintptr) int32
	deviceName       func(dev uintptr) string
	ledUpdate        func(dev uintptr, leds int32)
	deviceHasCap     func(dev uintptr, capability int32) int32
	deviceRef        func(dev uintptr) uintptr
	deviceUnref      func(dev uintptr) uintptr
	keyboardEvent    func(ev uintptr) uintptr
	keyTime          func(kev uintptr) uint64
	key              func(kev uintptr) uint32
	keyState         func(kev uintptr) int32
	pointerEvent     func(ev uintptr) uintptr
	pointerTime      func(pev uintptr) uint64
	dx, dy           func(pev uintptr) float64
	dxRaw, dyRaw     func(pev uintptr) float64
	absX, absY       func(pev uintptr, size uint32) float64
	button           func(pev uintptr) uint32
	buttonState      func(pev uintptr) int32
	hasAxis          func(pev uintptr, axis int32) int32
	scrollValue      func(pev uintptr, axis int32) float64
	scrollV120       func(pev uintptr, axis int32) float64
	touchEvent       func(ev uintptr) uintptr
	touchTime        func(tev uintptr) uint64
	touchSlot        func(tev uintptr) int32
	touchX, touchY   func(tev uintptr, size uint32) float64
	gestureEvent     func(ev uintptr) uintptr
	gestureTime      func(gev uintptr) uint64
	fingers          func(gev uintptr) int32
	cancelled        func(gev uintptr) int32
	gestureDX        func(gev uintptr) float64
	gestureDY        func(gev uintptr) float64
	gestureUnaccelDX func(gev uintptr) float64
	gestureUnaccelDY func(gev uintptr) float64
	gestureScale     func(gev uintptr) float64
	gestureAngle     func(gev uintptr) float64
	switchEvent      func(ev uintptr) uintptr
	switchTime       func(sev uintptr) uint64
	switchKind       func(sev uintptr) int32
	switchState      func(sev uintptr) int32
	tapFingers       func(dev uintptr) int32
	tap              func(dev uintptr, enable int32) int32
	tapDefault       func(dev uintptr) int32
	dragLock         func(dev uintptr, enable int32) int32
	dragDefault      func(dev uintptr) int32
	hasNatural       func(dev uintptr) int32
	natural          func(dev uintptr, enable int32) int32
	naturalDefault   func(dev uintptr) int32
	hasDWT           func(dev uintptr) int32
	dwt              func(dev uintptr, enable int32) int32
	dwtDefault       func(dev uintptr) int32
	hasMiddle        func(dev uintptr) int32
	middle           func(dev uintptr, enable int32) int32
	middleDefault    func(dev uintptr) int32
	clickMethods     func(dev uintptr) ClickMethod
	clickMethod      func(dev uintptr, method ClickMethod) int32
	clickDefault     func(dev uintptr) ClickMethod
	scrollMethods    func(dev uintptr) ScrollMethod
	scrollMethod     func(dev uintptr, method ScrollMethod) int32
	scrollDefault    func(dev uintptr) ScrollMethod
	hasAccel         func(dev uintptr) int32
	accelProfile     func(dev uintptr, profile AccelProfile) int32
	profileDefault   func(dev uintptr) AccelProfile
	accelSpeed       func(dev uintptr, speed float64) int32
	speedDefault     func(dev uintptr) float64
}

type Opener interface {
	TakeDevice(path string) (int, Device, error)
	ReleaseDevice(d Device) error
}

type InputDevice struct {
	name         string
	caps         backend.Capability
	h            uintptr
	touchpad     bool
	scrollFactor float64
	ledUpdate    func(dev uintptr, leds int32)
}

var _ backend.KeyboardLEDs = (*InputDevice)(nil)

func (d *InputDevice) Name() string                     { return d.name }
func (d *InputDevice) Capabilities() backend.Capability { return d.caps }

const (
	ledNumLock    = 1 << 0
	ledCapsLock   = 1 << 1
	ledScrollLock = 1 << 2
)

func (d *InputDevice) SetLEDs(l xkb.Locks) {
	var leds int32
	for _, led := range []struct {
		on   bool
		flag int32
	}{{l.Num, ledNumLock}, {l.Caps, ledCapsLock}, {l.Scroll, ledScrollLock}} {
		if led.on {
			leds |= led.flag
		}
	}
	d.ledUpdate(d.h, leds)
}

type Libinput struct {
	fn      libinputFuncs
	loop    *loop.Loop
	work    loop.Serial
	ready   map[string]takenDevice
	adding  map[string]struct{}
	resumes uint64
	lib     uintptr
	li      uintptr
	id      uintptr
	opener  Opener
	config  InputConfig
	report  func(error)
	handler backend.Handler
	src     *loop.Source
	devices map[uintptr]*InputDevice
	paths   map[string]uintptr
	fds     map[int32]Device
	openErr error
	out     []backend.Event
	motion  backend.PointerMotion
	swipe   backend.SwipeUpdate
	pinch   backend.PinchUpdate
	frame   backend.PointerFrame
	key     backend.Key
	abs     backend.PointerMotionAbsolute
	button  backend.PointerButton
	axes    [2]backend.PointerAxis
	touch   struct {
		down   backend.TouchDown
		motion backend.TouchMotion
		up     backend.TouchUp
		cancel backend.TouchCancel
		frame  backend.TouchFrame
	}
	// suspended: the devices were let go for an inactive session, and a
	// resume takes them again. A resume without one would take each
	// device a second time, which logind refuses.
	suspended bool
}

type libinputInterface struct {
	open, close uintptr
}

var callbacks = struct {
	once   sync.Once
	iface  *libinputInterface
	mu     sync.Mutex
	next   uintptr
	inputs map[uintptr]*Libinput
}{inputs: map[uintptr]*Libinput{}}

func goString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 { //nolint:gosec // a NUL-terminated path from libinput
		n++
	}
	return string(unsafe.Slice(p, n)) //nolint:gosec // n bytes before the NUL
}

func lookup(userData uintptr) *Libinput {
	callbacks.mu.Lock()
	defer callbacks.mu.Unlock()
	return callbacks.inputs[userData]
}

func openRestricted(path *byte, _ int32, userData uintptr) int32 {
	in := lookup(userData)
	if in == nil {
		return -int32(unix.ENODEV)
	}
	fd, dev, err := in.take(goString(path))
	if err != nil {
		in.openErr = err
		if errno, ok := errors.AsType[unix.Errno](err); ok {
			return -int32(errno) //nolint:gosec // errno values are small
		}
		return -int32(unix.EACCES)
	}
	in.fds[int32(fd)] = dev //nolint:gosec // file descriptors fit an int32
	return int32(fd)        //nolint:gosec // file descriptors fit an int32
}

func closeRestricted(fd int32, userData uintptr) {
	in := lookup(userData)
	if in == nil {
		return
	}
	dev, ok := in.fds[fd]
	delete(in.fds, fd)
	err := unix.Close(int(fd))
	if ok {
		in.releaseLater(dev)
	}
	if err != nil {
		in.report(fmt.Errorf("session: close input device fd %d: %w", fd, err))
	}
}

func OpenLibinput(l *loop.Loop, path string, opener Opener, config InputConfig, report func(error)) (*Libinput, error) {
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("session: load libinput from %s (set %s to its path): %w", path, LibinputEnv, err)
	}
	in := &Libinput{loop: l, ready: map[string]takenDevice{}, adding: map[string]struct{}{}, lib: lib, opener: opener, config: config, report: report, devices: map[uintptr]*InputDevice{}, paths: map[string]uintptr{}, fds: map[int32]Device{}}
	if err := in.bind(path); err != nil {
		return nil, errors.Join(err, purego.Dlclose(lib))
	}
	callbacks.once.Do(func() {
		callbacks.iface = &libinputInterface{open: purego.NewCallback(openRestricted), close: purego.NewCallback(closeRestricted)}
	})
	callbacks.mu.Lock()
	callbacks.next++
	in.id = callbacks.next
	callbacks.inputs[in.id] = in
	callbacks.mu.Unlock()
	if in.li = in.fn.createContext(unsafe.Pointer(callbacks.iface), in.id); in.li == 0 { //nolint:gosec // the interface is a package-level heap object libinput may keep
		return nil, errors.Join(errors.New("session: libinput_path_create_context failed"), in.forget(), purego.Dlclose(lib))
	}
	if in.src, err = l.AddFD(int(in.fn.getFD(in.li)), loop.Readable, in.read); err != nil {
		return nil, errors.Join(err, in.Close())
	}
	return in, nil
}

func hotCall[R uintptr | int32 | uint64](f *libinputFuncs, sym uintptr) func(uintptr) R {
	return func(arg uintptr) R {
		f.args[0] = arg
		r, _, _ := purego.SyscallN(sym, f.args[:]...)
		return R(r)
	}
}

func (in *Libinput) bind(path string) error {
	f := &in.fn
	for _, s := range []struct {
		fn   any
		name string
	}{
		{&f.createContext, "libinput_path_create_context"},
		{&f.unref, "libinput_unref"},
		{&f.addDevice, "libinput_path_add_device"},
		{&f.removeDevice, "libinput_path_remove_device"},
		{&f.getFD, "libinput_get_fd"},
		{&f.dispatch, "libinput_dispatch"},
		{&f.getEvent, "libinput_get_event"},
		{&f.eventType, "libinput_event_get_type"},
		{&f.eventDevice, "libinput_event_get_device"},
		{&f.destroyEvent, "libinput_event_destroy"},
		{&f.suspend, "libinput_suspend"},
		{&f.resume, "libinput_resume"},
		{&f.deviceName, "libinput_device_get_name"},
		{&f.ledUpdate, "libinput_device_led_update"},
		{&f.deviceHasCap, "libinput_device_has_capability"},
		{&f.deviceRef, "libinput_device_ref"},
		{&f.deviceUnref, "libinput_device_unref"},
		{&f.keyboardEvent, "libinput_event_get_keyboard_event"},
		{&f.keyTime, "libinput_event_keyboard_get_time_usec"},
		{&f.key, "libinput_event_keyboard_get_key"},
		{&f.keyState, "libinput_event_keyboard_get_key_state"},
		{&f.pointerEvent, "libinput_event_get_pointer_event"},
		{&f.pointerTime, "libinput_event_pointer_get_time_usec"},
		{&f.dx, "libinput_event_pointer_get_dx"},
		{&f.dy, "libinput_event_pointer_get_dy"},
		{&f.dxRaw, "libinput_event_pointer_get_dx_unaccelerated"},
		{&f.dyRaw, "libinput_event_pointer_get_dy_unaccelerated"},
		{&f.absX, "libinput_event_pointer_get_absolute_x_transformed"},
		{&f.absY, "libinput_event_pointer_get_absolute_y_transformed"},
		{&f.button, "libinput_event_pointer_get_button"},
		{&f.buttonState, "libinput_event_pointer_get_button_state"},
		{&f.hasAxis, "libinput_event_pointer_has_axis"},
		{&f.scrollValue, "libinput_event_pointer_get_scroll_value"},
		{&f.scrollV120, "libinput_event_pointer_get_scroll_value_v120"},
		{&f.touchEvent, "libinput_event_get_touch_event"},
		{&f.touchTime, "libinput_event_touch_get_time_usec"},
		{&f.touchSlot, "libinput_event_touch_get_seat_slot"},
		{&f.touchX, "libinput_event_touch_get_x_transformed"},
		{&f.touchY, "libinput_event_touch_get_y_transformed"},
		{&f.gestureEvent, "libinput_event_get_gesture_event"},
		{&f.gestureTime, "libinput_event_gesture_get_time_usec"},
		{&f.fingers, "libinput_event_gesture_get_finger_count"},
		{&f.cancelled, "libinput_event_gesture_get_cancelled"},
		{&f.gestureDX, "libinput_event_gesture_get_dx"},
		{&f.gestureDY, "libinput_event_gesture_get_dy"},
		{&f.gestureUnaccelDX, "libinput_event_gesture_get_dx_unaccelerated"},
		{&f.gestureUnaccelDY, "libinput_event_gesture_get_dy_unaccelerated"},
		{&f.gestureScale, "libinput_event_gesture_get_scale"},
		{&f.gestureAngle, "libinput_event_gesture_get_angle_delta"},
		{&f.switchEvent, "libinput_event_get_switch_event"},
		{&f.switchTime, "libinput_event_switch_get_time_usec"},
		{&f.switchKind, "libinput_event_switch_get_switch"},
		{&f.switchState, "libinput_event_switch_get_switch_state"},
		{&f.tapFingers, "libinput_device_config_tap_get_finger_count"},
		{&f.tap, "libinput_device_config_tap_set_enabled"},
		{&f.tapDefault, "libinput_device_config_tap_get_default_enabled"},
		{&f.dragLock, "libinput_device_config_tap_set_drag_lock_enabled"},
		{&f.dragDefault, "libinput_device_config_tap_get_default_drag_lock_enabled"},
		{&f.hasNatural, "libinput_device_config_scroll_has_natural_scroll"},
		{&f.natural, "libinput_device_config_scroll_set_natural_scroll_enabled"},
		{&f.naturalDefault, "libinput_device_config_scroll_get_default_natural_scroll_enabled"},
		{&f.hasDWT, "libinput_device_config_dwt_is_available"},
		{&f.dwt, "libinput_device_config_dwt_set_enabled"},
		{&f.dwtDefault, "libinput_device_config_dwt_get_default_enabled"},
		{&f.hasMiddle, "libinput_device_config_middle_emulation_is_available"},
		{&f.middle, "libinput_device_config_middle_emulation_set_enabled"},
		{&f.middleDefault, "libinput_device_config_middle_emulation_get_default_enabled"},
		{&f.clickMethods, "libinput_device_config_click_get_methods"},
		{&f.clickMethod, "libinput_device_config_click_set_method"},
		{&f.clickDefault, "libinput_device_config_click_get_default_method"},
		{&f.scrollMethods, "libinput_device_config_scroll_get_methods"},
		{&f.scrollMethod, "libinput_device_config_scroll_set_method"},
		{&f.scrollDefault, "libinput_device_config_scroll_get_default_method"},
		{&f.hasAccel, "libinput_device_config_accel_is_available"},
		{&f.accelProfile, "libinput_device_config_accel_set_profile"},
		{&f.profileDefault, "libinput_device_config_accel_get_default_profile"},
		{&f.accelSpeed, "libinput_device_config_accel_set_speed"},
		{&f.speedDefault, "libinput_device_config_accel_get_default_speed"},
	} {
		if _, err := purego.Dlsym(in.lib, s.name); err != nil {
			return fmt.Errorf("session: %s lacks %s: %w", path, s.name, err)
		}
		purego.RegisterLibFunc(s.fn, in.lib, s.name)
	}
	return in.bindHot(path)
}

func (in *Libinput) bindHot(path string) error {
	f := &in.fn
	for _, s := range []struct {
		name string
		set  func(sym uintptr)
	}{
		{"libinput_dispatch", func(sym uintptr) { f.dispatch = hotCall[int32](f, sym) }},
		{"libinput_get_event", func(sym uintptr) { f.getEvent = hotCall[uintptr](f, sym) }},
		{"libinput_event_get_type", func(sym uintptr) { f.eventType = hotCall[int32](f, sym) }},
		{"libinput_event_get_device", func(sym uintptr) { f.eventDevice = hotCall[uintptr](f, sym) }},
		{"libinput_event_destroy", func(sym uintptr) {
			destroy := hotCall[uintptr](f, sym)
			f.destroyEvent = func(ev uintptr) { destroy(ev) }
		}},
		{"libinput_event_get_pointer_event", func(sym uintptr) { f.pointerEvent = hotCall[uintptr](f, sym) }},
		{"libinput_event_pointer_get_time_usec", func(sym uintptr) { f.pointerTime = hotCall[uint64](f, sym) }},
	} {
		sym, err := purego.Dlsym(in.lib, s.name)
		if err != nil {
			return fmt.Errorf("session: %s lacks %s: %w", path, s.name, err)
		}
		s.set(sym)
	}
	return nil
}

func (in *Libinput) forget() error {
	callbacks.mu.Lock()
	defer callbacks.mu.Unlock()
	delete(callbacks.inputs, in.id)
	return nil
}

func (in *Libinput) Start(h backend.Handler) {
	in.handler = h
}

var ErrDeclined = errors.New("libinput does not handle the device")

func (in *Libinput) Add(path string) error {
	if _, ok := in.paths[path]; ok {
		return nil
	}
	in.openErr = nil
	dev := in.fn.addDevice(in.li, path)
	if dev == 0 {
		if in.openErr != nil {
			return fmt.Errorf("session: add input device %s: %w", path, in.openErr)
		}
		return fmt.Errorf("session: add input device %s: %w", path, ErrDeclined)
	}
	in.paths[path] = in.fn.deviceRef(dev)
	return in.drain()
}

func (in *Libinput) Remove(path string) error {
	delete(in.adding, path)
	dev, ok := in.paths[path]
	if !ok {
		return nil
	}
	delete(in.paths, path)
	in.fn.removeDevice(dev)
	in.fn.deviceUnref(dev)
	return in.drain()
}

func (in *Libinput) Suspend() {
	in.resumes++
	if in.suspended {
		return
	}
	in.fn.suspend(in.li)
	in.suspended = true
}

// Resume takes the devices back after a Suspend; without one there is
// nothing to take back, as when a session turns active right after
// lys-wm started on it.
func (in *Libinput) Resume() error {
	if !in.suspended {
		return nil
	}
	if in.fn.resume(in.li) != 0 {
		return errors.New("session: libinput_resume failed")
	}
	in.suspended = false
	return in.drain()
}

type takenDevice struct {
	fd  int
	dev Device
	err error
}

func (in *Libinput) take(path string) (int, Device, error) {
	if t, ok := in.ready[path]; ok {
		delete(in.ready, path)
		return t.fd, t.dev, t.err
	}
	return in.opener.TakeDevice(path)
}

func (in *Libinput) releaseLater(dev Device) {
	in.work.Run(func() {
		if err := in.opener.ReleaseDevice(dev); err != nil {
			in.postReport(fmt.Errorf("session: release input device %d:%d: %w", dev.Major, dev.Minor, err))
		}
	})
}

func (in *Libinput) postReport(err error) {
	if posted := in.loop.Post(func() error { in.report(err); return nil }); posted != nil && !errors.Is(posted, loop.ErrClosed) {
		panic(fmt.Sprintf("session: hand an input device error back to the loop: %v", posted))
	}
}

func (in *Libinput) dropReady() {
	for path, t := range in.ready {
		delete(in.ready, path)
		in.letGo(t)
	}
}

func (in *Libinput) letGo(t takenDevice) {
	if t.err != nil {
		return
	}
	if err := unix.Close(t.fd); err != nil {
		in.report(fmt.Errorf("session: close input device fd %d: %w", t.fd, err))
	}
	in.releaseLater(t.dev)
}

func (in *Libinput) AddLater(path string) {
	if _, ok := in.paths[path]; ok {
		return
	}
	if _, ok := in.adding[path]; ok {
		return
	}
	in.adding[path] = struct{}{}
	in.work.Run(func() {
		var t takenDevice
		t.fd, t.dev, t.err = in.opener.TakeDevice(path)
		in.postTaken(t, func() error { return in.added(path, t) })
	})
}

func (in *Libinput) postTaken(t takenDevice, then func() error) {
	err := in.loop.Post(then)
	if err == nil {
		return
	}
	if t.err == nil {
		_ = unix.Close(t.fd)
	}
	if !errors.Is(err, loop.ErrClosed) {
		panic(fmt.Sprintf("session: hand a taken input device back to the loop: %v", err))
	}
}

func (in *Libinput) added(path string, t takenDevice) error {
	if _, wanted := in.adding[path]; !wanted {
		in.letGo(t)
		return nil
	}
	delete(in.adding, path)
	in.ready[path] = t
	err := in.Add(path)
	if t, unused := in.ready[path]; unused {
		delete(in.ready, path)
		in.letGo(t)
	}
	if err != nil && !errors.Is(err, ErrDeclined) {
		in.report(err)
	}
	return nil
}

func (in *Libinput) ResumeLater() {
	if !in.suspended {
		return
	}
	in.resumes++
	resume := in.resumes
	paths := make([]string, 0, len(in.paths))
	for path := range in.paths {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	in.work.Run(func() {
		taken := make(map[string]takenDevice, len(paths))
		for _, path := range paths {
			var t takenDevice
			t.fd, t.dev, t.err = in.opener.TakeDevice(path)
			taken[path] = t
		}
		if err := in.loop.Post(func() error { return in.resumed(resume, taken) }); err != nil && !errors.Is(err, loop.ErrClosed) {
			panic(fmt.Sprintf("session: hand the resumed input devices back to the loop: %v", err))
		}
	})
}

func (in *Libinput) resumed(resume uint64, taken map[string]takenDevice) error {
	if resume != in.resumes {
		for _, t := range taken {
			in.letGo(t)
		}
		return nil
	}
	maps.Copy(in.ready, taken)
	err := in.Resume()
	in.dropReady()
	return err
}

func (in *Libinput) read(loop.Events) error {
	if rc := in.fn.dispatch(in.li); rc != 0 {
		return fmt.Errorf("session: libinput_dispatch: %w", unix.Errno(-rc)) //nolint:gosec // libinput returns a negated errno
	}
	return in.drain()
}

func (in *Libinput) drain() error {
	var errs error
	for ev := in.fn.getEvent(in.li); ev != 0; ev = in.fn.getEvent(in.li) {
		errs = errors.Join(errs, in.event(ev))
		in.fn.destroyEvent(ev)
	}
	return errs
}

func usec(v uint64) time.Duration {
	return time.Duration(v) * time.Microsecond //nolint:gosec // monotonic microseconds fit a Duration for centuries
}

func (in *Libinput) event(ev uintptr) error {
	if in.handler == nil {
		return nil
	}
	f := &in.fn
	h := f.eventDevice(ev)
	typ := f.eventType(ev)
	switch typ {
	case evDeviceAdded:
		d := &InputDevice{name: f.deviceName(h), h: f.deviceRef(h), touchpad: f.tapFingers(h) > 0, scrollFactor: 1, ledUpdate: f.ledUpdate}
		for c, flag := range map[int32]backend.Capability{capKeyboard: backend.Keyboard, capPointer: backend.Pointer, capTouch: backend.Touch} {
			if f.deviceHasCap(h, c) != 0 {
				d.caps |= flag
			}
		}
		in.devices[h] = d
		if err := in.configure(d); err != nil {
			in.report(err)
		}
		return in.handler.DeviceAdded(d)
	case evDeviceRemoved:
		d, ok := in.devices[h]
		if !ok {
			return nil
		}
		delete(in.devices, h)
		f.deviceUnref(d.h)
		return in.handler.DeviceRemoved(d)
	}
	d, ok := in.devices[h]
	if !ok {
		return nil
	}
	in.out = in.translate(in.out[:0], d, ev, typ)
	for _, e := range in.out {
		if err := in.handler.Input(d, e); err != nil {
			return err
		}
	}
	return nil
}

var gestures = map[int32]backend.Gesture{
	evSwipeBegin: backend.Swipe, evSwipeEnd: backend.Swipe,
	evPinchBegin: backend.Pinch, evPinchEnd: backend.Pinch,
	evHoldBegin: backend.Hold, evHoldEnd: backend.Hold,
}

var axisSources = map[int32]backend.AxisSource{evScrollWheel: backend.Wheel, evScrollFinger: backend.Finger, evScrollContinuous: backend.Continuous}

var scrollAxes = [2]struct {
	axis        int32
	orientation backend.Orientation
}{{axisVertical, backend.Vertical}, {axisHorizontal, backend.Horizontal}}

func (in *Libinput) pointerFrame(out []backend.Event, at time.Duration) []backend.Event {
	in.frame.Time = at
	return append(out, &in.frame)
}

func (in *Libinput) translate(out []backend.Event, d *InputDevice, ev uintptr, typ int32) []backend.Event {
	f := &in.fn
	switch typ {
	case evKeyboardKey:
		k := f.keyboardEvent(ev)
		in.key = backend.Key{Time: usec(f.keyTime(k)), Code: f.key(k), Pressed: f.keyState(k) == 1}
		return append(out, &in.key)
	case evPointerMotion:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		in.motion = backend.PointerMotion{Time: at, DX: f.dx(p), DY: f.dy(p), UnaccelDX: f.dxRaw(p), UnaccelDY: f.dyRaw(p)}
		return in.pointerFrame(append(out, &in.motion), at)
	case evPointerAbsolute:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		in.abs = backend.PointerMotionAbsolute{Time: at, X: f.absX(p, 1), Y: f.absY(p, 1)}
		return in.pointerFrame(append(out, &in.abs), at)
	case evPointerButton:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		in.button = backend.PointerButton{Time: at, Button: f.button(p), Pressed: f.buttonState(p) == 1}
		return in.pointerFrame(append(out, &in.button), at)
	case evScrollWheel, evScrollFinger, evScrollContinuous:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		for i, a := range scrollAxes {
			if f.hasAxis(p, a.axis) == 0 {
				continue
			}
			ax := &in.axes[i]
			*ax = backend.PointerAxis{Time: at, Orientation: a.orientation, Source: axisSources[typ], Value: f.scrollValue(p, a.axis) * d.scrollFactor}
			if typ == evScrollWheel {
				ax.Value120 = int32(math.Round(f.scrollV120(p, a.axis) * d.scrollFactor))
			}
			out = append(out, ax)
		}
		return in.pointerFrame(out, at)
	case evTouchDown, evTouchMotion:
		t := f.touchEvent(ev)
		at, id, x, y := usec(f.touchTime(t)), f.touchSlot(t), f.touchX(t, 1), f.touchY(t, 1)
		if typ == evTouchDown {
			in.touch.down = backend.TouchDown{Time: at, ID: id, X: x, Y: y}
			return append(out, &in.touch.down)
		}
		in.touch.motion = backend.TouchMotion{Time: at, ID: id, X: x, Y: y}
		return append(out, &in.touch.motion)
	case evTouchUp:
		t := f.touchEvent(ev)
		in.touch.up = backend.TouchUp{Time: usec(f.touchTime(t)), ID: f.touchSlot(t)}
		return append(out, &in.touch.up)
	case evTouchCancel:
		t := f.touchEvent(ev)
		in.touch.cancel = backend.TouchCancel{Time: usec(f.touchTime(t)), ID: f.touchSlot(t)}
		return append(out, &in.touch.cancel)
	case evTouchFrame:
		in.touch.frame = backend.TouchFrame{Time: usec(f.touchTime(f.touchEvent(ev)))}
		return append(out, &in.touch.frame)
	case evSwipeBegin, evPinchBegin, evHoldBegin:
		g := f.gestureEvent(ev)
		return append(out, backend.GestureBegin{Time: usec(f.gestureTime(g)), Gesture: gestures[typ], Fingers: uint32(max(f.fingers(g), 0))})
	case evSwipeUpdate:
		g := f.gestureEvent(ev)
		in.swipe = backend.SwipeUpdate{Time: usec(f.gestureTime(g)), DX: f.gestureDX(g), DY: f.gestureDY(g), UnaccelDX: f.gestureUnaccelDX(g), UnaccelDY: f.gestureUnaccelDY(g)}
		return append(out, &in.swipe)
	case evPinchUpdate:
		g := f.gestureEvent(ev)
		in.pinch = backend.PinchUpdate{Time: usec(f.gestureTime(g)), DX: f.gestureDX(g), DY: f.gestureDY(g), Scale: f.gestureScale(g), Rotation: f.gestureAngle(g)}
		return append(out, &in.pinch)
	case evSwipeEnd, evPinchEnd, evHoldEnd:
		g := f.gestureEvent(ev)
		return append(out, backend.GestureEnd{Time: usec(f.gestureTime(g)), Gesture: gestures[typ], Cancelled: f.cancelled(g) != 0})
	case evSwitchToggle:
		s := f.switchEvent(ev)
		kind, ok := switchKinds[f.switchKind(s)]
		if !ok {
			return out
		}
		return append(out, backend.Switch{Time: usec(f.switchTime(s)), Kind: kind, On: f.switchState(s) == 1})
	}
	return out
}

func (in *Libinput) Close() error {
	var errs []error
	in.work.Wait()
	in.dropReady()
	if in.src != nil {
		errs = append(errs, in.src.Remove())
	}
	for path := range in.paths {
		errs = append(errs, in.Remove(path))
	}
	for h, d := range in.devices {
		in.fn.deviceUnref(d.h)
		delete(in.devices, h)
	}
	if in.li != 0 {
		in.fn.unref(in.li)
		in.li = 0
	}
	in.work.Wait()
	return errors.Join(append(errs, in.forget(), purego.Dlclose(in.lib))...)
}

func (in *Libinput) Park() error {
	return in.src.Modify(0)
}

func (in *Libinput) Unpark() error {
	return in.src.Modify(loop.Readable)
}
