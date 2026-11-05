package compositor

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type countedTarget struct {
	render.TextureTarget
	destroyed *int
}

func (t countedTarget) Destroy() error {
	*t.destroyed++
	return nil
}

func TestABlurNotDrawnInAFrameIsDropped(t *testing.T) {
	var destroyed int
	var bc blurCaches
	kept, dropped := new(surface.Surface), new(surface.Surface)
	for _, s := range []*surface.Surface{kept, dropped} {
		bc.of(s).chain = []render.TextureTarget{countedTarget{destroyed: &destroyed}}
	}
	bc.shared().chain = []render.TextureTarget{countedTarget{destroyed: &destroyed}}

	bc.of(kept).used = true
	bc.shared().used = true
	if err := bc.dropUnused(); err != nil {
		t.Fatal(err)
	}
	if destroyed != 1 || len(bc.bySurface) != 1 || bc.bySurface[kept] == nil || bc.xray == nil {
		t.Fatalf("after one frame: %d destroyed, caches %v, xray %v", destroyed, bc.bySurface, bc.xray)
	}

	if err := bc.dropUnused(); err != nil {
		t.Fatal(err)
	}
	if destroyed != 3 || len(bc.bySurface) != 0 || bc.xray != nil {
		t.Fatalf("after a frame drawing no blur: %d destroyed, caches %v, xray %v", destroyed, bc.bySurface, bc.xray)
	}
}

func TestABlurAreaFollowsAShapeWithoutReallocating(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 600)
	var b blurCache
	small := image.Rect(10, 10, 60, 60)
	first := b.areaFor(small, bounds)
	if first.Size() != image.Pt(textureRoundUp, textureRoundUp) || !small.In(first) {
		t.Errorf("the first draw takes area %v, want a rounded-up area holding %v", first, small)
	}
	b.chain, b.area = []render.TextureTarget{countedTarget{}}, first
	grown := image.Rect(10, 10, 60, 80)
	if got := b.areaFor(grown, bounds); got != first {
		t.Errorf("a growing shape takes area %v, want the area it fits, %v", got, first)
	}
	moved := image.Rect(400, 300, 480, 380)
	if got := b.areaFor(moved, bounds); got.Size() != first.Size() || !moved.In(got) {
		t.Errorf("a moved shape takes area %v, want one of the same size around %v", got, moved)
	}
	edge := image.Rect(950, 550, 1000, 600)
	if got := b.areaFor(edge, bounds); got.Size() != first.Size() || !edge.In(got) || !got.In(bounds) {
		t.Errorf("a shape at the edge takes area %v, want one of the same size inside the output", got)
	}
	b.area = image.Rect(0, 0, 512, 512)
	tiny := image.Rect(10, 10, 20, 20)
	if got := b.areaFor(tiny, bounds); got.Size() != image.Pt(textureRoundUp, textureRoundUp) {
		t.Errorf("a shape far smaller than the area takes area %v, want a new rounded-up one", got)
	}
}

func TestAGrowingBlurAllocatesItsChainABoundedNumberOfTimes(t *testing.T) {
	r := soft.New()
	c := &Compositor{renderer: r}
	bounds := image.Rect(0, 0, 1000, 600)
	spec := chainSpec{offset: 2, passes: 3}
	b := &blurCache{}
	newTarget := func(size image.Point) (render.TextureTarget, error) { return r.NewTextureTarget(size) }
	before := r.Counts().Targets
	frames := 0
	for w := 100; w <= 400; w += 6 {
		frames++
		pb := &pendingBlur{cache: b, spec: spec, area: b.areaFor(image.Rect(100, 100, 100+w, 100+w/2), bounds)}
		if err := c.renderBlur(newTarget, pb, nil, colorOut{}); err != nil {
			t.Fatal(err)
		}
		if len(b.chain) != spec.passes+1 {
			t.Fatalf("a chain of %d passes holds %d targets, want %d", spec.passes, len(b.chain), spec.passes+1)
		}
	}
	if got := (r.Counts().Targets - before) / uint64(spec.passes+1); got > 4 {
		t.Fatalf("a blur growing over %d frames allocated its chain %d times", frames, got)
	}
}

func TestAChainRedrawsWhenItsBlurSettingsChange(t *testing.T) {
	spec := chainSpec{offset: 2, passes: 1}
	sc := newScene()
	b := &blurCache{chain: []render.TextureTarget{countedTarget{}}, spec: spec, area: image.Rect(0, 0, 10, 10)}
	if blurChanged(sc, b, spec, b.area, nil, colorOut{}) {
		t.Error("a chain with nothing changed redraws")
	}
	if !blurChanged(sc, b, chainSpec{offset: 4, passes: 1}, b.area, nil, colorOut{}) {
		t.Error("a chain does not redraw when its size changes")
	}
}

func TestPendingOpsOfAFailedBlurAreDropped(t *testing.T) {
	var f frame
	b := &blurCache{}
	p := f.pend(b, chainSpec{}, image.Rect(0, 0, 4, 4), nil, false)
	f.blurs[p-1].failed = true
	kept := f.settle([]drawOp{{pending: p}, {fill: true}})
	if len(kept) != 1 || !kept[0].fill {
		t.Fatalf("settled to %+v, want only the fill", kept)
	}
}

func TestSharedChainsPendOnce(t *testing.T) {
	var f frame
	b := &blurCache{}
	area := image.Rect(0, 0, 4, 4)
	below := []drawOp{{fill: true, dst: area}}
	if a, c := f.pend(b, chainSpec{}, area, below, true), f.pend(b, chainSpec{}, area, below, true); a != c || len(f.blurs) != 1 {
		t.Fatalf("pended %d and %d, %d blurs", a, c, len(f.blurs))
	}
}
