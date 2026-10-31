package compositor

import (
	"image"
	"testing"

	swlr "github.com/stubbedev/lys-wm/internal/protocol/server/wlrlayershell"
)

func TestSlideOffLeavesThroughTheNearestEdge(t *testing.T) {
	area := image.Rect(0, 0, 1000, 600)
	cases := []struct{ r, want image.Rectangle }{
		{image.Rect(10, 100, 210, 500), image.Rect(-200, 100, 0, 500)},
		{image.Rect(800, 100, 990, 500), image.Rect(1000, 100, 1190, 500)},
		{image.Rect(300, 20, 700, 120), image.Rect(300, -100, 700, 0)},
		{image.Rect(300, 480, 700, 590), image.Rect(300, 600, 700, 710)},
	}
	for _, c := range cases {
		if got := slideOff(area, c.r); got != c.want {
			t.Errorf("%v slides to %v, want %v", c.r, got, c.want)
		}
		if slideOff(area, c.r).Overlaps(area) {
			t.Errorf("%v slides to a place still on the output", c.r)
		}
	}
}

func TestLayerSurfacesSlideFromTheEdgeTheyAreAnchoredTo(t *testing.T) {
	area := image.Rect(0, 0, 1000, 600)
	top, bottom := swlr.ZwlrLayerSurfaceV1AnchorTop, swlr.ZwlrLayerSurfaceV1AnchorBottom
	left, right := swlr.ZwlrLayerSurfaceV1AnchorLeft, swlr.ZwlrLayerSurfaceV1AnchorRight
	cases := []struct {
		r      image.Rectangle
		anchor swlr.ZwlrLayerSurfaceV1Anchor
		want   image.Rectangle
	}{
		{image.Rect(0, 0, 1000, 30), top | left | right, image.Rect(0, -30, 1000, 0)},
		{image.Rect(300, 550, 700, 600), bottom, image.Rect(300, 600, 700, 650)},
		{image.Rect(940, 10, 1000, 200), right | top, image.Rect(940, -190, 1000, 0)},
		{image.Rect(0, 0, 40, 600), left | top | bottom, image.Rect(-40, 0, 0, 600)},
		{image.Rect(400, 200, 600, 400), 0, image.Rect(400, -200, 600, 0)},
	}
	for _, c := range cases {
		if got := anchoredSlide(area, c.r, c.anchor); got != c.want {
			t.Errorf("%v anchored %v slides to %v, want %v", c.r, c.anchor, got, c.want)
		}
	}
}
