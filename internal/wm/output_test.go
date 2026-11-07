package wm

import (
	"errors"
	"image"
	"slices"
	"testing"
)

func run(t *testing.T, s State, text string) State {
	t.Helper()
	a, err := ParseAction(text)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := Dispatch(s, a, Settings{Collapse: CollapseEmptySplits})
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return next
}

func twoOutputs(t *testing.T) State {
	t.Helper()
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	return ok(t)(s.AddOutput("DP-2", right, right))
}

func treeOf(t *testing.T, s State, id WorkspaceID) string {
	t.Helper()
	w, found := s.Workspace(id)
	if !found {
		t.Fatalf("no workspace %d", id)
	}
	return w.Tree.String()
}

func focusedIs(t *testing.T, s State, output string, win WindowID) {
	t.Helper()
	if f, _ := s.Focused(); s.FocusedOutput() != output || f != win {
		t.Fatalf("focus on %s window %d, want %s window %d", s.FocusedOutput(), f, output, win)
	}
}

func TestMoveAtTheWorkspaceEdgeCrossesToTheNeighbouringOutput(t *testing.T) {
	s := twoOutputs(t)
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = run(t, s, "focusoutput right")
	s = ok(t)(s.MapWindow(3, Autotile{}))
	s = ok(t)(s.MapWindow(4, Autotile{}))
	s = run(t, s, "focusoutput left")

	s = run(t, s, "move right")
	if got := treeOf(t, s, 2); got != "h[*2 3 4]" {
		t.Fatalf("the right output's tree is %s, want the window at its left edge", got)
	}
	if got := treeOf(t, s, 1); got != "h[*1]" {
		t.Fatalf("the left output's tree is %s", got)
	}
	focusedIs(t, s, "DP-2", 2)

	s = run(t, s, "move left")
	if got := treeOf(t, s, 1); got != "h[1 *2]" {
		t.Fatalf("moving back left gave %s, want the window at the right edge", got)
	}
	focusedIs(t, s, "DP-1", 2)

	s = run(t, s, "move left")
	if got := treeOf(t, s, 1); got != "h[*2 1]" {
		t.Fatalf("a move inside the tree gave %s", got)
	}
	s = run(t, s, "move left")
	if got := treeOf(t, s, 1); got != "h[*2 1]" || s.FocusedOutput() != "DP-1" {
		t.Fatalf("a move off the outer edge changed %s on %s", got, s.FocusedOutput())
	}
}

func TestEnteringATreeAcrossItsSplitWrapsTheRoot(t *testing.T) {
	cases := []struct {
		name string
		tree func(t *testing.T) Tree
		d    Direction
		want string
	}{
		{"empty", func(*testing.T) Tree { return Tree{} }, Right, "h[*9]"},
		{"along", func(t *testing.T) Tree { return build(t, 1, 2) }, Left, "h[1 2 *9]"},
		{"a lone window turns", func(t *testing.T) Tree { return build(t, 1) }, Down, "v[*9 1]"},
		{"across", func(t *testing.T) Tree { return build(t, 1, 2) }, Up, "v[h[1 *2] *9]"},
		{"tabs stay a group", func(t *testing.T) Tree {
			tree := on(t)(Tree{}.Insert(1, Tabbed))
			return on(t)(tree.Insert(2, Tabbed))
		}, Right, "h[*9 tab[1 *2]]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := on(t)(c.tree(t).InsertEdge(9, c.d, left))
			if got.String() != c.want {
				t.Fatalf("entering %s gave %s, want %s", c.d, got, c.want)
			}
		})
	}
}

func TestMoveToWorkspaceFollowSwitchesOnceWithTheWindowFocused(t *testing.T) {
	s := twoOutputs(t)
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = run(t, s, "movetoworkspace 3 follow")
	if got := active(s); !slices.Equal(got, []WorkspaceID{3, 2}) {
		t.Fatalf("active %v, want 3 shown on DP-1", got)
	}
	focusedIs(t, s, "DP-1", 2)
	if got := treeOf(t, s, 1); got != "h[*1]" {
		t.Fatalf("workspace 1 kept %s", got)
	}
	s = run(t, s, "movetoworkspace 2 follow")
	focusedIs(t, s, "DP-2", 2)
	if got := active(s); !slices.Equal(got, []WorkspaceID{3, 2}) {
		t.Fatalf("active %v, want DP-1 still showing the emptied 3", got)
	}
}

func TestOutputRelativeAndEmptyWorkspaces(t *testing.T) {
	s := twoOutputs(t)
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = run(t, s, "workspace 3")
	s = ok(t)(s.MapWindow(3, Autotile{}))
	s = run(t, s, "focusoutput DP-2")
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = run(t, s, "workspace 4")
	s = ok(t)(s.MapWindow(4, Autotile{}))
	s = run(t, s, "focusoutput DP-1")
	for _, st := range []struct {
		target string
		want   []WorkspaceID
	}{
		{"m+1", []WorkspaceID{1, 4}},
		{"m+1", []WorkspaceID{3, 4}},
		{"m-2", []WorkspaceID{3, 4}},
		{"e+1", []WorkspaceID{3, 4}},
	} {
		s = run(t, s, "workspace "+st.target)
		if got := active(s); !slices.Equal(got, st.want) {
			t.Fatalf("workspace %s: active %v, want %v", st.target, got, st.want)
		}
	}
	s = run(t, s, "focusoutput DP-1")
	s = run(t, s, "movetoworkspace empty")
	if w, found := s.Workspace(5); !found || w.Output != "DP-1" || !w.Tree.Contains(3) {
		t.Fatalf("movetoworkspace empty: %v %v", found, w)
	}
	s = run(t, s, "workspace empty")
	if got := active(s); got[0] != 6 {
		t.Fatalf("workspace empty showed %v, want the lowest free number 6 on DP-1", got)
	}
}

func TestFocusAndMoveToAnOutput(t *testing.T) {
	s := twoOutputs(t)
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = run(t, s, "focusoutput right")
	focusedIs(t, s, "DP-2", 0)
	s = run(t, s, "focusoutput right")
	focusedIs(t, s, "DP-2", 0)
	s = run(t, s, "focusoutput DP-1")
	focusedIs(t, s, "DP-1", 2)

	s = run(t, s, "movetooutput DP-2")
	focusedIs(t, s, "DP-2", 2)
	if got := treeOf(t, s, 2); got != "h[*2]" {
		t.Fatalf("DP-2's workspace holds %s", got)
	}
	s = ok(t)(s.Float(2, image.Rect(2000, 100, 2400, 400)))
	s = run(t, s, "movetooutput left")
	if r, _ := s.FloatingRect(2); r != image.Rect(80, 100, 480, 400) {
		t.Fatalf("a floating window moved to %v, want its place relative to the output", r)
	}
	focusedIs(t, s, "DP-1", 2)

	for _, text := range []string{"focusoutput HDMI-9", "movetooutput HDMI-9", "moveworkspacetooutput HDMI-9", "swapworkspaces DP-1 HDMI-9"} {
		a, err := ParseAction(text)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Dispatch(s, a, Settings{}); !errors.Is(err, ErrUnknownOutput) {
			t.Errorf("%s: %v, want an unknown output", text, err)
		}
	}
}

func TestMoveWorkspaceToOutputKeepsItsTreeFloatingAndFocus(t *testing.T) {
	small := image.Rect(1920, 0, 3200, 720)
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("DP-2", small, small))
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	s = run(t, s, "makegroup v")
	s = ok(t)(s.MapFloating(4, image.Rect(1000, 800, 1800, 1050)))
	s = run(t, s, "focuswindow 2")
	tree := treeOf(t, s, 1)

	s = run(t, s, "moveworkspacetooutput right")
	w, _ := s.Workspace(1)
	if w.Output != "DP-2" || w.Tree.String() != tree {
		t.Fatalf("workspace 1 on %s with %s, want DP-2 with %s", w.Output, w.Tree, tree)
	}
	if r, _ := s.FloatingRect(4); r != image.Rect(2400, 470, 3200, 720) {
		t.Fatalf("the floating window is at %v, want it moved with the output and clamped inside it", r)
	}
	focusedIs(t, s, "DP-2", 2)
	if got := active(s); !slices.Equal(got, []WorkspaceID{3, 1}) {
		t.Fatalf("active %v, want DP-1 on a fresh workspace and DP-2 showing 1", got)
	}
	if _, kept := s.Workspace(2); kept {
		t.Fatal("the empty workspace 2 DP-2 showed survived")
	}

	s = run(t, s, "swapworkspaces DP-1 DP-2")
	if got := active(s); !slices.Equal(got, []WorkspaceID{1, 3}) {
		t.Fatalf("after the swap active %v", got)
	}
	if w, _ := s.Workspace(1); w.Output != "DP-1" {
		t.Fatalf("workspace 1 on %s after the swap", w.Output)
	}
	if r, _ := s.FloatingRect(4); !r.In(left) {
		t.Fatalf("the floating window at %v left DP-1's area", r)
	}
}
