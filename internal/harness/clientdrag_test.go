package harness

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func clientDragConfig(cfg *compositor.Config) {
	cfg.WM.Gaps = wm.Gaps{}
	cfg.Decor.BorderWidth = 0
}

func (h *Harness) movePointer(at image.Point) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		size := o.State().Mode.Size
		return errors.Join(p.MoveTo(o, float64(at.X)/float64(size.X), float64(at.Y)/float64(size.Y)), p.Frame())
	})
}

func (h *Harness) button(pressed bool) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Button(btnLeft, pressed), p.Frame())
	})
}

func (h *Harness) dragging() bool {
	var on bool
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		on = c.Dragging()
		return nil
	})
	return on
}

func (h *Harness) titledRect(title string) image.Rectangle {
	h.t.Helper()
	var r image.Rectangle
	found := false
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		for _, w := range c.Snapshot().Windows {
			if w.Title == title {
				r, found = image.Rect(w.Rect.X, w.Rect.Y, w.Rect.X+w.Rect.Width, w.Rect.Y+w.Rect.Height), true
			}
		}
		return nil
	})
	if !found {
		h.t.Fatalf("no window titled %q", title)
	}
	return r
}

func (h *Harness) settledAt(title string, want image.Rectangle) {
	h.t.Helper()
	h.Eventually(fmt.Sprintf("%s at %v", title, want), func(_ *headless.Backend, c *compositor.Compositor) bool {
		for _, w := range c.Snapshot().Windows {
			if w.Title == title {
				return image.Rect(w.Rect.X, w.Rect.Y, w.Rect.X+w.Rect.Width, w.Rect.Y+w.Rect.Height) == want && !c.Holding()
			}
		}
		return false
	})
}

func pressOn(h *Harness, c *testclient.Client, in *testclient.Input, at image.Point) uint32 {
	h.t.Helper()
	before := in.Press
	h.movePointer(at)
	h.button(true)
	c.DispatchUntil("the press", func() bool { return in.Press != before })
	return in.Press
}

func request(t *testing.T, c *testclient.Client, send func() error) {
	t.Helper()
	if err := errors.Join(send(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
}

func floatingClient(t *testing.T, h *Harness, title string, setup func(*testclient.Window) error) (*testclient.Client, *cw.WlSeat, *testclient.Input, *testclient.Window) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	seat, in := c.Seat()
	w := c.WindowWith(title, testclient.Fill(0, 0, 0xff), setup)
	c.DispatchUntil(title+" to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil(title+" floating", func() bool { return w.Logical == image.Pt(80, 50) })
	h.settledAt(title, image.Rect(40, 25, 120, 75))
	return c, seat, in, w
}

func noSetup(*testclient.Window) error { return nil }

func TestAClientMovesItsFloatingWindowWithThePointer(t *testing.T) {
	t.Parallel()
	h := StartWith(t, clientDragConfig, small)
	c, seat, in, w := floatingClient(t, h, "app", noSetup)
	serial := pressOn(h, c, in, image.Pt(80, 50))
	request(t, c, func() error { return w.Toplevel.Move(seat, serial) })
	if !h.dragging() {
		t.Fatal("the move request did not start a drag")
	}
	h.movePointer(image.Pt(90, 55))
	h.settledAt("app", image.Rect(50, 30, 130, 80))
	h.movePointer(image.Pt(100, 60))
	h.button(false)
	h.settledAt("app", image.Rect(60, 35, 140, 85))
	if h.dragging() {
		t.Fatal("the drag outlived the release")
	}
	h.movePointer(image.Pt(70, 40))
	h.settledAt("app", image.Rect(60, 35, 140, 85))
	h.Eventually("the pointer back on the window", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return comp.Seat().PointerFocus() != nil
	})
}

func TestAClientResizesItsFloatingWindowFromEachEdge(t *testing.T) {
	t.Parallel()
	minimum, maximum := image.Pt(44, 36), image.Pt(110, 70)
	h := StartWith(t, clientDragConfig, small)
	c, seat, in, w := floatingClient(t, h, "app", func(w *testclient.Window) error {
		return errors.Join(w.Toplevel.SetMinSize(int32(minimum.X), int32(minimum.Y)), w.Toplevel.SetMaxSize(int32(maximum.X), int32(maximum.Y)))
	})
	for i, edge := range []cxdg.XdgToplevelResizeEdge{
		cxdg.XdgToplevelResizeEdgeTop, cxdg.XdgToplevelResizeEdgeBottom, cxdg.XdgToplevelResizeEdgeLeft, cxdg.XdgToplevelResizeEdgeRight,
		cxdg.XdgToplevelResizeEdgeTopLeft, cxdg.XdgToplevelResizeEdgeBottomRight, cxdg.XdgToplevelResizeEdgeTopRight, cxdg.XdgToplevelResizeEdgeBottomLeft,
	} {
		from := h.titledRect("app")
		grow := 1
		if i%2 == 1 {
			grow = -1
		}
		d := image.Pt(6, 4).Mul(grow)
		if edge&cxdg.XdgToplevelResizeEdgeLeft != 0 {
			d.X = -d.X
		}
		if edge&cxdg.XdgToplevelResizeEdgeTop != 0 {
			d.Y = -d.Y
		}
		want := from
		if edge&cxdg.XdgToplevelResizeEdgeLeft != 0 {
			want.Min.X += d.X
		}
		if edge&cxdg.XdgToplevelResizeEdgeRight != 0 {
			want.Max.X += d.X
		}
		if edge&cxdg.XdgToplevelResizeEdgeTop != 0 {
			want.Min.Y += d.Y
		}
		if edge&cxdg.XdgToplevelResizeEdgeBottom != 0 {
			want.Max.Y += d.Y
		}
		at := from.Min.Add(from.Size().Div(2))
		serial := pressOn(h, c, in, at)
		request(t, c, func() error { return w.Toplevel.Resize(seat, serial, edge) })
		h.movePointer(at.Add(d))
		h.button(false)
		h.settledAt("app", want)
		c.DispatchUntil("the client told its new size", func() bool { return w.Logical == want.Size() })
	}

	from := h.titledRect("app")
	at := from.Min.Add(from.Size().Div(2))
	serial := pressOn(h, c, in, at)
	request(t, c, func() error { return w.Toplevel.Resize(seat, serial, cxdg.XdgToplevelResizeEdgeTopLeft) })
	h.movePointer(at.Sub(image.Pt(60, 60)))
	h.button(false)
	h.settledAt("app", image.Rectangle{Min: from.Max.Sub(maximum), Max: from.Max})
	c.DispatchUntil("the client at its maximum", func() bool { return w.Logical == maximum })
	from = h.titledRect("app")
	at = from.Max.Sub(image.Pt(2, 2))
	serial = pressOn(h, c, in, at)
	request(t, c, func() error { return w.Toplevel.Resize(seat, serial, cxdg.XdgToplevelResizeEdgeBottomRight) })
	h.movePointer(at.Sub(from.Size()))
	h.button(false)
	h.settledAt("app", image.Rectangle{Min: from.Min, Max: from.Min.Add(minimum)})
	c.DispatchUntil("the client at its minimum", func() bool { return w.Logical == minimum })
}

func TestAMoveOrResizeWithoutItsPressIsIgnored(t *testing.T) {
	t.Parallel()
	h := StartWith(t, clientDragConfig, small)
	c, seat, in, w := floatingClient(t, h, "app", noSetup)
	other := testclient.Connect(t, h.Socket())
	otherSeat, _ := other.Seat()
	otherWindow := other.Window("other", testclient.Fill(0, 0xff, 0))
	other.DispatchUntil("the other window to map", otherWindow.Mapped)
	h.settledAt("other", image.Rect(0, 0, 160, 100))

	old := pressOn(h, c, in, image.Pt(80, 50))
	h.button(false)
	for name, send := range map[string]func() error{
		"a released press": func() error { return w.Toplevel.Move(seat, old) },
		"no press at all":  func() error { return w.Toplevel.Resize(seat, 0, cxdg.XdgToplevelResizeEdgeRight) },
	} {
		request(t, c, send)
		if h.dragging() {
			t.Fatalf("%s started a drag", name)
		}
	}
	held := pressOn(h, c, in, image.Pt(80, 50))
	request(t, c, func() error { return w.Toplevel.Move(seat, old) })
	if h.dragging() {
		t.Fatal("a stale serial started a drag while another press was held")
	}
	request(t, other, func() error { return otherWindow.Toplevel.Move(otherSeat, held) })
	if h.dragging() {
		t.Fatal("another client's serial started a drag")
	}
	h.button(false)
}

func TestAClientMovesItsTiledWindowIntoAnotherPlace(t *testing.T) {
	t.Parallel()
	h := StartWith(t, clientDragConfig, small)
	c := testclient.Connect(t, h.Socket())
	seat, in := c.Seat()
	left := c.Window("left", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("right to map", right.Mapped)
	h.Eventually("both tiled", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(10, 50, redColor)(b, comp) && showing(150, 50, blueColor)(b, comp)
	})

	serial := pressOn(h, c, in, image.Pt(40, 50))
	request(t, c, func() error { return left.Toplevel.Resize(seat, serial, cxdg.XdgToplevelResizeEdgeRight) })
	h.movePointer(image.Pt(70, 50))
	h.button(false)
	c.DispatchUntil("the left tile grown by the resize", func() bool { return left.Logical == image.Pt(110, 100) })
	c.DispatchUntil("the right tile shrunk", func() bool { return right.Logical == image.Pt(50, 100) })

	var id wm.WindowID
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		for _, w := range comp.Snapshot().Windows {
			if w.Title == "right" {
				id = w.ID
			}
		}
		return nil
	})
	serial = pressOn(h, c, in, image.Pt(140, 50))
	request(t, c, func() error { return right.Toplevel.Move(seat, serial) })
	h.movePointer(image.Pt(130, 50))
	h.movePointer(image.Pt(10, 50))
	var want image.Rectangle
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		var err error
		want, err = comp.WM().DropTarget(id, image.Pt(10, 50), compositor.PlainSettings().Autotile, wm.Spacing{TabBar: compositor.PlainDecor().Tab.Height + compositor.PlainDecor().Tab.Padding})
		return err
	})
	inside := want.Min.Add(want.Size().Div(2))
	h.Eventually("a preview over the drop target, the window still in its tile", func(b *headless.Backend, comp *compositor.Compositor) bool {
		snap := snapshot(b.Outputs()[0])
		got := pixel(snap, inside.X, inside.Y)
		return got != redColor && pixel(snap, 150, 50) == blueColor
	})
	h.button(false)
	h.Eventually("right dropped where the preview was", showing(inside.X, inside.Y, blueColor))
	h.settledAt("right", want)
}

func TestATouchMovesTheWindowItsClientAskedToMove(t *testing.T) {
	t.Parallel()
	h := StartWith(t, clientDragConfig, small)
	c, seat, in, w := floatingClient(t, h, "app", noSetup)
	touch := func(f func(*headless.Touch, *headless.Output) error) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(f(b.Touch(), b.Outputs()[0]), b.Touch().Frame())
		})
	}
	before := in.Press
	touch(func(tc *headless.Touch, o *headless.Output) error { return tc.Down(3, o, 0.5, 0.5) })
	c.DispatchUntil("the touch down", func() bool { return in.Press != before })
	request(t, c, func() error { return w.Toplevel.Move(seat, in.Press) })
	if !h.dragging() {
		t.Fatal("a move request with a touch-down serial did not start a drag")
	}
	touch(func(tc *headless.Touch, _ *headless.Output) error { return tc.Motion(3, 100.0/160, 60.0/100) })
	h.settledAt("app", image.Rect(60, 35, 140, 85))
	touch(func(tc *headless.Touch, _ *headless.Output) error { return tc.Up(3) })
	if h.dragging() {
		t.Fatal("the drag outlived the touch")
	}
	c.DispatchUntil("the touch ended for the client", func() bool { return slices.Contains(in.Log, "up 3") })
}

func TestAClientWindowMenuRequestIsAnnounced(t *testing.T) {
	t.Parallel()
	h := StartWith(t, clientDragConfig, small)
	lines := h.watch()
	c, seat, in, w := floatingClient(t, h, "app", noSetup)
	var id wm.WindowID
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		id = comp.Snapshot().Windows[0].ID
		return nil
	})
	menus := func(send func() error) []string {
		t.Helper()
		request(t, c, send)
		h.dispatch("submap menu-barrier")
		var got []string
		deadline := time.After(clientTimeout)
		for {
			select {
			case line := <-lines:
				switch {
				case strings.Contains(line, `"windowmenu"`):
					got = append(got, line)
				case line == `{"event":"dispatch","action":"submap menu-barrier"}`:
					h.dispatch("submap reset")
					return got
				}
			case <-deadline:
				t.Fatal("no barrier on the event stream")
			}
		}
	}
	if got := menus(func() error { return w.Toplevel.ShowWindowMenu(seat, 12345, 10, 5) }); len(got) != 0 {
		t.Fatalf("a menu request without a press was announced: %q", got)
	}
	serial := pressOn(h, c, in, image.Pt(80, 50))
	h.button(false)
	want := fmt.Sprintf(`{"event":"windowmenu","window":%d,"x":50,"y":30}`, id)
	if got := menus(func() error { return w.Toplevel.ShowWindowMenu(seat, serial, 10, 5) }); len(got) != 1 || got[0] != want {
		t.Fatalf("menu events %q, want one %s", got, want)
	}
}

func (c *xclient) moveResize(win xproto.Window, direction, button uint32) {
	c.t.Helper()
	c.intern("_NET_WM_MOVERESIZE")
	ev := xproto.ClientMessageEvent{
		Format: 32, Window: win, Type: c.atoms["_NET_WM_MOVERESIZE"],
		Data: xproto.ClientMessageDataUnionData32New([]uint32{0, 0, direction, button, 1}),
	}
	if err := xproto.SendEventChecked(c.x, false, c.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(ev.Bytes())).Check(); err != nil {
		c.t.Fatal(err)
	}
}

func onlyRect(c *compositor.Compositor) (image.Rectangle, bool) {
	ws := windowsOf(c)
	if len(ws) != 1 {
		return image.Rectangle{}, false
	}
	return image.Rect(ws[0].Rect.X, ws[0].Rect.Y, ws[0].Rect.X+ws[0].Rect.Width, ws[0].Rect.Y+ws[0].Rect.Height), true
}

func onlyWindowRect(h *Harness) image.Rectangle {
	h.t.Helper()
	var r image.Rectangle
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		var ok bool
		if r, ok = onlyRect(c); !ok {
			return fmt.Errorf("%d windows, want 1", len(windowsOf(c)))
		}
		return nil
	})
	return r
}

func onlyAt(want image.Rectangle) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		r, ok := onlyRect(c)
		return ok && r == want && !c.Dragging()
	}
}

func TestAnXClientMovesAndResizesThroughNetWMMoveResize(t *testing.T) {
	t.Parallel()
	h := StartXwaylandWith(t, clientDragConfig, xReport(t), small)
	xc := xDial(t, h)
	xc.intern("_NET_SUPPORTED", "_NET_WM_MOVERESIZE")
	if !slices.Contains(xc.atomsOf(xc.root, "_NET_SUPPORTED"), xc.atoms["_NET_WM_MOVERESIZE"]) {
		t.Fatal("_NET_SUPPORTED does not list _NET_WM_MOVERESIZE")
	}
	win := xc.window(image.Rect(0, 0, 40, 30), 0x0000ff, false, map[string][]byte{
		"WM_CLASS":            []byte("dialog\x00XTest\x00"),
		"_NET_WM_WINDOW_TYPE": xc.atom("_NET_WM_WINDOW_TYPE_DIALOG"),
	})
	h.Eventually("the dialog floating", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].Floating && !c.Holding()
	})
	from := onlyWindowRect(h)
	at := from.Min.Add(from.Size().Div(2))

	h.movePointer(at)
	h.button(true)
	xc.moveResize(win, 8, 0)
	h.Eventually("the move drag", func(_ *headless.Backend, c *compositor.Compositor) bool { return c.Dragging() })
	h.movePointer(at.Add(image.Pt(15, 10)))
	h.button(false)
	moved := from.Add(image.Pt(15, 10))
	h.Eventually("the dialog moved", onlyAt(moved))

	at = at.Add(image.Pt(15, 10))
	h.movePointer(at)
	h.button(true)
	xc.moveResize(win, 4, 1)
	h.Eventually("the resize drag", func(_ *headless.Backend, c *compositor.Compositor) bool { return c.Dragging() })
	h.movePointer(at.Add(image.Pt(10, 8)))
	grown := image.Rectangle{Min: moved.Min, Max: moved.Max.Add(image.Pt(10, 8))}
	h.Eventually("the dialog grown from its bottom right", func(_ *headless.Backend, c *compositor.Compositor) bool {
		r, ok := onlyRect(c)
		return ok && r == grown
	})
	xc.moveResize(win, 11, 0)
	h.Eventually("the drag cancelled", func(_ *headless.Backend, c *compositor.Compositor) bool { return !c.Dragging() })
	h.movePointer(at.Add(image.Pt(20, 16)))
	h.button(false)
	h.Eventually("the X window told its size", func(*headless.Backend, *compositor.Compositor) bool { return xc.geometry(win).Size() == grown.Size() })
	if got := onlyWindowRect(h); got != grown {
		t.Fatalf("the dialog went on to %v after the cancel, want %v", got, grown)
	}

	h.movePointer(at)
	xc.moveResize(win, 8, 0)
	xc.sendState(win, 1, "_NET_WM_STATE_FULLSCREEN", "")
	h.Eventually("the fullscreen request after it handled", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].Fullscreen
	})
	if h.dragging() {
		t.Fatal("_NET_WM_MOVERESIZE started a drag with no button held")
	}
}

func TestARealGTKHeaderBarDragMovesItsWindow(t *testing.T) {
	t.Parallel()
	demo := testenv.Require(t, "gtk3-demo")
	for _, backend := range []string{"wayland", "x11"} {
		t.Run(backend, func(t *testing.T) {
			h := StartXwaylandWith(t, func(cfg *compositor.Config) {
				clientDragConfig(cfg)
				cfg.HoldBound = gtkHoldBound
			}, xReport(t), desktop)
			env := []string{"env", "GDK_BACKEND=" + backend, "GTK_A11Y=none", "NO_AT_BRIDGE=1", "DBUS_SESSION_BUS_ADDRESS=disabled:"}
			if backend == "x11" {
				env = append(env, "DISPLAY="+h.xSocket())
			}
			gtk := h.Run(env[0], append(env[1:], demo)...)
			defer func() {
				if t.Failed() {
					t.Logf("gtk3-demo said:\n%s", gtk.Output())
					for l := range strings.SplitSeq(h.Trace(), "\n") {
						if strings.Contains(l, "xdg_toplevel") || strings.Contains(l, "button") || strings.Contains(l, "pointer") || strings.Contains(l, "decoration") {
							t.Log(l)
						}
					}
				}
			}()
			h.Eventually("the GTK window mapped", func(_ *headless.Backend, c *compositor.Compositor) bool {
				return len(windowsOf(c)) == 1 && !c.Holding()
			})
			h.dispatch("togglefloating")
			h.Eventually("the GTK window floating", func(_ *headless.Backend, c *compositor.Compositor) bool {
				ws := windowsOf(c)
				return len(ws) == 1 && ws[0].Floating && !c.Holding()
			})
			from := onlyWindowRect(h)
			h.Eventually("GTK drew itself at its floating size", showing(5, 5, previewBackground))
			at := image.Pt(from.Min.X+from.Dx()/3, from.Min.Y+12)
			presses := 0
			for ; presses < 5 && !h.dragging(); presses++ {
				h.movePointer(at)
				h.button(true)
				h.movePointer(at.Add(image.Pt(20, 0)))
				deadline := time.Now().Add(time.Second)
				for !h.dragging() && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if !h.dragging() {
					h.button(false)
				}
			}
			if !h.dragging() {
				t.Fatalf("GTK never asked lys-wm to move its window in %d header bar drags", presses)
			}
			h.movePointer(at.Add(image.Pt(80, 40)))
			h.button(false)
			h.Eventually("the GTK window moved with the pointer", func(_ *headless.Backend, c *compositor.Compositor) bool {
				r, ok := onlyRect(c)
				return ok && !c.Dragging() && r == from.Add(image.Pt(60, 40))
			})
		})
	}
}
