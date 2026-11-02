package compositor

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render"
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
	extent := image.Rect(0, 0, 200, 200)
	var b blurCache
	small := image.Rect(10, 10, 60, 60)
	if got := b.areaFor(small, extent); got != small {
		t.Errorf("the first draw takes area %v, want what it needs, %v", got, small)
	}
	b.chain, b.area = []render.TextureTarget{countedTarget{}}, small
	grown := image.Rect(10, 10, 60, 80)
	if got := b.areaFor(grown, extent); got != extent {
		t.Errorf("a growing shape takes area %v, want its surface's %v", got, extent)
	}
	b.area = extent
	if got := b.areaFor(image.Rect(10, 10, 150, 150), extent); got != extent {
		t.Errorf("a shape within the area takes area %v, want it kept", got)
	}
	tiny := image.Rect(10, 10, 20, 20)
	if got := b.areaFor(tiny, extent); got != tiny {
		t.Errorf("a shape far smaller than the area takes area %v, want %v", got, tiny)
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
