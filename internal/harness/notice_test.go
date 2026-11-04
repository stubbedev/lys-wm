package harness

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestAConfigProblemShowsOnScreenUntilItIsDismissedOrFixed(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("under", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	right := small.Size.X - 5
	blue, red := [3]byte{0, 0, 0xff}, [3]byte{0xb0, 0x20, 0x20}
	h.Eventually("the window", showing(right, 5, blue))

	show := func(p compositor.ConfigProblems) {
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			comp.ShowConfigProblems(p)
			return nil
		})
	}
	show(compositor.ConfigProblems{Rejected: true, Messages: []string{"c.yaml:1: gpu: bad"}})
	h.Eventually("the notice's first line over the window", showing(right, 5, red))
	h.Eventually("the notice's message under it", showing(right, 30, red))
	h.Eventually("the window under the notice", showing(right, 60, blue))

	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		return comp.Dispatch(wm.DismissConfigNoticeAction{})
	})
	h.Eventually("the dismissed notice gone", showing(right, 5, blue))

	show(compositor.ConfigProblems{Messages: []string{"c.yaml:1: colour: unknown key"}})
	h.Eventually("a new problem showing again", showing(right, 5, red))

	show(compositor.ConfigProblems{})
	h.Eventually("the notice gone after a clean reload", showing(right, 5, blue))
}
