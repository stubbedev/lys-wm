package harness

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func TestAFullscreenWindowStaysShownAndFocusedWhenItsOutputGoes(t *testing.T) {
	t.Parallel()
	green := [3]byte{0, 0xff, 0}
	lid := func(closed bool) func(*headless.Backend, *compositor.Compositor) error {
		return func(b *headless.Backend, _ *compositor.Compositor) error {
			return b.Switches().Toggle(backend.SwitchLid, closed)
		}
	}
	for _, tc := range []struct {
		name       string
		lose, back func(*headless.Backend, *compositor.Compositor) error
	}{
		{"the lid", lid(true), lid(false)},
		{"the panel", func(b *headless.Backend, _ *compositor.Compositor) error {
			return b.RemoveOutput(b.Outputs()[0])
		}, func(b *headless.Backend, _ *compositor.Compositor) error {
			_, err := b.AddNamedOutput("eDP-1", small)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := dockedLaptop(t)
			c := testclient.Connect(t, h.Socket())
			w := c.Window("form", fillGreen)
			c.DispatchUntil("the form to map", w.Mapped)
			request(t, c, func() error { return w.Toplevel.SetFullscreen(nil) })
			shownOn := func(when, output string) {
				t.Helper()
				h.settled(c, "the form fullscreen and focused on "+output+" "+when, func(x ipc.Window) bool {
					return x.Fullscreen && x.Focused && x.Output == output
				}, "form")
				h.Eventually("the form drawn on "+output+" "+when, showingOn(outputIndex(t, h, output), 5, 5, green))
				c.DispatchUntil("the form activated at the size of "+output+" "+when, func() bool {
					return w.Size == small.Size && slices.Contains(w.States, cxdg.XdgToplevelStateActivated)
				})
			}
			shownOn("once mapped", "eDP-1")

			h.Do(tc.lose)
			shownOn("after eDP-1 went", "DP-1")

			h.Do(tc.back)
			shownOn("after eDP-1 came back", "eDP-1")
		})
	}
}
