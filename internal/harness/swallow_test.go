package harness

import (
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const swallowChildSocket = "LYS_WM_SWALLOW_CHILD_SOCKET"

func TestSwallowChildProcess(t *testing.T) {
	socket := os.Getenv(swallowChildSocket)
	if socket == "" {
		t.Skip("runs only as the child process of TestATerminalIsSwallowedByTheWindowStartedFromIt")
	}
	c := testclient.Connect(t, socket)
	w := c.Window("viewer", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the viewer to map", w.Mapped)
	var closed atomic.Bool
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		closed.Store(true)
	}()
	c.DispatchUntil("the parent to close stdin", closed.Load)
}

type swallowChild struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func startSwallowChild(t *testing.T, h *Harness) swallowChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSwallowChildProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), swallowChildSocket+"="+h.Socket())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := swallowChild{cmd: cmd, stdin: stdin}
	t.Cleanup(child.stop)
	return child
}

func (c swallowChild) stop() {
	_ = c.stdin.Close()
	_ = c.cmd.Wait()
}

func windowTitled(h *Harness, title string) (ipc.Window, bool) {
	for _, w := range query[[]ipc.Window](h, ipc.KindWindows) {
		if w.Title == title {
			return w, true
		}
	}
	return ipc.Window{}, false
}

func TestATerminalIsSwallowedByTheWindowStartedFromIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		except  *regexp.Regexp
		swallow bool
	}{{"swallowing", nil, true}, {"an excepted title", regexp.MustCompile("view"), false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := StartWith(t, func(cfg *compositor.Config) {
				cfg.Swallow.Terminals = regexp.MustCompile(`^lys-wm\.testclient$`)
				cfg.Swallow.ExceptTitle = tc.except
			}, desktop)
			c := testclient.Connect(t, h.Socket())
			other := c.Window("other", fillGreen)
			c.DispatchUntil("other to map", other.Mapped)
			term := c.Window("term", fillBlue)
			c.DispatchUntil("the terminal to map", term.Mapped)
			h.settled(c, "both tiled", func(ipc.Window) bool { return true }, "term")
			tile, _ := windowTitled(h, "term")

			child := startSwallowChild(t, h)
			h.Eventually("the viewer to map", func(_ *headless.Backend, comp *compositor.Compositor) bool {
				_, ok := windowNamed(comp, "viewer")
				return ok && !comp.Holding()
			})
			c.Sync()
			viewer, _ := windowTitled(h, "viewer")
			termNow, _ := windowTitled(h, "term")
			if !tc.swallow {
				if termNow.SwallowedBy != 0 || viewer.Rect == tile.Rect {
					t.Fatalf("an excepted window swallowed: terminal %+v, viewer %+v", termNow, viewer)
				}
				return
			}
			if viewer.Rect != tile.Rect || termNow.SwallowedBy != viewer.ID || !viewer.Focused {
				t.Fatalf("the viewer is at %+v and the terminal %+v, want the viewer in the terminal's tile %+v", viewer.Rect, termNow, tile.Rect)
			}
			if o, _ := windowTitled(h, "other"); o.Rect == viewer.Rect {
				t.Fatal("the other window moved")
			}

			h.ctl("swallow off")
			h.settled(c, "both shown", func(w ipc.Window) bool { return w.SwallowedBy == 0 }, "term")
			h.ctl("swallow on")
			h.settled(c, "hidden again", func(w ipc.Window) bool { return w.SwallowedBy != 0 }, "term")

			child.stop()
			h.Eventually("the viewer to close", func(_ *headless.Backend, comp *compositor.Compositor) bool {
				_, ok := windowNamed(comp, "viewer")
				return !ok && !comp.Holding()
			})
			c.Sync()
			back, _ := windowTitled(h, "term")
			if back.Rect != tile.Rect || back.SwallowedBy != 0 || !back.Focused {
				t.Fatalf("the terminal came back as %+v, want %+v focused", back, tile.Rect)
			}
		})
	}
}
