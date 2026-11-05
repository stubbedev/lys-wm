package compositor

import (
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/region"
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
		if err := c.renderBlur(newTarget, pb, nil, colorOut{}, nil); err != nil {
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
	b := &blurCache{chain: []render.TextureTarget{countedTarget{}}, spec: spec, area: image.Rect(0, 0, 10, 10)}
	if blurStale(b, spec, b.area, nil, colorOut{}) {
		t.Error("a chain with nothing changed redraws")
	}
	if !blurStale(b, chainSpec{offset: 4, passes: 1}, b.area, nil, colorOut{}) {
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

func blurOf(t *testing.T, c *Compositor, r *soft.Renderer, pb *pendingBlur, below []drawOp, damage *region.Set) []byte {
	t.Helper()
	newTarget := func(size image.Point) (render.TextureTarget, error) { return r.NewTextureTarget(size) }
	if err := c.renderBlur(newTarget, pb, below, colorOut{plans: &c.plans}, damage); err != nil {
		t.Fatal(err)
	}
	b, ok := pb.cache.chain[0].(*soft.TextureTarget)
	if !ok {
		t.Fatalf("chain target %T", pb.cache.chain[0])
	}
	return slices.Clone(b.Pix)
}

func TestAChangeUnderABlurRefiltersOnlyWhatItsReachTouchesAndMatchesAWholeRedraw(t *testing.T) {
	r := soft.New()
	area := image.Rect(0, 0, 256, 192)
	pix := make([]byte, area.Dx()*area.Dy()*4)
	for i := range pix {
		pix[i] = byte(i*7 + i/1024)
	}
	tex, err := r.NewTexture(render.XRGB8888, area.Size(), area.Dx()*4, pix)
	if err != nil {
		t.Fatal(err)
	}
	below := []drawOp{{tex: tex, src: render.FRect{W: 256, H: 192}, dst: area, alpha: 1}}
	spec := chainSpec{offset: 1.5, passes: 2}
	c := &Compositor{renderer: r}
	partial := &pendingBlur{cache: &blurCache{}, spec: spec, area: area}
	blurOf(t, c, r, partial, below, nil)
	whole := c.tallied.filterPx

	changed := image.Rect(120, 90, 130, 96)
	band := make([]byte, changed.Dx()*changed.Dy()*4)
	for i := range band {
		band[i] = 0xff
	}
	if err := tex.Update(changed, changed.Dx()*4, band); err != nil {
		t.Fatal(err)
	}
	damage := region.Capped(maxDamageRects)
	damage.Add(changed)
	version := partial.cache.version
	before := c.tallied.filterPx
	got := blurOf(t, c, r, partial, below, &damage)
	if filtered := c.tallied.filterPx - before; filtered*4 > whole {
		t.Errorf("a %v change refiltered %d pixels, a whole redraw %d", changed.Size(), filtered, whole)
	}
	if partial.cache.version != version || !partial.cache.partial {
		t.Error("a partial redraw changed the chain's version, so its op repaints whole")
	}
	if level := partial.cache.levels[0]; len(level) != 1 || !changed.In(level[0]) || level[0] == area {
		t.Errorf("a partial redraw damages %v, want a rect around %v", level, changed)
	}
	want := blurOf(t, c, r, &pendingBlur{cache: &blurCache{}, spec: spec, area: area}, below, nil)
	if !slices.Equal(got, want) {
		t.Error("the partially redrawn blur differs from a whole redraw of the same content")
	}
}

func TestAPartialBlurRedrawAllocatesNothing(t *testing.T) {
	r := soft.New()
	area := image.Rect(0, 0, 128, 96)
	tex, err := r.NewTexture(render.XRGB8888, area.Size(), area.Dx()*4, make([]byte, area.Dx()*area.Dy()*4))
	if err != nil {
		t.Fatal(err)
	}
	c := &Compositor{renderer: r}
	pb := &pendingBlur{cache: &blurCache{}, spec: chainSpec{offset: 2, passes: 2}, area: area}
	below := []drawOp{{tex: tex, src: render.FRect{W: 128, H: 96}, dst: area, alpha: 1}}
	if err := c.renderBlur(r.NewTextureTarget, pb, below, colorOut{plans: &c.plans}, nil); err != nil {
		t.Fatal(err)
	}
	damage := region.Capped(maxDamageRects)
	damage.Add(image.Rect(40, 40, 50, 50))
	redraw := func() {
		if err := c.renderBlur(r.NewTextureTarget, pb, below, colorOut{plans: &c.plans}, &damage); err != nil {
			t.Fatal(err)
		}
	}
	redraw()
	if allocs := testing.AllocsPerRun(50, redraw); allocs != 0 {
		t.Fatalf("%v allocations per partial blur redraw", allocs)
	}
}
