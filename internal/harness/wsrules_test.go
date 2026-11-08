package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func workspaceRule(id wm.WorkspaceID, r wm.WorkspaceRule, bind string) compositor.WorkspaceRule {
	r.ID = id
	rule := compositor.WorkspaceRule{WorkspaceRule: r}
	if bind != "" {
		rule.Bind = &wm.OutputMatch{Name: bind}
	}
	return rule
}

func ipcWorkspaces(h *Harness) []ipc.Workspace {
	return query[[]ipc.Workspace](h, wmipc.KindWorkspaces)
}

func TestWorkspaceRulesBindNameAndKeepWorkspaces(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.WorkspaceRules = []compositor.WorkspaceRule{
			workspaceRule(1, wm.WorkspaceRule{Default: true}, "HEADLESS-1"),
			workspaceRule(9, wm.WorkspaceRule{Name: "chat", Persistent: true}, "HEADLESS-2"),
		}
	}, small, small)
	v, watcher, _ := watchWorkspaces(t, h)
	watcher.DispatchUntil("chat listed by name from startup", func() bool { return v.named("chat") != nil })
	if ws := ipcWorkspaces(h); !strings.Contains(strings.Join(func() []string {
		var out []string
		for _, w := range ws {
			if w.ID == 9 && w.Name == "chat" && w.Windows == 0 && w.Output == "HEADLESS-2" {
				out = append(out, "chat")
			}
		}
		return out
	}(), ","), "chat") {
		t.Fatalf("lys-wmctl workspaces: %+v, want the empty persistent chat on HEADLESS-2", ws)
	}
	h.dispatch("focus right")
	h.dispatch("workspace 1")
	h.Eventually("workspace 1 on its output with focus there", func(_ *headless.Backend, c *compositor.Compositor) bool {
		s := c.WM()
		id, _ := s.ActiveOn("HEADLESS-1")
		return id == 1 && s.FocusedOutput() == "HEADLESS-1"
	})
	h.dispatch("workspace chat")
	h.Eventually("workspace chat shown on HEADLESS-2", func(_ *headless.Backend, c *compositor.Compositor) bool {
		s := c.WM()
		id, _ := s.ActiveOn("HEADLESS-2")
		return id == 9 && s.FocusedOutput() == "HEADLESS-2"
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return b.RemoveOutput(b.Outputs()[0]) })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddNamedOutput("HEADLESS-1", small)
		return err
	})
	h.Eventually("workspace 1 shown on HEADLESS-1 again after a replug", func(_ *headless.Backend, c *compositor.Compositor) bool {
		w, ok := c.WM().Workspace(1)
		id, _ := c.WM().ActiveOn("HEADLESS-1")
		return ok && w.Output == "HEADLESS-1" && id == 1
	})
}

func TestOnCreatedEmptyRunsOncePerShowingEmpty(t *testing.T) {
	t.Parallel()
	marks := filepath.Join(t.TempDir(), "marks")
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.WorkspaceRules = []compositor.WorkspaceRule{workspaceRule(3, wm.WorkspaceRule{OnCreatedEmpty: "echo x >> " + marks}, "")}
	}, small)
	count := func() int {
		b, _ := os.ReadFile(marks)
		return strings.Count(string(b), "x")
	}
	h.dispatch("workspace 3")
	h.Eventually("the command ran", func(*headless.Backend, *compositor.Compositor) bool { return count() == 1 })
	h.dispatch("workspace 3")
	h.dispatch("workspace 1")
	h.dispatch("workspace 3")
	h.Eventually("it ran again for the second showing", func(*headless.Backend, *compositor.Compositor) bool { return count() == 2 })
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		c.WaitForExecs()
		return nil
	})
	if n := count(); n != 2 {
		t.Fatalf("ran %d times for two showings", n)
	}
}

func TestASingleTiledRuleDropsGapsAndBorderForALoneWindow(t *testing.T) {
	t.Parallel()
	zero := 0
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.BorderWidth = 3
		cfg.WM.Gaps = wm.Gaps{Inner: 4, Outer: wm.Insets{Top: 5, Right: 5, Bottom: 5, Left: 5}}
		cfg.WorkspaceRules = []compositor.WorkspaceRule{{Select: wm.SelectSingleTiled, Look: wm.WorkspaceLook{Gaps: &wm.Gaps{}, BorderWidth: &zero}}}
	}, desktop)
	c := testclient.Connect(t, h.Socket())
	a := c.Window("a", fillGreen)
	c.DispatchUntil("a to map", a.Mapped)
	h.settled(c, "a alone", func(w ipc.Window) bool { return true }, "a")
	if a.Size != desktop.Size {
		t.Fatalf("a lone window configured %v, want the whole output", a.Size)
	}
	before := a.Configures
	b := c.Window("b", fillGreen)
	c.DispatchUntil("b to map", b.Mapped)
	h.settled(c, "both", func(w ipc.Window) bool { return true }, "b")
	if want := (desktop.Size.X-10-4)/2 - 6; a.Size.X != want {
		t.Fatalf("with b, a is %v wide, want %d with gaps and border back", a.Size, want)
	}
	if n := a.Configures - before; n != 1 {
		t.Fatalf("a was configured %d times when b opened, want 1", n)
	}
}
