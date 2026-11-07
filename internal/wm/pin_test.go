package wm

import (
	"testing"
)

func TestAPinnedWindowStaysOnScreenThroughWorkspaceSwitches(t *testing.T) {
	must := ok(t)
	s := must(State{}.AddOutput("A", left, left))
	s = must(s.AddOutput("B", right, right))
	s = must(s.MapWindow(1, Autotile{}))
	s = must(s.MapWindow(2, Autotile{}))
	s = must(s.SwitchPin(SwitchToggle))
	pip, _ := placementOf(s, Spacing{}, 2)
	if !pip.Pinned || pip.Layer != LayerFloating {
		t.Fatalf("pinning a tiled window: %+v, want it floating and pinned", pip)
	}
	s = must(s.FocusWindow(1))
	s = must(s.SwitchWorkspace(3))
	p, _ := placementOf(s, Spacing{}, 2)
	if !p.Visible || p.Rect != pip.Rect || p.Workspace != 3 {
		t.Fatalf("after a switch: %+v, want shown at %v on workspace 3", p, pip.Rect)
	}
	if f, ok := s.Focused(); ok && f == 2 {
		t.Fatal("a switch focused the pinned window it did not have focused")
	}
	s = must(s.SwitchWorkspace(1))
	if p, _ := placementOf(s, Spacing{}, 2); !p.Visible || p.Workspace != 1 {
		t.Fatalf("back on 1: %+v", p)
	}
	if other, _ := placementOf(s, Spacing{}, 1); !other.Visible {
		t.Fatal("the workspace's own window is hidden")
	}
	s = must(s.FocusWindow(2))
	s = must(s.SwitchWorkspace(4))
	if f, _ := s.Focused(); f != 2 {
		t.Fatalf("a focused pinned window lost focus over a switch to %d", f)
	}
	s = must(s.SetPinned(2, false))
	s = must(s.SwitchWorkspace(1))
	if p, _ := placementOf(s, Spacing{}, 2); p.Visible || p.Workspace != 4 || p.Pinned {
		t.Fatalf("unpinned on 4 then switched away: %+v", p)
	}
	s = must(s.SwitchWorkspace(4))
	s = must(s.SetPinned(2, true))
	s = must(s.RemoveOutput("A"))
	s = must(s.SwitchWorkspace(7))
	if p, _ := placementOf(s, Spacing{}, 2); !p.Visible || !p.Pinned || p.Workspace != 7 {
		t.Fatalf("after its output went: %+v", p)
	}
	s = must(s.MoveToWorkspace(8, Autotile{}))
	if s.Pinned(2) {
		t.Fatal("moving a pinned window to a workspace kept it pinned")
	}
}

func TestPinsSurviveSaving(t *testing.T) {
	s := ok(t)(minimizable(t).SetPinned(2, true))
	data, err := s.Save()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(data)
	if err != nil || !loaded.Pinned(2) || loaded.Pinned(1) {
		t.Fatalf("after a save: %t %t, %v", loaded.Pinned(2), loaded.Pinned(1), err)
	}
}
