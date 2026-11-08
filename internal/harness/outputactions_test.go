package harness

import (
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (h *Harness) ctl(action string) {
	h.t.Helper()
	if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: wmipc.KindDispatch, Action: action}); err != nil {
		h.t.Fatalf("lys-wmctl dispatch %s: %v", action, err)
	}
}

func workspaceOn(h *Harness, id wm.WorkspaceID) ipc.Workspace {
	h.t.Helper()
	all := query[[]ipc.Workspace](h, wmipc.KindWorkspaces)
	i := slices.IndexFunc(all, func(w ipc.Workspace) bool { return w.ID == id })
	if i < 0 {
		h.t.Fatalf("IPC lists no workspace %d: %+v", id, all)
	}
	return all[i]
}

func TestWindowsAndWorkspacesMoveBetweenOutputs(t *testing.T) {
	t.Parallel()
	h := Start(t, small, small)
	red, blue, green := [3]byte{0xff, 0, 0}, [3]byte{0, 0, 0xff}, [3]byte{0, 0xff, 0}
	c := testclient.Connect(t, h.Socket())
	r := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", r.Mapped)
	b := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", b.Mapped)
	h.ctl("focusoutput right")
	g := c.Window("green", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("green to map", g.Mapped)
	h.ctl("focusoutput HEADLESS-1")
	v, watcher, _ := watchWorkspaces(t, h)

	h.ctl("move right")
	h.Eventually("blue at the left edge of the second output's tree", func(x *headless.Backend, comp *compositor.Compositor) bool {
		return showingOn(1, 20, 50, blue)(x, comp) && showingOn(1, 140, 50, green)(x, comp) && showingOn(0, 140, 50, red)(x, comp) && !comp.Holding()
	})
	if w := query[ipc.Window](h, wmipc.KindActiveWindow); w.Title != "blue" || w.Workspace != 2 {
		t.Fatalf("after move right the active window is %+v, want blue on workspace 2", w)
	}
	if w := workspaceOn(h, 2); w.Windows != 2 || w.Output != "HEADLESS-2" || !w.Focused {
		t.Fatalf("workspace 2 is %+v", w)
	}

	h.ctl("moveworkspacetooutput left")
	h.Eventually("workspace 2 shown on the first output", func(x *headless.Backend, comp *compositor.Compositor) bool {
		return showingOn(0, 20, 50, blue)(x, comp) && showingOn(0, 140, 50, green)(x, comp) && !comp.Holding()
	})
	if w := workspaceOn(h, 2); w.Output != "HEADLESS-1" || !w.Visible || !w.Focused {
		t.Fatalf("workspace 2 after moving it left is %+v", w)
	}
	if w := query[ipc.Window](h, wmipc.KindActiveWindow); w.Title != "blue" {
		t.Fatalf("moving the workspace moved focus to %+v", w)
	}
	watcher.DispatchUntil("ext-workspace puts workspace 2 in the first output's group", func() bool {
		return v.members[v.named("2")] == v.groups[0] && v.active("2")
	})

	h.ctl("swapworkspaces HEADLESS-1 HEADLESS-2")
	watcher.DispatchUntil("ext-workspace puts workspace 2 back in the second output's group", func() bool {
		return v.members[v.named("2")] == v.groups[1] && v.active("2")
	})
	h.ctl("focusoutput right")
	h.ctl("movetooutput left")
	if w := query[ipc.Window](h, wmipc.KindActiveWindow); w.Title != "blue" || w.Output != "HEADLESS-1" {
		t.Fatalf("movetooutput left left focus on %+v", w)
	}
}

func TestMoveToWorkspaceFollowRunsOneSwitch(t *testing.T) {
	t.Parallel()
	now := time.Hour
	d := time.Second
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Workspaces: {Enabled: &on, Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	red, blue := [3]byte{0xff, 0, 0}, [3]byte{0, 0, 0xff}
	c := testclient.Connect(t, h.Socket())
	r := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", r.Mapped)
	b := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", b.Mapped)
	h.Eventually("both tiled", func(x *headless.Backend, comp *compositor.Compositor) bool {
		return showing(20, 60, red)(x, comp) && showing(140, 60, blue)(x, comp) && !comp.Holding()
	})

	at(2 * time.Hour)
	h.ctl("movetoworkspace 3 follow")
	c.DispatchUntil("red to fill workspace 1", func() bool { return r.Logical == tall.Size })
	h.Eventually("the move to land", func(_ *headless.Backend, comp *compositor.Compositor) bool { return !comp.Holding() })
	at(2*time.Hour + 500*time.Millisecond)
	h.Eventually("workspace 1 leaving to the left and 3 with blue arriving from the right", func(x *headless.Backend, comp *compositor.Compositor) bool {
		return showing(20, 60, red)(x, comp) && showing(140, 60, blue)(x, comp) && !comp.Holding()
	})
	at(2*time.Hour + time.Second)
	h.Eventually("workspace 3 in place with blue alone", func(x *headless.Backend, comp *compositor.Compositor) bool {
		return showing(20, 60, blue)(x, comp) && showing(140, 60, blue)(x, comp)
	})
	if w := query[ipc.Window](h, wmipc.KindActiveWindow); w.Title != "blue" || w.Workspace != 3 {
		t.Fatalf("the followed window is %+v", w)
	}
	before := framesOn(h, 0)
	time.Sleep(200 * time.Millisecond)
	if n := framesOn(h, 0); n != before {
		t.Fatalf("%d frames ran after the switch ended", n-before)
	}
}
