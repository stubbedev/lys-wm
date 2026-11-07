package harness

import (
	"errors"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func quickSwitch(cfg *compositor.Config) {
	cfg.Decor.DragPreview.Switch = compositor.DragSwitch{Width: 4, Frames: 3}
}

func firstWorkspaceEvent(t *testing.T, lines <-chan string) string {
	t.Helper()
	deadline := time.After(clientTimeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the event stream ended before a workspace event")
			}
			if strings.HasPrefix(line, `{"event":"workspace",`) {
				return line
			}
		case <-deadline:
			t.Fatal("no workspace event")
		}
	}
}

func (h *Harness) awayFromTheEdgeOnceSwitched(at image.Point) {
	h.t.Helper()
	h.Eventually("the held edge to switch workspaces", func(b *headless.Backend, c *compositor.Compositor) bool {
		if c.Snapshot().Outputs[0].Workspace == 1 {
			return false
		}
		o := b.Outputs()[0]
		size := o.State().Mode.Size
		if err := errors.Join(b.Pointer().MoveTo(o, float64(at.X)/float64(size.X), float64(at.Y)/float64(size.Y)), b.Pointer().Frame()); err != nil {
			h.t.Error(err)
		}
		return true
	})
}

func startDataDrag(t *testing.T, h *Harness, from *party, at image.Point) *testclient.Source {
	t.Helper()
	h.pointAt(0, float64(at.X), float64(at.Y))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame())
	})
	from.seen(t, "the press", func() bool { return from.in.Press != 0 })
	dragged := from.c.DataSource(map[string]string{"text/plain": "dragged"})
	if err := errors.Join(dragged.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy), from.cb.Device.StartDrag(dragged.Data, from.w.Surface, nil, from.in.Press), from.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	from.seen(t, "the drag entering the source", func() bool { return from.cb.Drag != nil })
	return dragged
}

func dropOn(t *testing.T, h *Harness, from *party, dragged *testclient.Source, to *party) {
	t.Helper()
	to.seen(t, "the drag entering the target", func() bool { return to.cb.Drag != nil })
	mime := "text/plain"
	if err := errors.Join(to.cb.Drag.Data.Accept(to.cb.Serial, &mime), to.cb.Drag.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy, cw.WlDataDeviceManagerDndActionCopy), to.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	from.seen(t, "the source told the target", func() bool { return slices.Contains(dragged.Log, "target text/plain") })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, false), b.Pointer().Frame())
	})
	to.seen(t, "the drop", func() bool { return slices.Contains(to.cb.Log, "drop") })
	if got := to.c.Receive(func(fd int) error { return to.cb.Drag.Data.Receive(mime, fd) }, from.c); got != "dragged" {
		t.Fatalf("dropped %q", got)
	}
}

func (h *Harness) idOf(title string) wm.WindowID {
	h.t.Helper()
	w, ok := windowTitled(h, title)
	if !ok {
		h.t.Fatalf("no window titled %q", title)
	}
	return w.ID
}

func TestAWindowHeldAtTheEdgeOfTheOnlyOutputMovesToTheNextWorkspace(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		dragConfig(t, cfg)
		quickSwitch(cfg)
	}, small)
	floatingForDrag(t, h)
	id := h.idOf("dragged")
	lines := h.watch()
	var looks uint64
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		looks = c.DragHoldLooks()
		return nil
	})
	holdDrag(t, h, image.Pt(80, 50), image.Pt(159, 50))
	h.awayFromTheEdgeOnceSwitched(image.Pt(80, 50))
	releaseDrag(h)
	if got := firstWorkspaceEvent(t, lines); got != `{"event":"workspace","output":"HEADLESS-1","workspace":2}` {
		t.Fatalf("the first switch: %s", got)
	}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if c.DragHoldLooks() == looks {
			t.Error("the drag never looked for an edge")
		}
		if ws, _ := c.WM().WorkspaceOf(id); ws != c.Snapshot().Outputs[0].Workspace {
			t.Errorf("the dragged window is on workspace %d, the output shows %d", ws, c.Snapshot().Outputs[0].Workspace)
		}
		return nil
	})
	h.Eventually("the carried window shown where it was dropped", showing(45, 30, redColor))
}

func TestADragAndDropHeldAtTheEdgeSwitchesWorkspacesAndDropsThere(t *testing.T) {
	t.Parallel()
	h := StartWith(t, quickSwitch, small)
	target := join(t, h, "b")
	h.dispatch("movetoworkspace 2")
	source := join(t, h, "a")
	dragged := startDataDrag(t, h, source, image.Pt(20, 50))
	lines := h.watch()
	h.pointAt(0, 159, 50)
	h.awayFromTheEdgeOnceSwitched(image.Pt(80, 50))
	if got := firstWorkspaceEvent(t, lines); got != `{"event":"workspace","output":"HEADLESS-1","workspace":2}` {
		t.Fatalf("the first switch: %s", got)
	}
	dropOn(t, h, source, dragged, target)
}

func TestTheOverviewOpensDuringADragAndAHeldTileTakesTheDrop(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		plainOverview(cfg)
		quickSwitch(cfg)
	}, overviewMode)
	source := join(t, h, "a")
	target := join(t, h, "b")
	dragged := startDataDrag(t, h, source, image.Pt(100, 250))
	h.dispatch("overview show")
	h.expectOverview(true)
	id := h.idOf("b")
	at := h.tileCentre(id)
	h.pointAt(0, float64(at.X), float64(at.Y))
	h.Eventually("the held tile to close the overview and focus b", func(_ *headless.Backend, c *compositor.Compositor) bool {
		focused, ok := c.WM().Focused()
		return !c.Snapshot().Overview && ok && focused == id
	})
	dropOn(t, h, source, dragged, target)
}

func TestAHeldBackgroundTabIsRaisedDuringADrag(t *testing.T) {
	t.Parallel()
	h := StartWith(t, quickSwitch, small)
	join(t, h, "a")
	h.dispatch("makegroup tab")
	b := join(t, h, "b")
	ids := [2]wm.WindowID{h.idOf("a"), h.idOf("b")}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if focused, _ := c.WM().Focused(); focused != ids[1] {
			t.Fatalf("window %d focused, want b's %d", focused, ids[1])
		}
		return nil
	})
	startDataDrag(t, h, b, image.Pt(120, 60))
	h.pointAt(0, 40, 5)
	h.Eventually("a's tab raised", func(_ *headless.Backend, c *compositor.Compositor) bool {
		focused, _ := c.WM().Focused()
		return focused == ids[0]
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, false), b.Pointer().Frame())
	})
}

func TestAThumbnailHeldDuringADragShowsItsWorkspaceAndTakesTheDrop(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		plainOverview(cfg)
		quickSwitch(cfg)
	}, overviewMode)
	target := join(t, h, "b")
	h.dispatch("movetoworkspace 2")
	source := join(t, h, "a")
	dragged := startDataDrag(t, h, source, image.Pt(400, 250))
	h.dispatch("overview show")
	h.expectOverview(true)
	plan := planOf(h, 0)
	if !slices.Equal(plan.spaces, []wm.WorkspaceID{1, 2}) {
		t.Fatalf("spaces %v", plan.spaces)
	}
	at := centre(plan.Strip[1])
	h.pointAt(0, float64(at.X), float64(at.Y))
	h.Eventually("the held thumbnail to show workspace 2", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Snapshot().Outputs[0].Workspace == 2
	})
	h.expectOverview(true)
	id := h.idOf("b")
	at = h.tileCentre(id)
	h.pointAt(0, float64(at.X), float64(at.Y))
	h.Eventually("the held tile to close the overview", func(_ *headless.Backend, c *compositor.Compositor) bool { return !c.Snapshot().Overview })
	dropOn(t, h, source, dragged, target)
}

func (h *Harness) tileCentre(id wm.WindowID) image.Point {
	h.t.Helper()
	var tile image.Rectangle
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		var ok bool
		if tile, ok = c.OverviewWindowRect(id, c.Now()); !ok {
			h.t.Errorf("window %d has no overview tile", id)
		}
		return nil
	})
	return centre(tile)
}
