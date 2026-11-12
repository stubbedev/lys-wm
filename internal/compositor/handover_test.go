package compositor

import (
	"errors"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/handover"
	"github.com/stubbedev/lys-wm/internal/loop"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

type inputDevice backend.Capability

func (inputDevice) Name() string { return "test input" }

func (d inputDevice) Capabilities() backend.Capability { return backend.Capability(d) }

var inputDevices = []inputDevice{inputDevice(backend.Keyboard), inputDevice(backend.Pointer), inputDevice(backend.Touch)}

func (r *rig) plug() {
	r.t.Helper()
	r.on(func() {
		for _, d := range inputDevices {
			if err := r.comp.DeviceAdded(d); err != nil {
				r.t.Error(err)
			}
		}
	})
}

type seatLog struct{ caps []cw.WlSeatCapability }

func (s *seatLog) Capabilities(_ *cw.WlSeat, caps cw.WlSeatCapability) error {
	s.caps = append(s.caps, caps)
	return nil
}

func (*seatLog) Name(*cw.WlSeat, string) error { return nil }

type keymapTaker struct{ cw.WlKeyboardHandler }

func (keymapTaker) Keymap(_ *cw.WlKeyboard, _ cw.WlKeyboardKeymapFormat, fd int, _ uint32) error {
	return unix.Close(fd)
}

func (keymapTaker) RepeatInfo(*cw.WlKeyboard, int32, int32) error { return nil }

type touchCancelled struct{ cw.WlTouchHandler }

func (touchCancelled) Cancel(*cw.WlTouch) error { return nil }

func (r *rig) handOver() *rig {
	r.t.Helper()
	var pair [2]int
	var err error
	if pair, err = unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0); err != nil {
		r.t.Fatal(err)
	}
	defer unix.Close(pair[0])
	defer unix.Close(pair[1])
	r.on(func() {
		fds := &handover.FDs{}
		defer fds.CloseOwned()
		if err = errors.Join(r.comp.display.Park(), r.comp.Settle()); err != nil {
			return
		}
		r.comp.Park()
		var st *HandoverState
		if st, err = r.comp.HandoverState(fds, func(*server.Client) bool { return false }); err == nil {
			err = handover.Send(pair[0], st, fds)
		}
	})
	if err != nil {
		r.t.Fatal(err)
	}
	st := &HandoverState{}
	fds, err := handover.Receive(pair[1], st)
	if err != nil {
		r.t.Fatal(err)
	}
	next := boot(r.t, func(l *loop.Loop, opts server.Options) (*server.Display, error) {
		return server.Adopt(l, opts, st.Display, fds)
	}, func(_ *loop.Loop, s *spyRenderer) backend.Backend { return fakeBackend{s} }, func(*Config) {})
	var release func() error
	next.on(func() { release, err = next.comp.AdoptHandover(st, fds) })
	if err != nil {
		r.t.Fatal(err)
	}
	next.plug()
	next.on(func() { err = errors.Join(next.comp.FinishAdoption(), release(), next.comp.CompleteAdoption()) })
	if err != nil {
		r.t.Fatal(err)
	}
	return next
}

func TestAnUpgradeKeepsAClientsInputObjectsThoughTheNextCompositorsDevicesComeLater(t *testing.T) {
	dir := testenv.SocketDir(t)
	old := boot(t, func(l *loop.Loop, opts server.Options) (*server.Display, error) {
		opts.RuntimeDir = dir
		return server.New(l, opts)
	}, func(_ *loop.Loop, s *spyRenderer) backend.Backend { return fakeBackend{s} }, func(*Config) {})
	old.on(func() { old.comp.display.KeepHandoverJournal() })
	old.plug()
	old.connect()
	seen := &seatLog{}
	seat, err := cw.NewWlSeat(old.client, 9, seen)
	must(t, err, old.registry.Bind(old.globals["wl_seat"], seat))
	keyboard, err := seat.GetKeyboard(keymapTaker{})
	must(t, err)
	pointer, err := seat.GetPointer(struct{ cw.WlPointerHandler }{})
	must(t, err)
	touch, err := seat.GetTouch(touchCancelled{})
	must(t, err)
	old.roundtrip()

	next := old.handOver()
	old.roundtrip()
	every := cw.WlSeatCapabilityKeyboard | cw.WlSeatCapabilityPointer | cw.WlSeatCapabilityTouch
	if len(seen.caps) == 0 || slices.ContainsFunc(seen.caps, func(caps cw.WlSeatCapability) bool { return caps != every }) {
		t.Fatalf("capabilities %v: the client was told of a loss though every device came back", seen.caps)
	}
	var kept []string
	next.on(func() {
		for _, o := range next.comp.display.Clients()[0].Objects() {
			if name := o.Base().Interface().Name; name == "wl_keyboard" || name == "wl_pointer" || name == "wl_touch" {
				kept = append(kept, name)
			}
		}
	})
	slices.Sort(kept)
	if want := []string{"wl_keyboard", "wl_pointer", "wl_touch"}; !slices.Equal(kept, want) {
		t.Fatalf("the next compositor rebuilt %v, want %v", kept, want)
	}
	must(t, keyboard.Release(), pointer.Release(), touch.Release())
	old.roundtrip()
}
