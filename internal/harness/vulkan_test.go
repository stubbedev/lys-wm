package harness

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func composed(t *testing.T, h *Harness) *soft.Buffer {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	w := c.Window("quadrants", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window on the output", showing(1, 1, [3]byte{0xff, 0, 0}))
	return h.Rendered(0)
}

func TestTheVulkanRendererComposesLikeTheSoftwareRenderer(t *testing.T) {
	want := composed(t, Start(t, small))
	got := composed(t, StartVulkan(t, func(*compositor.Config) {}, small))
	if got.W != want.W || got.H != want.H {
		t.Fatalf("Vulkan composed %dx%d, soft %dx%d", got.W, got.H, want.W, want.H)
	}
	bad := 0
	for y := range want.H {
		for x := range want.W {
			if a, b := pixel(got, x, y), pixel(want, x, y); a != b {
				if bad < 10 {
					t.Errorf("%v: Vulkan %v, soft %v", image.Pt(x, y), a, b)
				}
				bad++
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d pixels differ", bad)
	}
}
