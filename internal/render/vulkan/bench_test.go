package vulkan_test

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/rendertest"
)

func benchWindowsOnHardware(b *testing.B, mask func(image.Rectangle) render.Mask) {
	g, _ := hardware(b)
	rendertest.BenchWindows(b, g, target(b, g, rendertest.BenchOutput), mask)
}

func BenchmarkRoundedWindowsOnHardware(b *testing.B) {
	benchWindowsOnHardware(b, rendertest.BenchRounded)
}

func BenchmarkUnmaskedWindowsOnHardware(b *testing.B) {
	benchWindowsOnHardware(b, rendertest.BenchUnmasked)
}
