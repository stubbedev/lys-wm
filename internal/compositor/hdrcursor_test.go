package compositor

import (
	"image"
	"math"
	"testing"

	"github.com/stubbedev/lys-wm/internal/colorspace"
)

func TestAnHDRCursorPixelMatchesTheFramesEncodeWithinACodeValue(t *testing.T) {
	co := colorspace.Output{SDRBrightness: 203, Peak: 1000, MaxAverage: 600, Panel: colorspace.BT2020}
	working, signal := colorspace.Working(co), colorspace.Signal(co, [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1})
	var plans colorspace.Plans
	direct := plans.Of(colorspace.SDR, signal)
	toWorking, encode := plans.Of(colorspace.SDR, working), plans.Of(working.Description(), signal)
	for _, px := range [][4]byte{{0, 0, 0, 255}, {255, 255, 255, 255}, {30, 120, 220, 255}, {255, 0, 0, 255}, {12, 200, 64, 255}} {
		src := []byte{px[2], px[1], px[0], px[3]}
		dst := make([]byte, 4)
		encodeCursor(direct, dst, src, 4, 4, image.Pt(1, 1))
		lin := toWorking.Apply([4]float32{float32(px[0]) / 255, float32(px[1]) / 255, float32(px[2]) / 255, 1})
		want := encode.Apply(lin)
		for i, got := range [3]byte{dst[2], dst[1], dst[0]} {
			if d := math.Abs(float64(got) - float64(want[i])*255); d > 1 {
				t.Errorf("sRGB %v channel %d encodes to %d, the frame's two passes to %.2f", px, i, got, float64(want[i])*255)
			}
		}
	}
}
