package harness

import (
	"encoding/binary"
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func (c *xclient) sendState(win xproto.Window, action uint32, a, b string) {
	c.t.Helper()
	ev := xproto.ClientMessageEvent{
		Format: 32, Window: win, Type: c.atoms["_NET_WM_STATE"],
		Data: xproto.ClientMessageDataUnionData32New([]uint32{action, uint32(c.atoms[a]), uint32(c.atoms[b]), 1, 0}),
	}
	if err := xproto.SendEventChecked(c.x, false, c.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(ev.Bytes())).Check(); err != nil {
		c.t.Fatal(err)
	}
}

func (c *xclient) atomsOf(win xproto.Window, name string) []xproto.Atom {
	c.t.Helper()
	r, err := xproto.GetProperty(c.x, false, win, c.atoms[name], xproto.GetPropertyTypeAny, 0, 64).Reply()
	if err != nil {
		c.t.Fatal(err)
	}
	out := make([]xproto.Atom, 0, len(r.Value)/4)
	for at := 0; at+4 <= len(r.Value); at += 4 {
		out = append(out, xproto.Atom(binary.LittleEndian.Uint32(r.Value[at:])))
	}
	return out
}

func TestAnXWindowDemandingAttentionIsUrgentUntilFocused(t *testing.T) {
	t.Parallel()
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	xc.intern("_NET_WM_STATE_DEMANDS_ATTENTION")
	first := xc.window(image.Rect(0, 0, 40, 30), 0xff0000, false, map[string][]byte{"WM_CLASS": []byte("first\x00XTest\x00")})
	h.Eventually("the first window", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 1 })
	xc.window(image.Rect(0, 0, 40, 30), 0x00ff00, false, map[string][]byte{"WM_CLASS": []byte("second\x00XTest\x00")})
	h.Eventually("the second window focused", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[1].Focused && !ws[0].Urgent
	})
	xc.sendState(first, 1, "_NET_WM_STATE_DEMANDS_ATTENTION", "")
	h.Eventually("the first window urgent", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[0].Urgent && !ws[0].Focused
	})
	h.dispatch("focus left")
	h.Eventually("the first window focused and no longer asking", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[0].Focused && !ws[0].Urgent && !slices.Contains(xc.atomsOf(first, "_NET_WM_STATE"), xc.atoms["_NET_WM_STATE_DEMANDS_ATTENTION"])
	})
	h.dispatch("focus right")
	h.Eventually("the first window not urgent again once unfocused", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[1].Focused && !ws[0].Urgent
	})
}

func TestAnXWindowMaximizesOnRequestAndAtMap(t *testing.T) {
	t.Parallel()
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	xc.intern("_NET_WM_STATE_MAXIMIZED_VERT", "_NET_WM_STATE_MAXIMIZED_HORZ")
	maximized := slices.Concat(xc.atom("_NET_WM_STATE_MAXIMIZED_VERT"), xc.atom("_NET_WM_STATE_MAXIMIZED_HORZ"))
	xc.window(image.Rect(0, 0, 40, 30), 0x00ff00, false, map[string][]byte{"WM_CLASS": []byte("plain\x00XTest\x00")})
	h.Eventually("the plain window", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 1 })
	win := xc.create(image.Rect(10, 10, 90, 70), xproto.CwBackPixel|xproto.CwEventMask, []uint32{0xff0000, xproto.EventMaskStructureNotify},
		map[string][]byte{"WM_CLASS": []byte("xtest\x00XTest\x00"), "_NET_WM_STATE": maximized})
	maximizedState := func() bool {
		states := xc.atomsOf(win, "_NET_WM_STATE")
		return slices.Contains(states, xc.atoms["_NET_WM_STATE_MAXIMIZED_VERT"]) && slices.Contains(states, xc.atoms["_NET_WM_STATE_MAXIMIZED_HORZ"])
	}
	isMaximized := func(on bool, rect image.Rectangle) func(*headless.Backend, *compositor.Compositor) bool {
		return func(_ *headless.Backend, c *compositor.Compositor) bool {
			ws := windowsOf(c)
			return len(ws) == 2 && ws[1].Maximized == on && xc.geometry(win) == rect && maximizedState() == on
		}
	}
	h.Eventually("the window that mapped maximized covering the output", isMaximized(true, image.Rect(0, 0, 160, 100)))
	xc.sendState(win, 0, "_NET_WM_STATE_MAXIMIZED_VERT", "_NET_WM_STATE_MAXIMIZED_HORZ")
	h.Eventually("the window back in its tile", isMaximized(false, image.Rect(80, 0, 160, 100)))
	xc.sendState(win, 2, "_NET_WM_STATE_MAXIMIZED_VERT", "_NET_WM_STATE_MAXIMIZED_HORZ")
	h.Eventually("the window maximized by a toggle", isMaximized(true, image.Rect(0, 0, 160, 100)))
}

func TestTheXWindowManagerAdvertisesAndKeepsTheStackingList(t *testing.T) {
	t.Parallel()
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	xc.intern("_NET_SUPPORTED", "_NET_CLIENT_LIST_STACKING", "_NET_WM_STATE_DEMANDS_ATTENTION", "_NET_WM_STATE_MAXIMIZED_VERT", "_NET_WM_STATE_MAXIMIZED_HORZ")
	supported := xc.atomsOf(xc.root, "_NET_SUPPORTED")
	for _, name := range []string{"_NET_CLIENT_LIST_STACKING", "_NET_WM_STATE_DEMANDS_ATTENTION", "_NET_WM_STATE_MAXIMIZED_VERT", "_NET_WM_STATE_MAXIMIZED_HORZ"} {
		if !slices.Contains(supported, xc.atoms[name]) {
			t.Errorf("_NET_SUPPORTED leaves out %s", name)
		}
	}
	first := xc.window(image.Rect(0, 0, 40, 30), 0xff0000, false, map[string][]byte{"WM_CLASS": []byte("first\x00XTest\x00")})
	h.Eventually("the first window", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 1 })
	second := xc.window(image.Rect(0, 0, 40, 30), 0x00ff00, false, map[string][]byte{"WM_CLASS": []byte("second\x00XTest\x00")})
	stacked := func(want ...xproto.Window) func(*headless.Backend, *compositor.Compositor) bool {
		return func(*headless.Backend, *compositor.Compositor) bool {
			return slices.Equal(xc.atomsOf(xc.root, "_NET_CLIENT_LIST_STACKING"), windowAtoms(want))
		}
	}
	h.Eventually("the focused second window on top", stacked(first, second))
	h.dispatch("focus left")
	h.Eventually("the first window raised by its focus", stacked(second, first))
	if err := xproto.DestroyWindowChecked(xc.x, first).Check(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the destroyed window gone from the list", stacked(second))
}

func windowAtoms(ws []xproto.Window) []xproto.Atom {
	out := make([]xproto.Atom, len(ws))
	for i, w := range ws {
		out[i] = xproto.Atom(w)
	}
	return out
}

func (c *xclient) workArea() image.Rectangle {
	c.t.Helper()
	v := c.atomsOf(c.root, "_NET_WORKAREA")
	if len(v) != 4 {
		return image.Rectangle{}
	}
	return image.Rect(int(v[0]), int(v[1]), int(v[0]+v[2]), int(v[1]+v[3]))
}

func TestTheXWorkAreaIsTheFocusedOutputsUsableArea(t *testing.T) {
	t.Parallel()
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, scaled)
	xc := xDial(t, h)
	xc.intern("_NET_SUPPORTED", "_NET_WORKAREA")
	if !slices.Contains(xc.atomsOf(xc.root, "_NET_SUPPORTED"), xc.atoms["_NET_WORKAREA"]) {
		t.Error("_NET_SUPPORTED leaves out _NET_WORKAREA")
	}
	h.Eventually("the whole output as the work area", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.workArea() == image.Rect(0, 0, 240, 150)
	})
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the bar to map", bar.Mapped)
	h.Eventually("the work area below the bar", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.workArea() == image.Rect(0, 20, 240, 150)
	})
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		return c.SetOutputScale(b.Outputs()[0], 180)
	})
	h.Eventually("the work area in Xwayland's physical pixels", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.workArea() == image.Rect(0, 30, 240, 150)
	})
	if err := errors.Join(bar.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the whole output once the bar is gone", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.workArea() == image.Rect(0, 0, 240, 150)
	})
}
