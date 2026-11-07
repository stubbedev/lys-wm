package wm

import (
	"image"
	"slices"
	"testing"
)

func groupedState(t *testing.T) State {
	t.Helper()
	s := twoOutputs(t)
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	s = run(t, s, "focuswindow 2")
	s = run(t, s, "makegroup v")
	s = run(t, s, "focuswindow 3")
	s = run(t, s, "move left")
	if got := treeOf(t, s, 1); got != "h[1 *v[2 *3]]" {
		t.Fatalf("set up %s", got)
	}
	return s
}

func TestALiftedGroupMovesToAWorkspaceWhole(t *testing.T) {
	s := run(t, groupedState(t), "focusparent")
	s = run(t, s, "movetoworkspace 5")
	if got := treeOf(t, s, 5); got != "v[2 *3]" {
		t.Fatalf("workspace 5 holds %s, want the group with its layout", got)
	}
	if got := treeOf(t, s, 1); got != "h[*1]" {
		t.Fatalf("workspace 1 kept %s", got)
	}
	focusedIs(t, s, "DP-1", 1)
	s = run(t, s, "workspace 5")
	if w, _ := s.Workspace(5); !w.Tree.lifted() {
		t.Fatal("the moved group is no longer the focus of its workspace")
	}

	s = run(t, groupedState(t), "focusparent")
	s = run(t, s, "movetoworkspace 2 follow")
	if got := treeOf(t, s, 2); got != "v[2 *3]" {
		t.Fatalf("workspace 2 holds %s", got)
	}
	focusedIs(t, s, "DP-2", 3)
}

func (t Tree) lifted() bool {
	_, ok := t.LiftedGroup()
	return ok
}

func TestALiftedGroupIsKilledFloatedAndMovedWhole(t *testing.T) {
	s := run(t, groupedState(t), "focusparent")
	a, err := ParseAction("kill")
	if err != nil {
		t.Fatal(err)
	}
	_, effects, err := Dispatch(s, a, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(effects, []Effect{CloseWindowAction{ID: 2}, CloseWindowAction{ID: 3}}) {
		t.Fatalf("kill on the group gave %v", effects)
	}

	floated := run(t, s, "togglefloating")
	if got := treeOf(t, floated, 1); got != "h[*1]" || !floated.Floats(2) || !floated.Floats(3) {
		t.Fatalf("togglefloating on the group left %s", got)
	}
	r2, _ := floated.FloatingRect(2)
	r3, _ := floated.FloatingRect(3)
	if r3 != r2.Add(image.Pt(cascade, cascade)) {
		t.Fatalf("the floated windows are at %v and %v, want them cascaded", r2, r3)
	}

	moved := run(t, s, "move left")
	if got := treeOf(t, moved, 1); got != "h[*v[2 *3] 1]" {
		t.Fatalf("moving the group left gave %s", got)
	}
	crossed := run(t, s, "move right")
	if got := treeOf(t, crossed, 2); got != "v[2 *3]" {
		t.Fatalf("moving the group off the right edge gave DP-2 %s", got)
	}
	focusedIs(t, crossed, "DP-2", 3)

	child := run(t, s, "focuschild")
	if w, _ := child.Workspace(1); w.Tree.lifted() {
		t.Fatal("focuschild kept the group focused")
	}
	if w, _ := run(t, s, "focuswindow 1").Workspace(1); w.Tree.lifted() {
		t.Fatal("focusing a window kept the group focused")
	}
}

func TestLiftAndLocksSurviveAHandover(t *testing.T) {
	s := run(t, groupedState(t), "changegroup tab")
	s = run(t, s, "locktab on")
	s = run(t, s, "focusparent")
	data, err := s.Save()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(data)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := loaded.Workspace(1)
	g, lifted := w.Tree.LiftedGroup()
	if !lifted || !g.Locked || g.untabbed != SplitV {
		t.Fatalf("after a handover lifted %t, group %+v", lifted, g)
	}
}
