package harness

import (
	"encoding/binary"
	"errors"
	"image"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

var xdndAtoms = []string{"XdndAware", "XdndSelection", "XdndEnter", "XdndPosition", "XdndStatus", "XdndLeave", "XdndDrop", "XdndFinished", "XdndActionCopy", "XdndTypeList", "UTF8_STRING", "INCR", "XSEL_DATA"}

func (c *xclient) xdnd(name string) xproto.ClientMessageEvent {
	c.t.Helper()
	for {
		m := c.clientMessage(clientTimeout)
		if m.Type == c.atoms[name] {
			return m
		}
	}
}

func (c *xclient) reply(to xproto.Window, name string, data ...uint32) {
	c.t.Helper()
	ev := xproto.ClientMessageEvent{Format: 32, Window: to, Type: c.atoms[name], Data: xproto.ClientMessageDataUnionData32New(append(data, make([]uint32, 5-len(data))...))}
	if err := xproto.SendEventChecked(c.x, false, to, xproto.EventMaskNoEvent, string(ev.Bytes())).Check(); err != nil {
		c.t.Fatal(err)
	}
}

type xdndRig struct {
	h    *Harness
	a    *party
	r    *inputRig
	xc   *xclient
	xwin xproto.Window
	src  *testclient.Source
}

func xdndSetup(t *testing.T, aware bool, report func(error)) *xdndRig {
	t.Helper()
	h := StartXwayland(t, report, small)
	a := join(t, h, "a")
	xc := xDial(t, h)
	xc.intern(xdndAtoms...)
	props := map[string][]byte{"WM_CLASS": []byte("target\x00XTest\x00")}
	if aware {
		props["XdndAware"] = binary.LittleEndian.AppendUint32(nil, 5)
	}
	xwin := xc.create(image.Rect(0, 0, 40, 30), xproto.CwBackPixel, []uint32{0xff0000}, props)
	h.Eventually("the X window beside a", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 2 })
	r := &inputRig{t: t, h: h, c: a.c, in: a.in, w: a.w}
	r.moveOn(0, 20, 50)
	a.seen(t, "the pointer over a", func() bool { return slices.Contains(a.in.Log, "motion 20,50") })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame())
	})
	a.seen(t, "the press", func() bool { return a.in.Press != 0 })
	src := a.c.DataSource(map[string]string{"text/plain;charset=utf-8": "dropped into X", "text/uri-list": "file:///tmp/x"})
	if err := errors.Join(src.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy), a.cb.Device.StartDrag(src.Data, a.w.Surface, nil, a.in.Press), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the origin loses the pointer", func() bool { return slices.Contains(a.in.Log, "leave a") })
	return &xdndRig{h: h, a: a, r: r, xc: xc, xwin: xwin, src: src}
}

func (x *xdndRig) release() {
	x.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, false), b.Pointer().Frame())
	})
}

func TestAWaylandDragDropsIntoAnXWindow(t *testing.T) {
	t.Parallel()
	x := xdndSetup(t, true, unreported(t))
	xc := x.xc
	x.r.moveOn(0, 130, 50)
	enter := xc.xdnd("XdndEnter")
	d := enter.Data.Data32
	if d[1]>>24 != 5 || enter.Window != x.xwin {
		t.Fatalf("XdndEnter to %#x with %#x, want version 5 to %#x", enter.Window, d[1], x.xwin)
	}
	source := xproto.Window(d[0])
	if types := xc.names(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, d[2]), d[3])); !slices.Equal(types, []string{"UTF8_STRING", "text/uri-list"}) {
		t.Fatalf("offered types %v", types)
	}
	pos := xc.xdnd("XdndPosition").Data.Data32
	if at := image.Pt(int(pos[2]>>16), int(pos[2]&0xffff)); !at.In(xc.geometry(x.xwin)) || xproto.Atom(pos[4]) != xc.atoms["XdndActionCopy"] {
		t.Fatalf("XdndPosition at %v with action %d, outside %v or not copy", at, pos[4], xc.geometry(x.xwin))
	}
	xc.reply(source, "XdndStatus", uint32(x.xwin), 1, 0, 0, uint32(xc.atoms["XdndActionCopy"]))
	x.a.seen(t, "the Wayland source told the X window takes it", func() bool {
		return slices.Contains(x.src.Log, "target text/plain;charset=utf-8") && slices.Contains(x.src.Log, "action 1")
	})
	x.release()
	xc.xdnd("XdndDrop")
	x.a.seen(t, "the drop performed", func() bool { return slices.Contains(x.src.Log, "dropped") })
	typ, got := xc.paste(x.xwin, "XdndSelection", "UTF8_STRING", func(done <-chan struct{}) {
		x.a.seen(t, "the source asked for the text", func() bool { return slices.Contains(x.src.Log, "send text/plain;charset=utf-8") })
		<-done
	})
	if typ != xc.atoms["UTF8_STRING"] || string(got) != "dropped into X" {
		t.Fatalf("the X window got %q of type %d", got, typ)
	}
	xc.reply(source, "XdndFinished", uint32(x.xwin), 1, uint32(xc.atoms["XdndActionCopy"]))
	x.a.seen(t, "the source finished", func() bool { return slices.Contains(x.src.Log, "finished") })
	if slices.Contains(x.src.Log, "cancelled") {
		t.Fatalf("a finished drop also cancelled: %q", x.src.Log)
	}
}

func TestAWaylandDragLeavingAnXWindowTellsIt(t *testing.T) {
	t.Parallel()
	x := xdndSetup(t, true, unreported(t))
	x.r.moveOn(0, 130, 50)
	source := xproto.Window(x.xc.xdnd("XdndEnter").Data.Data32[0])
	x.xc.reply(source, "XdndStatus", uint32(x.xwin), 1, 0, 0, uint32(x.xc.atoms["XdndActionCopy"]))
	x.a.seen(t, "the X window accepting", func() bool { return slices.Contains(x.src.Log, "target text/plain;charset=utf-8") })
	x.r.moveOn(0, 20, 50)
	x.xc.xdnd("XdndLeave")
	x.a.seen(t, "the source told nothing takes it now", func() bool { return slices.Contains(x.src.Log, "target none") })
	x.release()
	x.a.seen(t, "the drop over nothing that takes it cancelled", func() bool { return slices.Contains(x.src.Log, "cancelled") })
}

func TestAWaylandDropOnAnXWindowThatIsNotXdndAwareIsCancelled(t *testing.T) {
	t.Parallel()
	x := xdndSetup(t, false, unreported(t))
	x.r.moveOn(0, 130, 50)
	x.release()
	x.a.seen(t, "the drop cancelled", func() bool { return slices.Contains(x.src.Log, "cancelled") })
	if slices.Contains(x.src.Log, "dropped") {
		t.Fatalf("a drop on a window without XdndAware was performed: %q", x.src.Log)
	}
}

func unreported(t *testing.T) func(error) {
	return func(err error) { t.Errorf("xwayland reported: %v", err) }
}

func TestAnXWindowThatNeverFinishesADropReleasesTheSource(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reported []error
	x := xdndSetup(t, true, func(err error) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, err)
	})
	x.r.moveOn(0, 130, 50)
	source := xproto.Window(x.xc.xdnd("XdndEnter").Data.Data32[0])
	x.xc.reply(source, "XdndStatus", uint32(x.xwin), 1, 0, 0, uint32(x.xc.atoms["XdndActionCopy"]))
	x.a.seen(t, "the X window accepting", func() bool { return slices.Contains(x.src.Log, "target text/plain;charset=utf-8") })
	x.release()
	x.xc.xdnd("XdndDrop")
	x.a.seen(t, "the unfinished drop given up", func() bool { return slices.Contains(x.src.Log, "cancelled") })
	mu.Lock()
	defer mu.Unlock()
	if !slices.ContainsFunc(reported, func(err error) bool { return strings.Contains(err.Error(), "did not finish a drop") }) {
		t.Fatalf("reported %v", reported)
	}
}
