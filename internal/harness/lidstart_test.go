package harness

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func lidClosed(c *compositor.Config) { c.LidClosedAtStart = true }

func startedWithOutputs(t *testing.T, configure func(*compositor.Config), outputs ...string) *Harness {
	t.Helper()
	h := StartWith(t, configure)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		for _, name := range outputs {
			if _, err := b.AddNamedOutput(name, small); err != nil {
				return err
			}
		}
		return nil
	})
	return h
}

func expectLit(t *testing.T, h *Harness, when string, want map[string]bool) {
	t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		for _, o := range b.Outputs() {
			if lit, ok := want[o.Name()]; ok && o.State().Enabled() != lit {
				t.Errorf("%s: %s lit %v, want %v", when, o.Name(), !lit, lit)
			}
		}
		return nil
	})
}

func expectFullscreenOn(t *testing.T, h *Harness, name, when string) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	w := c.WindowWith("greeter", fillGreen, func(w *testclient.Window) error { return w.Toplevel.SetFullscreen(nil) })
	c.DispatchUntil(when+": the fullscreen window to map", w.Mapped)
	h.settled(c, when+": the fullscreen window focused on "+name, func(x ipc.Window) bool { return x.Fullscreen && x.Focused && x.Output == name }, "greeter")
	h.Eventually(when+": "+name+" showing the fullscreen window", showingOn(outputIndex(t, h, name), 5, 5, greenColor))
}

func TestStartedDockedWithTheLidClosedThePanelStaysDark(t *testing.T) {
	t.Parallel()
	for _, order := range [][]string{{"eDP-1", "DP-1"}, {"DP-1", "eDP-1"}} {
		t.Run(order[0]+" first", func(t *testing.T) {
			t.Parallel()
			h := startedWithOutputs(t, lidClosed, order...)
			expectLit(t, h, "started docked with the lid closed", map[string]bool{"eDP-1": false, "DP-1": true})
			if n := len(testclient.Connect(t, h.Socket()).Outputs); n != 1 {
				t.Errorf("%d outputs advertised, want the external one", n)
			}
			expectFullscreenOn(t, h, "DP-1", "started docked with the lid closed")
		})
	}
}

func TestStartedUndockedWithTheLidClosedThePanelStaysLit(t *testing.T) {
	t.Parallel()
	h := startedWithOutputs(t, lidClosed, "eDP-1")
	expectLit(t, h, "started undocked with the lid closed", map[string]bool{"eDP-1": true})
	expectFullscreenOn(t, h, "eDP-1", "started undocked with the lid closed")
}

func TestALidEventContradictingTheStartStateIsApplied(t *testing.T) {
	t.Parallel()
	h := startedWithOutputs(t, lidClosed, "eDP-1", "DP-1")
	lid := func(closed bool) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return b.Switches().Toggle(backend.SwitchLid, closed)
		})
	}
	expectLit(t, h, "started docked with the lid closed", map[string]bool{"eDP-1": false, "DP-1": true})
	lid(true)
	expectLit(t, h, "a lid close confirming the start state", map[string]bool{"eDP-1": false, "DP-1": true})
	lid(false)
	expectLit(t, h, "the lid opening after a start with it closed", map[string]bool{"eDP-1": true, "DP-1": true})
	if n := len(testclient.Connect(t, h.Socket()).Outputs); n != 2 {
		t.Errorf("%d outputs advertised after the lid opened, want both", n)
	}
}

func TestTheLidClosedPanelFollowsTheExternalOutput(t *testing.T) {
	t.Parallel()
	h := startedWithOutputs(t, lidClosed, "eDP-1")
	var external *headless.Output
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		var err error
		external, err = b.AddNamedOutput("DP-1", small)
		return err
	})
	expectLit(t, h, "plugging an external output with the lid closed", map[string]bool{"eDP-1": false, "DP-1": true})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return b.RemoveOutput(external) })
	expectLit(t, h, "unplugging the external output with the lid closed", map[string]bool{"eDP-1": true})
	expectFullscreenOn(t, h, "eDP-1", "unplugged with the lid closed")
}

func TestWithTheLidClosedAnExternalOutputConfiguredOffLeavesThePanelLit(t *testing.T) {
	t.Parallel()
	h := startedWithOutputs(t, func(c *compositor.Config) {
		lidClosed(c)
		c.Outputs = compositor.OutputRules{Named: []compositor.OutputRule{{Match: wm.OutputMatch{Name: "DP-1"}, Disabled: true}}}
	}, "eDP-1", "DP-1")
	expectLit(t, h, "started with the lid closed and the external output configured off", map[string]bool{"eDP-1": true, "DP-1": false})
	expectFullscreenOn(t, h, "eDP-1", "the external output configured off")
}
