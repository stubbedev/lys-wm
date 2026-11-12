package wm

import (
	"image"
	"slices"
	"testing"
)

var third = image.Rect(3840, 0, 5760, 1080)

type view struct {
	active  []WorkspaceID
	output  string
	focused WindowID
}

func viewOf(s State) view {
	f, _ := s.Focused()
	return view{active: active(s), output: s.FocusedOutput(), focused: f}
}

func wantView(t *testing.T, s State, when string, want view) {
	t.Helper()
	if got := viewOf(s); !slices.Equal(got.active, want.active) || got.output != want.output || got.focused != want.focused {
		t.Fatalf("%s: active %v, focus on %s window %d; want active %v, focus on %s window %d (workspaces %v)", when, got.active, got.output, got.focused, want.active, want.output, want.focused, layoutOf(s))
	}
}

func wantShown(t *testing.T, s State, when string, id WindowID, rect image.Rectangle) {
	t.Helper()
	if p, _ := placementOf(s, Spacing{}, id); !p.Visible || p.Rect != rect {
		t.Fatalf("%s: window %d placed %+v, want it shown at %v", when, id, p, rect)
	}
}

func docked(t *testing.T) State {
	t.Helper()
	must := ok(t)
	s := must(State{}.AddOutput("eDP-1", left, left))
	return must(s.AddOutput("DP-1", right, right))
}

func TestTheFocusedWorkspaceOfALostOutputTakesOverWhereItMovesAndGoesBackWithIt(t *testing.T) {
	must := ok(t)
	s := must(docked(t).FocusOutput("DP-1"))
	s = must(s.MapWindow(2, Autotile{}))
	s = must(s.SwitchWorkspace(5))
	s = must(s.MapWindow(5, Autotile{}))
	s = must(s.FocusOutput("eDP-1"))
	s = must(s.MapWindow(1, Autotile{}))
	s = must(s.SetFullscreen(1, true))

	s = must(s.RemoveOutput("eDP-1"))
	wantView(t, s, "eDP-1 lost", view{active: []WorkspaceID{1}, output: "DP-1", focused: 1})
	wantShown(t, s, "eDP-1 lost", 1, right)
	if p, _ := placementOf(s, Spacing{}, 5); p.Visible || p.Workspace != 5 {
		t.Fatalf("DP-1's own window under the moved workspace: %+v", p)
	}
	if !s.Fullscreen(1) {
		t.Fatal("the moved window left fullscreen")
	}

	s = must(s.AddOutput("eDP-1", left, left))
	wantView(t, s, "eDP-1 back", view{active: []WorkspaceID{5, 1}, output: "eDP-1", focused: 1})
	wantShown(t, s, "eDP-1 back", 1, left)
	wantShown(t, s, "eDP-1 back", 5, right)
}

func TestTheFocusedWorkspaceGoesHomeShownWhenItsOutputComesBack(t *testing.T) {
	must := ok(t)
	s := must(docked(t).MapWindow(1, Autotile{}))
	s = must(s.SwitchWorkspace(3))
	s = must(s.MapWindow(3, Autotile{}))
	s = must(s.SwitchWorkspace(1))
	s = must(s.RemoveOutput("eDP-1"))
	s = must(s.SwitchWorkspace(3))
	s = must(s.AddOutput("eDP-1", left, left))
	wantView(t, s, "eDP-1 back", view{active: []WorkspaceID{2, 3}, output: "eDP-1", focused: 3})
	wantShown(t, s, "eDP-1 back", 3, left)
}

func TestAnUnfocusedLostOutputsWorkspaceTakesOverOnlyAnOutputShowingNothing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		receiving []WindowID
		lost      view
		back      view
	}{
		{"empty", nil, view{[]WorkspaceID{1}, "DP-1", 1}, view{[]WorkspaceID{2, 1}, "eDP-1", 1}},
		{"busy", []WindowID{2}, view{[]WorkspaceID{2}, "DP-1", 2}, view{[]WorkspaceID{2, 1}, "DP-1", 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			must := ok(t)
			s := must(docked(t).MapWindow(1, Autotile{}))
			s = must(s.FocusOutput("DP-1"))
			for _, id := range tc.receiving {
				s = must(s.MapWindow(id, Autotile{}))
			}
			s = must(s.RemoveOutput("eDP-1"))
			wantView(t, s, "eDP-1 lost", tc.lost)
			if p, _ := placementOf(s, Spacing{}, 1); p.Visible != (tc.lost.focused == 1) {
				t.Fatalf("eDP-1's window: %+v", p)
			}
			s = must(s.AddOutput("eDP-1", left, left))
			wantView(t, s, "eDP-1 back", tc.back)
			wantShown(t, s, "eDP-1 back", 1, left)
		})
	}
}

func TestALostOutputShowingNothingLeavesTheReceivingOutputAsItWas(t *testing.T) {
	must := ok(t)
	s := must(docked(t).FocusOutput("DP-1"))
	s = must(s.MapWindow(2, Autotile{}))
	s = must(s.FocusOutput("eDP-1"))
	s = must(s.RemoveOutput("eDP-1"))
	wantView(t, s, "eDP-1 lost", view{active: []WorkspaceID{2}, output: "DP-1", focused: 2})
	if got := layoutOf(s); len(got) != 1 {
		t.Fatalf("workspaces %v: eDP-1's empty workspace outlived it", got)
	}
}

func TestLosingSeveralOutputsKeepsTheFocusedWorkspaceShownUntilEachComesBack(t *testing.T) {
	must := ok(t)
	s := must(docked(t).AddOutput("HDMI-1", third, third))
	for i, name := range []string{"eDP-1", "DP-1", "HDMI-1"} {
		s = must(s.FocusOutput(name))
		s = must(s.MapWindow(WindowID(i+1), Autotile{}))
	}
	s = must(s.FocusOutput("DP-1"))

	s = must(s.RemoveOutput("DP-1"))
	wantView(t, s, "DP-1 lost", view{active: []WorkspaceID{2, 3}, output: "eDP-1", focused: 2})
	s = must(s.RemoveOutput("eDP-1"))
	wantView(t, s, "eDP-1 lost too", view{active: []WorkspaceID{2}, output: "HDMI-1", focused: 2})
	wantShown(t, s, "eDP-1 lost too", 2, third)

	s = must(s.AddOutput("eDP-1", left, left))
	wantView(t, s, "eDP-1 back", view{active: []WorkspaceID{2, 1}, output: "HDMI-1", focused: 2})
	s = must(s.AddOutput("DP-1", right, right))
	wantView(t, s, "DP-1 back", view{active: []WorkspaceID{3, 1, 2}, output: "DP-1", focused: 2})
	for id, rect := range map[WindowID]image.Rectangle{1: left, 2: right, 3: third} {
		wantShown(t, s, "every output back", id, rect)
	}
}

func TestTheNextOutputAfterTheLastWasLostShowsWhatTheLastOneShowed(t *testing.T) {
	must := ok(t)
	s := must(State{}.AddOutput("eDP-1", left, left))
	s = must(s.MapWindow(1, Autotile{}))
	s = must(s.SwitchWorkspace(3))
	s = must(s.MapWindow(3, Autotile{}))
	s = must(s.RemoveOutput("eDP-1"))
	s = must(s.AddOutput("DP-1", right, right))
	wantView(t, s, "DP-1 after eDP-1", view{active: []WorkspaceID{3}, output: "DP-1", focused: 3})
	wantShown(t, s, "DP-1 after eDP-1", 3, right)
}

func TestAScratchpadShownOnALostOutputStaysShown(t *testing.T) {
	must := ok(t)
	spec := ScratchpadSpec{Size: Extent{W: Length{N: 50, Percent: true}, H: Length{N: 50, Percent: true}}}
	s := must(docked(t).FocusOutput("DP-1"))
	s = must(s.MapWindow(2, Autotile{}))
	s = must(s.FocusOutput("eDP-1"))
	s = must(s.MapWindow(1, Autotile{}))
	s = must(s.MapWindow(3, Autotile{}))
	s = must(s.MoveWindowToScratchpad(3, "term", spec, Autotile{}))
	s, _, err := s.ToggleScratchpad("term", spec)
	if err != nil {
		t.Fatal(err)
	}
	s = must(s.RemoveOutput("eDP-1"))
	wantView(t, s, "eDP-1 lost", view{active: []WorkspaceID{1}, output: "DP-1", focused: 3})
	if p, _ := placementOf(s, Spacing{}, 3); !p.Scratch {
		t.Fatalf("the scratchpad window: %+v", p)
	}
	wantShown(t, s, "eDP-1 lost", 3, image.Rect(2400, 270, 3360, 810))
}

func TestAFloatingWindowFollowsItsWorkspaceToAnotherOutputAndBack(t *testing.T) {
	must := ok(t)
	at := image.Rect(100, 100, 500, 400)
	s := must(docked(t).MapWindow(1, Autotile{}))
	s = must(s.Float(1, at))
	s = must(s.RemoveOutput("eDP-1"))
	wantShown(t, s, "eDP-1 lost", 1, at.Add(right.Min))
	s = must(s.AddOutput("eDP-1", left, left))
	wantShown(t, s, "eDP-1 back", 1, at)
}
