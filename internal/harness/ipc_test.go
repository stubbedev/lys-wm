package harness

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	chgs "github.com/stubbedev/lys-wm/internal/protocol/client/hyprlandglobalshortcuts"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

func query[T any](h *Harness, kind ipc.Kind) T {
	h.t.Helper()
	raw, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: kind})
	if err != nil {
		h.t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		h.t.Fatalf("decode %s %s: %v", kind, raw, err)
	}
	return v
}

func (h *Harness) watch() <-chan string {
	h.t.Helper()
	stream, err := ipc.Subscribe(h.IPCSocket())
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = stream.Close() })
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
	return lines
}

func expectEvent(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(clientTimeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the event stream ended before %s", want)
			}
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("no %s on the event stream", want)
		}
	}
}

func TestEveryActionThroughIPC(t *testing.T) {
	reloads, exits := 0, 0
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Reload = func() error {
			reloads++
			return nil
		}
		cfg.Exit = func() error {
			exits++
			return nil
		}
	}, desktop)
	lines := h.watch()
	c := testclient.Connect(t, h.Socket())
	var windows []*testclient.Window
	for _, name := range []string{"a", "b", "c"} {
		w := c.Window(name, quadrants)
		c.DispatchUntil(name+" to map", w.Mapped)
		windows = append(windows, w)
	}
	shortcuts, err := chgs.NewHyprlandGlobalShortcutsManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(shortcuts)
	if _, err := shortcuts.RegisterShortcut(&shortcutLog{}, "ptt", "voice", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	actions := []string{
		"focus left", "move right", "makegroup tab ephemeral", "togglefloating", "togglefloating",
		"fullscreen", "fullscreen", "resizeactive 10 0", "submap resize", "submap reset",
		"movetoworkspace 3", "workspace 3", "workspace e-1", "exec true", "global voice:ptt", "reload", "exit", "dpms off", "dpms toggle HEADLESS-1", "kill",
	}
	mouseOnly := []string{"movewindow", "resizewindow"}
	byID := []string{"focuswindow", "closewindow"}
	for _, spec := range wm.Actions {
		if !slices.ContainsFunc(slices.Concat(actions, mouseOnly, byID), func(a string) bool { return strings.Fields(a)[0] == spec.Name }) {
			t.Errorf("action %s is not walked through IPC", spec.Name)
		}
	}
	for _, a := range mouseOnly {
		if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: a}); err == nil || !strings.Contains(err.Error(), compositor.ErrNotFromMouseBind.Error()) {
			t.Fatalf("%s through IPC: %v", a, err)
		}
	}
	dispatch := func(a string) {
		t.Helper()
		if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: a}); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		expectEvent(t, lines, `{"event":"dispatch","action":"`+a+`"}`)
	}
	for _, a := range actions {
		dispatch(a)
		if a == "workspace 3" {
			expectEvent(t, lines, `{"event":"workspace","output":"HEADLESS-1","workspace":3}`)
			if w := query[ipc.Window](h, ipc.KindActiveWindow); w.Workspace != 3 || !w.Focused {
				t.Fatalf("the window moved to workspace 3 is not active: %+v", w)
			}
		}
	}
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	if reloads != 1 || exits != 1 {
		t.Fatalf("reload ran %d times and exit %d", reloads, exits)
	}
	all := query[[]ipc.Window](h, ipc.KindWindows)
	other := slices.IndexFunc(all, func(w ipc.Window) bool { return w.Workspace == 1 && !w.Focused })
	if other < 0 {
		t.Fatalf("no unfocused window on workspace 1 among %+v", all)
	}
	for _, name := range byID {
		dispatch(fmt.Sprintf("%s %d", name, all[other].ID))
	}
	if w := query[ipc.Window](h, ipc.KindActiveWindow); w.ID != all[other].ID {
		t.Fatalf("focuswindow %d left window %d active", all[other].ID, w.ID)
	}
	c.DispatchUntil("kill and closewindow to close two windows", func() bool {
		closed := 0
		for _, w := range windows {
			if w.Closed {
				closed++
			}
		}
		return closed == 2
	})
	outputs := query[[]ipc.Output](h, ipc.KindOutputs)
	if len(outputs) != 1 || outputs[0].Workspace != 1 || !outputs[0].Focused || outputs[0].Rect != (ipc.Rect{Width: 1280, Height: 800}) {
		t.Fatalf("outputs %+v", outputs)
	}
	got := query[[]ipc.Workspace](h, ipc.KindWorkspaces)
	want := []ipc.Workspace{{ID: 1, Output: "HEADLESS-1", Windows: 2, Visible: true, Focused: true}, {ID: 3, Output: "HEADLESS-1", Windows: 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("workspaces\n got %+v\nwant %+v", got, want)
	}
}

func TestSubmapAndLayoutAreQueriedWithoutWaitingForAChange(t *testing.T) {
	dk, err := testenv.XKBLibrary(t).Compile(xkb.Names{Layout: "dk"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dk.Close() })
	var comp *compositor.Compositor
	h := StartWith(t, func(cfg *compositor.Config) {
		next := compositor.DefaultConfig(dk, cfg.Font)
		next.Report = cfg.Report
		cfg.Reload = func() error { return comp.Reconfigure(next) }
	}, small)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		comp = c
		return nil
	})
	if got := query[string](h, ipc.KindSubmap); got != "" {
		t.Fatalf("a fresh lys-wm is in submap %q", got)
	}
	if got := query[ipc.Layout](h, ipc.KindLayout); got.Index != 0 || got.Name == "" {
		t.Fatalf("a fresh lys-wm has layout %+v", got)
	}
	for _, a := range []string{"reload", "submap resize"} {
		if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: a}); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	if got := query[string](h, ipc.KindSubmap); got != "resize" {
		t.Fatalf("after submap resize the submap is %q", got)
	}
	if got := query[ipc.Layout](h, ipc.KindLayout); got != (ipc.Layout{Name: "Danish"}) {
		t.Fatalf("after reloading a Danish keymap the layout is %+v", got)
	}
}

func TestFocusAndCloseAWindowByIDThroughIPC(t *testing.T) {
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	a := c.Window("a", quadrants)
	c.DispatchUntil("a to map", a.Mapped)
	dispatch := func(action string) error {
		_, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: action})
		return err
	}
	if err := dispatch("workspace 2"); err != nil {
		t.Fatal(err)
	}
	b := c.Window("b", quadrants)
	c.DispatchUntil("b to map", b.Mapped)
	ids := map[string]wm.WindowID{}
	for _, w := range query[[]ipc.Window](h, ipc.KindWindows) {
		ids[w.Title] = w.ID
	}
	if err := dispatch(fmt.Sprintf("focuswindow %d", ids["a"])); err != nil {
		t.Fatal(err)
	}
	if w := query[ipc.Window](h, ipc.KindActiveWindow); w.ID != ids["a"] || w.Workspace != 1 {
		t.Fatalf("after focuswindow %d the active window is %+v", ids["a"], w)
	}
	if o := query[[]ipc.Output](h, ipc.KindOutputs); o[0].Workspace != 1 {
		t.Fatalf("after focuswindow %d the output shows workspace %d", ids["a"], o[0].Workspace)
	}
	c.DispatchUntil("a to be activated", func() bool { return slices.Contains(a.States, cxdg.XdgToplevelStateActivated) })
	if err := dispatch(fmt.Sprintf("closewindow %d", ids["b"])); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("b to be asked to close", func() bool { return b.Closed })
	if a.Closed {
		t.Fatal("closewindow on b closed a too")
	}
	for _, action := range []string{"focuswindow 999", "closewindow 999"} {
		if err := dispatch(action); err == nil || !strings.Contains(err.Error(), "window 999: wm: unknown window") {
			t.Errorf("%s: %v", action, err)
		}
	}
}

func (h *Harness) expectActive(c *Client) {
	h.t.Helper()
	deadline := time.Now().Add(clientTimeout)
	for {
		raw, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindActiveWindow})
		if err != nil {
			h.t.Fatal(err)
		}
		var w ipc.Window
		if raw != nil {
			if err := json.Unmarshal(raw, &w); err != nil {
				h.t.Fatal(err)
			}
		}
		if raw != nil && int(w.PID) == c.cmd.Process.Pid && w.Focused {
			if windows := query[[]ipc.Window](h, ipc.KindWindows); !slices.ContainsFunc(windows, func(x ipc.Window) bool { return x.ID == w.ID }) {
				h.t.Fatalf("the active window %d is not among the windows %+v", w.ID, windows)
			}
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("IPC never showed %s (pid %d) as the active window; last %s", filepath.Base(c.cmd.Path), c.cmd.Process.Pid, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
