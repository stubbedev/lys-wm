package harness

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cxtd "github.com/stubbedev/lys-wm/internal/protocol/client/xdgtopleveldrag"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type tabDrag struct {
	h      *Harness
	a      *party
	source *testclient.Source
	drag   *cxtd.XdgToplevelDragV1
	at     image.Point
}

func startTabDrag(t *testing.T, h *Harness, a *party, at image.Point, attach func(*cxtd.XdgToplevelDragV1) error) *tabDrag {
	t.Helper()
	d := &tabDrag{h: h, a: a, at: at}
	pressOn(h, a.c, a.in, at)
	m, err := cxtd.NewXdgToplevelDragManagerV1(a.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.c.Bind(m)
	d.source = a.c.DataSource(map[string]string{"text/plain": "tab"})
	if d.drag, err = m.GetXdgToplevelDrag(d.source.Data); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(attach(d.drag), d.source.Data.SetActions(cw.WlDataDeviceManagerDndActionMove), a.cb.Device.StartDrag(d.source.Data, a.w.Surface, nil, a.in.Press), a.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the drag", func(_ *headless.Backend, c *compositor.Compositor) bool { return c.DataDragging() })
	return d
}

func windowRect(h *Harness, id wm.WindowID) (ipc.Window, bool) {
	var got ipc.Window
	found := false
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		for _, w := range windowsOf(c) {
			if w.ID == id {
				got, found = w, true
			}
		}
		return nil
	})
	return got, found
}

func TestADraggedToplevelFollowsThePointerWithoutConfigures(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	a := join(t, h, "a")
	tab := a.c.Window("tab", quadrants)
	a.c.DispatchUntil("the tab window to map", tab.Mapped)
	windows := query[[]ipc.Window](h, wmipc.KindWindows)
	idTab := windows[1].ID
	offset := image.Pt(12, 7)
	start := image.Pt(windows[0].Rect.X+40, windows[0].Rect.Y+40)
	startTabDrag(t, h, a, start, func(d *cxtd.XdgToplevelDragV1) error { return d.Attach(tab.Toplevel, int32(offset.X), int32(offset.Y)) })
	h.Eventually("the tab floating at the pointer", func(_ *headless.Backend, c *compositor.Compositor) bool {
		w := windowsOf(c)[1]
		return w.Floating && image.Pt(w.Rect.X, w.Rect.Y) == start.Sub(offset)
	})
	a.c.DispatchUntil("the floating size", func() bool {
		w, _ := windowRect(h, idTab)
		return tab.Logical == image.Pt(w.Rect.Width, w.Rect.Height)
	})
	settle(t, h)
	configures := tab.Configures
	last, _ := windowRect(h, idTab)
	for i := 1; i <= 100; i++ {
		at := start.Add(image.Pt(i*3, 0))
		h.movePointer(at)
		h.Eventually("the tab following", func(_ *headless.Backend, c *compositor.Compositor) bool {
			for _, w := range windowsOf(c) {
				if w.ID == idTab {
					return image.Pt(w.Rect.X, w.Rect.Y) == at.Sub(offset)
				}
			}
			return false
		})
		now, _ := windowRect(h, idTab)
		h.Rendered(0)
		var damage region.Set
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			for _, r := range b.Outputs()[0].LastDamage() {
				damage.Add(r)
			}
			return nil
		})
		moved := rectOf(last.Rect).Union(rectOf(now.Rect)).Inset(-64)
		for _, r := range damage.Rects() {
			if !r.In(moved) {
				t.Fatalf("motion %d damaged %v outside the tab's old and new places %v", i, r, moved)
			}
		}
		last = now
	}
	a.c.Sync()
	if n := tab.Configures - configures; n != 0 {
		t.Fatalf("100 motions sent the dragged toplevel %d configures", n)
	}
	h.button(false)
	h.Eventually("the drop", func(_ *headless.Backend, c *compositor.Compositor) bool { return !c.DataDragging() })
	if w, _ := windowRect(h, idTab); !w.Floating || w.Rect != last.Rect {
		t.Fatalf("after the drop the tab is %+v, want floating at %+v", w, last.Rect)
	}
}

func rectOf(r wmipc.Rect) image.Rectangle {
	return image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height)
}

func TestAnUnmappedToplevelAttachedToADragMapsAtThePointer(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	a := join(t, h, "a")
	at := image.Pt(300, 200)
	offset := image.Pt(20, 5)
	var torn *testclient.Window
	d := startTabDrag(t, h, a, at, func(*cxtd.XdgToplevelDragV1) error { return nil })
	torn = a.c.WindowWith("torn", quadrants, func(w *testclient.Window) error {
		return d.drag.Attach(w.Toplevel, int32(offset.X), int32(offset.Y))
	})
	a.c.DispatchUntil("the torn window to map", torn.Mapped)
	h.Eventually("the torn window at the pointer", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[1].Floating && image.Pt(ws[1].Rect.X, ws[1].Rect.Y) == at.Sub(offset)
	})
	if err := errors.Join(d.source.Data.Destroy(), a.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	h.button(false)
}

func TestASecondAttachToADraggedToplevelIsAProtocolError(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	a := join(t, h, "a")
	tab := a.c.Window("tab", quadrants)
	a.c.DispatchUntil("the tab window to map", tab.Mapped)
	other := a.c.Window("other", quadrants)
	a.c.DispatchUntil("the other window to map", other.Mapped)
	d := startTabDrag(t, h, a, image.Pt(40, 40), func(d *cxtd.XdgToplevelDragV1) error { return d.Attach(tab.Toplevel, 0, 0) })
	if err := errors.Join(d.drag.Attach(other.Toplevel, 0, 0), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	a.c.ExpectProtocolError(uint32(cxtd.XdgToplevelDragV1ErrorToplevelAttached))
}

func TestATabDragSourceCannotBeTheSelection(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	a := join(t, h, "a")
	a.seen(t, "a focused", func() bool { return a.in.Serial != 0 })
	m, err := cxtd.NewXdgToplevelDragManagerV1(a.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.c.Bind(m)
	src := a.c.DataSource(map[string]string{"text/plain": "tab"})
	if _, err := m.GetXdgToplevelDrag(src.Data); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(a.cb.Device.SetSelection(src.Data, a.in.Serial), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	a.c.ExpectProtocolError(uint32(cxtd.XdgToplevelDragManagerV1ErrorInvalidSource))
}

func TestADropTargetIsTheWindowUnderTheDraggedToplevel(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	a := join(t, h, "a")
	tab := a.c.Window("tab", quadrants)
	a.c.DispatchUntil("the tab window to map", tab.Mapped)
	windows := query[[]ipc.Window](h, wmipc.KindWindows)
	under, origin := windows[0].Rect, windows[1].Rect
	pressOn(h, a.c, a.in, image.Pt(origin.X+20, origin.Y+20))
	m, err := cxtd.NewXdgToplevelDragManagerV1(a.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.c.Bind(m)
	source := a.c.DataSource(map[string]string{"text/plain": "tab"})
	drag, err := m.GetXdgToplevelDrag(source.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(source.Data.SetActions(cw.WlDataDeviceManagerDndActionMove), a.cb.Device.StartDrag(source.Data, tab.Surface, nil, a.in.Press), drag.Attach(tab.Toplevel, 20, 20), a.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	tabID := windows[1].ID
	h.Eventually("the tab floating", func(_ *headless.Backend, c *compositor.Compositor) bool {
		for _, w := range windowsOf(c) {
			if w.ID == tabID {
				return w.Floating
			}
		}
		return false
	})
	a.c.DispatchUntil("the tab at its floating size", func() bool {
		w, _ := windowRect(h, tabID)
		return tab.Logical == image.Pt(w.Rect.Width, w.Rect.Height)
	})
	settle(t, h)
	at := image.Pt(under.X+100, under.Y+80)
	h.movePointer(at)
	want := fmt.Sprintf("motion %d,%d", at.X-under.X, at.Y-under.Y)
	a.c.Sync()
	if !slices.Contains(a.cb.Log, want) {
		t.Fatalf("the drag logged %v, want %q", a.cb.Log, want)
	}
	h.button(false)
}
