package compositor

import (
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
)

func TestRoundedOpsCullOnlyWhatTheirSolidPartCovers(t *testing.T) {
	window := image.Rect(10, 10, 110, 90)
	rounded := render.Mask{Keep: render.RoundRect{Rect: window, Radius: 8}}
	opaque := render.Color{R: 0x40, A: 0xff}
	var f frame
	f.FillRect(image.Rect(0, 0, 120, 100), opaque)
	f.FillRect(image.Rect(10, 10, 14, 14), opaque)
	f.FillRect(image.Rect(30, 30, 60, 60), opaque)
	f.fillMasked(window, opaque, rounded)
	f.fillMasked(image.Rect(20, 20, 40, 40), opaque, render.Mask{Keep: render.RoundRect{Rect: window, Radius: 8}, Cut: render.RoundRect{Rect: image.Rect(30, 30, 35, 35)}})
	f.cursorFrom = len(f.ops)
	var covered region.Set
	f.cull(&covered)
	var kept []image.Rectangle
	for _, op := range f.ops {
		kept = append(kept, op.dst)
	}
	want := []image.Rectangle{image.Rect(0, 0, 120, 100), image.Rect(10, 10, 14, 14), window, image.Rect(20, 20, 40, 40)}
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v, want %v: the background and the corner square under the rounded corner stay, the square inside it goes", kept, want)
	}
	if f.ops[2].covers() || !(drawOp{opaque: true}).covers() {
		t.Fatal("a rounded op claims to cover its whole rectangle")
	}
}

func TestRemapMovesMasksWithTheirOps(t *testing.T) {
	var f frame
	m := render.Mask{Keep: render.RoundRect{Rect: image.Rect(0, 0, 100, 50), Radius: 6}}
	f.fillMasked(image.Rect(0, 0, 100, 50), render.Color{A: 0xff}, m)
	f.FillRect(image.Rect(10, 10, 20, 20), render.Color{A: 0xff})
	f.remap(0, image.Rect(0, 0, 100, 50), image.Rect(50, 25, 100, 50))
	if got := f.ops[0]; got.dst != image.Rect(50, 25, 100, 50) || got.mask.Keep.Rect != got.dst || got.mask.Cut.Rect != (image.Rectangle{}) {
		t.Fatalf("remapped to %v with mask %+v", got.dst, got.mask)
	}
	if got := f.ops[1].dst; got != image.Rect(55, 30, 60, 35) {
		t.Fatalf("an inner op remapped to %v", got)
	}
}
