package wm

import (
	"image"
	"math/rand/v2"
	"testing"
)

var overviewScreen = image.Rect(0, 0, 1920, 1080)

func randomWindows(rng *rand.Rand, n int) []image.Rectangle {
	out := make([]image.Rectangle, n)
	for i := range out {
		x, y := rng.IntN(1600), rng.IntN(800)
		out[i] = image.Rect(x, y, x+100+rng.IntN(1800), y+100+rng.IntN(1000))
	}
	return out
}

func TestOverviewWindowsNeverOverlapAndStayInTheirArea(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for n := 1; n <= 64; n++ {
		for range 20 {
			windows := randomWindows(rng, n)
			minimized := randomWindows(rng, rng.IntN(5))
			plan := PlanOverview(overviewScreen, 1+rng.IntN(9), windows, minimized)
			placed := append(append([]image.Rectangle{}, plan.Windows...), plan.Minimized...)
			placed = append(placed, plan.Strip...)
			placed = append(placed, plan.Plus)
			for i, a := range placed {
				if a.Empty() {
					t.Fatalf("%d windows: rectangle %d is empty", n, i)
				}
				if !a.In(overviewScreen) {
					t.Fatalf("%d windows: %v leaves the output", n, a)
				}
				for _, b := range placed[i+1:] {
					if a.Overlaps(b) {
						t.Fatalf("%d windows: %v overlaps %v", n, a, b)
					}
				}
			}
		}
	}
}

func TestOverviewKeepsAspectRatios(t *testing.T) {
	windows := []image.Rectangle{image.Rect(0, 0, 1920, 1080), image.Rect(0, 0, 600, 1000), image.Rect(100, 100, 1100, 300)}
	plan := PlanOverview(overviewScreen, 1, windows, nil)
	for i, r := range plan.Windows {
		src := windows[i]
		cross := float64(r.Dx()*src.Dy()) / float64(r.Dy()*src.Dx())
		if cross < 0.97 || cross > 1.03 {
			t.Errorf("window %d: %v from %v changes its aspect ratio by %.3f", i, r, src, cross)
		}
	}
}

func TestOverviewKeepsRelativeSizes(t *testing.T) {
	windows := []image.Rectangle{image.Rect(0, 0, 1600, 900), image.Rect(0, 0, 800, 450)}
	plan := PlanOverview(overviewScreen, 1, windows, nil)
	if got, want := plan.Windows[0].Dx(), 2*plan.Windows[1].Dx(); got < want-2 || got > want+2 {
		t.Errorf("a window twice as wide came out %d against %d", plan.Windows[0].Dx(), plan.Windows[1].Dx())
	}
}

func TestOverviewNeverEnlargesAWindow(t *testing.T) {
	windows := []image.Rectangle{image.Rect(10, 10, 210, 110)}
	plan := PlanOverview(overviewScreen, 1, windows, nil)
	if plan.Windows[0].Size() != windows[0].Size() {
		t.Errorf("a small window came out %v", plan.Windows[0])
	}
}

func TestOverviewKeepsRelativePositions(t *testing.T) {
	const half = 960
	quad := []image.Rectangle{
		image.Rect(0, 0, half, 540), image.Rect(half, 0, 1920, 540),
		image.Rect(0, 540, half, 1080), image.Rect(half, 540, 1920, 1080),
	}
	p := PlanOverview(overviewScreen, 1, quad, nil).Windows
	if p[0].Min.X >= p[1].Min.X || p[2].Min.X >= p[3].Min.X {
		t.Errorf("left windows are not left of right ones: %v", p)
	}
	if p[0].Min.Y >= p[2].Min.Y || p[1].Min.Y >= p[3].Min.Y {
		t.Errorf("top windows are not above bottom ones: %v", p)
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for n := 2; n <= 30; n++ {
		windows := randomWindows(rng, n)
		placed := PlanOverview(overviewScreen, 1, windows, nil).Windows
		for i := range windows {
			for j := range windows {
				sameRow := placed[i].Min.Y < placed[j].Max.Y && placed[j].Min.Y < placed[i].Max.Y
				if sameRow && centreOf(windows[i]).X < centreOf(windows[j]).X && placed[i].Min.X > placed[j].Min.X {
					t.Fatalf("%d windows: %v was left of %v and now is not", n, windows[i], windows[j])
				}
				if !sameRow && centreOf(windows[i]).Y < centreOf(windows[j]).Y && placed[i].Min.Y > placed[j].Min.Y {
					t.Fatalf("%d windows: %v was above %v and now is not", n, windows[i], windows[j])
				}
			}
		}
	}
}

func TestOverviewTabsInTheSameRectangleSeparate(t *testing.T) {
	tab := image.Rect(0, 0, 1920, 1080)
	p := PlanOverview(overviewScreen, 1, []image.Rectangle{tab, tab, tab}, nil).Windows
	if p[0].Overlaps(p[1]) || p[1].Overlaps(p[2]) || p[0].Overlaps(p[2]) {
		t.Errorf("tabs stay on top of each other: %v", p)
	}
}

func TestOverviewPlansTheSameTwice(t *testing.T) {
	windows := randomWindows(rand.New(rand.NewPCG(5, 6)), 20)
	a, b := PlanOverview(overviewScreen, 3, windows, nil), PlanOverview(overviewScreen, 3, windows, nil)
	for i := range a.Windows {
		if a.Windows[i] != b.Windows[i] {
			t.Fatalf("window %d: %v then %v", i, a.Windows[i], b.Windows[i])
		}
	}
}

func TestOverviewWithNothingStillHasAStrip(t *testing.T) {
	p := PlanOverview(overviewScreen, 1, nil, nil)
	if len(p.Strip) != 1 || p.Strip[0].Empty() || p.Plus.Empty() || len(p.Windows) != 0 {
		t.Errorf("empty plan: %+v", p)
	}
	if got := p.Strip[0]; got.Dx()*1080 < got.Dy()*1920-2000 || got.Dx()*1080 > got.Dy()*1920+2000 {
		t.Errorf("a thumbnail %v does not have its output's shape", got)
	}
}

func TestOverviewStripShrinksToFit(t *testing.T) {
	p := PlanOverview(image.Rect(100, 50, 900, 650), 40, nil, nil)
	if len(p.Strip) != 40 {
		t.Fatalf("got %d thumbnails", len(p.Strip))
	}
	for _, r := range p.Strip {
		if r.Empty() || !r.In(image.Rect(100, 50, 900, 650)) {
			t.Errorf("%v does not fit the strip", r)
		}
	}
}

func TestOverviewMinimizedHaveARowOfTheirOwn(t *testing.T) {
	plan := PlanOverview(overviewScreen, 1, []image.Rectangle{image.Rect(0, 0, 1920, 1080)}, []image.Rectangle{image.Rect(0, 0, 800, 600), image.Rect(0, 0, 800, 600)})
	if plan.Minimized[0].Min.Y != plan.Minimized[1].Min.Y {
		t.Errorf("minimized windows are not in one row: %v", plan.Minimized)
	}
	if plan.Windows[0].Max.Y > plan.Minimized[0].Min.Y {
		t.Errorf("window %v runs into the minimized row %v", plan.Windows[0], plan.Minimized)
	}
}

func TestOverviewNeighbourFollowsTheArrow(t *testing.T) {
	rects := []image.Rectangle{
		image.Rect(0, 0, 100, 100), image.Rect(200, 0, 300, 100),
		image.Rect(0, 200, 100, 300), image.Rect(200, 200, 300, 300),
	}
	for _, c := range []struct {
		from int
		dir  Direction
		want int
		ok   bool
	}{
		{0, Right, 1, true},
		{0, Down, 2, true},
		{3, Left, 2, true},
		{3, Up, 1, true},
		{0, Left, 0, false},
		{0, Up, 0, false},
		{3, Right, 0, false},
		{3, Down, 0, false},
	} {
		got, ok := OverviewNeighbour(rects, c.from, c.dir)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("from %d %s: got %d %v, want %d %v", c.from, c.dir, got, ok, c.want, c.ok)
		}
	}
}

func TestWorkspacePlacementsCoverHiddenTabsAndOtherWorkspaces(t *testing.T) {
	s := State{}
	s, err := s.AddOutput("A", overviewScreen, overviewScreen)
	if err != nil {
		t.Fatal(err)
	}
	at := Autotile{}
	for _, id := range []WindowID{1, 2} {
		if s, err = s.MapWindow(id, at); err != nil {
			t.Fatal(err)
		}
	}
	if s, _, err = Dispatch(s, MakeGroupAction{Layout: Tabbed}, Settings{}); err != nil {
		t.Fatal(err)
	}
	focused, _ := s.Focused()
	if s, err = s.Minimize(focused, overviewScreen); err != nil {
		t.Fatal(err)
	}
	if s, err = s.SwitchWorkspace(2); err != nil {
		t.Fatal(err)
	}
	got := s.WorkspacePlacements(1, Spacing{})
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	var minimized, shown int
	for _, p := range got {
		if p.Minimized {
			minimized++
		}
		if p.Visible {
			shown++
		}
	}
	if minimized != 1 || shown != 1 {
		t.Errorf("workspace 1, not shown, gives %+v", got)
	}
	if s.WorkspacePlacements(9, Spacing{}) != nil {
		t.Error("an unknown workspace has placements")
	}
}

func TestMoveWindowToWorkspaceTakesAnyWindow(t *testing.T) {
	s, err := State{}.AddOutput("A", overviewScreen, overviewScreen)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []WindowID{1, 2} {
		if s, err = s.MapWindow(id, Autotile{}); err != nil {
			t.Fatal(err)
		}
	}
	focused, _ := s.Focused()
	other := WindowID(1)
	if focused == other {
		other = 2
	}
	if s, err = s.MoveWindowToWorkspace(other, 3, Autotile{}); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.WorkspaceOf(other); id != 3 {
		t.Errorf("the window went to workspace %d", id)
	}
	if now, _ := s.Focused(); now != focused {
		t.Errorf("focus moved to %d", now)
	}
	if active, _ := s.ActiveOn("A"); active != 1 {
		t.Errorf("the output now shows workspace %d", active)
	}
	if _, err := s.MoveWindowToWorkspace(99, 1, Autotile{}); err == nil {
		t.Error("moved an unknown window")
	}
}
