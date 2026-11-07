package wm

import (
	"testing"
)

func placementOfOK(t *testing.T, s State, id WindowID) Placement {
	t.Helper()
	p, found := placementOf(s, Spacing{}, id)
	if !found {
		t.Fatalf("window %d has no placement", id)
	}
	return p
}

func swallowing(t *testing.T) State {
	t.Helper()
	s := ok(t)(State{}.AddOutput("A", left, left))
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	s = run(t, s, "focuswindow 2")
	tile := placementOfOK(t, s, 2).Rect
	s = ok(t)(s.Open(9, Opening{Swallow: 2}))
	if got := treeOf(t, s, 1); got != "h[1 *9 3]" {
		t.Fatalf("after swallowing the tree is %s", got)
	}
	if p := placementOfOK(t, s, 9); p.Rect != tile || !p.Visible {
		t.Fatalf("the child is at %+v, want the terminal's tile %v", p, tile)
	}
	if p := placementOfOK(t, s, 2); p.Visible || p.Laid || p.Minimized {
		t.Fatalf("the swallowed terminal is placed %+v", p)
	}
	if by, ok := s.SwallowedBy(2); !ok || by != 9 {
		t.Fatalf("swallowed by %d %t", by, ok)
	}
	return s
}

func TestAWindowSwallowsItsTerminalAndGivesItBack(t *testing.T) {
	s := swallowing(t)
	focusedIs(t, s, "A", 9)
	s = ok(t)(s.UnmapWindow(9))
	if got := treeOf(t, s, 1); got != "h[1 *2 3]" {
		t.Fatalf("closing the child left %s, want the terminal back in its slot", got)
	}
	focusedIs(t, s, "A", 2)
	if _, ok := s.SwallowedBy(2); ok {
		t.Fatal("the terminal is still swallowed")
	}

	s = ok(t)(swallowing(t).UnmapWindow(2))
	if got := treeOf(t, s, 1); got != "h[1 *9 3]" {
		t.Fatalf("closing the terminal first changed the tree to %s", got)
	}
	s = ok(t)(s.UnmapWindow(9))
	if got := treeOf(t, s, 1); got != "h[1 *3]" && got != "h[*1 3]" {
		t.Fatalf("closing the child after its terminal left %s", got)
	}
}

func TestSwallowToggleShowsBothAndHidesAgain(t *testing.T) {
	s := run(t, swallowing(t), "swallow off")
	if got := treeOf(t, s, 1); got != "h[1 *9 2 3]" {
		t.Fatalf("swallow off gave %s", got)
	}
	focusedIs(t, s, "A", 9)
	if _, ok := s.SwallowedBy(2); ok {
		t.Fatal("a shown terminal reports being swallowed")
	}
	s = run(t, s, "swallow")
	if got := treeOf(t, s, 1); got != "h[1 *9 3]" {
		t.Fatalf("swallow toggle gave %s", got)
	}
	s = ok(t)(s.UnmapWindow(9))
	if got := treeOf(t, s, 1); got != "h[1 *2 3]" {
		t.Fatalf("closing the re-swallowing child gave %s", got)
	}
}

func TestOnlyATiledUnswallowedWindowCanBeSwallowed(t *testing.T) {
	s := swallowing(t)
	if s.Swallowable(2) || s.Swallowable(9) {
		t.Fatal("a window already in a swallow pair can be swallowed")
	}
	if !s.Swallowable(1) {
		t.Fatal("a tiled window cannot be swallowed")
	}
	s = ok(t)(s.MapFloating(5, left.Inset(300)))
	if s.Swallowable(5) {
		t.Fatal("a floating window can be swallowed")
	}
	if _, err := s.Open(6, Opening{Swallow: 5}); err == nil {
		t.Fatal("opening in the place of a floating window succeeded")
	}
	if next := run(t, s, "focuswindow 2"); func() WindowID { f, _ := next.Focused(); return f }() != 9 {
		t.Fatal("focusing the hidden terminal did not focus the window in its place")
	}
}

func TestASwallowSurvivesAHandover(t *testing.T) {
	data, err := swallowing(t).Save()
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadState(data)
	if err != nil {
		t.Fatal(err)
	}
	if by, ok := s.SwallowedBy(2); !ok || by != 9 {
		t.Fatalf("after a handover swallowed by %d %t", by, ok)
	}
}
