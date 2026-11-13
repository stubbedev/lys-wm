package session

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/stubbedev/lys-lib/clib"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
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
	evDeviceAdded           = 1
	evDeviceRemoved         = 2
	evKeyboardKey           = 300
	evPointerMotion         = 400
	evPointerAbsolute       = 401
	evPointerButton         = 402
	evScrollWheel           = 404
	evScrollFinger          = 405
	evScrollContinuous      = 406
	evTouchDown             = 500
	evTouchUp               = 501
	evTouchMotion           = 502
	evTouchCancel           = 503
	evTouchFrame            = 504
	evSwipeBegin            = 800
	evSwipeUpdate           = 801
	evSwipeEnd              = 802
	evPinchBegin            = 803
	evPinchUpdate           = 804
	evPinchEnd              = 805
	evHoldBegin             = 806
	evHoldEnd               = 807
	evSwitchToggle          = 900
	capKeyboard             = 0
	capPointer              = 1
	capTouch                = 2
	axisVertical            = 0
	axisHorizontal          = 1
	switchLid               = 1
	switchTabletMode        = 2
	configStatusSuccess     = 0
	configStatusUnsupported = 1
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
	logSetHandler    func(li uintptr, handler uintptr)
	logSetPriority   func(li uintptr, priority int32)
	vsnprintf        func(buf *byte, size uintptr, format *byte, args uintptr) int32
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
	config           configFuncs
	tablet           tabletFuncs
	pad              padFuncs
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
	reader       *inputReader
	tablet       bool
	tabletID     tabletinput.ID
	pad          bool
	padLayout    tabletinput.PadLayout
	group        uintptr
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
	d.reader.on("the LEDs of "+d.name, func() { d.reader.setLEDs(d, leds) })
}

type Libinput struct {
	loop       *loop.Loop
	reader     *inputReader
	queue      *inputQueue
	handler    backend.Handler
	report     func(error)
	delivering []*inputBatch
}

type inputReader struct {
	fn        libinputFuncs
	in        *Libinput
	deliver   func() error
	loop      *loop.Loop
	src       *loop.Source
	queue     *inputQueue
	filling   *inputBatch
	work      loop.Serial
	opener    Opener
	config    InputConfig
	ready     map[string]takenDevice
	adding    map[string]struct{}
	resumes   uint64
	lib       uintptr
	li        uintptr
	id        uintptr
	devices   map[uintptr]*InputDevice
	paths     map[string]uintptr
	fds       map[int32]Device
	tools     map[uintptr]*tabletinput.Tool
	openErr   error
	out       []backend.Event
	logLine   [1024]byte
	done      chan struct{}
	stopErr   error
	suspended bool
	closing   bool
}

type libinputInterface struct {
	open, close uintptr
}

const (
	logPriorityInfo  = 20
	logPriorityError = 30
	synDropped       = "SYN_DROPPED"
)

var callbacks = struct {
	once     sync.Once
	iface    *libinputInterface
	log      uintptr
	mu       sync.Mutex
	next     uintptr
	readers  map[uintptr]*inputReader
	contexts map[uintptr]*inputReader
}{readers: map[uintptr]*inputReader{}, contexts: map[uintptr]*inputReader{}}

func lookup(table map[uintptr]*inputReader, key uintptr) *inputReader {
	callbacks.mu.Lock()
	defer callbacks.mu.Unlock()
	return table[key]
}

func openRestricted(path *byte, _ int32, userData uintptr) int32 {
	r := lookup(callbacks.readers, userData)
	if r == nil {
		return -int32(unix.ENODEV)
	}
	fd, dev, err := r.take(clib.GoString(path))
	if err != nil {
		r.openErr = err
		if errno, ok := errors.AsType[unix.Errno](err); ok {
			return -int32(errno) //nolint:gosec // errno values are small
		}
		return -int32(unix.EACCES)
	}
	r.fds[int32(fd)] = dev //nolint:gosec // file descriptors fit an int32
	return int32(fd)       //nolint:gosec // file descriptors fit an int32
}

func closeRestricted(fd int32, userData uintptr) {
	r := lookup(callbacks.readers, userData)
	if r == nil {
		return
	}
	dev, ok := r.fds[fd]
	delete(r.fds, fd)
	err := unix.Close(int(fd))
	if ok {
		r.releaseLater(dev)
	}
	if err != nil {
		r.filling.report(fmt.Errorf("session: close input device fd %d: %w", fd, err))
	}
}

func logged(li uintptr, priority int32, format *byte, args uintptr) {
	if r := lookup(callbacks.contexts, li); r != nil {
		r.logged(priority, format, args)
	}
}

type RaisePriority func(hand func(what string, job func() error)) (restore func() error)

func OpenLibinput(l *loop.Loop, path string, opener Opener, config InputConfig, report func(error), raise RaisePriority) (*Libinput, error) {
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("session: load libinput from %s (set %s to its path): %w", path, LibinputEnv, err)
	}
	in := &Libinput{loop: l, queue: &inputQueue{}, report: report}
	r := &inputReader{in: in, deliver: in.deliver, queue: in.queue, filling: &inputBatch{}, ready: map[string]takenDevice{}, adding: map[string]struct{}{}, lib: lib, opener: opener, config: config, devices: map[uintptr]*InputDevice{}, paths: map[string]uintptr{}, fds: map[int32]Device{}, done: make(chan struct{})}
	in.reader = r
	if err := r.bind(path); err != nil {
		return nil, errors.Join(err, purego.Dlclose(lib))
	}
	if r.loop, err = loop.New(); err != nil {
		return nil, errors.Join(fmt.Errorf("session: the input reader's loop: %w", err), purego.Dlclose(lib))
	}
	opened := make(chan error, 1)
	go r.run(raise, opened)
	if err := <-opened; err != nil {
		<-r.done
		return nil, errors.Join(err, purego.Dlclose(lib))
	}
	return in, nil
}

func (r *inputReader) run(raise RaisePriority, opened chan<- error) {
	runtime.LockOSThread()
	defer close(r.done)
	restore := raise(r.loop.Hand)
	if err := r.open(); err != nil {
		opened <- errors.Join(err, r.teardown(), r.loop.Close(), restore())
		return
	}
	opened <- nil
	if err := r.loop.Run(); err != nil {
		r.filling.fail(fmt.Errorf("session: the input reader stopped: %w", err))
	}
	r.stopErr = errors.Join(r.teardown(), r.loop.Close(), restore())
}

func (r *inputReader) open() error {
	callbacks.once.Do(func() {
		callbacks.iface = &libinputInterface{open: purego.NewCallback(openRestricted), close: purego.NewCallback(closeRestricted)}
		callbacks.log = purego.NewCallback(logged)
	})
	callbacks.mu.Lock()
	callbacks.next++
	r.id = callbacks.next
	callbacks.readers[r.id] = r
	callbacks.mu.Unlock()
	if r.li = r.fn.createContext(unsafe.Pointer(callbacks.iface), r.id); r.li == 0 { //nolint:gosec // the interface is a package-level heap object libinput may keep
		return errors.New("session: libinput_path_create_context failed")
	}
	callbacks.mu.Lock()
	callbacks.contexts[r.li] = r
	callbacks.mu.Unlock()
	r.fn.logSetHandler(r.li, callbacks.log)
	r.fn.logSetPriority(r.li, logPriorityInfo)
	src, err := r.loop.AddFD(int(r.fn.getFD(r.li)), loop.Readable, r.read)
	if err != nil {
		return fmt.Errorf("session: watch libinput's fd on the input reader: %w", err)
	}
	r.src = src
	r.loop.BeforeWait(r.publish)
	return nil
}

func (r *inputReader) logged(priority int32, format *byte, args uintptr) {
	if priority < logPriorityError && !strings.Contains(clib.GoString(format), synDropped) {
		return
	}
	n := int(r.fn.vsnprintf(&r.logLine[0], uintptr(len(r.logLine)), format, args))
	if n < 0 {
		r.filling.report(fmt.Errorf("session: libinput logged %q, which vsnprintf could not format", clib.GoString(format)))
		return
	}
	r.filling.report(fmt.Errorf("session: libinput: %s", strings.TrimSpace(string(r.logLine[:min(n, len(r.logLine)-1)]))))
}

func hotCall[R uintptr | int32 | uint64](f *libinputFuncs, sym uintptr) func(uintptr) R {
	return func(arg uintptr) R {
		f.args[0] = arg
		r, _, _ := purego.SyscallN(sym, f.args[:]...)
		return R(r)
	}
}

func (r *inputReader) bind(path string) error {
	f := &r.fn
	syms := []libSymbol{
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
		{&f.logSetHandler, "libinput_log_set_handler"},
		{&f.logSetPriority, "libinput_log_set_priority"},
		{&f.vsnprintf, "vsnprintf"},
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
	}
	if err := r.register(path, syms); err != nil {
		return err
	}
	for _, bind := range []func(string) error{r.bindConfig, r.bindTablet, r.bindPad, r.bindHot} {
		if err := bind(path); err != nil {
			return err
		}
	}
	return nil
}

func (r *inputReader) register(path string, syms []libSymbol) error {
	for _, s := range syms {
		if _, err := purego.Dlsym(r.lib, s.name); err != nil {
			return fmt.Errorf("session: %s lacks %s: %w", path, s.name, err)
		}
		purego.RegisterLibFunc(s.fn, r.lib, s.name)
	}
	return nil
}

func (r *inputReader) bindHot(path string) error {
	f := &r.fn
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
		sym, err := purego.Dlsym(r.lib, s.name)
		if err != nil {
			return fmt.Errorf("session: %s lacks %s: %w", path, s.name, err)
		}
		s.set(sym)
	}
	return nil
}

func (r *inputReader) forget() {
	callbacks.mu.Lock()
	defer callbacks.mu.Unlock()
	delete(callbacks.readers, r.id)
	delete(callbacks.contexts, r.li)
}

func (r *inputReader) on(what string, job func()) {
	r.loop.Hand(what, func() error {
		job()
		return nil
	})
}

func (r *inputReader) call(what string, job func() error) error {
	result := make(chan error, 1)
	if err := r.loop.Post(func() error {
		result <- job()
		return nil
	}); err != nil {
		return fmt.Errorf("session: %s on the input reader: %w", what, err)
	}
	select {
	case err := <-result:
		return err
	case <-r.done:
	}
	select {
	case err := <-result:
		return err
	default:
		return fmt.Errorf("session: %s: the input reader stopped first", what)
	}
}

func (r *inputReader) publish() error {
	if r.filling.empty() {
		return nil
	}
	next, wake := r.queue.push(r.filling)
	r.filling = next
	if wake {
		r.in.loop.Hand("the input events", r.deliver)
	}
	return nil
}

func (in *Libinput) deliver() error {
	batches := in.queue.take(in.delivering)
	in.delivering = nil
	var err error
	for _, b := range batches {
		err = errors.Join(err, in.deliverBatch(b))
	}
	in.queue.recycle(batches)
	clear(batches)
	in.delivering = batches[:0]
	return err
}

func (in *Libinput) deliverBatch(b *inputBatch) error {
	var err error
	for _, rec := range b.records {
		switch rec.kind {
		case inputReport:
			in.report(rec.err)
		case inputFailure:
			err = errors.Join(err, rec.err)
		case deviceAdded:
			if in.handler != nil {
				err = errors.Join(err, in.handler.DeviceAdded(rec.dev))
			}
		case deviceRemoved:
			if in.handler != nil {
				err = errors.Join(err, in.handler.DeviceRemoved(rec.dev))
			}
		case inputEvent:
			if in.handler != nil {
				err = errors.Join(err, in.handler.Input(rec.dev, rec.ev))
			}
		}
	}
	if b.dropped > 0 {
		in.report(fmt.Errorf("session: dropped %d libinput events: %d input events were already waiting for the compositor's loop, the most lys-wm's input queue holds", b.dropped, inputQueueBound))
	}
	return err
}

func (in *Libinput) Start(h backend.Handler) {
	in.handler = h
}

var ErrDeclined = errors.New("libinput does not handle the device")

func (in *Libinput) AddLater(path string) {
	in.reader.on("adding the input device "+path, func() { in.reader.addLater(path) })
}

func (in *Libinput) Remove(path string) {
	in.reader.on("removing the input device "+path, func() { in.reader.remove(path) })
}

func (in *Libinput) Suspend() {
	in.reader.on("suspending input", in.reader.suspend)
}

func (in *Libinput) ResumeLater() {
	in.reader.on("resuming input", in.reader.resumeLater)
}

func (in *Libinput) Reconfigure(config InputConfig) error {
	return in.reader.call("reconfigure the input devices", func() error { return in.reader.reconfigure(config) })
}

func (in *Libinput) Park() error {
	err := in.reader.call("park input", func() error {
		err := in.reader.src.Modify(0)
		return errors.Join(err, in.reader.publish())
	})
	return errors.Join(err, in.deliver())
}

func (in *Libinput) Unpark() error {
	return in.reader.call("unpark input", func() error { return in.reader.src.Modify(loop.Readable) })
}

func (in *Libinput) Close() error {
	r := in.reader
	err := r.call("stop taking input devices", func() error {
		r.closing = true
		return nil
	})
	r.work.Wait()
	r.on("stopping the input reader", r.loop.Stop)
	<-r.done
	r.work.Wait()
	return errors.Join(err, r.stopErr, in.deliver(), purego.Dlclose(r.lib))
}

func (r *inputReader) teardown() error {
	r.dropReady()
	var errs []error
	if r.src != nil {
		errs = append(errs, r.src.Remove())
	}
	if r.li != 0 {
		for _, path := range slices.Sorted(maps.Keys(r.paths)) {
			r.remove(path)
		}
		for h, d := range r.devices {
			r.fn.deviceUnref(d.h)
			delete(r.devices, h)
		}
		r.dropTabletTools()
		r.fn.unref(r.li)
	}
	r.forget()
	r.li = 0
	return errors.Join(append(errs, r.publish())...)
}

func (r *inputReader) setLEDs(d *InputDevice, leds int32) {
	if r.devices[d.h] != d {
		return
	}
	r.fn.ledUpdate(d.h, leds)
}

func (r *inputReader) add(path string) error {
	if _, ok := r.paths[path]; ok {
		return nil
	}
	r.openErr = nil
	dev := r.fn.addDevice(r.li, path)
	if dev == 0 {
		if r.openErr != nil {
			return fmt.Errorf("session: add input device %s: %w", path, r.openErr)
		}
		return fmt.Errorf("session: add input device %s: %w", path, ErrDeclined)
	}
	r.paths[path] = r.fn.deviceRef(dev)
	r.drain()
	return nil
}

func (r *inputReader) remove(path string) {
	delete(r.adding, path)
	dev, ok := r.paths[path]
	if !ok {
		return
	}
	delete(r.paths, path)
	r.fn.removeDevice(dev)
	r.fn.deviceUnref(dev)
	r.drain()
}

func (r *inputReader) suspend() {
	r.resumes++
	if r.suspended {
		return
	}
	r.fn.suspend(r.li)
	r.suspended = true
}

func (r *inputReader) resume() error {
	if !r.suspended {
		return nil
	}
	if r.fn.resume(r.li) != 0 {
		return errors.New("session: libinput_resume failed")
	}
	r.suspended = false
	r.drain()
	return nil
}

type takenDevice struct {
	fd  int
	dev Device
	err error
}

func (r *inputReader) take(path string) (int, Device, error) {
	if t, ok := r.ready[path]; ok {
		delete(r.ready, path)
		return t.fd, t.dev, t.err
	}
	return r.opener.TakeDevice(path)
}

func (r *inputReader) releaseLater(dev Device) {
	r.work.Run(func() { r.release(dev) })
}

func (r *inputReader) release(dev Device) {
	if err := r.opener.ReleaseDevice(dev); err != nil {
		r.postReport(fmt.Errorf("session: release input device %d:%d: %w", dev.Major, dev.Minor, err))
	}
}

func (r *inputReader) postReport(err error) {
	r.in.loop.Hand("an input device error", func() error { r.in.report(err); return nil })
}

func (r *inputReader) dropReady() {
	for path, t := range r.ready {
		delete(r.ready, path)
		r.letGo(t)
	}
}

func (r *inputReader) letGo(t takenDevice) {
	if t.err != nil {
		return
	}
	if err := unix.Close(t.fd); err != nil {
		r.filling.report(fmt.Errorf("session: close input device fd %d: %w", t.fd, err))
	}
	r.releaseLater(t.dev)
}

func (r *inputReader) addLater(path string) {
	if r.closing {
		return
	}
	if _, ok := r.paths[path]; ok {
		return
	}
	if _, ok := r.adding[path]; ok {
		return
	}
	r.adding[path] = struct{}{}
	r.work.Run(func() {
		var t takenDevice
		t.fd, t.dev, t.err = r.opener.TakeDevice(path)
		r.handBack("the taken input device "+path, func() { r.added(path, t) }, t)
	})
}

func (r *inputReader) handBack(what string, then func(), taken ...takenDevice) {
	err := r.loop.Post(func() error {
		then()
		return nil
	})
	if err == nil {
		return
	}
	if !errors.Is(err, loop.ErrClosed) {
		panic(fmt.Errorf("session: hand %s to the input reader: %w", what, err))
	}
	for _, t := range taken {
		if t.err != nil {
			continue
		}
		if err := unix.Close(t.fd); err != nil {
			r.postReport(fmt.Errorf("session: close input device fd %d: %w", t.fd, err))
		}
		r.release(t.dev)
	}
}

func (r *inputReader) added(path string, t takenDevice) {
	if _, wanted := r.adding[path]; !wanted || r.closing {
		delete(r.adding, path)
		r.letGo(t)
		return
	}
	delete(r.adding, path)
	r.ready[path] = t
	err := r.add(path)
	if t, unused := r.ready[path]; unused {
		delete(r.ready, path)
		r.letGo(t)
	}
	if err != nil && !errors.Is(err, ErrDeclined) {
		r.filling.report(err)
	}
}

func (r *inputReader) resumeLater() {
	if !r.suspended || r.closing {
		return
	}
	r.resumes++
	resume := r.resumes
	paths := slices.Sorted(maps.Keys(r.paths))
	r.work.Run(func() {
		taken := make(map[string]takenDevice, len(paths))
		for _, path := range paths {
			var t takenDevice
			t.fd, t.dev, t.err = r.opener.TakeDevice(path)
			taken[path] = t
		}
		r.handBack("the resumed input devices", func() { r.resumed(resume, taken) }, slices.Collect(maps.Values(taken))...)
	})
}

func (r *inputReader) resumed(resume uint64, taken map[string]takenDevice) {
	if resume != r.resumes || r.closing {
		for _, t := range taken {
			r.letGo(t)
		}
		return
	}
	maps.Copy(r.ready, taken)
	if err := r.resume(); err != nil {
		r.filling.fail(err)
	}
	r.dropReady()
}

func (r *inputReader) read(loop.Events) error {
	if rc := r.fn.dispatch(r.li); rc != 0 {
		r.filling.fail(errors.Join(fmt.Errorf("session: libinput_dispatch: %w", unix.Errno(-rc)), r.src.Modify(0))) //nolint:gosec // libinput returns a negated errno
		return nil
	}
	r.drain()
	return nil
}

func (r *inputReader) drain() {
	for ev := r.fn.getEvent(r.li); ev != 0; ev = r.fn.getEvent(r.li) {
		r.event(ev)
		r.fn.destroyEvent(ev)
	}
}

func usec(v uint64) time.Duration {
	return time.Duration(v) * time.Microsecond //nolint:gosec // monotonic microseconds fit a Duration for centuries
}

func (r *inputReader) event(ev uintptr) {
	f := &r.fn
	h := f.eventDevice(ev)
	typ := f.eventType(ev)
	switch typ {
	case evDeviceAdded:
		d := &InputDevice{name: f.deviceName(h), h: f.deviceRef(h), touchpad: f.config.tapFingers(h) > 0, scrollFactor: 1, reader: r}
		for c, flag := range map[int32]backend.Capability{capKeyboard: backend.Keyboard, capPointer: backend.Pointer, capTouch: backend.Touch} {
			if f.deviceHasCap(h, c) != 0 {
				d.caps |= flag
			}
		}
		d.group = f.pad.deviceGroup(h)
		if f.deviceHasCap(h, capTabletTool) != 0 {
			d.tablet, d.tabletID = true, tabletinput.ID{Vendor: f.tablet.vendor(h), Product: f.tablet.product(h)}
		}
		if f.deviceHasCap(h, capTabletPad) != 0 {
			d.pad, d.padLayout = true, r.padLayout(h)
		}
		r.devices[h] = d
		if err := r.configure(d); err != nil {
			r.filling.report(err)
		}
		r.filling.device(deviceAdded, d)
		return
	case evDeviceRemoved:
		d, ok := r.devices[h]
		if !ok {
			return
		}
		delete(r.devices, h)
		f.deviceUnref(d.h)
		r.filling.device(deviceRemoved, d)
		return
	}
	d, ok := r.devices[h]
	if !ok {
		return
	}
	if r.queue.full(r.filling) {
		r.filling.dropped++
		return
	}
	r.out = r.translate(r.out[:0], &r.filling.events, d, ev, typ)
	for _, e := range r.out {
		r.filling.input(d, e)
	}
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

func pointerFrame(out []backend.Event, into *translated, at time.Duration) []backend.Event {
	return append(out, into.frame.put(backend.PointerFrame{Time: at}))
}

func (r *inputReader) translate(out []backend.Event, into *translated, d *InputDevice, ev uintptr, typ int32) []backend.Event {
	f := &r.fn
	switch typ {
	case evKeyboardKey:
		k := f.keyboardEvent(ev)
		return append(out, into.key.put(backend.Key{Time: usec(f.keyTime(k)), Code: f.key(k), Pressed: f.keyState(k) == 1}))
	case evPointerMotion:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		return pointerFrame(append(out, into.motion.put(backend.PointerMotion{Time: at, DX: f.dx(p), DY: f.dy(p), UnaccelDX: f.dxRaw(p), UnaccelDY: f.dyRaw(p)})), into, at)
	case evPointerAbsolute:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		return pointerFrame(append(out, into.absolute.put(backend.PointerMotionAbsolute{Time: at, X: f.absX(p, 1), Y: f.absY(p, 1)})), into, at)
	case evPointerButton:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		return pointerFrame(append(out, into.button.put(backend.PointerButton{Time: at, Button: f.button(p), Pressed: f.buttonState(p) == 1})), into, at)
	case evScrollWheel, evScrollFinger, evScrollContinuous:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		for _, a := range scrollAxes {
			if f.hasAxis(p, a.axis) == 0 {
				continue
			}
			ax := backend.PointerAxis{Time: at, Orientation: a.orientation, Source: axisSources[typ], Value: f.scrollValue(p, a.axis) * d.scrollFactor}
			if typ == evScrollWheel {
				ax.Value120 = int32(math.Round(f.scrollV120(p, a.axis) * d.scrollFactor))
			}
			out = append(out, into.axis.put(ax))
		}
		return pointerFrame(out, into, at)
	case evTouchDown, evTouchMotion:
		t := f.touchEvent(ev)
		at, id, x, y := usec(f.touchTime(t)), f.touchSlot(t), f.touchX(t, 1), f.touchY(t, 1)
		if typ == evTouchDown {
			return append(out, into.touchDown.put(backend.TouchDown{Time: at, ID: id, X: x, Y: y}))
		}
		return append(out, into.touchMotion.put(backend.TouchMotion{Time: at, ID: id, X: x, Y: y}))
	case evTouchUp:
		t := f.touchEvent(ev)
		return append(out, into.touchUp.put(backend.TouchUp{Time: usec(f.touchTime(t)), ID: f.touchSlot(t)}))
	case evTouchCancel:
		t := f.touchEvent(ev)
		return append(out, into.touchCancel.put(backend.TouchCancel{Time: usec(f.touchTime(t)), ID: f.touchSlot(t)}))
	case evTouchFrame:
		return append(out, into.touchFrame.put(backend.TouchFrame{Time: usec(f.touchTime(f.touchEvent(ev)))}))
	case evSwipeBegin, evPinchBegin, evHoldBegin:
		g := f.gestureEvent(ev)
		return append(out, backend.GestureBegin{Time: usec(f.gestureTime(g)), Gesture: gestures[typ], Fingers: uint32(max(f.fingers(g), 0))})
	case evSwipeUpdate:
		g := f.gestureEvent(ev)
		return append(out, into.swipe.put(backend.SwipeUpdate{Time: usec(f.gestureTime(g)), DX: f.gestureDX(g), DY: f.gestureDY(g), UnaccelDX: f.gestureUnaccelDX(g), UnaccelDY: f.gestureUnaccelDY(g)}))
	case evPinchUpdate:
		g := f.gestureEvent(ev)
		return append(out, into.pinch.put(backend.PinchUpdate{Time: usec(f.gestureTime(g)), DX: f.gestureDX(g), DY: f.gestureDY(g), Scale: f.gestureScale(g), Rotation: f.gestureAngle(g)}))
	case evSwipeEnd, evPinchEnd, evHoldEnd:
		g := f.gestureEvent(ev)
		return append(out, backend.GestureEnd{Time: usec(f.gestureTime(g)), Gesture: gestures[typ], Cancelled: f.cancelled(g) != 0})
	case evTabletToolAxis, evTabletToolProximity, evTabletToolTip, evTabletToolButton:
		return r.translateTablet(out, into, ev, typ)
	case evTabletPadButton, evTabletPadRing, evTabletPadStrip, evTabletPadDial:
		return r.translatePad(out, into, ev, typ)
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
