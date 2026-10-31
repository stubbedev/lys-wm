package compositor

import (
	"image"
	"testing"
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
