package compositor

import (
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render"
)

func TestAnAlphaShapeKeepsOnlyTexelsAboveTheThreshold(t *testing.T) {
	size := image.Pt(6, 4)
	pix := make([]byte, size.X*size.Y*4)
	alpha := [4]string{
		"..##..",
		".####.",
		".####.",
		"######",
	}
	for y, row := range alpha {
		for x, ch := range row {
			if ch == '#' {
				pix[(y*size.X+x)*4+3] = 0x40
			} else {
				pix[(y*size.X+x)*4+3] = 0x10
			}
		}
	}
	got := alphaShape(render.ARGB8888, size, pix, size.X*4, 0.2, nil)
	want := []image.Rectangle{image.Rect(2, 0, 4, 1), image.Rect(1, 1, 5, 3), image.Rect(0, 3, 6, 4)}
	if !slices.Equal(got, want) {
		t.Fatalf("shape %v, want %v", got, want)
	}
	if got := alphaShape(render.XRGB8888, size, pix, size.X*4, 0.2, nil); !slices.Equal(got, []image.Rectangle{{Max: size}}) {
		t.Fatalf("an opaque format's shape %v", got)
	}
}
