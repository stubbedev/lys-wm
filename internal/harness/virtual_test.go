package harness

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/vinput"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	clvt "github.com/stubbedev/lys-wm/internal/protocol/client/lysvirtualtouch"
	cvk "github.com/stubbedev/lys-wm/internal/protocol/client/virtualkeyboard"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cvp "github.com/stubbedev/lys-wm/internal/protocol/client/wlrvirtualpointer"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const typedKeymap = "xkb_keymap { xkb_keycodes \"typed\" {}; };\x00"

func keymapFD(t *testing.T, padding int) (int, uint32) {
	t.Helper()
	fd, err := unix.MemfdCreate("keymap", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	keymap := typedKeymap + strings.Repeat("\n", padding)
	if _, err := unix.Write(fd, []byte(keymap)); err != nil {
		t.Fatal(err)
	}
	return fd, uint32(len(keymap))
}

func TestVirtualKeyboardsTypeWithTheirOwnKeymap(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	real := r.in.KeymapSize
	typist := testclient.Connect(t, r.h.Socket())
	seat, _ := typist.Seat()
	mgr, err := cvk.NewZwpVirtualKeyboardManagerV1(typist.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	typist.Bind(mgr)
	kb, err := mgr.CreateVirtualKeyboard(seat)
	if err != nil {
		t.Fatal(err)
	}
	fd, size := keymapFD(t, 0)
	if err := errors.Join(kb.Keymap(uint32(cw.WlKeyboardKeymapFormatXkbV1), fd, size), unix.Close(fd),
		kb.Key(1, 30, uint32(cw.WlKeyboardKeyStatePressed)), kb.Key(2, 30, uint32(cw.WlKeyboardKeyStateReleased)), typist.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("key 30 1", "key 30 0")
	if r.in.KeymapSize != size {
		t.Fatalf("typed with a %d-byte keymap, want the virtual keyboard's %d", r.in.KeymapSize, size)
	}
	fd, size = keymapFD(t, 7)
	if err := errors.Join(kb.Keymap(uint32(cw.WlKeyboardKeymapFormatXkbV1), fd, size), unix.Close(fd),
		kb.Key(3, 30, uint32(cw.WlKeyboardKeyStatePressed)), kb.Key(4, 30, uint32(cw.WlKeyboardKeyStateReleased)), typist.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("key 30 1", "key 30 0")
	if r.in.KeymapSize != size {
		t.Fatalf("after a second keymap, typed with a %d-byte keymap, want the new %d", r.in.KeymapSize, size)
	}
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Keyboard().Key(31, true), b.Keyboard().Key(31, false))
	})
	r.expect("key 31 1", "key 31 0")
	if r.in.KeymapSize != real {
		t.Fatalf("a physical key came with a %d-byte keymap, want the real %d", r.in.KeymapSize, real)
	}
	nokeys, err := mgr.CreateVirtualKeyboard(seat)
	if err != nil {
		t.Fatal(err)
	}
	if err := nokeys.Key(3, 30, 1); err != nil {
		t.Fatal(err)
	}
	typist.ExpectProtocolError(uint32(cvk.ZwpVirtualKeyboardV1ErrorNoKeymap))
}

func TestVirtualPointersMoveClickAndScroll(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	driver := testclient.Connect(t, r.h.Socket())
	seat, _ := driver.Seat()
	mgr, err := cvp.NewZwlrVirtualPointerManagerV1(driver.Display, 2)
	if err != nil {
		t.Fatal(err)
	}
	driver.Bind(mgr)
	vp, err := mgr.CreateVirtualPointerWithOutput(seat, driver.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(vp.MotionAbsolute(1, 40, 25, 160, 100), vp.Frame(), driver.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("motion 40,25", "frame")
	if err := errors.Join(vp.Motion(2, wire.FixedFromFloat(10), wire.FixedFromFloat(5)), vp.Frame(),
		vp.Button(3, 0x110, cw.WlPointerButtonStatePressed), vp.Frame(), vp.Button(4, 0x110, cw.WlPointerButtonStateReleased), vp.Frame(),
		vp.AxisSource(cw.WlPointerAxisSourceWheel), vp.AxisDiscrete(5, cw.WlPointerAxisVerticalScroll, wire.FixedFromFloat(15), 1), vp.Frame(),
		driver.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("motion 50,30", "frame", "button 272 1", "frame", "button 272 0", "frame")
	r.c.DispatchUntil("the scroll", func() bool {
		return slices.ContainsFunc(r.in.Log, func(s string) bool { return s == "axis 0 15" })
	})
	if err := errors.Join(vp.AxisSource(cw.WlPointerAxisSourceFinger), vp.AxisStop(6, cw.WlPointerAxisVerticalScroll), vp.Frame(), driver.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.c.DispatchUntil("the scroll stop", func() bool { return slices.Contains(r.in.Log, "axis_stop 0") })
}

type keySerials struct{ serials []uint32 }

func (*keySerials) Keymap(_ *cw.WlKeyboard, _ cw.WlKeyboardKeymapFormat, fd int, _ uint32) error {
	return unix.Close(fd)
}

func (*keySerials) Enter(*cw.WlKeyboard, uint32, *cw.WlSurface, []byte) error { return nil }

func (*keySerials) Leave(*cw.WlKeyboard, uint32, *cw.WlSurface) error { return nil }

func (k *keySerials) Key(_ *cw.WlKeyboard, serial, _, _ uint32, _ cw.WlKeyboardKeyState) error {
	k.serials = append(k.serials, serial)
	return nil
}

func (*keySerials) Modifiers(*cw.WlKeyboard, uint32, uint32, uint32, uint32, uint32) error {
	return nil
}

func (*keySerials) RepeatInfo(*cw.WlKeyboard, int32, int32) error { return nil }

func TestEveryKeyboardOfAClientSharesAKeysSerial(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	a, b := &keySerials{}, &keySerials{}
	for _, k := range []*keySerials{a, b} {
		if _, err := r.seat.GetKeyboard(k); err != nil {
			t.Fatal(err)
		}
	}
	r.c.Sync()
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Keyboard().Key(30, true), b.Keyboard().Key(30, false))
	})
	r.c.DispatchUntil("the physical key on both keyboards", func() bool { return len(a.serials) == 2 && len(b.serials) == 2 })

	typist := testclient.Connect(t, r.h.Socket())
	seat, _ := typist.Seat()
	mgr, err := cvk.NewZwpVirtualKeyboardManagerV1(typist.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	typist.Bind(mgr)
	kb, err := mgr.CreateVirtualKeyboard(seat)
	if err != nil {
		t.Fatal(err)
	}
	fd, size := keymapFD(t, 0)
	if err := errors.Join(kb.Keymap(uint32(cw.WlKeyboardKeymapFormatXkbV1), fd, size), unix.Close(fd),
		kb.Key(1, 31, uint32(cw.WlKeyboardKeyStatePressed)), kb.Key(2, 31, uint32(cw.WlKeyboardKeyStateReleased)), typist.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.c.DispatchUntil("the virtual key on both keyboards", func() bool { return len(a.serials) == 4 && len(b.serials) == 4 })
	if !slices.Equal(a.serials, b.serials) {
		t.Fatalf("one key event reached two keyboards of a client with serials %v and %v", a.serials, b.serials)
	}
}

func TestVirtualTouchesLandInTheLayout(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	driver := testclient.Connect(t, r.h.Socket())
	seat, _ := driver.Seat()
	mgr, err := clvt.NewLysVirtualTouchManagerV1(driver.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	driver.Bind(mgr)
	vt, err := mgr.CreateVirtualTouch(seat)
	if err != nil {
		t.Fatal(err)
	}
	const point = 1<<30 + 3
	if err := errors.Join(vt.Down(1, 3, 40, 50, 160, 100), vt.Frame(), vt.Motion(2, 3, 80, 25, 160, 100), vt.Frame(),
		vt.Up(3, 3), vt.Frame(), vt.Motion(4, 3, 10, 10, 160, 100), vt.Up(5, 3), vt.Frame(), driver.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect(fmt.Sprint("down ", point, " win 40,50"), "touch_frame", fmt.Sprint("touch_motion ", point, " 80,25"), "touch_frame",
		fmt.Sprint("up ", point), "touch_frame")
	if err := errors.Join(vt.Down(6, 4, 20, 20, 160, 100), vt.Frame(), vt.Destroy(), driver.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect(fmt.Sprint("down ", point+1, " win 20,20"), "touch_frame", "cancel")
}

func TestVirtualTouchRefusesBadPoints(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	for _, tc := range []struct {
		name string
		send func(vt *clvt.LysVirtualTouchV1) error
		code clvt.LysVirtualTouchV1Error
	}{
		{"a point down twice", func(vt *clvt.LysVirtualTouchV1) error {
			return errors.Join(vt.Down(1, 1, 1, 1, 10, 10), vt.Down(2, 1, 2, 2, 10, 10))
		}, clvt.LysVirtualTouchV1ErrorInvalidID},
		{"an id past the block", func(vt *clvt.LysVirtualTouchV1) error { return vt.Down(1, 1<<16, 1, 1, 10, 10) }, clvt.LysVirtualTouchV1ErrorInvalidID},
		{"an empty extent", func(vt *clvt.LysVirtualTouchV1) error { return vt.Down(1, 1, 0, 0, 0, 10) }, clvt.LysVirtualTouchV1ErrorInvalidExtent},
		{"a position past its extent", func(vt *clvt.LysVirtualTouchV1) error { return vt.Down(1, 1, 11, 1, 10, 10) }, clvt.LysVirtualTouchV1ErrorInvalidExtent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testclient.Connect(t, r.h.Socket())
			seat, _ := c.Seat()
			mgr, err := clvt.NewLysVirtualTouchManagerV1(c.Display, 1)
			if err != nil {
				t.Fatal(err)
			}
			c.Bind(mgr)
			vt, err := mgr.CreateVirtualTouch(seat)
			if err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(tc.send(vt), c.Display.Flush()); err != nil {
				t.Fatal(err)
			}
			c.ExpectProtocolError(uint32(tc.code))
		})
	}
}

func TestVinputTouchscreenDrivesTheSeat(t *testing.T) {
	t.Parallel()
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	if ok, err := vinput.TouchAvailable(r.h.Socket()); !ok || err != nil {
		t.Fatalf("TouchAvailable = %v, %v; lys-wm serves lys_virtual_touch_manager_v1", ok, err)
	}
	d, err := vinput.Dial(r.h.Socket(), vinput.Options{Touch: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := errors.Join(d.TouchDown(0, 80, 50, 160, 100), d.TouchMotion(0, 120, 75, 160, 100), d.TouchUp(0)); err != nil {
		t.Fatal(err)
	}
	const point = 1 << 30
	r.expect(fmt.Sprint("down ", point, " win 80,50"), "touch_frame", fmt.Sprint("touch_motion ", point, " 120,75"), "touch_frame",
		fmt.Sprint("up ", point), "touch_frame")
}
