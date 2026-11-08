package harness

import (
	"errors"
	"image"
	"regexp"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func titled(title string, d wm.Decision) wm.Rule {
	return wm.Rule{Match: wm.Match{Title: regexp.MustCompile("^" + title + "$")}, Decision: d}
}

func ruled(rules ...wm.Rule) func(*compositor.Config) {
	return func(cfg *compositor.Config) {
		cfg.Rules = rules
		cfg.Decor.BorderWidth = 0
		cfg.WM.Gaps = wm.Gaps{}
	}
}

func focusedTitle(c *compositor.Compositor) string {
	for _, w := range windowsOf(c) {
		if w.Focused {
			return w.Title
		}
	}
	return ""
}

func TestASilentWorkspaceRuleOpensTheWindowThereWithoutFocusOrAFrameHere(t *testing.T) {
	t.Parallel()
	h := StartWith(t, ruled(titled("chat", wm.Decision{Workspace: &wm.WorkspacePlacement{ID: 4, Silent: true}})), small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("base to map", c.Window("base", fillGreen).Mapped)
	chat := c.Window("chat", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("chat to map", chat.Mapped)
	h.settled(c, "chat on workspace 4, hidden", func(w ipc.Window) bool { return w.Workspace == 4 && w.Stack == nil }, "chat")
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		if f := focusedTitle(comp); f != "base" {
			t.Errorf("focus went to %q", f)
		}
		sn := snapshot(b.Outputs()[0])
		for y := range 100 {
			for x := range 160 {
				if pixel(sn, x, y) == [3]byte{0, 0, 0xff} {
					t.Errorf("the silent window was drawn on the shown workspace at %d,%d", x, y)
					return nil
				}
			}
		}
		if chat.Size != image.Pt(160, 100) {
			t.Errorf("chat was configured to %v, want the whole of workspace 4", chat.Size)
		}
		return nil
	})
}

func TestAnOutputRuleOpensTheWindowOnThatOutput(t *testing.T) {
	t.Parallel()
	m := wm.OutputMatch{Name: "HEADLESS-2"}
	h := StartWith(t, ruled(titled("side", wm.Decision{Output: &m})), small, small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("base to map", c.Window("base", fillGreen).Mapped)
	c.DispatchUntil("side to map", c.Window("side", fillGreen).Mapped)
	h.settled(c, "side on the second output, focused", func(w ipc.Window) bool { return w.Output == "HEADLESS-2" && w.Focused && w.Stack != nil }, "side")
}

func TestAFocusFalseRuleOpensTheWindowBehindTheFocusedOne(t *testing.T) {
	t.Parallel()
	no := false
	h := StartWith(t, ruled(titled("prompt", wm.Decision{Focus: &no, Mode: wm.RuleFloat, Position: wm.Position{Kind: wm.PositionCenter}, Size: wm.Px(40, 20)})), small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("base to map", c.Window("base", fillGreen).Mapped)
	c.DispatchUntil("prompt to map", c.Window("prompt", fillGreen).Mapped)
	h.settled(c, "prompt shown, centred, unfocused", func(w ipc.Window) bool {
		return w.Stack != nil && !w.Focused && !w.Urgent && w.Rect == wmipc.Rect{X: 60, Y: 40, Width: 40, Height: 20}
	}, "prompt")
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if f := focusedTitle(comp); f != "base" {
			t.Errorf("focus went to %q", f)
		}
		return nil
	})
}

func TestAFullscreenFalseRuleRefusesARequestFromBeforeTheFirstCommit(t *testing.T) {
	t.Parallel()
	no := false
	h := StartWith(t, ruled(titled("game", wm.Decision{Fullscreen: &no})), small)
	c := testclient.Connect(t, h.Socket())
	w := c.WindowWith("game", fillGreen, func(w *testclient.Window) error { return w.Toplevel.SetFullscreen(nil) })
	c.DispatchUntil("game to map", w.Mapped)
	h.settled(c, "game tiled", func(x ipc.Window) bool { return !x.Fullscreen }, "game")
	if slices.Contains(w.States, cxdg.XdgToplevelStateFullscreen) {
		t.Fatalf("the refused window was told %v", w.States)
	}
}

func TestAGroupTabRuleOpensWindowsAsTabs(t *testing.T) {
	t.Parallel()
	tab := wm.Tabbed
	h := StartWith(t, ruled(wm.Rule{Match: wm.Match{Title: regexp.MustCompile("^doc")}, Group: &tab}), desktop)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("doc1 to map", c.Window("doc1", fillGreen).Mapped)
	c.DispatchUntil("doc2 to map", c.Window("doc2", fillGreen).Mapped)
	c.DispatchUntil("doc3 to map", c.Window("doc3", fillGreen).Mapped)
	h.Eventually("three tabs sharing one rect", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws := windowsOf(comp)
		return len(ws) == 3 && ws[0].Rect == ws[1].Rect && ws[1].Rect == ws[2].Rect && ws[0].Rect.Width == 1280
	})
}

func TestASuppressActivateRuleIgnoresActivation(t *testing.T) {
	t.Parallel()
	h := StartWith(t, ruled(titled("target", wm.Decision{Suppress: wm.SuppressActivate})), small)
	target, minter := twoWorkspaces(t, h)
	token := requestToken(t, minter.c, minter.act, minter.seat, minter.keyPress(t, h))
	target.activate(t, token)
	minter.c.Sync()
	h.expectActivation("nothing happened", 2, "minter", false, "target")
}

func TestInitialTitleAndExcludeMatchAsDocumented(t *testing.T) {
	t.Parallel()
	yes := true
	r := wm.Rule{Match: wm.Match{InitialTitle: regexp.MustCompile("^Picture"), Exclude: &wm.Match{Title: regexp.MustCompile("menu")}}, Mode: wm.RuleFloat, Pin: &yes}
	h := StartWith(t, ruled(r), small)
	c := testclient.Connect(t, h.Socket())
	c.DispatchUntil("base to map", c.Window("base", fillGreen).Mapped)
	pip := c.Window("Picture-in-Picture", fillGreen)
	c.DispatchUntil("pip to map", pip.Mapped)
	h.settled(c, "pip floats pinned", func(w ipc.Window) bool { return w.Floating && w.Pinned }, "Picture-in-Picture")
	if err := errors.Join(pip.Toplevel.SetTitle("Video"), pip.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.settled(c, "still floating after a retitle", func(w ipc.Window) bool { return w.Floating }, "Video")
	menu := c.Window("Picture menu", fillGreen)
	c.DispatchUntil("menu to map", menu.Mapped)
	h.settled(c, "the excluded window tiles", func(w ipc.Window) bool { return !w.Floating && !w.Pinned }, "Picture menu")
}

func TestAnXWindowMatchesPlacementRulesTheSameWay(t *testing.T) {
	t.Parallel()
	yes := true
	r := wm.Rule{Match: wm.Match{AppID: regexp.MustCompile("^XTest$"), XWayland: &yes}, Workspace: &wm.WorkspacePlacement{ID: 3, Silent: true}}
	h := StartXwaylandWith(t, ruled(r), func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	xc.window(image.Rect(0, 0, 40, 30), 0xff0000, false, map[string][]byte{"WM_CLASS": []byte("x\x00XTest\x00")})
	h.Eventually("the X window on workspace 3", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws := windowsOf(comp)
		return len(ws) == 1 && ws[0].Workspace == 3 && ws[0].Stack == nil
	})
}
