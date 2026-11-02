package compositor

import (
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestHoldBoundIsShortWhileDragging(t *testing.T) {
	if got := holdBound(false, DefaultHoldBound); got != DefaultHoldBound {
		t.Errorf("a key's change holds for %v, want %v", got, DefaultHoldBound)
	}
	if got := holdBound(true, DefaultHoldBound); got != dragHoldBound {
		t.Errorf("a drag holds for %v, want %v", got, dragHoldBound)
	}
	if got := holdBound(true, time.Millisecond); got != time.Millisecond {
		t.Errorf("a drag under a 1ms bound holds for %v", got)
	}
}

func TestAStalledWindowIsWaitedForOnlyOnceCaughtUpAndNotDuringADrag(t *testing.T) {
	for _, c := range []struct {
		stalled, dragging, stale, want bool
	}{
		{false, false, false, false},
		{false, true, true, false},
		{true, false, true, true},
		{true, false, false, false},
		{true, true, false, true},
		{true, true, true, true},
	} {
		if got := stillStalled(c.stalled, c.dragging, c.stale); got != c.want {
			t.Errorf("stalled %t, dragging %t, stale %t: %t, want %t", c.stalled, c.dragging, c.stale, got, c.want)
		}
	}
}

func TestSameArrangementComparesPlacementsAndBars(t *testing.T) {
	base := func() wm.Arrangement {
		return wm.Arrangement{
			Windows: []wm.Placement{{ID: 1, Rect: image.Rect(0, 0, 10, 10), Visible: true}, {ID: 2, Rect: image.Rect(10, 0, 20, 10), Visible: true}},
			TabBars: []wm.TabBar{{Rect: image.Rect(0, 0, 20, 2), Tabs: []wm.WindowID{1, 2}, Active: 0}},
		}
	}
	if !sameArrangement(base(), base()) {
		t.Fatal("an arrangement differs from its copy")
	}
	for name, change := range map[string]func(a *wm.Arrangement){
		"a moved window":  func(a *wm.Arrangement) { a.Windows[1].Rect.Min.X = 11 },
		"a hidden window": func(a *wm.Arrangement) { a.Windows[0].Visible = false },
		"a gone window":   func(a *wm.Arrangement) { a.Windows = a.Windows[:1] },
		"another tab":     func(a *wm.Arrangement) { a.TabBars[0].Active = 1 },
		"reordered tabs":  func(a *wm.Arrangement) { a.TabBars[0].Tabs[0], a.TabBars[0].Tabs[1] = 2, 1 },
		"no bar":          func(a *wm.Arrangement) { a.TabBars = nil },
	} {
		a := base()
		change(&a)
		if sameArrangement(base(), a) {
			t.Errorf("%s reads as the same arrangement", name)
		}
	}
}

func TestStretchMapsDestinationsAndLeavesTheMaskAlone(t *testing.T) {
	var f frame
	f.FillRect(image.Rect(100, 100, 150, 150), Background)
	f.FillRect(image.Rect(0, 0, 10, 10), Background)
	f.ops[1].dst = image.Rect(100, 100, 125, 125)
	f.ops[0].mask.Keep.Rect = image.Rect(100, 100, 300, 300)
	f.stretch(0, image.Rect(100, 100, 150, 150), image.Rect(100, 100, 300, 200))
	if got, want := f.ops[0].dst, image.Rect(100, 100, 300, 200); got != want {
		t.Errorf("the whole content stretched to %v, want %v", got, want)
	}
	if got, want := f.ops[1].dst, image.Rect(100, 100, 200, 150); got != want {
		t.Errorf("a part of it stretched to %v, want %v", got, want)
	}
	if got, want := f.ops[0].mask.Keep.Rect, image.Rect(100, 100, 300, 300); got != want {
		t.Errorf("the mask moved to %v, want it where the frame is, %v", got, want)
	}
}
