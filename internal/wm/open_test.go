package wm

import (
	"slices"
	"testing"
)

func TestOpeningPlacesAWindowWhereItsRulesSay(t *testing.T) {
	must := ok(t)
	base := func() State {
		s := must(State{}.AddOutput("A", left, left))
		s = must(s.AddOutput("B", right, right))
		return must(s.MapWindow(1, Autotile{}))
	}

	s := must(base().Open(2, Opening{Workspace: 4, Silent: true}))
	if p, _ := placementOf(s, Spacing{}, 2); p.Workspace != 4 || p.Visible {
		t.Fatalf("silent: %+v", p)
	}
	if f, _ := s.Focused(); f != 1 || s.FocusedOutput() != "A" {
		t.Fatalf("silent moved focus to %d on %s", f, s.FocusedOutput())
	}
	s = must(base().Open(2, Opening{Workspace: 4}))
	if f, _ := s.Focused(); f != 2 {
		t.Fatalf("a workspace without silent did not follow: focus %d", f)
	}

	s = must(base().Open(2, Opening{Output: "B", Silent: true}))
	if p, _ := placementOf(s, Spacing{}, 2); !p.Visible || !p.Rect.In(right) {
		t.Fatalf("output B: %+v", p)
	}

	s = must(base().Open(2, Opening{NoFocus: true}))
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("no focus: focus %d", f)
	}
	if w, _ := s.Workspace(1); !slices.Equal(w.history, []WindowID{2, 1}) {
		t.Fatalf("no focus: history %v, want the new window behind the focused one", w.history)
	}

	tab := Tabbed
	s = must(base().Open(2, Opening{Group: &tab}))
	s = must(s.Open(3, Opening{Group: &tab}))
	w, _ := s.Workspace(1)
	if g, ok := w.Tree.root.Children[0].Node.(*Group); !ok || g.Layout != Tabbed || len(g.Children) != 3 || len(w.Tree.root.Children) != 1 {
		t.Fatalf("group tab: %+v", w.Tree.root)
	}
}
