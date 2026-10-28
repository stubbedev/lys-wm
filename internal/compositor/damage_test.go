package compositor

import (
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
)

func (sc *scene) frameWith(ops ...drawOp) {
	f := sc.begin()
	f.ops = append(f.ops, ops...)
}

func TestSceneDiffAndBufferAge(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	bg := drawOp{fill: true, color: Background, dst: bounds}
	box := func(x int) drawOp {
		return drawOp{fill: true, color: render.Color{A: 0xff}, dst: image.Rect(x, 0, x+10, 10)}
	}
	sc := newScene()
	sc.frameWith(bg, box(0))
	if d := sc.diff(bounds); d.Area() != 100*100 {
		t.Fatalf("the first frame damaged %v", d.Rects())
	}
	sc.presented()
	sc.frameWith(bg, box(0))
	if d := sc.diff(bounds); !d.Empty() {
		t.Fatalf("an identical frame damaged %v", d.Rects())
	}
	sc.begin().FillRect(image.Rect(60, 60, 70, 70), render.Color{A: 0xff})
	sc.frameWith(bg, box(0))
	if d := sc.diff(bounds); !d.Empty() {
		t.Fatalf("an abandoned frame left ops behind: %v", d.Rects())
	}
	sc.frameWith(bg, box(20))
	if d := sc.diff(bounds); d.Area() != 200 {
		t.Fatalf("moving a box damaged %v", d.Rects())
	}
	sc.presented()
	sc.frameWith(bg, box(40))
	sc.diff(bounds)
	cases := []struct {
		age  int
		area int
	}{{1, 200}, {2, 300}, {0, 100 * 100}, {maxBufferAge + 1, 100 * 100}}
	for _, c := range cases {
		if got := sc.repaintFor(backend.Frame{Age: c.age}, bounds).Area(); got != c.area {
			t.Errorf("age %d repaints %d pixels, want %d", c.age, got, c.area)
		}
	}
}

func TestSurfaceDamageScalesWithItsDestination(t *testing.T) {
	sc := newScene()
	op := drawOp{dst: image.Rect(10, 10, 30, 30), size: image.Pt(10, 10)}
	var d region.Set
	d.Add(image.Rect(2, 3, 4, 5))
	sc.addSurfaceDamage(op, &d)
	if r := sc.damage.Rects(); len(r) != 1 || r[0] != image.Rect(14, 16, 18, 20) {
		t.Fatalf("damage %v", r)
	}
}

func TestModePolicies(t *testing.T) {
	modes := []backend.Mode{
		{Size: image.Pt(1920, 1080), RefreshMHz: 60000, Preferred: true},
		{Size: image.Pt(2560, 1440), RefreshMHz: 60000},
		{Size: image.Pt(2560, 1440), RefreshMHz: 143912},
		{Size: image.Pt(1280, 720), RefreshMHz: 75000},
	}
	current := modes[3]
	for _, c := range []struct {
		policy ModePolicy
		want   backend.Mode
	}{
		{PreferredMode{}, modes[0]},
		{HighResMode{}, modes[2]},
		{ExactMode{Size: image.Pt(2560, 1440), RefreshMHz: 144000}, modes[2]},
		{ExactMode{Size: image.Pt(2560, 1440)}, modes[2]},
		{ExactMode{Size: image.Pt(2560, 1440), RefreshMHz: 59940}, modes[1]},
	} {
		if got, err := c.policy.choose(modes, current); err != nil || got != c.want {
			t.Errorf("%#v chose %+v, %v; want %+v", c.policy, got, err, c.want)
		}
	}
	if _, err := (ExactMode{Size: image.Pt(800, 600)}).choose(modes, current); err == nil {
		t.Error("a missing exact mode was chosen")
	}
	if got, _ := (PreferredMode{}).choose(modes[1:], current); got != current {
		t.Errorf("no preferred mode chose %+v, want the current one", got)
	}
}

func TestAPointFloorsToItsPixel(t *testing.T) {
	for _, c := range []struct {
		p    point
		want image.Point
	}{{point{0.5, 2.7}, image.Pt(0, 2)}, {point{-0.5, -2.7}, image.Pt(-1, -3)}, {point{-1, 3}, image.Pt(-1, 3)}} {
		if got := c.p.pixel(); got != c.want {
			t.Errorf("%+v.pixel() = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestCullingDropsOpsHiddenBehindOpaqueOnes(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	bg := drawOp{fill: true, color: render.Color{A: 0xff}, dst: bounds, opaque: true}
	wallpaper := drawOp{dst: bounds, alpha: 1, opaque: true}
	bar := drawOp{dst: image.Rect(0, 0, 100, 20), alpha: 1, opaque: true}
	translucent := drawOp{dst: image.Rect(10, 10, 30, 30), alpha: 1}
	game := drawOp{dst: bounds, alpha: 1, opaque: true}
	cursor := drawOp{dst: image.Rect(50, 50, 60, 60), alpha: 1}

	f := frame{ops: []drawOp{bg, wallpaper, bar, game, translucent, cursor}, cursorFrom: 5}
	var covered region.Set
	f.cull(&covered)
	if want := []drawOp{game, translucent, cursor}; !slices.Equal(f.ops, want) || f.cursorFrom != 2 {
		t.Fatalf("culled to %+v with the cursor from %d, want the game, what is above it and the cursor from 2", f.ops, f.cursorFrom)
	}

	faded := game
	faded.opaque = false
	f = frame{ops: []drawOp{bg, bar, faded}, cursorFrom: 3}
	f.cull(&covered)
	if want := []drawOp{bg, bar, faded}; !slices.Equal(f.ops, want) {
		t.Fatalf("a translucent cover culled %+v", f.ops)
	}

	half := drawOp{dst: image.Rect(0, 0, 50, 100), alpha: 1, opaque: true}
	f = frame{ops: []drawOp{bg, bar, half}, cursorFrom: 3}
	f.cull(&covered)
	if want := []drawOp{bg, bar, half}; !slices.Equal(f.ops, want) {
		t.Fatalf("a partial cover culled %+v", f.ops)
	}
}
