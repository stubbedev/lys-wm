package harness

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

var groupTitles = [3]string{"a", "b", "c"}

type groupScene struct {
	*Harness
	clients []*testclient.Client
	windows []*testclient.Window
}

func startGroupScene(t *testing.T) groupScene {
	t.Helper()
	off := false
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		heldForever(cfg)
		cfg.Animations = table
		cfg.Decor = compositor.PlainDecor()
		cfg.Decor.BorderWidth = 4
		cfg.Decor.ActiveBorder = render.SolidBorder(render.Color{R: 0xff, A: 0xff})
		cfg.Decor.InactiveBorder = render.SolidBorder(render.Color{B: 0xff, A: 0xff})
	}, tall)
	s := groupScene{Harness: h}
	for _, title := range groupTitles {
		c, w := open(t, h, title, testclient.Fill(0x30, 0x30, 0x30))
		s.clients, s.windows = append(s.clients, c), append(s.windows, w)
		s.settle()
	}
	return s
}

func (s groupScene) settle() {
	s.t.Helper()
	for i, c := range s.clients {
		w := s.windows[i]
		c.DispatchUntil(groupTitles[i]+" to answer its configure", func() bool { return w.Logical == w.Size })
		c.Sync()
	}
	s.Eventually("the change to land", func(_ *headless.Backend, c *compositor.Compositor) bool { return !c.Holding() })
}

func (s groupScene) configures() [3]int {
	return [3]int{s.windows[0].Configures, s.windows[1].Configures, s.windows[2].Configures}
}

func (s groupScene) act(action string, configured [3]bool, resized bool) {
	s.t.Helper()
	before, landedBefore := s.configures(), s.holds().Landed
	s.ctl(action)
	s.settle()
	after := s.configures()
	for i := range after {
		want := 0
		if configured[i] {
			want = 1
		}
		if n := after[i] - before[i]; n != want {
			s.t.Fatalf("%s sent %s %d configures, want %d", action, groupTitles[i], n, want)
		}
	}
	want := landedBefore
	if resized {
		want++
	}
	if got := s.holds().Landed; got != want {
		s.t.Fatalf("%s landed %d transactions, want %d", action, got-landedBefore, want-landedBefore)
	}
}

func TestGroupActionsLandInOneTransactionAndAFocusedGroupHasTheActiveBorder(t *testing.T) {
	t.Parallel()
	s := startGroupScene(t)
	red, blue := [3]byte{0xff, 0, 0}, [3]byte{0, 0, 0xff}
	s.act("focus left", [3]bool{false, true, true}, false)
	s.act("makegroup v", [3]bool{}, false)
	s.act("focus right", [3]bool{false, true, true}, false)
	s.act("move left", [3]bool{true, true, true}, true)
	if a, b := s.windows[0].Size, s.windows[1].Size; a != image.Pt(72, 112) || b != image.Pt(72, 52) {
		t.Fatalf("a is %v and b %v, want a beside b over c", a, b)
	}
	s.Eventually("b inactive with a blue border", showing(157, 30, blue))

	s.act("focusparent", [3]bool{}, false)
	s.Eventually("the focused group ringed in red", showing(157, 30, red))
	if w := query[ipc.Window](s.Harness, wmipc.KindActiveWindow); w.Title != "c" {
		t.Fatalf("focusparent moved keyboard focus to %+v", w)
	}

	s.act("changegroup h", [3]bool{false, true, true}, true)
	s.act("swap left", [3]bool{}, false)
	s.Eventually("the group on the left with its ring", func(x *headless.Backend, comp *compositor.Compositor) bool {
		return showing(2, 30, red)(x, comp) && showing(157, 30, blue)(x, comp)
	})
	s.act("changegroup toggletab", [3]bool{false, true, true}, true)
	s.act("changegroup toggletab", [3]bool{false, true, true}, true)
	s.act("focuschild", [3]bool{}, false)
	s.act("resizeactive 10 0", [3]bool{true, true, true}, true)
	s.act("equalize", [3]bool{}, false)
	s.act("equalize all", [3]bool{true, true, true}, true)
	s.act("moveout", [3]bool{true, true, true}, true)
	if got := query[[]ipc.Window](s.Harness, wmipc.KindWindows); len(got) != 3 {
		t.Fatalf("windows %+v", got)
	}
}
