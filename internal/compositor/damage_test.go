package compositor

import (
	"bytes"
	"image"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/surface"
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

type nopPass struct {
	render.Pass
	sets int
}

func (p *nopPass) SetColor(*render.ColorTransform) { p.sets++ }

func (*nopPass) FillRect(image.Rectangle, render.Color, render.Mask) {}

func benchFrame() *frame {
	f := &frame{}
	for range 100 {
		f.FillRect(image.Rect(0, 0, 10, 10), render.Color{R: 1, A: 0xff})
	}
	return f
}

func TestAnSDRReplayNeverSetsAColor(t *testing.T) {
	p := &nopPass{}
	benchFrame().replay(p, colorOut{plans: &colorspace.Plans{}})
	if p.sets != 0 {
		t.Fatalf("%d SetColor calls on an SDR pass", p.sets)
	}
	to := colorOut{plans: &colorspace.Plans{}}
	f := benchFrame()
	allocs := testing.AllocsPerRun(100, func() { f.replay(p, to) })
	if allocs > 0 {
		t.Fatalf("an SDR replay allocates %v times", allocs)
	}
}

func BenchmarkSDRReplayOf100Ops(b *testing.B) {
	p := &nopPass{}
	to := colorOut{plans: &colorspace.Plans{}}
	f := benchFrame()
	b.ReportAllocs()
	for b.Loop() {
		f.replay(p, to)
	}
}

func TestManyDamageRectsStayAtTheCapWithoutCollapsing(t *testing.T) {
	bounds := image.Rect(0, 0, 4000, 100)
	bg := drawOp{fill: true, color: Background, dst: bounds}
	boxes := func(n int) []drawOp {
		ops := []drawOp{bg}
		for i := range n {
			ops = append(ops, drawOp{fill: true, color: render.Color{A: 0xff}, dst: image.Rect(i*20, 0, i*20+10, 10)})
		}
		return ops
	}
	for _, tc := range []struct {
		n, rects int
		area     int
	}{
		{n: maxDamageRects, rects: maxDamageRects, area: maxDamageRects * 100},
		{n: maxDamageRects + 1, rects: maxDamageRects, area: (maxDamageRects + 2) * 100},
	} {
		sc := newScene()
		sc.frameWith(bg)
		sc.diff(bounds)
		sc.presented()
		sc.frameWith(boxes(tc.n)...)
		if d := sc.diff(bounds); len(d.Rects()) != tc.rects || d.Area() != tc.area {
			t.Fatalf("%d boxes damaged %d rects of area %d, want %d of %d", tc.n, len(d.Rects()), d.Area(), tc.rects, tc.area)
		}
	}
}

func diffOf(prev, cur []drawOp, bounds image.Rectangle) *region.Set {
	sc := newScene()
	sc.prev.ops, sc.cur.ops = prev, cur
	sc.prev.cursorFrom, sc.cur.cursorFrom = len(prev), len(cur)
	sc.drawn = true
	return sc.diff(bounds)
}

func TestAnInsertedOpDamagesOnlyItself(t *testing.T) {
	bounds := image.Rect(0, 0, 3840, 2160)
	prev := []drawOp{{fill: true, color: render.Color{A: 255}, dst: bounds, opaque: true}}
	for i := range 8 {
		x := 60 + i*460
		prev = append(prev, drawOp{surface: &surface.Surface{}, size: image.Pt(400, 1800), dst: image.Rect(x, 100, x+400, 1900), alpha: 1})
	}
	box := image.Rect(70, 110, 170, 150)
	cur := slices.Insert(slices.Clone(prev), 2, drawOp{fill: true, color: render.Color{R: 10, A: 255}, dst: box, opaque: true})
	d := diffOf(prev, cur, bounds)
	if got, want := d.Area(), box.Dx()*box.Dy(); got > 2*want {
		t.Fatalf("inserting a %v op damaged %d px in %d rects, want at most %d", box, got, len(d.Rects()), 2*want)
	}
}

func draggedFloat(windows []*surface.Surface, bounds, at image.Rectangle) []drawOp {
	w := bounds.Dx() / len(windows)
	ops := []drawOp{{fill: true, color: render.Color{A: 255}, dst: bounds, opaque: true}}
	for i, s := range windows {
		ops = append(ops, drawOp{
			surface: s, size: image.Pt(w, bounds.Dy()), src: render.FRect{W: float64(w), H: float64(bounds.Dy())},
			dst: image.Rect(i*w, 0, (i+1)*w, bounds.Dy()), alpha: 1, opaque: true, transform: render.Normal,
		})
	}
	ops = append(ops, drawOp{fill: true, color: render.Color{R: 9, A: 255}, dst: at, opaque: true})
	f := frame{ops: ops, cursorFrom: len(ops)}
	f.clip()
	return slices.Clone(f.ops)
}

func TestADraggedFloatDamagesWhatItCrossedWhateverTheOpsUnderItDo(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	windows := []*surface.Surface{{}, {}, {}, {}}
	base := image.Rect(0, 200, 300, 900)
	crossed := 0
	for x := 0; x < 1600; x += 2 {
		a, b := base.Add(image.Pt(x, 0)), base.Add(image.Pt(x+2, 0))
		prev, cur := draggedFloat(windows, bounds, a), draggedFloat(windows, bounds, b)
		if len(prev) != len(cur) {
			crossed++
		}
		var truth region.Set
		truth.Add(a)
		truth.Add(b)
		d := diffOf(prev, cur, bounds)
		if d.Area() > 2*truth.Area() {
			t.Fatalf("x=%d: ops %d->%d damaged %d px in %d rects, true %d", x, len(prev), len(cur), d.Area(), len(d.Rects()), truth.Area())
		}
	}
	if crossed == 0 {
		t.Fatal("the drag never crossed the point where a window stops being worth splitting")
	}
}

func TestPartialRepaintsOfSplitOpsMatchAFullRepaint(t *testing.T) {
	bounds := image.Rect(0, 0, 640, 480)
	r := soft.New()
	for seed := range uint64(12) {
		rng := rand.New(rand.NewPCG(seed, 2))
		sc := newScene()
		size := bounds.Size()
		pixels := func() render.Texture {
			pix := make([]byte, size.X*size.Y*4)
			for i := range pix {
				pix[i] = byte(i*7 + i>>11)
				if i%4 == 3 {
					pix[i] = 0xff
				}
			}
			tex, err := r.NewTexture(render.XRGB8888, size, size.X*4, pix)
			if err != nil {
				t.Fatal(err)
			}
			return tex
		}
		under := &surface.Surface{}
		tex := pixels()
		target := soft.NewBuffer(size.X, size.Y)
		at := image.Rect(100, 100, 400, 350)
		for step := range 40 {
			at = at.Add(image.Pt(rng.IntN(120)-60, rng.IntN(120)-60))
			f := sc.begin()
			f.FillRect(bounds, Background)
			f.ops = append(f.ops, drawOp{
				tex: tex, surface: under, size: size, src: render.FRect{W: float64(size.X), H: float64(size.Y)},
				dst: bounds, alpha: 1, opaque: true, transform: render.Normal,
			})
			f.FillRect(at, render.Color{R: byte(step), G: 40, B: 90, A: 0xff})
			f.cursorFrom = len(f.ops)
			full := slices.Clone(f.ops)
			want := soft.NewBuffer(size.X, size.Y)
			p, err := r.Begin(want, render.Region{bounds})
			if err != nil {
				t.Fatal(err)
			}
			(&frame{ops: full}).replay(p, colorOut{})
			if err := p.End(); err != nil {
				t.Fatal(err)
			}
			sc.cur.clip()
			if !sc.diff(bounds).Empty() {
				p, err := r.Begin(target, render.Region(sc.repaintFor(backend.Frame{Age: 1}, bounds).Rects()))
				if err != nil {
					t.Fatal(err)
				}
				sc.cur.replay(p, colorOut{})
				if err := p.End(); err != nil {
					t.Fatal(err)
				}
				sc.presented()
			}
			if !bytes.Equal(target.Pix, want.Pix) {
				t.Fatalf("seed %d step %d: the screen differs from a full repaint", seed, step)
			}
		}
	}
}

func TestADiffOfAnInsertAllocatesNothingOnceWarm(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	windows := []*surface.Surface{{}, {}, {}, {}}
	a := draggedFloat(windows, bounds, image.Rect(100, 100, 500, 500))
	b := draggedFloat(windows, bounds, image.Rect(104, 100, 504, 500))
	sc := newScene()
	sc.prev.ops, sc.cur.ops = a, b
	sc.drawn = true
	sc.diff(bounds)
	if allocs := testing.AllocsPerRun(100, func() { sc.diff(bounds) }); allocs != 0 {
		t.Fatalf("%v allocations per diff", allocs)
	}
}
