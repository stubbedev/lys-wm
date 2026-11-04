package compositor

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

func BenchmarkClipAndDiffOf100Ops(b *testing.B) {
	bounds := image.Rect(0, 0, 3840, 2160)
	surfaces := make([]*surface.Surface, 100)
	for i := range surfaces {
		surfaces[i] = &surface.Surface{}
	}
	sc := newScene()
	build := func(f *frame, shift int) {
		f.ops = f.ops[:0]
		for i, s := range surfaces {
			x, y := i*30+shift, i*15
			f.ops = append(f.ops, drawOp{surface: s, size: image.Pt(800, 600), src: render.FRect{W: 800, H: 600}, dst: image.Rect(x, y, x+800, y+600), alpha: 1, opaque: i%3 == 0, transform: render.Normal})
		}
		f.cursorFrom = len(f.ops)
		f.clip()
	}
	build(&sc.prev, 0)
	sc.drawn = true
	for b.Loop() {
		build(&sc.cur, 4)
		sc.diff(bounds)
	}
}
