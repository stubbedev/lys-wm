package harness

import (
	"encoding/binary"
	"errors"
	"image"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
)

type xDragSource struct {
	*xclient
	t    *testing.T
	win  xproto.Window
	text []byte
	msgs chan xproto.ClientMessageEvent
}

func (s *xDragSource) serve() {
	for {
		ev, xerr := s.x.WaitForEvent()
		if ev == nil && xerr == nil {
			close(s.msgs)
			return
		}
		switch e := ev.(type) {
		case xproto.SelectionRequestEvent:
			reply := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: e.Property}
			if e.Selection == s.atoms["XdndSelection"] && e.Target == s.atoms["UTF8_STRING"] {
				xproto.ChangeProperty(s.x, xproto.PropModeReplace, e.Requestor, e.Property, e.Target, 8, uint32(len(s.text)), s.text)
			} else {
				reply.Property = 0
			}
			xproto.SendEvent(s.x, false, e.Requestor, xproto.EventMaskNoEvent, string(reply.Bytes()))
		case xproto.ClientMessageEvent:
			s.msgs <- e
		}
	}
}

func (s *xDragSource) message(name string) xproto.ClientMessageEvent {
	s.t.Helper()
	timeout := time.After(clientTimeout)
	for {
		select {
		case m, open := <-s.msgs:
			if !open {
				s.t.Fatal("the X connection closed")
			}
			if m.Type == s.atoms[name] {
				return m
			}
		case <-timeout:
			s.t.Fatalf("no %s arrived", name)
		}
	}
}

func (s *xDragSource) send(to xproto.Window, name string, data ...uint32) {
	s.t.Helper()
	ev := xproto.ClientMessageEvent{Format: 32, Window: to, Type: s.atoms[name], Data: xproto.ClientMessageDataUnionData32New(append(data, make([]uint32, 5-len(data))...))}
	if err := xproto.SendEventChecked(s.x, false, to, xproto.EventMaskNoEvent, string(ev.Bytes())).Check(); err != nil {
		s.t.Fatal(err)
	}
}

func (s *xDragSource) awareAt(p image.Point) (xproto.Window, bool) {
	r, err := xproto.TranslateCoordinates(s.x, s.root, s.root, int16(p.X), int16(p.Y)).Reply()
	if err != nil || r.Child == 0 {
		return 0, false
	}
	v, err := xproto.GetProperty(s.x, false, r.Child, s.atoms["XdndAware"], xproto.AtomAtom, 0, 1).Reply()
	if err != nil || len(v.Value) < 4 {
		return 0, false
	}
	return r.Child, binary.LittleEndian.Uint32(v.Value) >= 5
}

type xDragRig struct {
	h    *Harness
	zone *party
	r    *inputRig
	src  *xDragSource
}

func xDragSetup(t *testing.T) *xDragRig {
	t.Helper()
	h := StartXwayland(t, unreported(t), small)
	zone := join(t, h, "zone")
	xc := xDial(t, h)
	xc.intern(xdndAtoms...)
	win := xc.create(image.Rect(0, 0, 40, 30), xproto.CwBackPixel, []uint32{0xff00ff}, map[string][]byte{"WM_CLASS": []byte("source\x00XTest\x00")})
	h.Eventually("the X window beside the zone", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 2 })
	src := &xDragSource{xclient: xc, t: t, win: win, text: []byte("dragged out of X"), msgs: make(chan xproto.ClientMessageEvent, 64)}
	r := &inputRig{t: t, h: h, c: zone.c, in: zone.in, w: zone.w}
	r.moveOn(0, 130, 50)
	h.Eventually("the pointer on the X window", func(_ *headless.Backend, c *compositor.Compositor) bool {
		f := c.Seat().PointerFocus()
		return f != nil && h.xwayland.IsClient(f.Client())
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame())
	})
	if err := xproto.SetSelectionOwnerChecked(xc.x, win, xc.atoms["XdndSelection"], xproto.TimeCurrentTime).Check(); err != nil {
		t.Fatal(err)
	}
	go src.serve()
	return &xDragRig{h: h, zone: zone, r: r, src: src}
}

func (x *xDragRig) enterZone(at image.Point) xproto.Window {
	x.src.t.Helper()
	var proxy xproto.Window
	for deadline := time.Now().Add(clientTimeout); ; {
		x.r.moveOn(0, float64(at.X), float64(at.Y))
		if w, ok := x.src.awareAt(at); ok {
			proxy = w
			break
		}
		if time.Now().After(deadline) {
			x.src.t.Fatal("no XdndAware window appeared over the Wayland window")
		}
		time.Sleep(10 * time.Millisecond)
	}
	x.src.send(proxy, "XdndEnter", uint32(x.src.win), 5<<24, uint32(x.src.atoms["UTF8_STRING"]))
	x.src.send(proxy, "XdndPosition", uint32(x.src.win), 0, uint32(at.X)<<16|uint32(at.Y), 0, uint32(x.src.atoms["XdndActionCopy"]))
	x.zone.seen(x.src.t, "the drag entering the zone", func() bool { return x.zone.cb.Drag != nil })
	return proxy
}

func (x *xDragRig) release() {
	x.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, false), b.Pointer().Frame())
	})
}

func readDrop(t *testing.T, p *party, mime string) string {
	t.Helper()
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		t.Fatal(err)
	}
	r := os.NewFile(uintptr(fds[0]), "drop")
	defer r.Close()
	if err := errors.Join(p.cb.Drag.Data.Receive(mime, fds[1]), unix.Close(fds[1]), p.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	if err := r.SetReadDeadline(time.Now().Add(clientTimeout)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func TestAnXDragDropsIntoAWaylandWindow(t *testing.T) {
	t.Parallel()
	x := xDragSetup(t)
	proxy := x.enterZone(image.Pt(20, 50))
	zone := x.zone
	if o := zone.cb.Drag; !slices.Equal(o.Mimes, []string{"text/plain;charset=utf-8"}) || o.SourceActions != cw.WlDataDeviceManagerDndActionCopy {
		t.Fatalf("the zone was offered %v with actions %d", o.Mimes, o.SourceActions)
	}
	mime := "text/plain;charset=utf-8"
	if err := errors.Join(zone.cb.Drag.Data.Accept(zone.cb.Serial, &mime), zone.cb.Drag.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy, cw.WlDataDeviceManagerDndActionCopy), zone.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	for {
		s := x.src.message("XdndStatus").Data.Data32
		if s[1]&1 != 0 {
			if xproto.Window(s[0]) != proxy || xproto.Atom(s[4]) != x.src.atoms["XdndActionCopy"] {
				t.Fatalf("XdndStatus %v, want an accepting copy from %#x", s, proxy)
			}
			break
		}
	}
	x.release()
	x.h.Eventually("the proxy gone once the button is up", func(*headless.Backend, *compositor.Compositor) bool {
		_, aware := x.src.awareAt(image.Pt(20, 50))
		return !aware
	})
	x.src.send(proxy, "XdndDrop", uint32(x.src.win), 0, 0)
	zone.seen(t, "the drop", func() bool { return slices.Contains(zone.cb.Log, "drop") })
	if got := readDrop(t, zone, mime); got != "dragged out of X" {
		t.Fatalf("the zone received %q", got)
	}
	if err := errors.Join(zone.cb.Drag.Data.Finish(), zone.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	f := x.src.message("XdndFinished").Data.Data32
	if xproto.Window(f[0]) != proxy || f[1]&1 == 0 || xproto.Atom(f[2]) != x.src.atoms["XdndActionCopy"] {
		t.Fatalf("XdndFinished %v, want a successful copy from %#x", f, proxy)
	}
}

func TestAnXDragLeavingTheWaylandWindowsLeavesThem(t *testing.T) {
	t.Parallel()
	x := xDragSetup(t)
	proxy := x.enterZone(image.Pt(20, 50))
	x.src.send(proxy, "XdndLeave", uint32(x.src.win))
	x.zone.seen(t, "the drag leaving the zone", func() bool { return slices.Contains(x.zone.cb.Log, "leave") })
	x.r.moveOn(0, 130, 50)
	x.h.Eventually("the proxy gone once the pointer is back over X", func(*headless.Backend, *compositor.Compositor) bool {
		_, aware := x.src.awareAt(image.Pt(130, 50))
		return !aware
	})
	x.release()
	if err := xproto.SetSelectionOwnerChecked(x.src.x, 0, x.src.atoms["XdndSelection"], xproto.TimeCurrentTime).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestAnXDropNobodyAcceptedFinishesUnsuccessfully(t *testing.T) {
	t.Parallel()
	x := xDragSetup(t)
	proxy := x.enterZone(image.Pt(20, 50))
	x.release()
	x.src.send(proxy, "XdndDrop", uint32(x.src.win), 0, 0)
	f := x.src.message("XdndFinished").Data.Data32
	if f[1]&1 != 0 {
		t.Fatalf("an unaccepted drop finished %v", f)
	}
	x.zone.seen(t, "the zone left without a drop", func() bool { return slices.Contains(x.zone.cb.Log, "leave") })
	if slices.Contains(x.zone.cb.Log, "drop") {
		t.Fatalf("the zone got a drop nobody accepted: %q", x.zone.cb.Log)
	}
}
