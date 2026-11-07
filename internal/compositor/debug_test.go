package compositor

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render"
)

func TestTheDamageOverlayFillsEachDamageRectAndFadesToNoDamage(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	bg := drawOp{fill: true, color: Background, dst: bounds}
	box := func(x int) drawOp {
		return drawOp{fill: true, color: render.Color{A: 0xff}, dst: image.Rect(x, 0, x+10, 10)}
	}
	d := &debugOverlays{damage: true}
	sc := newScene()
	frame := func(ops ...drawOp) (real, fills, damaged int) {
		t.Helper()
		sc.frameWith(ops...)
		real = len(sc.diff(bounds).Rects())
		before := len(sc.cur.ops)
		d.draw(sc)
		fills, damaged = len(sc.cur.ops)-before, len(sc.damage.Rects())
		sc.presented()
		return real, fills, damaged
	}
	frame(bg, box(0))
	for range debugFade {
		frame(bg, box(0))
	}
	if real, fills, damaged := frame(bg, box(0)); real != 0 || fills != 0 || damaged != 0 {
		t.Fatalf("a settled static scene: %d real damage, %d fills, %d damage", real, fills, damaged)
	}
	real, fills, _ := frame(bg, box(50))
	if real == 0 || fills != real {
		t.Fatalf("moving a box damaged %d rects and drew %d fills, want one fill per rect", real, fills)
	}
	frames := 0
	for {
		real, _, damaged := frame(bg, box(50))
		if real != 0 {
			t.Fatalf("the overlay fed %d rects back into the scene's own damage", real)
		}
		if damaged == 0 {
			break
		}
		frames++
		if frames > debugFade {
			t.Fatalf("the overlay still damaged the output %d frames after the scene settled", frames)
		}
		if !d.fading(sc) && damaged != 0 && frames < debugFade {
			t.Fatalf("frame %d damages the output but the overlay says it is not fading", frames)
		}
	}
	if d.fading(sc) {
		t.Fatal("the overlay still fades with nothing left to show")
	}
}

func TestTheOpaqueOverlayDamagesOnlyWhenItChanges(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	d := &debugOverlays{opaque: true}
	sc := newScene()
	sc.frameWith(drawOp{fill: true, color: Background, dst: bounds})
	sc.diff(bounds)
	d.draw(sc)
	sc.presented()
	sc.frameWith(drawOp{fill: true, color: Background, dst: bounds})
	sc.diff(bounds)
	d.draw(sc)
	if !sc.damage.Empty() {
		t.Fatalf("an unchanged opaque overlay damaged %v", sc.damage.Rects())
	}
}
