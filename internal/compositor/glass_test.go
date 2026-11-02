package compositor

import (
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/render"
)

func TestARimHiddenFromTheFrameDoesNotAskForRedraws(t *testing.T) {
	seen := rim{box: render.RoundRect{Rect: image.Rect(0, 0, 10, 10)}, from: [2]float32{1, 1}, to: [2]float32{2, 2}}
	hidden := rim{box: render.RoundRect{Rect: image.Rect(20, 0, 30, 10)}, from: [2]float32{3, 3}, to: [2]float32{4, 4}}
	f := frame{rims: []rim{seen, hidden}, ops: []drawOp{{fill: true, gradient: render.Gradient{Stops: stubStops{}, From: seen.from, To: seen.to}}}}
	f.dropHiddenRims()
	if len(f.rims) != 1 || f.rims[0].box != seen.box {
		t.Fatalf("rims left: %+v", f.rims)
	}
}

type stubStops struct{ render.Texture }

func TestRimStepsFollowThePointerAroundTheBox(t *testing.T) {
	c := &Compositor{}
	box := image.Rect(0, 0, 100, 100)
	for _, tc := range []struct {
		tip  image.Point
		want int
	}{{image.Pt(200, 50), 0}, {image.Pt(50, 200), rimSteps / 4}, {image.Pt(-200, 50), rimSteps / 2}, {image.Pt(50, -200), rimSteps * 3 / 4}} {
		c.animations = animatedTable(t)
		if got := c.rimStep(tc.tip, box); got != tc.want {
			t.Errorf("a pointer at %v: step %d, want %d", tc.tip, got, tc.want)
		}
	}
	if got := (&Compositor{}).rimStep(image.Pt(200, 50), box); got != rimResting {
		t.Errorf("with animations off the light moves to step %d", got)
	}
}

func animatedTable(t *testing.T) anim.Table {
	t.Helper()
	d := time.Millisecond
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return table
}

func TestUnderReducedMotionTheLightRests(t *testing.T) {
	table, errs := anim.ResolveReduced(nil)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	c := &Compositor{animations: table}
	if got := c.rimStep(image.Pt(200, 50), image.Rect(0, 0, 100, 100)); got != rimResting {
		t.Errorf("the light moves to step %d", got)
	}
}
