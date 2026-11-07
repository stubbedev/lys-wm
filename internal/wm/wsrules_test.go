package wm

import (
	"testing"
)

func TestWorkspaceRulesBindNameAndKeepWorkspaces(t *testing.T) {
	must := ok(t)
	zero := 0
	rules := []WorkspaceRule{
		{ID: 1, Output: "B", Default: true, Persistent: true},
		{ID: 9, Name: "chat", Output: "A", Persistent: true},
		{Select: SelectSingleTiled, Look: WorkspaceLook{Gaps: &Gaps{}, BorderWidth: &zero}},
	}
	s := State{}.WithWorkspaceRules(rules)
	s = must(s.AddOutput("A", left, left))
	s = must(s.AddOutput("B", right, right))
	if w, ok := s.Workspace(1); !ok || w.Output != "B" {
		t.Fatalf("workspace 1: %+v %t, want it on B", w, ok)
	}
	if id, _ := s.ActiveOn("B"); id != 1 {
		t.Fatalf("B shows %d, want its default 1", id)
	}
	if w, ok := s.Workspace(9); !ok || w.Output != "A" {
		t.Fatalf("persistent workspace 9: %+v %t", w, ok)
	}
	s = must(s.FocusOutput("A"))
	next, _, err := Dispatch(s, WorkspaceAction{Target: WorkspaceID(1)}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if next.FocusedOutput() != "B" {
		t.Fatalf("workspace 1 from A focused %s, want its output B", next.FocusedOutput())
	}
	next, _, err = Dispatch(next, WorkspaceAction{Target: WorkspaceName("chat")}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := next.ActiveOn("A"); id != 9 || next.FocusedOutput() != "A" {
		t.Fatalf("workspace chat: A shows %d, focus on %s", id, next.FocusedOutput())
	}
	next = must(next.SwitchWorkspace(4))
	if _, ok := next.Workspace(9); !ok {
		t.Fatal("a persistent workspace was dropped once hidden and empty")
	}
	if _, _, err := Dispatch(next, WorkspaceAction{Target: WorkspaceName("mail")}, Settings{}); err == nil {
		t.Fatal("an unnamed workspace name resolved")
	}

	s = must(s.RemoveOutput("B"))
	s = must(s.AddOutput("B", right, right))
	if w, _ := s.Workspace(1); w.Output != "B" {
		t.Fatalf("after B came back workspace 1 is on %s", w.Output)
	}

	s = must(s.FocusOutput("B"))
	s = must(s.SwitchWorkspace(1))
	s = must(s.MapWindow(1, Autotile{}))
	sp := Spacing{Gaps: Gaps{Inner: 4, Outer: Insets{Top: 8, Right: 8, Bottom: 8, Left: 8}}}
	p, _ := placementOf(s, sp, 1)
	if p.Rect != right || p.Look.BorderWidth == nil {
		t.Fatalf("a lone tiled window: %+v, want the whole output without gaps or border", p)
	}
	s = must(s.MapWindow(2, Autotile{}))
	if p, _ := placementOf(s, sp, 1); p.Rect.Min.X != right.Min.X+8 || p.Look.BorderWidth != nil {
		t.Fatalf("with a second window: %+v, want the gaps back", p)
	}
}
