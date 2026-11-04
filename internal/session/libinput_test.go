package session

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

type refusingOpener struct{ asked []string }

func (o *refusingOpener) TakeDevice(path string) (int, Device, error) {
	o.asked = append(o.asked, path)
	return -1, Device{}, unix.EPERM
}

func (o *refusingOpener) ReleaseDevice(Device) error { return nil }

func libinput(t testing.TB, opener Opener) *Libinput {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	path := LibinputPath(os.Getenv)
	in, err := OpenLibinput(l, path, opener, InputConfig{}, func(err error) { t.Errorf("reported: %v", err) })
	if err != nil {
		if _, statErr := os.Stat(path); statErr != nil {
			testenv.Require(t, "libinput")
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := in.Close(); err != nil {
			t.Error(err)
		}
	})
	return in
}

func evdevNode(t *testing.T) string {
	t.Helper()
	nodes, err := filepath.Glob("/dev/input/event*")
	if err != nil || len(nodes) == 0 {
		t.Skip("no evdev nodes here")
	}
	slices.Sort(nodes)
	return nodes[0]
}

func TestLibinputAsksTheOpenerForDevices(t *testing.T) {
	node := evdevNode(t)
	opener := &refusingOpener{}
	in := libinput(t, opener)
	err := in.Add(node)
	if !errors.Is(err, unix.EPERM) || errors.Is(err, ErrDeclined) || !strings.Contains(err.Error(), node) {
		t.Fatalf("a refused open: %v", err)
	}
	if !slices.Equal(opener.asked, []string{node}) {
		t.Fatalf("the opener was asked for %v", opener.asked)
	}
	in.Suspend()
	if err := in.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := in.Remove(node); err != nil {
		t.Fatal(err)
	}
}

func TestLibinputRejectsNonInputDevices(t *testing.T) {
	in := libinput(t, PlainOpener{})
	if err := in.Add(os.DevNull); !errors.Is(err, ErrDeclined) {
		t.Fatalf("/dev/null as an input device: %v", err)
	}
	if _, err := OpenLibinput(nil, "/nonexistent/libinput.so.10", PlainOpener{}, InputConfig{}, nil); err == nil || !strings.Contains(err.Error(), LibinputEnv) {
		t.Fatalf("a missing library: %v", err)
	}
}

func TestLibinputDeclinesAHeadphoneJack(t *testing.T) {
	jack := newVirtualDevice(t, "lys-wm virtual headphone jack", []uinputBit{
		{uiSetEvBit, evSw},
		{uiSetSwBit, swHeadphoneInsert},
	})
	in := libinput(t, PlainOpener{})
	if err := in.Add(jack.node); !errors.Is(err, ErrDeclined) {
		t.Fatalf("a headphone jack: %v", err)
	}
}

func TestPlainOpenerNamesTheDeviceItOpened(t *testing.T) {
	fd, d, err := PlainOpener{}.TakeDevice("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if d != (Device{Major: 1, Minor: 3}) {
		t.Fatalf("/dev/null is %+v", d)
	}
	if _, _, err := (PlainOpener{}).TakeDevice("/"); err == nil {
		t.Fatal("a directory was taken as a device")
	}
}

// takeOnceOpener lends a device once until it is released, as logind's
// TakeDevice does.
type takeOnceOpener struct {
	taken map[Device]bool
}

func (o *takeOnceOpener) TakeDevice(path string) (int, Device, error) {
	d, err := DeviceOf(path)
	if err != nil {
		return -1, Device{}, err
	}
	if o.taken[d] {
		return -1, Device{}, unix.EBUSY
	}
	fd, d, err := PlainOpener{}.TakeDevice(path)
	if err == nil {
		o.taken[d] = true
	}
	return fd, d, err
}

func (o *takeOnceOpener) ReleaseDevice(d Device) error {
	delete(o.taken, d)
	return nil
}

// A session that turns active without having been inactive resumes
// nothing: libinput resuming devices it never let go of takes each a
// second time, which logind refuses, and lys-wm used to exit on that
// when a greeter's session turned active right after it started.
func TestLibinputResumesOnlyAfterASuspend(t *testing.T) {
	kbd := newVirtualKeyboard(t)
	opener := &takeOnceOpener{taken: map[Device]bool{}}
	in := libinput(t, opener)
	if err := in.Add(kbd.node); err != nil {
		t.Fatal(err)
	}
	if err := in.Resume(); err != nil {
		t.Fatalf("a resume without a suspend: %v", err)
	}
	in.Suspend()
	in.Suspend()
	if err := in.Resume(); err != nil {
		t.Fatalf("a resume after a suspend: %v", err)
	}
	if d, _ := DeviceOf(kbd.node); !opener.taken[d] {
		t.Error("the keyboard was not taken back after the resume")
	}
}

type blockingOpener struct {
	entered, release chan struct{}
	released         chan Device
}

func (o *blockingOpener) TakeDevice(path string) (int, Device, error) {
	close(o.entered)
	<-o.release
	return PlainOpener{}.TakeDevice(path)
}

func (o *blockingOpener) ReleaseDevice(d Device) error {
	o.released <- d
	return nil
}

func TestAnInputDeviceIsTakenOffTheLoop(t *testing.T) {
	opener := &blockingOpener{entered: make(chan struct{}), release: make(chan struct{}), released: make(chan Device, 1)}
	in := libinput(t, opener)
	in.AddLater("/dev/null")
	<-opener.entered
	ticks := 0
	if err := in.loop.Post(func() error {
		ticks++
		in.loop.Stop()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := in.loop.Run(); err != nil {
		t.Fatal(err)
	}
	if ticks != 1 {
		t.Fatalf("%d posted ticks ran while an opener was stuck in logind", ticks)
	}
	if _, adding := in.adding["/dev/null"]; !adding {
		t.Fatal("the device was not in flight while the opener held it")
	}
	if err := in.loop.Post(func() error { close(opener.release); return nil }); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(30 * time.Second)
	for {
		var pending bool
		if err := in.loop.Post(func() error {
			_, pending = in.adding["/dev/null"]
			in.loop.Stop()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := in.loop.Run(); err != nil {
			t.Fatal(err)
		}
		if !pending {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the taken device never reached the loop")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if len(in.ready) != 0 || len(in.paths) != 0 {
		t.Fatalf("a device libinput declines left ready %v, paths %v", in.ready, in.paths)
	}
	select {
	case <-opener.released:
	case <-deadline:
		t.Fatal("the declined device was never released")
	}
}

func TestAReleaseDoesNotWaitForTheOpener(t *testing.T) {
	opener := &blockingOpener{entered: make(chan struct{}), release: make(chan struct{}), released: make(chan Device)}
	in := libinput(t, opener)
	in.releaseLater(Device{Major: 13, Minor: 64})
	select {
	case d := <-opener.released:
		if d.Major != 13 {
			t.Fatalf("released %+v", d)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the release never ran")
	}
}
