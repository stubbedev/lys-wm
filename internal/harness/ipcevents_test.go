package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	csb "github.com/stubbedev/lys-wm/internal/protocol/client/systembell"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type streamed struct {
	Event     string       `json:"event"`
	Window    *wm.WindowID `json:"window"`
	Change    string       `json:"change"`
	Namespace string       `json:"namespace"`
	Floating  bool         `json:"floating"`
	Group     *ipc.Group   `json:"group"`
	Action    string       `json:"action"`
	Opened    *ipc.Window  `json:"-"`
	Raw       string       `json:"-"`
}

func parseStreamed(t *testing.T, line string) streamed {
	t.Helper()
	var e streamed
	var opened struct {
		Window json.RawMessage `json:"window"`
	}
	if err := json.Unmarshal([]byte(line), &opened); err != nil {
		t.Fatalf("event %s: %v", line, err)
	}
	if strings.HasPrefix(line, `{"event":"open"`) {
		var w ipc.Window
		if err := json.Unmarshal(opened.Window, &w); err != nil {
			t.Fatalf("open event %s: %v", line, err)
		}
		e.Event, e.Opened, e.Raw = "open", &w, line
		return e
	}
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		t.Fatalf("event %s: %v", line, err)
	}
	e.Raw = line
	return e
}

func awaitStreamed(t *testing.T, lines <-chan string, what string, match func(streamed) bool) streamed {
	t.Helper()
	deadline := time.After(clientTimeout)
	var seen []string
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the event stream ended before %s; saw %s", what, strings.Join(seen, "\n"))
			}
			seen = append(seen, line)
			if e := parseStreamed(t, line); match(e) {
				return e
			}
		case <-deadline:
			t.Fatalf("no %s on the event stream; saw %s", what, strings.Join(seen, "\n"))
		}
	}
}

func windowIDTitled(t *testing.T, h *Harness, title string) wm.WindowID {
	t.Helper()
	for _, w := range query[[]ipc.Window](h, ipc.KindWindows) {
		if w.Title == title {
			return w.ID
		}
	}
	t.Fatalf("no window titled %s", title)
	return 0
}

func TestModeGroupLayerAndBellEventsAndTheLayerAndDeviceQueries(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	lines := h.watch()
	c := testclient.Connect(t, h.Socket())
	a := c.Window("a", quadrants)
	c.DispatchUntil("a to map", a.Mapped)
	b := c.Window("b", quadrants)
	c.DispatchUntil("b to map", b.Mapped)
	ida, idb := windowIDTitled(t, h, "a"), windowIDTitled(t, h, "b")

	h.dispatch("togglefloating")
	awaitStreamed(t, lines, "b's floating mode", func(e streamed) bool {
		return e.Event == "mode" && *e.Window == idb && e.Floating
	})
	if w := query[ipc.Window](h, ipc.KindActiveWindow); w.Group != nil {
		t.Fatalf("the floating window reports group %+v", *w.Group)
	}
	h.dispatch("togglefloating")
	awaitStreamed(t, lines, "b tiled again", func(e streamed) bool {
		return e.Event == "mode" && *e.Window == idb && !e.Floating
	})
	h.dispatch("makegroup tab")
	tabs := awaitStreamed(t, lines, "b entering a tab group", func(e streamed) bool {
		return e.Event == "group" && *e.Window == idb && e.Group != nil && e.Group.Layout == "tab"
	})
	for _, w := range query[[]ipc.Window](h, ipc.KindWindows) {
		if w.ID == idb && (w.Group == nil || *w.Group != *tabs.Group) {
			t.Fatalf("b reports group %+v, its event %+v", w.Group, *tabs.Group)
		}
		if w.ID == ida && (w.Group == nil || w.Group.ID == tabs.Group.ID || w.Group.Layout != "h") {
			t.Fatalf("a reports group %+v beside b's tab group %+v", w.Group, *tabs.Group)
		}
	}

	bar := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight, Size: image.Pt(0, 30), Namespace: "bar", Interactivity: cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand}, testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the bar to map", bar.Mapped)
	opened := awaitStreamed(t, lines, "the bar opening", func(e streamed) bool { return e.Event == "layer" && e.Change == "open" })
	if opened.Namespace != "bar" || !strings.Contains(opened.Raw, `"output":"HEADLESS-1","layer":"top"`) {
		t.Fatalf("layer event %s", opened.Raw)
	}
	layers := query[[]ipc.Layer](h, ipc.KindLayers)
	if len(layers) != 1 || layers[0].Namespace != "bar" || layers[0].Rect != (ipc.Rect{Width: desktop.Size.X, Height: 30}) || layers[0].Keyboard != ipc.KeyboardOnDemand {
		t.Fatalf("layers %+v", layers)
	}
	if err := errors.Join(bar.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	awaitStreamed(t, lines, "the bar closing", func(e streamed) bool { return e.Event == "layer" && e.Change == "close" && e.Namespace == "bar" })

	devices := query[[]ipc.Device](h, ipc.KindDevices)
	keyboard := slices.IndexFunc(devices, func(d ipc.Device) bool { return slices.Contains(d.Capabilities, ipc.CapKeyboard) })
	pointer := slices.IndexFunc(devices, func(d ipc.Device) bool { return slices.Contains(d.Capabilities, ipc.CapPointer) })
	if keyboard < 0 || pointer < 0 || devices[keyboard].Layout == nil || devices[keyboard].Name == "" {
		t.Fatalf("devices %+v", devices)
	}

	bell, err := csb.NewXdgSystemBellV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(bell)
	if err := errors.Join(bell.Ring(a.Surface), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	awaitStreamed(t, lines, "a's bell", func(e streamed) bool { return e.Event == "bell" && e.Window != nil && *e.Window == ida })
}

func TestAWatchWithASnapshotMissesNoWindowMappedMeanwhile(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	const rounds, perRound = 3, 4
	var streams []<-chan string
	for r := range rounds {
		subscribed := make(chan (<-chan string), 1)
		go func() {
			stream, err := ipc.Subscribe(h.IPCSocket(), true)
			if err != nil {
				t.Error(err)
				subscribed <- nil
				return
			}
			t.Cleanup(func() { _ = stream.Close() })
			lines := make(chan string, 4096)
			go func() {
				defer close(lines)
				for {
					line, err := stream.Next()
					if err != nil {
						return
					}
					lines <- strings.TrimSpace(string(line))
				}
			}()
			subscribed <- lines
		}()
		for i := range perRound {
			w := c.Window(fmt.Sprintf("w%d-%d", r, i), quadrants)
			c.DispatchUntil("a window to map", w.Mapped)
		}
		lines := <-subscribed
		if lines == nil {
			t.FailNow()
		}
		streams = append(streams, lines)
	}
	for _, w := range query[[]ipc.Window](h, ipc.KindWindows) {
		for i, lines := range streams {
			awaitStreamed(t, lines, fmt.Sprintf("window %s opening on stream %d", w.Title, i), func(e streamed) bool {
				return e.Event == "open" && e.Opened.ID == w.ID
			})
		}
	}
}

func TestABatchOfDispatchesLandsAsOneBurst(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	for _, name := range []string{"a", "b", "c"} {
		w := c.Window(name, quadrants)
		c.DispatchUntil(name+" to map", w.Mapped)
	}
	lines := h.watch()
	batch := []string{"move left", "move left", "focus right"}
	if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Actions: batch}); err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: "submap done"}); err != nil {
		t.Fatal(err)
	}
	var got []streamed
	awaitStreamed(t, lines, "the end of the burst", func(e streamed) bool {
		got = append(got, e)
		return e.Event == "submap"
	})
	first := slices.IndexFunc(got, func(e streamed) bool { return e.Event == "dispatch" })
	if first < 0 || len(got) < first+3 {
		t.Fatalf("events %v", raws(got))
	}
	for i, a := range batch {
		if e := got[first+i]; e.Event != "dispatch" || e.Action != a {
			t.Fatalf("the batch's dispatches are not one burst: %v", raws(got))
		}
	}
	rects := map[wm.WindowID]int{}
	for _, e := range got {
		if e.Event == "rect" {
			rects[*e.Window]++
		}
	}
	if len(rects) == 0 {
		t.Fatalf("the batch moved no window: %v", raws(got))
	}
	for id, n := range rects {
		if n != 1 {
			t.Fatalf("window %d sent %d rect events for one batch: %v", id, n, raws(got))
		}
	}
}

func raws(events []streamed) string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Raw
	}
	return strings.Join(out, "\n")
}

func TestPickWindowAnswersTheClickedWindowAndEscapeCancels(t *testing.T) {
	t.Parallel()
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	left := c.Window("left", quadrants)
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", quadrants)
	c.DispatchUntil("right to map", right.Mapped)
	idLeft := windowIDTitled(t, h, "left")
	pick := func() <-chan string {
		answer := make(chan string, 1)
		go func() {
			got, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindPickWindow})
			if err != nil {
				answer <- "error: " + err.Error()
				return
			}
			answer <- string(got)
		}()
		h.Eventually("the crosshair", func(_ *headless.Backend, comp *compositor.Compositor) bool { return comp.CursorShape() == "crosshair" })
		return answer
	}
	answer := pick()
	h.click(0, 100, 400, btnLeft)
	if got := <-answer; got != fmt.Sprint(idLeft) {
		t.Fatalf("picking the left window answered %s, want %d", got, idLeft)
	}
	h.Eventually("the default cursor back", func(_ *headless.Backend, comp *compositor.Compositor) bool { return comp.CursorShape() != "crosshair" })

	answer = pick()
	h.tap(keyEsc)
	if got := <-answer; !strings.Contains(got, compositor.ErrPickCancelled.Error()) {
		t.Fatalf("escape answered %s", got)
	}
	h.Eventually("the default cursor back", func(_ *headless.Backend, comp *compositor.Compositor) bool { return comp.CursorShape() != "crosshair" })
}
