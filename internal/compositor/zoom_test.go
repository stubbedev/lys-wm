package compositor

import (
	"image"
	"testing"
)

func TestTheZoomViewStaysInsideTheScene(t *testing.T) {
	sizes := []image.Point{{160, 100}, {1920, 1080}, {2560, 1440}, {3839, 2161}, {1, 1}}
	factors := []float64{1, 1.0000001, 1.1, 1.5, 3, 7.3, 16}
	for _, size := range sizes {
		for _, factor := range factors {
			w, h := float64(size.X), float64(size.Y)
			for _, focus := range [][2]float64{{0, 0}, {w, h}, {w / 3, h / 7}, {w - 0.0001, h - 0.0001}, {-5, -5}, {w * 2, h * 2}} {
				v := zoomView(size, focus, factor)
				if v.X < 0 || v.Y < 0 || v.X+v.W > w || v.Y+v.H > h || v.W <= 0 || v.H <= 0 {
					t.Errorf("%v at %v focus %v: %+v", size, factor, focus, v)
				}
			}
		}
	}
}

func TestTheViewKeepsTheFocusWhereItIsOnTheScreen(t *testing.T) {
	size := image.Pt(1000, 500)
	focus := [2]float64{250, 400}
	for _, factor := range []float64{1.5, 2, 4, 16} {
		v := zoomView(size, focus, factor)
		if x := (focus[0] - v.X) * factor; x < 249.999 || x > 250.001 {
			t.Errorf("at %v the focus shows at x %v", factor, x)
		}
		if y := (focus[1] - v.Y) * factor; y < 399.999 || y > 400.001 {
			t.Errorf("at %v the focus shows at y %v", factor, y)
		}
	}
}

func TestCrispCursorSizesAreFewAndNeverBelowTheDrawnSize(t *testing.T) {
	seen := map[int]bool{}
	for f := 1.0; f <= 16; f += 0.01 {
		px := crispCursorPx(24, f)
		if float64(px) < 24*f-0.001 {
			t.Fatalf("at %v a %dpx cursor is magnified", f, px)
		}
		seen[px] = true
	}
	if len(seen) > 14 {
		t.Errorf("%d cursor sizes across the zoom range", len(seen))
	}
}
