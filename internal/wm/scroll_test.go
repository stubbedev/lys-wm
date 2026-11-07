package wm

import (
	"image"
	"testing"
)

var strip = image.Rect(0, 0, 1000, 600)

func scrolling(t *testing.T, ids ...WindowID) State {
	t.Helper()
	s := ok(t)(State{}.WithLayoutDefault(ScrollLayout).AddOutput("A", strip, strip))
	for _, id := range ids {
		s = ok(t)(s.MapWindow(id, Autotile{Enabled: true, MinTrigger: 400, Divisor: 3})).FollowFocus(Spacing{}, CenterNever)
	}
	return s
}

func scrollSettings() Settings {
	return Settings{Collapse: CollapseEmptySplits, Scroll: DefaultScrolling()}
}

func rectAt(t *testing.T, s State, id WindowID) image.Rectangle {
	t.Helper()
	return placementOfOK(t, s, id).Rect
}

func TestOpeningAWindowAppendsAColumnWithoutResizingTheOthers(t *testing.T) {
	s := scrolling(t, 1)
	first := rectAt(t, s, 1)
	if first.Dx() != 500 || first.Dy() != 600 {
		t.Fatalf("the first column is %v, want half the output wide", first)
	}
	s = scrolling(t, 1, 2, 3)
	if got := treeOf(t, s, 1); got != "scroll[1 2 *3]" {
		t.Fatalf("three windows gave %s", got)
	}
	for _, id := range []WindowID{1, 2, 3} {
		if r := rectAt(t, s, id); r.Size() != first.Size() {
			t.Fatalf("window %d is %v, want every column %v", id, r, first.Size())
		}
	}
	s = run(t, s, "focuswindow 1")
	s = ok(t)(s.MapWindow(4, Autotile{}))
	if got := treeOf(t, s, 1); got != "scroll[1 *4 2 3]" {
		t.Fatalf("a window opened from the first column landed in %s, want right of it", got)
	}
}

func TestOffScreenColumnsAreNotVisible(t *testing.T) {
	s := scrolling(t, 1, 2, 3)
	if s.Arrange(Spacing{}).Windows[0].Visible {
		t.Fatal("the first column is visible though the view followed the third")
	}
	for _, id := range []WindowID{2, 3} {
		if p := placementOfOK(t, s, id); !p.Visible || p.Clip != strip {
			t.Fatalf("window %d placed %+v, want visible and clipped to its output", id, p)
		}
	}
}

func TestTheViewKeepsTheFocusedColumnVisibleInEachMode(t *testing.T) {
	for _, tc := range []struct {
		mode  CenterMode
		views []int
	}{
		{CenterNever, []int{0, 0, 500, 1000, 0}},
		{CenterAlways, []int{-250, 250, 750, 1250, -250}},
		{CenterOnOverflow, []int{0, 0, 500, 1000, 0}},
	} {
		s := ok(t)(State{}.WithLayoutDefault(ScrollLayout).AddOutput("A", strip, strip))
		var views []int
		for _, id := range []WindowID{1, 2, 3, 4} {
			s = ok(t)(s.MapWindow(id, Autotile{})).FollowFocus(Spacing{}, tc.mode)
			w, _ := s.Workspace(1)
			views = append(views, w.Tree.View())
		}
		s = run(t, s, "focuswindow 1").FollowFocus(Spacing{}, tc.mode)
		w, _ := s.Workspace(1)
		views = append(views, w.Tree.View())
		for i, v := range views {
			if v != tc.views[i] {
				t.Fatalf("%s: views %v, want %v", tc.mode, views, tc.views)
			}
		}
		for _, p := range s.Arrange(Spacing{}).Windows {
			if p.ID == 1 && !p.Rect.In(strip) {
				t.Fatalf("%s: the focused column is at %v, outside the output", tc.mode, p.Rect)
			}
		}
	}
}

func TestPresetsCycleTheFocusedColumnsWidth(t *testing.T) {
	s := scrolling(t, 1)
	settings := scrollSettings()
	for _, step := range []struct {
		action string
		width  int
	}{
		{"columnwidth preset next", 667},
		{"columnwidth preset next", 333},
		{"columnwidth preset prev", 667},
		{"columnwidth -17%", 497},
		{"columnwidth 100%", 1000},
		{"columnwidth +10%", 1000},
	} {
		s = runWith(t, s, step.action, settings)
		if got := rectAt(t, s, 1).Dx(); got != step.width {
			t.Fatalf("%s gave a column %d wide, want %d", step.action, got, step.width)
		}
	}
}

func TestConsumeAndExpelMoveWindowsBetweenColumns(t *testing.T) {
	s := scrolling(t, 1, 2, 3)
	settings := scrollSettings()
	s = runWith(t, s, "consume left", settings)
	if got := treeOf(t, s, 1); got != "scroll[1 *v[2 *3]]" {
		t.Fatalf("consume left gave %s", got)
	}
	if r2, r3 := rectAt(t, s, 2), rectAt(t, s, 3); r2.Dx() != 500 || r2.Dy() != 300 || r3.Min.Y != 300 {
		t.Fatalf("the column stacks %v over %v", r2, r3)
	}
	s = runWith(t, s, "focus up", settings)
	if f, _ := s.Focused(); f != 2 {
		t.Fatalf("focus up inside the column focused %d", f)
	}
	s = runWith(t, s, "focus left", settings)
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("focus left focused %d", f)
	}
	s = runWith(t, s, "focuswindow 3", settings)
	s = runWith(t, s, "columndisplay tab", settings)
	if got := treeOf(t, s, 1); got != "scroll[1 *tab[2 *3]]" {
		t.Fatalf("columndisplay tab gave %s", got)
	}
	s = runWith(t, s, "expel", settings)
	if got := treeOf(t, s, 1); got != "scroll[1 tab[*2] *3]" {
		t.Fatalf("expel gave %s", got)
	}
	if r := rectAt(t, s, 3); r.Dx() != 500 {
		t.Fatalf("the expelled column is %v wide", r)
	}
	if consumed := runWith(t, scrolling(t, 1), "consume left", settings); treeOf(t, consumed, 1) != "scroll[*1]" {
		t.Fatal("consume with no column beside it changed the strip")
	}
}

func TestConvertingBetweenTreeAndScrollKeepsWindowsAndFocus(t *testing.T) {
	s := ok(t)(State{}.AddOutput("A", strip, strip))
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	s = run(t, s, "makegroup v")
	s = ok(t)(s.MapWindow(4, Autotile{}))
	s = run(t, s, "focuswindow 2")
	before := treeOf(t, s, 1)
	scrolled := run(t, s, "setlayout scroll")
	if got := treeOf(t, scrolled, 1); got != "scroll[1 *2 v[3 *4]]" {
		t.Fatalf("tree %s to scroll gave %s", before, got)
	}
	focusedIs(t, scrolled, "A", 2)
	back := run(t, scrolled, "setlayout tree")
	if got := treeOf(t, back, 1); got != "h[1 *2 v[3 *4]]" {
		t.Fatalf("back to a tree gave %s", got)
	}
	focusedIs(t, back, "A", 2)
	if p := placementOfOK(t, back, 1); p.Rect.Dx() != 333 {
		t.Fatalf("the round trip changed the shares: %v", p.Rect)
	}
}

func TestAScrolledViewSettlesOnAColumnEdgeAndFocusesIt(t *testing.T) {
	s := scrolling(t, 1, 2, 3, 4)
	s = run(t, s, "focuswindow 1").FollowFocus(Spacing{}, CenterNever)
	s = s.ScrollView(620)
	s = ok(t)(s.SettleView(Spacing{}))
	w, _ := s.Workspace(1)
	if w.Tree.View() != 500 {
		t.Fatalf("the view settled at %d, want the nearest column edge 500", w.Tree.View())
	}
	focusedIs(t, s, "A", 2)
	if next := s.FollowFocus(Spacing{}, CenterNever); next.Arrange(Spacing{}).Windows[0].Visible {
		t.Fatal("the column scrolled away is still visible")
	}
}

func TestScrollingTheViewChangesOnlyPositions(t *testing.T) {
	s := scrolling(t, 1, 2, 3, 4)
	before := s.Arrange(Spacing{}).Windows
	after := s.ScrollView(500).Arrange(Spacing{}).Windows
	for i := range before {
		if before[i].Rect.Size() != after[i].Rect.Size() {
			t.Fatalf("scrolling resized window %d from %v to %v", before[i].ID, before[i].Rect, after[i].Rect)
		}
	}
}

func TestWorkspacesTakeTheirLayoutFromTheRuleOrTheDefault(t *testing.T) {
	s := ok(t)(State{}.WithLayoutDefault(ScrollLayout).WithWorkspaceRules([]WorkspaceRule{{ID: 2, Layout: TreeLayout}}).AddOutput("A", strip, strip))
	s = run(t, s, "workspace 2")
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = run(t, s, "workspace 3")
	s = ok(t)(s.MapWindow(2, Autotile{}))
	if got := treeOf(t, s, 2); got != "h[*1]" {
		t.Fatalf("workspace 2 with a tree rule holds %s", got)
	}
	if got := treeOf(t, s, 3); got != "scroll[*2]" {
		t.Fatalf("workspace 3 under the scroll default holds %s", got)
	}
	data, err := s.Save()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(data)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := loaded.Workspace(3); w.Tree.Kind() != ScrollLayout {
		t.Fatal("a handover lost the scroll layout")
	}
}

func TestAFixedColumnWidthStaysInPixelsWhateverTheOutput(t *testing.T) {
	s := scrolling(t, 1, 2)
	settings := scrollSettings()
	for _, step := range []struct {
		action string
		width  int
	}{
		{"columnwidth 420px", 420},
		{"columnwidth +30px", 450},
		{"columnwidth -50px", 400},
		{"resizeactive 20 0", 420},
		{"columnwidth 5000px", 1000},
		{"columnwidth 50%", 500},
		{"columnwidth +10%", 600},
		{"columnwidth 420px", 420},
	} {
		s = runWith(t, s, step.action, settings)
		if got := rectAt(t, s, 2).Dx(); got != step.width {
			t.Fatalf("%s gave a column %d wide, want %d", step.action, got, step.width)
		}
	}
	w, _ := s.Workspace(1)
	wide := w.Tree.Arrange(image.Rect(0, 0, 2000, 600), Spacing{})
	for _, p := range wide.Windows {
		if want := map[WindowID]int{1: 1000, 2: 420}[p.ID]; p.Rect.Dx() != want {
			t.Fatalf("on a 2000 wide output window %d is %d wide, want %d", p.ID, p.Rect.Dx(), want)
		}
	}
	data, err := s.Save()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := rectAt(t, loaded, 2).Dx(); got != 420 {
		t.Fatalf("a handover made the fixed column %d wide, want 420", got)
	}
	tree := run(t, s, "setlayout tree")
	tw, _ := tree.Workspace(1)
	for _, p := range tw.Tree.Arrange(image.Rect(0, 0, 2000, 600), Spacing{}).Windows {
		if p.ID == 2 && p.Rect.Dx() <= 840 {
			t.Fatalf("as a tree the fixed column kept its pixels: %v", p.Rect)
		}
	}
}

func TestPresetsMixSharesAndPixelsInWidthOrder(t *testing.T) {
	s := scrolling(t, 1)
	settings := scrollSettings()
	settings.Scroll.Presets = []ColumnWidth{Pixels(800), Share(0.5), Pixels(300)}
	for _, step := range []struct {
		action string
		width  int
	}{
		{"columnwidth preset next", 800},
		{"columnwidth preset next", 300},
		{"columnwidth preset next", 500},
		{"columnwidth preset prev", 300},
		{"columnwidth preset prev", 800},
	} {
		s = runWith(t, s, step.action, settings)
		if got := rectAt(t, s, 1).Dx(); got != step.width {
			t.Fatalf("%s gave a column %d wide, want %d", step.action, got, step.width)
		}
	}
}

func TestColumnWidthRejectsMalformedPixels(t *testing.T) {
	for _, arg := range []string{"0px", "px", "+0px", "-px", "1.5px", "+x20px"} {
		if _, err := ParseAction("columnwidth " + arg); err == nil {
			t.Errorf("columnwidth %s parsed", arg)
		}
	}
}
