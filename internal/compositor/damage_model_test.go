package compositor

import (
	"bytes"
	"image"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type modelSurface struct {
	s      *surface.Surface
	tex    render.Texture
	at     image.Point
	size   image.Point
	opaque bool
	alpha  float32
	shown  bool
}

type modelBuffer struct {
	buf       *soft.Buffer
	presented int
}

func randomPixels(rng *rand.Rand, n int, opaque bool) []byte {
	pix := make([]byte, n*4)
	a := byte(0xff)
	if !opaque {
		a = byte(rng.IntN(256))
	}
	b, g, r := byte(rng.IntN(int(a)+1)), byte(rng.IntN(int(a)+1)), byte(rng.IntN(int(a)+1))
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = b, g, r, a
	}
	return pix
}

func randomRect(rng *rand.Rand, size image.Point) image.Rectangle {
	x0, y0 := rng.IntN(size.X), rng.IntN(size.Y)
	return image.Rect(x0, y0, x0+1+rng.IntN(size.X-x0), y0+1+rng.IntN(size.Y-y0))
}

func TestPartialRepaintsMatchAFullRepaint(t *testing.T) {
	bounds := image.Rect(0, 0, 48, 48)
	r := soft.New()
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 1))
		sc := newScene()
		surfaces := make([]*modelSurface, 4)
		for i := range surfaces {
			opaque := rng.IntN(2) == 0
			format := render.ARGB8888
			if opaque {
				format = render.XRGB8888
			}
			size := image.Pt(4+rng.IntN(30), 4+rng.IntN(30))
			tex, err := r.NewTexture(format, size, size.X*4, randomPixels(rng, size.X*size.Y, opaque))
			if err != nil {
				t.Fatal(err)
			}
			surfaces[i] = &modelSurface{
				s: new(surface.Surface), tex: tex, size: size, opaque: opaque, alpha: 1, shown: true,
				at: image.Pt(rng.IntN(48)-8, rng.IntN(48)-8),
			}
		}
		buffers := make([]*modelBuffer, 2+rng.IntN(2))
		for i := range buffers {
			buffers[i] = &modelBuffer{buf: soft.NewBuffer(bounds.Dx(), bounds.Dy())}
		}
		var front *modelBuffer
		presented := 0
		for step := range 60 {
			s := surfaces[rng.IntN(len(surfaces))]
			switch rng.IntN(7) {
			case 0:
				s.at = image.Pt(rng.IntN(48)-8, rng.IntN(48)-8)
			case 1, 2:
				d := randomRect(rng, s.size)
				if err := s.tex.Update(d, d.Dx()*4, randomPixels(rng, d.Dx()*d.Dy(), s.opaque)); err != nil {
					t.Fatal(err)
				}
				sc.damaged(s.s, []image.Rectangle{d})
			case 3:
				s.shown = !s.shown
			case 4:
				i, j := rng.IntN(len(surfaces)), rng.IntN(len(surfaces))
				surfaces[i], surfaces[j] = surfaces[j], surfaces[i]
			case 5:
				s.alpha = []float32{1, 0.5}[rng.IntN(2)]
			case 6:
				if rng.IntN(4) == 0 {
					sc.invalidate()
				}
			}
			f := sc.begin()
			f.FillRect(bounds, Background)
			for _, m := range surfaces {
				if m.shown {
					f.drawSurfaceOp(m)
				}
			}
			f.cursorFrom = len(f.ops)
			full := slices.Clip(f.ops)
			want := soft.NewBuffer(bounds.Dx(), bounds.Dy())
			p, err := r.Begin(want, render.Region{bounds})
			if err != nil {
				t.Fatal(err)
			}
			(&frame{ops: full}).replay(p)
			if err := p.End(); err != nil {
				t.Fatal(err)
			}
			sc.cur.ops = append([]drawOp(nil), full...)
			sc.cur.cull(&sc.covered)
			if !sc.diff(bounds).Empty() {
				var target *modelBuffer
				for _, b := range buffers {
					if b != front {
						target = b
						break
					}
				}
				age := 0
				if target.presented != 0 {
					age = presented - target.presented + 1
				}
				repaint := render.Region(sc.repaintFor(backend.Frame{Age: age}, bounds).Rects())
				p, err := r.Begin(target.buf, repaint)
				if err != nil {
					t.Fatal(err)
				}
				sc.cur.replay(p)
				if err := p.End(); err != nil {
					t.Fatal(err)
				}
				sc.presented()
				presented++
				target.presented = presented
				front = target
			}
			if front == nil {
				t.Fatalf("seed %d step %d: nothing was ever drawn", seed, step)
			}
			if !bytes.Equal(front.buf.Pix, want.Pix) {
				t.Fatalf("seed %d step %d: the screen differs from a full repaint", seed, step)
			}
		}
	}
}

func (f *frame) drawSurfaceOp(m *modelSurface) {
	dst := image.Rectangle{Min: m.at, Max: m.at.Add(m.size)}
	f.ops = append(f.ops, drawOp{
		tex: m.tex, src: render.FRect{W: float64(m.size.X), H: float64(m.size.Y)}, dst: dst,
		transform: render.Normal, alpha: m.alpha, surface: m.s, size: m.size, opaque: m.opaque && m.alpha == 1,
	})
}
