package wm

import (
	"image"
	"testing"
)

func layerOf(s State, id WindowID) (PlacementLayer, image.Rectangle, bool) {
	for _, p := range s.Arrange(Spacing{}).Windows {
		if p.ID == id {
			return p.Layer, p.Rect, p.Visible
		}
	}
	return 0, image.Rectangle{}, false
}

func TestFloatingAndFullscreen(t *testing.T) {
	full := image.Rect(0, 0, 1000, 600)
	area := image.Rect(0, 30, 1000, 600)
	s := ok(t)(State{}.AddOutput("DP-1", full, area))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = ok(t)(s.ToggleFloating(Autotile{}))
	if layer, rect, visible := layerOf(s, 2); layer != LayerFloating || rect != image.Rect(250, 172, 750, 457) || !visible {
		t.Fatalf("floated window 2: layer %d at %v visible %t", layer, rect, visible)
	}
	if _, rect, _ := layerOf(s, 1); rect != area {
		t.Fatalf("the tiled window did not take the freed space: %v", rect)
	}
	s = ok(t)(s.FocusWindow(1))
	s = ok(t)(s.FocusDirection(Right))
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("tiled focus wandered to %d; hy3 keeps it among tiled windows", f)
	}
	s = ok(t)(s.FocusWindow(2))
	s = ok(t)(s.FocusDirection(Left))
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("focus left from a floating window went to %d", f)
	}
	s = ok(t)(s.MapFloating(3, image.Rect(10, 40, 110, 140)))
	if f, _ := s.Focused(); f != 3 {
		t.Fatalf("a new floating window is not focused: %d", f)
	}
	s = ok(t)(s.SwitchMode(ModeFullscreen, SwitchToggle))
	if layer, rect, visible := layerOf(s, 3); layer != LayerFullscreen || rect != full || !visible {
		t.Fatalf("fullscreen window 3: layer %d at %v visible %t", layer, rect, visible)
	}
	if _, _, visible := layerOf(s, 1); visible {
		t.Fatal("a window under a fullscreen one is visible")
	}
	s = ok(t)(s.UnmapWindow(3))
	if _, _, visible := layerOf(s, 1); !visible {
		t.Fatal("closing the fullscreen window did not uncover the others")
	}
	s = ok(t)(s.FocusWindow(2))
	s = ok(t)(s.ToggleFloating(Autotile{}))
	if layer, _, _ := layerOf(s, 2); layer != LayerTiled {
		t.Fatal("toggling a floating window did not tile it again")
	}
}

func TestDropAndResizeTiled(t *testing.T) {
	area := image.Rect(0, 0, 900, 600)
	s := ok(t)(State{}.AddOutput("DP-1", area, area))
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	s = ok(t)(s.Reinsert(3, image.Pt(50, 300), Autotile{}))
	if got := s.workspace(1).Tree.String(); got != "h[*3 1 2]" {
		t.Fatalf("dropping 3 on the left half of 1: %s", got)
	}
	s = ok(t)(s.Reinsert(3, image.Pt(800, 300), Autotile{}))
	if got := s.workspace(1).Tree.String(); got != "h[1 2 *3]" {
		t.Fatalf("dropping 3 on the right half of 2: %s", got)
	}
	s = ok(t)(s.ResizeTiled(1, Right, 150))
	if _, rect, _ := layerOf(s, 1); rect != image.Rect(0, 0, 450, 600) {
		t.Fatalf("growing 1 to the right by 150: %v", rect)
	}
	s = ok(t)(s.ResizeTiled(2, Left, 10000))
	if _, rect, _ := layerOf(s, 1); rect.Dx() != minTile {
		t.Fatalf("a neighbour shrank below the minimum: %v", rect)
	}
	s = ok(t)(s.ResizeTiled(1, Up, 50))
	if _, rect, _ := layerOf(s, 1); rect.Dy() != 600 {
		t.Fatal("a vertical resize without a vertical split changed the window")
	}
}

func TestResizeFocused(t *testing.T) {
	area := image.Rect(0, 0, 900, 600)
	s := ok(t)(State{}.AddOutput("DP-1", area, area))
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	s = ok(t)(s.FocusWindow(1))
	s = ok(t)(s.ResizeFocused(50, 0))
	if _, rect, _ := layerOf(s, 1); rect.Dx() != 350 {
		t.Fatalf("growing the first window by 50: %v", rect)
	}
	s = ok(t)(s.FocusWindow(3))
	s = ok(t)(s.ResizeFocused(50, 0))
	if _, rect, _ := layerOf(s, 3); rect != image.Rect(550, 0, 900, 600) {
		t.Fatalf("the last window has no right neighbour, so it grows left: %v", rect)
	}
	s = ok(t)(s.ResizeFocused(0, 40))
	if _, rect, _ := layerOf(s, 3); rect.Dy() != 600 {
		t.Fatal("a vertical resize without a vertical split changed the window")
	}
	s = ok(t)(s.ToggleFloating(Autotile{}))
	s = ok(t)(s.ResizeFocused(-10, 20))
	if _, rect, _ := layerOf(s, 3); rect.Size() != image.Pt(440, 320) {
		t.Fatalf("a floating 450x300 window resized by -10,20: %v", rect.Size())
	}
}

func TestLimitsFit(t *testing.T) {
	for _, c := range []struct {
		size, minimum, maximum, want image.Point
	}{
		{image.Pt(40, 90), image.Pt(400, 300), image.Point{}, image.Pt(400, 300)},
		{image.Pt(900, 900), image.Point{}, image.Pt(800, 0), image.Pt(800, 900)},
		{image.Pt(500, 500), image.Pt(100, 100), image.Pt(600, 600), image.Pt(500, 500)},
		{image.Point{}, image.Point{}, image.Point{}, image.Point{}},
	} {
		if got := (Limits{Min: c.minimum, Max: c.maximum}).Fit(c.size); got != c.want {
			t.Errorf("Limits{%v, %v}.Fit(%v) = %v, want %v", c.minimum, c.maximum, c.size, got, c.want)
		}
	}
}

func TestFloatingWindowsKeepTheirLimits(t *testing.T) {
	area := image.Rect(0, 0, 1000, 600)
	limits := Limits{Min: image.Pt(300, 200), Max: image.Pt(600, 400)}
	floating := func(t *testing.T) State {
		t.Helper()
		s := ok(t)(State{}.AddOutput("DP-1", area, area))
		s = ok(t)(s.MapFloating(1, image.Rect(100, 100, 500, 400)))
		return ok(t)(s.SetLimits(1, limits))
	}
	for name, c := range map[string]struct {
		change func(State) (State, error)
		want   image.Rectangle
	}{
		"shrunk by the keyboard": {func(s State) (State, error) { return s.ResizeFocused(-300, -300) }, image.Rect(100, 100, 400, 300)},
		"grown by the keyboard":  {func(s State) (State, error) { return s.ResizeFocused(500, 500) }, image.Rect(100, 100, 700, 500)},
		"dragged from the left past min": {func(s State) (State, error) {
			return s.ResizeFloating(1, image.Rect(100, 100, 500, 400), image.Pt(300, 0), EdgeLeft|EdgeBottom)
		}, image.Rect(200, 100, 500, 400)},
		"dragged from the right past max": {func(s State) (State, error) {
			return s.ResizeFloating(1, image.Rect(100, 100, 500, 400), image.Pt(400, 0), EdgeRight|EdgeBottom)
		}, image.Rect(100, 100, 700, 400)},
		"dragged from the top past min, the bottom held": {func(s State) (State, error) {
			return s.ResizeFloating(1, image.Rect(100, 100, 500, 400), image.Pt(50, 250), EdgeTop)
		}, image.Rect(100, 200, 500, 400)},
		"dragged from the top left past max, the bottom right held": {func(s State) (State, error) {
			return s.ResizeFloating(1, image.Rect(100, 100, 500, 400), image.Pt(-300, -300), EdgeTop|EdgeLeft)
		}, image.Rect(-100, 0, 500, 400)},
		"placed too small":           {func(s State) (State, error) { return s.SetFloatingRect(1, image.Rect(0, 0, 10, 10)) }, image.Rect(0, 0, 300, 200)},
		"limits that grow after map": {func(s State) (State, error) { return s.SetLimits(1, Limits{Min: image.Pt(450, 350)}) }, image.Rect(100, 100, 550, 450)},
	} {
		t.Run(name, func(t *testing.T) {
			s := ok(t)(c.change(floating(t)))
			if _, rect, _ := layerOf(s, 1); rect != c.want {
				t.Fatalf("%v, want %v", rect, c.want)
			}
		})
	}
	s := ok(t)(State{}.AddOutput("DP-1", area, area))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.SetLimits(1, Limits{Min: image.Pt(700, 500)}))
	s = ok(t)(s.ToggleFloating(Autotile{}))
	if _, rect, _ := layerOf(s, 1); rect != image.Rect(150, 50, 850, 550) {
		t.Fatalf("toggled to float with a 700x500 minimum on a 1000x600 output: %v", rect)
	}
	if _, err := s.SetLimits(9, limits); err == nil {
		t.Fatal("limits were set on an unknown window")
	}
	s = ok(t)(s.UnmapWindow(1))
	if _, kept := s.limits[1]; kept {
		t.Fatal("an unmapped window's limits were kept")
	}
}

func TestSnapRectsFromEdgesAndCorners(t *testing.T) {
	area := image.Rect(100, 50, 1100, 650)
	gaps := Gaps{Inner: 10, Outer: Insets{Top: 20, Right: 20, Bottom: 20, Left: 20}}
	cases := []struct {
		at   image.Point
		want image.Rectangle
	}{
		{image.Pt(105, 300), image.Rect(120, 70, 595, 630)},
		{image.Pt(1095, 300), image.Rect(605, 70, 1080, 630)},
		{image.Pt(600, 55), image.Rect(120, 70, 1080, 630)},
		{image.Pt(600, 645), image.Rect(120, 355, 1080, 630)},
		{image.Pt(105, 55), image.Rect(120, 70, 595, 345)},
		{image.Pt(1095, 55), image.Rect(605, 70, 1080, 345)},
		{image.Pt(105, 645), image.Rect(120, 355, 595, 630)},
		{image.Pt(1095, 645), image.Rect(605, 355, 1080, 630)},
	}
	for _, c := range cases {
		if got, snapped := SnapRect(area, c.at, 16, gaps, SharedEdges{}); !snapped || got != c.want {
			t.Errorf("at %v: %v (%v), want %v", c.at, got, snapped, c.want)
		}
	}
	for _, at := range []image.Point{image.Pt(600, 300), image.Pt(116, 66), image.Pt(50, 300), image.Pt(600, 700)} {
		if got, snapped := SnapRect(area, at, 16, gaps, SharedEdges{}); snapped {
			t.Errorf("at %v snaps to %v", at, got)
		}
	}
}

func TestAnEdgeSharedWithAnotherOutputDoesNotSnap(t *testing.T) {
	area := image.Rect(0, 0, 1000, 600)
	if got, snapped := SnapRect(area, image.Pt(995, 300), 16, Gaps{}, SharedEdges{Right: true}); snapped {
		t.Errorf("the shared right edge snaps to %v", got)
	}
	if got, snapped := SnapRect(area, image.Pt(995, 5), 16, Gaps{}, SharedEdges{Right: true}); !snapped || got != area {
		t.Errorf("the top of a shared edge: %v (%v), want the top edge's whole area", got, snapped)
	}
}

func TestASnapTargetKeepsTheWindowsLimits(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", image.Rect(0, 0, 1000, 500), image.Rect(0, 0, 1000, 500)))
	s = ok(t)(s.MapFloating(1, image.Rect(10, 10, 110, 110)))
	s = ok(t)(s.SetLimits(1, Limits{Max: image.Pt(200, 200)}))
	got, snapped := s.SnapTarget(1, image.Rect(0, 0, 1000, 500), image.Pt(2, 250), 16, Gaps{}, SharedEdges{})
	if !snapped || got != image.Rect(0, 0, 200, 200) {
		t.Errorf("a window of at most 200x200 snaps to %v (%v)", got, snapped)
	}
}

func TestADropTargetIsWhereReinsertPutsTheWindow(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", image.Rect(0, 0, 1000, 500), image.Rect(0, 0, 1000, 500)))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	at := image.Pt(900, 400)
	got, err := s.DropTarget(1, at, Autotile{}, Spacing{})
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := s.Reinsert(1, at, Autotile{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range dropped.Arrange(Spacing{}).Windows {
		if p.ID == 1 && p.Rect != got {
			t.Errorf("target %v, the drop lands on %v", got, p.Rect)
		}
	}
	if _, err := s.DropTarget(9, at, Autotile{}, Spacing{}); err == nil {
		t.Error("a target for an unknown window")
	}
}

func TestPointsInTheSameDropZoneLandInTheSamePlace(t *testing.T) {
	area := image.Rect(0, 0, 1000, 500)
	s := ok(t)(State{}.AddOutput("DP-1", area, area))
	for id := WindowID(1); id <= 5; id++ {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	zones, err := s.DropZones(3, DropZones{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[DropZone]image.Rectangle{}
	for x := 0; x < area.Dx(); x += 13 {
		for y := 0; y < area.Dy(); y += 11 {
			at := image.Pt(x, y)
			got, err := s.DropTarget(3, at, Autotile{}, Spacing{})
			if err != nil {
				t.Fatal(err)
			}
			zone := zones.At(at)
			if want, known := seen[zone]; known && want != got {
				t.Fatalf("zone %+v at %v lands on %v, earlier on %v", zone, at, got, want)
			}
			seen[zone] = got
		}
	}
	if len(seen) < 4 {
		t.Errorf("only %d zones across the output", len(seen))
	}
}

func TestAFloatingResizeMovesOnlyTheGrabbedEdges(t *testing.T) {
	area := image.Rect(0, 0, 1000, 600)
	from := image.Rect(100, 100, 500, 400)
	s := ok(t)(State{}.AddOutput("DP-1", area, area))
	s = ok(t)(s.MapFloating(1, from))
	d := image.Pt(30, 20)
	for edges, want := range map[Edges]image.Rectangle{
		EdgeTop:                image.Rect(100, 120, 500, 400),
		EdgeBottom:             image.Rect(100, 100, 500, 420),
		EdgeLeft:               image.Rect(130, 100, 500, 400),
		EdgeRight:              image.Rect(100, 100, 530, 400),
		EdgeTop | EdgeLeft:     image.Rect(130, 120, 500, 400),
		EdgeTop | EdgeRight:    image.Rect(100, 120, 530, 400),
		EdgeBottom | EdgeLeft:  image.Rect(130, 100, 500, 420),
		EdgeBottom | EdgeRight: image.Rect(100, 100, 530, 420),
	} {
		if _, rect, _ := layerOf(ok(t)(s.ResizeFloating(1, from, d, edges)), 1); rect != want {
			t.Errorf("edges %04b dragged by %v: %v, want %v", edges, d, rect, want)
		}
	}
}
