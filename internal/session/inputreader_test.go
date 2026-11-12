package session

import (
	"encoding/binary"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
)

const (
	evdevGrab        = 0x40044590
	burstKeyboard    = "lys-wm virtual burst keyboard"
	firstBurstKey    = 2
	burstKeys        = 64
	kernelBufferKeys = 16
)

type grabbingOpener struct{ Opener }

func (o grabbingOpener) TakeDevice(path string) (int, Device, error) {
	fd, d, err := o.Opener.TakeDevice(path)
	if err != nil {
		return fd, d, err
	}
	if err := ioctl(uintptr(fd), evdevGrab, 1); err != nil {
		_ = unix.Close(fd)
		return -1, Device{}, fmt.Errorf("grab %s: %w", path, err)
	}
	return fd, d, nil
}

func newBurstKeyboard(t *testing.T) *virtualDevice {
	t.Helper()
	bits := []uinputBit{{uiSetEvBit, evKey}}
	for code := range burstKeys {
		bits = append(bits, uinputBit{uiSetKeyBit, uintptr(firstBurstKey + code)})
	}
	return newVirtualDevice(t, burstKeyboard, bits)
}

func keyStroke(code int32, pressed bool) []byte {
	value := uint32(0)
	if pressed {
		value = 1
	}
	buf := make([]byte, 0, 48)
	for _, e := range [][3]uint32{{evKey, uint32(code), value}, {evSyn, 0, 0}} {
		buf = append(buf, make([]byte, 16)...)
		buf = binary.NativeEndian.AppendUint16(buf, uint16(e[0]))
		buf = binary.NativeEndian.AppendUint16(buf, uint16(e[1]))
		buf = binary.NativeEndian.AppendUint32(buf, e[2])
	}
	return buf
}

func typeKeys(f *os.File, codes []int32, pace time.Duration) error {
	for _, code := range codes {
		for _, pressed := range []bool{true, false} {
			if _, err := f.Write(keyStroke(code, pressed)); err != nil {
				return fmt.Errorf("type key %d: %w", code, err)
			}
			time.Sleep(pace)
		}
	}
	return nil
}

func burstCodes(n int) []int32 {
	codes := make([]int32, n)
	for i := range codes {
		codes[i] = int32(firstBurstKey + i%burstKeys)
	}
	return codes
}

type keyRecorder struct {
	backend.Handler
	added, removed int
	keys           []backend.Key
}

func (r *keyRecorder) DeviceAdded(backend.Device) error {
	r.added++
	return nil
}

func (r *keyRecorder) DeviceRemoved(backend.Device) error {
	r.removed++
	return nil
}

func (r *keyRecorder) Input(_ backend.Device, e backend.Event) error {
	if k, ok := e.(*backend.Key); ok {
		r.keys = append(r.keys, *k)
	}
	return nil
}

func (r *keyRecorder) strokes(codes []int32) []backend.Key {
	want := make([]backend.Key, 0, 2*len(codes))
	for _, code := range codes {
		want = append(want, backend.Key{Code: uint32(code), Pressed: true}, backend.Key{Code: uint32(code)})
	}
	return want
}

func (r *keyRecorder) requireStrokes(t *testing.T, codes []int32) {
	t.Helper()
	want := r.strokes(codes)
	got := make([]backend.Key, len(r.keys))
	for i, k := range r.keys {
		got[i] = backend.Key{Code: k.Code, Pressed: k.Pressed}
	}
	if !slices.Equal(got, want) {
		at := 0
		for at < min(len(got), len(want)) && got[at] == want[at] {
			at++
		}
		t.Fatalf("the handler got %d of the %d key events typed; they part at event %d", len(got), len(want), at)
	}
	for i := 1; i < len(r.keys); i++ {
		if r.keys[i].Time < r.keys[i-1].Time || r.keys[i].Time <= 0 {
			t.Fatalf("key event %d at %v follows one at %v: the kernel's timestamps were lost", i, r.keys[i].Time, r.keys[i-1].Time)
		}
	}
}

func runUntil(t testing.TB, l *loop.Loop, what string, done func() bool) {
	t.Helper()
	if !runAwhile(t, l, 10*time.Second, done) {
		t.Fatalf("%s did not happen within 10s", what)
	}
}

func runAwhile(t testing.TB, l *loop.Loop, within time.Duration, done func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	var timer *loop.Timer
	timer, err := l.AddTimer(func() error {
		if done() || time.Now().After(deadline) {
			l.Stop()
			return nil
		}
		return timer.Arm(2 * time.Millisecond)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := timer.Remove(); err != nil {
			t.Error(err)
		}
	}()
	if err := timer.Arm(0); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(); err != nil {
		t.Fatal(err)
	}
	return done()
}

func addKeyboard(t *testing.T, in *Libinput, rec *keyRecorder, kbd *virtualDevice) {
	t.Helper()
	in.Start(rec)
	in.AddLater(kbd.node)
	runUntil(t, in.loop, "the keyboard's DeviceAdded", func() bool { return rec.added == 1 })
}

type reportLog struct {
	t       *testing.T
	reports []error
}

func (l *reportLog) report(err error) {
	l.t.Log(err)
	l.reports = append(l.reports, err)
}

func (l *reportLog) saying(what string) bool {
	return slices.ContainsFunc(l.reports, func(err error) bool { return strings.Contains(err.Error(), what) })
}

func (l *reportLog) requireNoLoss() {
	l.t.Helper()
	for _, loss := range []string{synDropped, "dropped"} {
		if l.saying(loss) {
			l.t.Fatalf("input was lost: %v", l.reports)
		}
	}
}

func TestKeysTypedWhileTheLoopIsBlockedAllReachTheHandlerInOrder(t *testing.T) {
	kbd := newBurstKeyboard(t)
	log := &reportLog{t: t}
	in := libinputReporting(t, grabbingOpener{PlainOpener{}}, log.report)
	rec := &keyRecorder{}
	addKeyboard(t, in, rec, kbd)
	codes := burstCodes(4 * kernelBufferKeys)
	typed := make(chan error, 1)
	if err := in.loop.Post(func() error {
		go func() { typed <- typeKeys(kbd.f, codes, time.Millisecond) }()
		return <-typed
	}); err != nil {
		t.Fatal(err)
	}
	runAwhile(t, in.loop, 10*time.Second, func() bool { return len(rec.keys) >= 2*len(codes) })
	rec.requireStrokes(t, codes)
	log.requireNoLoss()
}

func TestAKernelOverflowIsReported(t *testing.T) {
	kbd := newBurstKeyboard(t)
	log := &reportLog{t: t}
	in := libinputReporting(t, grabbingOpener{PlainOpener{}}, log.report)
	addKeyboard(t, in, &keyRecorder{}, kbd)
	var burst []byte
	for _, code := range burstCodes(4 * kernelBufferKeys) {
		burst = append(append(burst, keyStroke(code, true)...), keyStroke(code, false)...)
	}
	if _, err := kbd.f.Write(burst); err != nil {
		t.Fatal(err)
	}
	runUntil(t, in.loop, "a report of the kernel's SYN_DROPPED", func() bool { return log.saying(synDropped) })
}

func grabbedKeys(t *testing.T, kbd *virtualDevice, codes []int32) {
	t.Helper()
	f, err := os.Open(kbd.node)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := ioctl(f.Fd(), evdevGrab, 1); err != nil {
		t.Fatalf("grab %s while lys-wm's input is suspended: %v", kbd.node, err)
	}
	if err := typeKeys(kbd.f, codes, 0); err != nil {
		t.Fatal(err)
	}
	keys := 0
	buf := make([]byte, 24*64)
	for keys < 2*len(codes) {
		n, err := f.Read(buf)
		if err != nil {
			t.Fatalf("read back the keys typed while suspended: %v", err)
		}
		for e := buf[:n]; len(e) >= 24; e = e[24:] {
			if binary.NativeEndian.Uint16(e[16:]) == evKey {
				keys++
			}
		}
	}
}

func TestSuspendingInputStopsReadingAndResumingRestartsIt(t *testing.T) {
	kbd := newBurstKeyboard(t)
	in := libinput(t, grabbingOpener{&takeOnceOpener{taken: map[Device]bool{}}})
	rec := &keyRecorder{}
	addKeyboard(t, in, rec, kbd)
	in.Suspend()
	in.onReader(t, "wait for the suspend", func(*inputReader) error { return nil })
	in.reader.work.Wait()
	grabbedKeys(t, kbd, burstCodes(3))
	in.ResumeLater()
	runUntil(t, in.loop, "the keyboard coming back", func() bool { return rec.added == 2 })
	if rec.removed != 1 {
		t.Fatalf("the keyboard was removed %d times over a suspend", rec.removed)
	}
	after := burstCodes(kernelBufferKeys)[3:]
	if err := typeKeys(kbd.f, after, 0); err != nil {
		t.Fatal(err)
	}
	runUntil(t, in.loop, "the keys typed after the resume", func() bool { return len(rec.keys) >= 2*len(after) })
	rec.requireStrokes(t, after)
}

func TestParkingInputDeliversWhatWasReadAndReadsNothingUntilUnparked(t *testing.T) {
	kbd := newBurstKeyboard(t)
	log := &reportLog{t: t}
	in := libinputReporting(t, grabbingOpener{PlainOpener{}}, log.report)
	rec := &keyRecorder{}
	addKeyboard(t, in, rec, kbd)
	codes := burstCodes(5)
	before, parked := codes[:2], codes[2:]
	if err := typeKeys(kbd.f, before, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for in.queue.held.Load() < int64(2*len(before)) {
		if time.Now().After(deadline) {
			t.Fatalf("the reader queued %d of %d key events", in.queue.held.Load(), 2*len(before))
		}
		time.Sleep(time.Millisecond)
	}
	if err := in.Park(); err != nil {
		t.Fatal(err)
	}
	rec.requireStrokes(t, before)
	if err := typeKeys(kbd.f, parked, 0); err != nil {
		t.Fatal(err)
	}
	if runAwhile(t, in.loop, 200*time.Millisecond, func() bool { return len(rec.keys) > 2*len(before) }) {
		t.Fatal("parked input still read keys")
	}
	if err := in.Unpark(); err != nil {
		t.Fatal(err)
	}
	runUntil(t, in.loop, "the keys typed while parked", func() bool { return len(rec.keys) >= 2*len(codes) })
	rec.requireStrokes(t, codes)
	log.requireNoLoss()
}

func TestAFullInputQueueDropsAndReportsWhatItDropped(t *testing.T) {
	r := fakeInput()
	r.fn.eventDevice = func(uintptr) uintptr { return 1 }
	r.fn.eventType = func(uintptr) int32 { return evKeyboardKey }
	d := &InputDevice{name: "kbd", h: 1, scrollFactor: 1}
	r.devices = map[uintptr]*InputDevice{1: d}
	r.queue, r.filling = &inputQueue{}, &inputBatch{}
	r.queue.held.Store(inputQueueBound - 1)
	r.event(1)
	r.event(1)
	r.event(1)
	var reports []error
	rec := &keyRecorder{}
	in := &Libinput{queue: r.queue, handler: rec, report: func(err error) { reports = append(reports, err) }}
	if err := in.deliverBatch(r.filling); err != nil {
		t.Fatal(err)
	}
	if len(rec.keys) != 1 {
		t.Fatalf("a queue one short of its bound took %d keys, want 1", len(rec.keys))
	}
	if len(reports) != 1 || !strings.Contains(reports[0].Error(), "dropped 2 libinput events") {
		t.Fatalf("dropping two events reported %v", reports)
	}
}
