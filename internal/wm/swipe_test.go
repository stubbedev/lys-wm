package wm

import (
	"image"
	"math"
	"testing"
)

func swipeState(t *testing.T, ids ...WorkspaceID) State {
	t.Helper()
	full := image.Rect(0, 0, 100, 100)
	s, err := State{}.AddOutput("A", full, full)
	if err != nil {
		t.Fatal(err)
	}
	win := WindowID(100)
	for _, id := range ids {
		if s, err = s.SwitchWorkspace(id); err != nil {
			t.Fatal(err)
		}
		if s, err = s.MapWindow(win, Autotile{}); err != nil {
			t.Fatal(err)
		}
		win++
	}
	return s
}

func TestNeighbourStaysOnTheOutputAndDoesNotWrap(t *testing.T) {
	s := swipeState(t, 1, 3, 7)
	s, err := s.AddOutput("B", image.Rect(100, 0, 200, 100), image.Rect(100, 0, 200, 100))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id      WorkspaceID
		forward bool
		want    WorkspaceID
	}{{1, true, 3}, {3, true, 7}, {7, false, 3}, {3, false, 1}, {7, true, 0}, {1, false, 0}, {5, true, 7}, {5, false, 3}} {
		got, ok := s.Neighbour("A", tc.id, tc.forward)
		if got != tc.want || ok != (tc.want != 0) {
			t.Errorf("from %d forward %v: %d %v, want %d", tc.id, tc.forward, got, ok, tc.want)
		}
	}
}

func TestAnEdgeWorkspaceIsTheNeighbourOrTheNextUnusedNumberAhead(t *testing.T) {
	s := swipeState(t, 1, 3, 4)
	for _, tc := range []struct {
		id      WorkspaceID
		forward bool
		want    WorkspaceID
	}{{1, true, 3}, {3, false, 1}, {4, true, 5}, {1, false, 0}} {
		got, ok := s.EdgeWorkspace("A", tc.id, tc.forward)
		if got != tc.want || ok != (tc.want != 0) {
			t.Errorf("from %d forward %v: %d %v, want %d", tc.id, tc.forward, got, ok, tc.want)
		}
	}
	if got, _ := swipeState(t, 1, 2).EdgeWorkspace("B", 2, true); got != 3 {
		t.Errorf("ahead of 2 with 1 and 2 taken: %d, want 3", got)
	}
}

func TestSwipeTracksLinearlyBetweenWorkspaces(t *testing.T) {
	s := swipeState(t, 1, 2, 3)
	got := s.SwipeWorkspace("A", 2, 0.25, 0.25)
	if got != (WorkspaceSwipe{From: 2, To: 3, At: 0.5}) {
		t.Fatalf("forward: %+v", got)
	}
	got = s.SwipeWorkspace("A", 2, 0, -0.25)
	if got != (WorkspaceSwipe{From: 2, To: 1, At: -0.25}) {
		t.Fatalf("back: %+v", got)
	}
	got = s.SwipeWorkspace("A", 2, 0.1, -0.3)
	if got.To != 1 || math.Abs(got.At+0.2) > 1e-9 {
		t.Fatalf("turning round past the start: %+v", got)
	}
}

func TestSwipeWalksOnPastAWholeWorkspace(t *testing.T) {
	s := swipeState(t, 1, 2, 3)
	got := s.SwipeWorkspace("A", 1, 0.9, 0.3)
	if got.From != 2 || got.To != 3 || math.Abs(got.At-0.2) > 1e-9 || got.Shift != -1 {
		t.Fatalf("forward: %+v", got)
	}
	got = s.SwipeWorkspace("A", 3, -0.9, -0.3)
	if got.From != 2 || got.To != 1 || math.Abs(got.At+0.2) > 1e-9 || got.Shift != 1 {
		t.Fatalf("back: %+v", got)
	}
}

func TestSwipeRubberBandsAtBothEnds(t *testing.T) {
	s := swipeState(t, 1, 2)
	for _, tc := range []struct {
		from WorkspaceID
		step float64
	}{{2, 0.01}, {1, -0.01}} {
		at := 0.0
		for range 500 {
			sw := s.SwipeWorkspace("A", tc.from, at, tc.step)
			if sw.To != 0 || sw.From != tc.from {
				t.Fatalf("at the end of the line: %+v", sw)
			}
			at = sw.At
		}
		if a := math.Abs(at); a >= swipeRubberBand || a < 0.15 || (at > 0) != (tc.step > 0) {
			t.Fatalf("five whole swipes against the end reach %v", at)
		}
		back := s.SwipeWorkspace("A", tc.from, at, -math.Copysign(0.01, at))
		if math.Abs(back.At) >= math.Abs(at) {
			t.Fatalf("coming back moved from %v to %v", at, back.At)
		}
	}
}

func TestSwipeOnTheOnlyWorkspaceBandsBothWays(t *testing.T) {
	s := swipeState(t, 1)
	if sw := s.SwipeWorkspace("A", 1, 0.05, -0.1); sw.To != 0 || sw.At >= 0 || sw.At < -0.1 {
		t.Fatalf("crossing the rest position: %+v", sw)
	}
}

func TestSwipeRestDecidesByDistanceAndVelocity(t *testing.T) {
	forward := WorkspaceSwipe{From: 1, To: 2}
	back := WorkspaceSwipe{From: 2, To: 1}
	for _, tc := range []struct {
		name      string
		sw        WorkspaceSwipe
		projected float64
		rest      float64
		id        WorkspaceID
	}{
		{"a slow drag short of half", forward, 0.4, 0, 1},
		{"a slow drag past half", forward, 0.6, 1, 2},
		{"exactly half", forward, 0.5, 1, 2},
		{"a flick from near the start", forward, 0.55, 1, 2},
		{"a flick back from most of the way", forward, 0.3, 0, 1},
		{"a flick the other way", forward, -3, 0, 1},
		{"backward past half", back, -0.6, -1, 1},
		{"backward short of half", back, -0.4, 0, 2},
		{"no workspace there", WorkspaceSwipe{From: 2}, 3, 0, 2},
	} {
		rest, id := tc.sw.Rest(tc.projected)
		if rest != tc.rest || id != tc.id {
			t.Errorf("%s: rest %v on %d, want %v on %d", tc.name, rest, id, tc.rest, tc.id)
		}
	}
}

func TestArrangeLaysOutAWorkspaceThatIsNotShown(t *testing.T) {
	s := swipeState(t, 1, 2)
	for _, p := range s.Arrange(Spacing{}).Windows {
		shown := p.ID == 101
		if p.Visible != shown || !p.Laid {
			t.Errorf("window %d: visible %v, laid %v; want visible %v, laid", p.ID, p.Visible, p.Laid, shown)
		}
	}
}

func TestArrangeSaysWhichWorkspaceEachWindowIsOn(t *testing.T) {
	s := swipeState(t, 1, 2)
	for _, p := range s.Arrange(Spacing{}).Windows {
		want, ok := s.WorkspaceOf(p.ID)
		if !ok || p.Workspace != want {
			t.Errorf("window %d placed on workspace %d, want %d", p.ID, p.Workspace, want)
		}
	}
}
