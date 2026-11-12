package wm

import (
	"image"
	"testing"
)

var placeGaps = Gaps{Inner: 10, Outer: Insets{Top: 5, Right: 5, Bottom: 5, Left: 5}}

func placeSettings() Settings {
	return Settings{Collapse: CollapseEmptySplits, Gaps: placeGaps, FloatStep: DefaultFloatStep}
}

func runWith(t *testing.T, s State, text string, settings Settings) State {
	t.Helper()
	a, err := ParseAction(text)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := Dispatch(s, a, settings)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return next
}

func floatingAt(t *testing.T, r image.Rectangle) State {
	t.Helper()
	s := twoOutputs(t)
	s = ok(t)(s.MapWindow(1, Autotile{}))
	return ok(t)(s.MapFloating(2, r))
}

func rectOf(t *testing.T, s State, id WindowID) image.Rectangle {
	t.Helper()
	r, floats := s.FloatingRect(id)
	if !floats {
		t.Fatalf("window %d does not float", id)
	}
	return r
}

func TestMoveStepsAFloatingWindowAndCrossesAtTheEdge(t *testing.T) {
	s := floatingAt(t, image.Rect(1700, 100, 1880, 300))
	settings := placeSettings()
	s = runWith(t, s, "move right", settings)
	if got := rectOf(t, s, 2); got != image.Rect(1740, 100, 1920, 300) {
		t.Fatalf("one step right gave %v, want it clamped to the edge", got)
	}
	s = runWith(t, s, "move right", settings)
	if got := rectOf(t, s, 2); got != image.Rect(1920, 100, 2100, 300) {
		t.Fatalf("a step from the edge gave %v, want it entering DP-2 at its left edge", got)
	}
	focusedIs(t, s, "DP-2", 2)
	s = runWith(t, s, "move down", settings)
	if got := rectOf(t, s, 2); got != image.Rect(1920, 140, 2100, 340) {
		t.Fatalf("a step down gave %v", got)
	}
	s = runWith(t, s, "move left", settings)
	if got := rectOf(t, s, 2); got != image.Rect(1740, 140, 1920, 340) {
		t.Fatalf("crossing back gave %v, want it at DP-1's right edge", got)
	}
	focusedIs(t, s, "DP-1", 2)
}

func TestEachPlaceMatchesItsEdgeDrop(t *testing.T) {
	area := left
	for _, tc := range []struct {
		place Placing
		at    image.Point
	}{
		{PlaceLeft, image.Pt(2, 500)},
		{PlaceRight, image.Pt(1918, 500)},
		{PlaceBottom, image.Pt(900, 1078)},
		{PlaceTopLeft, image.Pt(2, 2)},
		{PlaceTopRight, image.Pt(1918, 2)},
		{PlaceBottomLeft, image.Pt(2, 1078)},
		{PlaceBottomRight, image.Pt(1918, 1078)},
		{PlaceFill, image.Pt(900, 2)},
	} {
		s := floatingAt(t, image.Rect(100, 100, 300, 300))
		want, snaps := s.SnapTarget(2, area, tc.at, 16, placeGaps, SharedEdges{})
		if !snaps {
			t.Fatalf("no drop snap at %v", tc.at)
		}
		got := rectOf(t, ok(t)(s.Place(2, tc.place, placeSettings())), 2)
		if got != want {
			t.Errorf("place %s gave %v, the drop at %v %v", tc.place, got, tc.at, want)
		}
	}
	s := floatingAt(t, image.Rect(100, 100, 300, 300))
	if got := rectOf(t, ok(t)(s.Place(2, PlaceTop, placeSettings())), 2); got != image.Rect(5, 5, 1915, 535) {
		t.Errorf("place top gave %v", got)
	}
	if got := rectOf(t, ok(t)(s.Place(2, PlaceCenter, placeSettings())), 2); got != image.Rect(860, 440, 1060, 640) {
		t.Errorf("place center gave %v", got)
	}
}

func TestPlaceRestoreReturnsToTheRectBeforeThePlaceOrSnap(t *testing.T) {
	from := image.Rect(100, 100, 300, 300)
	s := floatingAt(t, from)
	settings := placeSettings()
	s = runWith(t, s, "place right", settings)
	s = runWith(t, s, "place restore", settings)
	if got := rectOf(t, s, 2); got != from {
		t.Fatalf("restore after a place gave %v, want %v", got, from)
	}
	if again := runWith(t, s, "place restore", settings); rectOf(t, again, 2) != from {
		t.Fatal("a second restore moved the window")
	}

	snapped := ok(t)(s.SnapFloating(2, image.Rect(5, 5, 955, 1075), image.Rect(400, 400, 600, 600)))
	if got := rectOf(t, runWith(t, snapped, "place restore", settings), 2); got != image.Rect(400, 400, 600, 600) {
		t.Fatalf("restore after a drag snap gave %v", got)
	}

	tiled := runWith(t, s, "focuswindow 1", settings)
	tiled = runWith(t, tiled, "place left", settings)
	if !tiled.Floats(1) {
		t.Fatal("place left did not float the tiled window")
	}
	tiled = runWith(t, tiled, "place restore", settings)
	if tiled.Floats(1) || treeOf(t, tiled, 1) != "h[*1]" {
		t.Fatalf("restore of a window placed from its tile left %s", treeOf(t, tiled, 1))
	}
}

func TestADraggedWindowSnapsFlushToItsNeighbours(t *testing.T) {
	other := image.Rect(500, 500, 700, 700)
	for _, tc := range []struct {
		name    string
		r, want image.Rectangle
	}{
		{"beside on the right", image.Rect(712, 520, 812, 620), image.Rect(710, 520, 810, 620)},
		{"beside on the left", image.Rect(385, 500, 485, 600), image.Rect(390, 500, 490, 600)},
		{"below and aligned", image.Rect(503, 715, 603, 815), image.Rect(500, 710, 600, 810)},
		{"too far to snap", image.Rect(740, 520, 840, 620), image.Rect(740, 520, 840, 620)},
		{"not across from it", image.Rect(712, 900, 812, 1000), image.Rect(712, 900, 812, 1000)},
	} {
		if got := SnapToWindows(tc.r, []image.Rectangle{other}, 16, 10); got != tc.want {
			t.Errorf("%s: %v snapped to %v, want %v", tc.name, tc.r, got, tc.want)
		}
	}
}

func TestSnapCandidatesReuseTheirBuffer(t *testing.T) {
	s := floatingAt(t, image.Rect(100, 100, 300, 300))
	s = ok(t)(s.MapFloating(3, image.Rect(500, 500, 700, 700)))
	s = ok(t)(s.MapFloating(4, image.Rect(900, 500, 1000, 700)))
	buf := s.FloatingNeighbours(2, nil)
	if len(buf) != 2 {
		t.Fatalf("candidates %v", buf)
	}
	if n := testing.AllocsPerRun(100, func() {
		buf = s.FloatingNeighbours(2, buf[:0])
		_ = SnapToWindows(image.Rect(712, 520, 812, 620), buf, 16, 10)
	}); n != 0 {
		t.Fatalf("collecting and snapping allocated %v times per motion", n)
	}
}

func TestFocusFloatingSwitchesLayers(t *testing.T) {
	s := floatingAt(t, image.Rect(100, 100, 300, 300))
	settings := placeSettings()
	s = runWith(t, s, "focusfloating", settings)
	focusedIs(t, s, "DP-1", 1)
	s = runWith(t, s, "focusfloating", settings)
	focusedIs(t, s, "DP-1", 2)
	s = runWith(t, s, "focusfloating on", settings)
	focusedIs(t, s, "DP-1", 2)
	s = runWith(t, s, "focusfloating off", settings)
	focusedIs(t, s, "DP-1", 1)
}

func TestAMinimizedFloatingWindowKeepsItsFloatingRectAndIsNotPlaced(t *testing.T) {
	r := image.Rect(100, 100, 400, 300)
	s := ok(t)(floatingAt(t, r).Minimize(2, image.Rectangle{}))
	if got := rectOf(t, s, 2); got != r {
		t.Fatalf("the minimized window floats at %v, want the %v it had", got, r)
	}
	next, err := s.Place(2, PlaceLeft, placeSettings())
	if err != nil {
		t.Fatal(err)
	}
	if got := rectOf(t, next, 2); got != r {
		t.Errorf("placing the minimized window moved it to %v, want it left at %v", got, r)
	}
	if _, floats := ok(t)(s.Minimize(1, image.Rectangle{})).FloatingRect(1); floats {
		t.Error("a minimized tiled window has a floating rect")
	}
}
