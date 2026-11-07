package harness

import (
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestShowDesktopSlidesWindowsAsideWithoutConfiguringThem(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.BorderWidth = 0
		cfg.WM.Gaps = wm.Gaps{}
	}, desktop)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	blue, green, red := [3]byte{0, 0, 0xff}, [3]byte{0, 0xff, 0}, [3]byte{0xff, 0, 0}
	wall := c.Layer(testclient.LayerSpec{
		Layer:         cwlr.ZwlrLayerShellV1LayerBackground,
		Anchor:        cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight,
		ExclusiveZone: -1,
	}, testclient.Fill(0xff, 0, 0))
	in.Name(wall.Surface, "wall")
	c.DispatchUntil("the wallpaper to map", wall.Mapped)
	a := c.Window("a", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("a to map", a.Mapped)
	b := c.Window("b", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("b to map", b.Mapped)
	h.Eventually("a and b side by side", func(be *headless.Backend, comp *compositor.Compositor) bool {
		return showing(320, 300, green)(be, comp) && showing(960, 300, red)(be, comp) && !comp.Holding()
	})
	c.Sync()
	configures := a.Configures + b.Configures
	var before []ipc.Window
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		before = windowsOf(comp)
		return nil
	})

	h.dispatch("showdesktop on")
	h.Eventually("the windows slid to their edges, slivers left", func(be *headless.Backend, comp *compositor.Compositor) bool {
		return showing(320, 300, blue)(be, comp) && showing(960, 300, blue)(be, comp) && showing(10, 300, green)(be, comp) && showing(1270, 300, red)(be, comp)
	})
	c.Sync()
	if n := a.Configures + b.Configures - configures; n != 0 {
		t.Fatalf("show desktop sent %d configures, want none", n)
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if after := windowsOf(comp); !slices.EqualFunc(before, after, func(x, y ipc.Window) bool { return x.Rect == y.Rect && x.Workspace == y.Workspace }) {
			t.Errorf("the layout changed: %+v, was %+v", after, before)
		}
		if comp.Seat().Focus() != nil {
			t.Error("a window kept the keyboard while the desktop shows")
		}
		return nil
	})

	h.click(0, 640, 300, btnLeft)
	c.DispatchUntil("the wallpaper took the click", func() bool {
		return slices.ContainsFunc(in.Log, func(l string) bool { return strings.HasPrefix(l, "enter wall") }) && slices.Contains(in.Log, "button 272 1")
	})

	h.click(0, 10, 300, btnLeft)
	h.Eventually("a click on a sliver brings the windows back, focusing it", func(be *headless.Backend, comp *compositor.Compositor) bool {
		w, _ := windowNamed(comp, "a")
		return showing(320, 300, green)(be, comp) && showing(960, 300, red)(be, comp) && w.Focused && comp.Seat().Focus() != nil
	})
	h.Eventually("each window back at its exact rect", func(be *headless.Backend, comp *compositor.Compositor) bool {
		return showing(0, 799, green)(be, comp) && showing(639, 0, green)(be, comp) && showing(640, 0, red)(be, comp) && showing(1279, 799, red)(be, comp)
	})

	h.dispatch("showdesktop toggle")
	h.Eventually("the desktop shown again", showing(320, 300, blue))
	n := c.Window("new", testclient.Fill(0, 0xff, 0xff))
	c.DispatchUntil("new to map", n.Mapped)
	h.Eventually("a window opening ends it", func(be *headless.Backend, comp *compositor.Compositor) bool {
		return !showing(960, 300, blue)(be, comp) && len(comp.Snapshot().ShowDesktop) == 0
	})
}

func TestShowDesktopUnderReducedMotionIsInstant(t *testing.T) {
	t.Parallel()
	table, errs := anim.ResolveReduced(anim.DefaultMotion())
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Animations = table }, desktop)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("a to map", c.Window("a", testclient.Fill(0, 0xff, 0)).Mapped)
	h.Eventually("a settled", func(_ *headless.Backend, comp *compositor.Compositor) bool { return !comp.Animating(comp.Outputs()[0]) })
	h.dispatch("showdesktop on")
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if comp.Animating(comp.Outputs()[0]) {
			t.Error("show desktop animates under reduced motion")
		}
		return nil
	})
}
