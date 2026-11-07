package harness

import (
	"image"
	"strconv"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func floatingRectOf(h *Harness, title string) image.Rectangle {
	h.t.Helper()
	var r image.Rectangle
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		w, ok := windowNamed(comp, title)
		if !ok {
			h.t.Fatalf("no window %q", title)
		}
		r, _ = comp.WM().FloatingRect(w.ID)
		return nil
	})
	return r
}

func TestADraggedFloatingWindowSnapsFlushBesideAnother(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		snap  bool
		wantX int
	}{{"snapping", true, 82}, {"not snapping", false, 85}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := StartWith(t, func(cfg *compositor.Config) {
				dragConfig(t, cfg)
				cfg.WM.Gaps = wm.Gaps{Inner: 4}
				cfg.Decor.DragPreview.SnapWindows = tc.snap
			}, small)
			_, w := floatingForDrag(t, h)
			c := testclient.Connect(t, h.Socket())
			anchor := c.Window("anchor", testclient.Fill(0, 0xff, 0))
			c.DispatchUntil("the anchor to map", anchor.Mapped)
			h.ctl("focuswindow " + idOf(h, "anchor"))
			h.ctl("place left")
			c.DispatchUntil("the anchor on the left half", func() bool { return anchor.Logical == image.Pt(78, 100) })
			holdDrag(t, h, image.Pt(80, 50), image.Pt(125, 50))
			releaseDrag(h)
			h.Eventually("the drag to end", func(_ *headless.Backend, comp *compositor.Compositor) bool { return !comp.Holding() })
			if got := floatingRectOf(h, "dragged"); got != image.Rect(tc.wantX, 25, tc.wantX+80, 75) {
				t.Fatalf("dropped at %v, want x %d", got, tc.wantX)
			}
			if got := w.Logical; got != image.Pt(80, 50) {
				t.Fatalf("the drag resized the window to %v", got)
			}

			h.ctl("focuswindow " + idOf(h, "dragged"))
			h.ctl("place right")
			if got := floatingRectOf(h, "dragged"); got != image.Rect(82, 0, 160, 100) {
				t.Fatalf("place right gave %v", got)
			}
			h.ctl("place restore")
			if got := floatingRectOf(h, "dragged"); got != image.Rect(tc.wantX, 25, tc.wantX+80, 75) {
				t.Fatalf("place restore gave %v", got)
			}
			h.ctl("move left")
			if got := floatingRectOf(h, "dragged"); got != image.Rect(tc.wantX-wm.DefaultFloatStep, 25, tc.wantX-wm.DefaultFloatStep+80, 75) {
				t.Fatalf("move left gave %v", got)
			}
		})
	}
}

func idOf(h *Harness, title string) string {
	h.t.Helper()
	var id wm.WindowID
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		w, ok := windowNamed(comp, title)
		if !ok {
			h.t.Fatalf("no window %q", title)
		}
		id = w.ID
		return nil
	})
	return strconv.FormatUint(uint64(id), 10)
}
