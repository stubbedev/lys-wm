package render

import (
	"fmt"
	"image"
	"math"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

const MaxBorderColors = 10

type Border struct {
	colors [MaxBorderColors]Color
	n      int
	angle  float64
}

func SolidBorder(c Color) Border {
	return Border{colors: [MaxBorderColors]Color{c}, n: 1}
}

func GradientBorder(colors []Color, degrees float64) (Border, error) {
	if len(colors) < 2 || len(colors) > MaxBorderColors {
		return Border{}, fmt.Errorf("render: a gradient border takes 2 to %d colours, not %d", MaxBorderColors, len(colors))
	}
	if math.IsNaN(degrees) || math.IsInf(degrees, 0) {
		return Border{}, fmt.Errorf("render: gradient angle %v is not finite", degrees)
	}
	b := Border{n: len(colors), angle: degrees}
	copy(b.colors[:], colors)
	return b, nil
}

func (b Border) Colors() []Color {
	return b.colors[:b.n]
}

func (b Border) Gradient() bool {
	return b.n > 1
}

type BorderStops struct {
	colors [MaxBorderColors]Color
	n      int
}

func (b Border) Stops() BorderStops {
	return BorderStops{colors: b.colors, n: b.n}
}

func (b Border) Line(r image.Rectangle) (from, to [2]float32) {
	s, c := gomath.Sincos(b.angle * math.Pi / 180)
	cx, cy := float64(float64(r.Min.X+r.Max.X)/2), float64(float64(r.Min.Y+r.Max.Y)/2)
	h := math.Abs(float64(r.Dx())/2*c) + math.Abs(float64(r.Dy())/2*s)
	return [2]float32{float32(cx - float64(h*c)), float32(cy - float64(h*s))}, [2]float32{float32(cx + float64(h*c)), float32(cy + float64(h*s))}
}

func (b Border) Turned(degrees float64) Border {
	b.angle += degrees
	return b
}
