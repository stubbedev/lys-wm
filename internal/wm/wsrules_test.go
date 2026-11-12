package wm

import (
	"image"
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

func TestLookOfIsTheLookAWindowIsPlacedWith(t *testing.T) {
	must := ok(t)
	width := 7
	s := State{}.WithWorkspaceRules([]WorkspaceRule{{ID: 2, Select: SelectID, Look: WorkspaceLook{BorderWidth: &width}}})
	s = must(s.AddOutput("A", left, left))
	s = must(s.MapWindow(1, Autotile{}))
	s = must(s.SwitchWorkspace(2))
	s = must(s.MapWindow(2, Autotile{}))
	s = must(s.MapFloating(3, image.Rect(10, 10, 110, 110)))
	s = must(s.MapFloating(4, image.Rect(20, 20, 120, 120)))
	s = must(s.Minimize(4, image.Rectangle{}))
	s = must(s.MapWindow(5, Autotile{}))
	s = must(s.MoveWindowToScratchpad(5, "term", ScratchpadSpec{}, Autotile{}))
	headless := must(State{}.WithWorkspaceRules([]WorkspaceRule{{Select: SelectAny, Look: WorkspaceLook{BorderWidth: &width}}}).MapWindow(6, Autotile{}))
	for _, s := range []State{s, headless} {
		for _, p := range s.Arrange(Spacing{}).Windows {
			if got := s.LookOf(p.ID); got != p.Look {
				t.Errorf("window %d: LookOf gives %+v, want the %+v it is placed with", p.ID, got, p.Look)
			}
		}
	}
	if s.LookOf(3).BorderWidth != &width {
		t.Error("a floating window on workspace 2 does not take its border override")
	}
}
