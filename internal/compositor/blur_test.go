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
