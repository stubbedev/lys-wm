// Package rendertest holds the scene and checks every renderer's tests share,
// so each GPU renderer is held to the software renderer's pixels.
package rendertest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

var (
	TexSize = image.Pt(7, 5)
	Size    = image.Pt(64, 48)
)

func ARGB() []byte {
	pix := make([]byte, TexSize.X*TexSize.Y*4)
	for y := range TexSize.Y {
		for x := range TexSize.X {
			a := byte(255 - 30*x)
			if y == 0 {
				a = 255
			}
			p := pix[(y*TexSize.X+x)*4:]
			p[0], p[1], p[2], p[3] = byte(int(a)*y/TexSize.Y), byte(int(a)*x/TexSize.X), a/2, a //nolint:gosec // a scaled by a fraction below one fits a byte
		}
	}
	return pix
}

func XRGB() []byte {
	pix := ARGB()
	for i := 3; i < len(pix); i += 4 {
		pix[i] = byte(i)
	}
	return pix
}

type Textures struct{ ARGB, XRGB, Stops render.Texture }

func Load(t *testing.T, r render.Renderer) Textures {
	t.Helper()
	a, err := r.NewTexture(render.ARGB8888, TexSize, TexSize.X*4, ARGB())
	if err != nil {
		t.Fatal(err)
	}
	x, err := r.NewTexture(render.XRGB8888, TexSize, TexSize.X*4, XRGB())
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewTexture(render.ARGB8888, image.Pt(3, 1), 12, []byte{0, 0, 0xc8, 0xff, 0, 0xb4, 0, 0xff, 0x78, 0, 0, 0x78})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(a.Destroy(), x.Destroy(), s.Destroy()); err != nil {
			t.Error(err)
		}
	})
	return Textures{a, x, s}
}

func Paint(t *testing.T, r render.Renderer, target render.Target, damage render.Region, draw func(render.Pass)) {
	t.Helper()
	p, err := r.Begin(target, damage)
	if err != nil {
		t.Fatal(err)
	}
	draw(p)
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
}

func Scene(tx Textures) func(render.Pass) {
	return func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 64, 48), render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}, render.Mask{})
		p.FillRect(image.Rect(4, 30, 40, 46), render.Color{R: 0x40, G: 0x20, B: 0x00, A: 0x80}, render.Mask{})
		for i := range render.Flipped270 + 1 {
			size := i.Size(TexSize)
			at := image.Pt(1+(int(i)%4)*9, 1+int(i)/4*9)
			p.DrawTexture(tx.ARGB, render.FRect{W: 7, H: 5}, image.Rectangle{Min: at, Max: at.Add(size)}, i, 1, render.Mask{})
		}
		p.DrawTexture(tx.XRGB, render.FRect{W: 7, H: 5}, image.Rect(40, 2, 47, 7), render.Normal, 0.5, render.Mask{})
		p.DrawTexture(tx.ARGB, render.FRect{X: 1, Y: 1, W: 4, H: 3}, image.Rect(40, 10, 44, 13), render.Normal, 1, render.Mask{})
		p.DrawTexture(tx.XRGB, render.FRect{W: 7, H: 5}, image.Rect(50, 20, 55, 27), render.Rotate90, 1, render.Mask{})
	}
}

func MaskScene(tx Textures) func(render.Pass) {
	return func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 64, 48), render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}, render.Mask{})
		window := render.Mask{Keep: render.RoundRect{Rect: image.Rect(3, 4, 31, 29), Radius: 7.5}}
		pill := render.Mask{Keep: render.RoundRect{Rect: image.Rect(3, 31, 33, 45), Radius: 30}}
		for x := 3; x < 31; x += 7 {
			for y := 4; y < 29; y += 5 {
				p.DrawTexture(tx.XRGB, render.FRect{W: 7, H: 5}, image.Rect(x, y, x+7, y+5), render.Normal, 1, window)
			}
		}
		for x := 3; x < 33; x += 5 {
			p.DrawTexture(tx.ARGB, render.FRect{W: 7, H: 5}, image.Rect(x, 31, x+5, 38), render.Rotate90, 0.75, pill)
			p.DrawTexture(tx.ARGB, render.FRect{W: 7, H: 5}, image.Rect(x, 38, x+5, 45), render.Rotate90, 0.75, pill)
		}
		ring := render.Mask{
			Keep: render.RoundRect{Rect: image.Rect(34, 3, 61, 45), Radius: 9},
			Cut:  render.RoundRect{Rect: image.Rect(37, 6, 58, 42), Radius: 6},
		}
		p.FillRect(ring.Keep.Rect, render.Color{R: 0xcb, G: 0xa6, B: 0xf7, A: 0xff}, ring)
		p.FillRect(image.Rect(40, 10, 56, 38), render.Color{R: 0x20, G: 0x40, B: 0x10, A: 0x80}, render.Mask{Keep: render.RoundRect{Rect: image.Rect(40, 10, 56, 38), Radius: 4.25}})
		shadow := render.Mask{
			Keep:    render.RoundRect{Rect: image.Rect(12, 12, 24, 22), Radius: 3},
			Cut:     render.RoundRect{Rect: image.Rect(14, 13, 22, 20), Radius: 2},
			Falloff: render.Falloff{Range: 5.5, Power: 3},
		}
		p.FillRect(shadow.Keep.Rect.Inset(-6), render.Color{A: 0xc0}, shadow)
	}
}

const GradientTolerance = 2

func GradientScene(tx Textures) func(render.Pass) {
	return func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 64, 48), render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}, render.Mask{})
		border := render.Mask{Keep: render.RoundRect{Rect: image.Rect(2, 2, 62, 46), Radius: 5}, Cut: render.RoundRect{Rect: image.Rect(4, 4, 60, 44), Radius: 3}}
		p.FillGradient(border.Keep.Rect, render.Gradient{Stops: tx.Stops, From: [2]float32{10, 40}, To: [2]float32{54, 8}, Alpha: 1}, border)
		p.FillGradient(image.Rect(10, 10, 30, 30), render.Gradient{Stops: tx.Stops, From: [2]float32{30, 10}, To: [2]float32{10, 10}, Alpha: 0.6}, render.Mask{})
	}
}

func newTextureTarget(t *testing.T, r render.Renderer) render.TextureTarget {
	t.Helper()
	tt, err := r.NewTextureTarget(Size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tt.Destroy(); err != nil {
			t.Error(err)
		}
	})
	return tt
}

func offscreen(t *testing.T, r render.Renderer, target render.ReadTarget, rtx Textures, s *soft.Renderer, buf *soft.Buffer, stx Textures) {
	t.Helper()
	rtt, stt := newTextureTarget(t, r), newTextureTarget(t, s)
	full := render.Region{image.Rectangle{Max: Size}}
	Paint(t, r, rtt, full, Scene(rtx))
	Paint(t, s, stt, full, Scene(stx))
	want, ok := stt.(*soft.TextureTarget)
	if !ok {
		t.Fatalf("the software renderer made a %T", stt)
	}
	Compare(t, want.Buffer, rtt, 1)
	sample := func(tt render.TextureTarget) func(render.Pass) {
		return func(p render.Pass) {
			p.Clear(render.Color{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
			window := image.Rect(6, 5, 6+Size.X/2, 5+Size.Y/2)
			p.DrawTexture(tt.Texture(), render.FRect{W: float64(Size.X / 2), H: float64(Size.Y / 2)}, window, render.Normal, 0.75, render.Mask{Keep: render.RoundRect{Rect: window, Radius: 6}})
			p.DrawTexture(tt.Texture(), render.FRect{X: 10, Y: 8, W: 16, H: 20}, image.Rect(40, 20, 60, 36), render.Rotate90, 1, render.Mask{})
		}
	}
	Paint(t, r, target, full, sample(rtt))
	Paint(t, s, buf, full, sample(stt))
	Compare(t, buf, target, 1)
	kawase(t, r, rtt, s, stt)
	p, err := r.Begin(rtt, full)
	if err != nil {
		t.Fatal(err)
	}
	p.DrawTexture(rtt.Texture(), render.FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), render.Normal, 1, render.Mask{})
	if err := p.End(); err == nil {
		t.Fatalf("%T drew a target into itself", r)
	}
	if err := rtt.Texture().Update(image.Rect(0, 0, 1, 1), 4, make([]byte, 4)); err == nil {
		t.Fatalf("%T uploaded into a target's texture", r)
	}
}

const KawaseTolerance = 3

func kawase(t *testing.T, r render.Renderer, rtt render.TextureTarget, s *soft.Renderer, stt render.TextureTarget) {
	t.Helper()
	rprev, sprev := rtt, stt
	for _, step := range []struct {
		size image.Point
		up   bool
	}{{image.Pt(32, 24), false}, {image.Pt(16, 12), false}, {image.Pt(32, 24), true}, {Size, true}} {
		rnext, err := r.NewTextureTarget(step.size)
		if err != nil {
			t.Fatal(err)
		}
		snext, err := s.NewTextureTarget(step.size)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := errors.Join(rnext.Destroy(), snext.Destroy()); err != nil {
				t.Error(err)
			}
		})
		area := render.Region{image.Rectangle{Max: step.size}}
		Paint(t, r, rnext, area, func(p render.Pass) {
			p.Clear(render.Color{})
			p.Kawase(rprev.Texture(), image.Rectangle{Max: step.size}, 1.5, step.up)
		})
		want, ok := snext.(*soft.TextureTarget)
		if !ok {
			t.Fatalf("the software renderer made a %T", snext)
		}
		Paint(t, s, snext, area, func(p render.Pass) {
			p.Clear(render.Color{})
			p.Kawase(sprev.Texture(), image.Rectangle{Max: step.size}, 1.5, step.up)
		})
		Compare(t, want.Buffer, rnext, KawaseTolerance)
		pix := make([]byte, step.size.X*step.size.Y*4)
		if err := rnext.Read(pix, step.size.X*4); err != nil {
			t.Fatal(err)
		}
		copy(want.Pix, pix)
		rprev, sprev = rnext, snext
	}
}

func Compare(t *testing.T, want *soft.Buffer, got render.ReadTarget, tolerance int) {
	t.Helper()
	pix := make([]byte, len(want.Pix))
	if err := got.Read(pix, want.Stride); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for y := range want.H {
		for x := range want.W {
			i := y*want.Stride + x*4
			for c := range 4 {
				if d := int(pix[i+c]) - int(want.Pix[i+c]); d < -tolerance || d > tolerance {
					if bad < 10 {
						t.Errorf("(%d,%d): %T %v, soft %v", x, y, got, pix[i:i+4], want.Pix[i:i+4])
					}
					bad++
					break
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d pixels differ", bad)
	}
}

func Parity(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	s := soft.New()
	rtx, stx := Load(t, r), Load(t, s)
	buf := soft.NewBuffer(Size.X, Size.Y)
	full := render.Region{image.Rectangle{Max: Size}}
	Paint(t, r, target, full, Scene(rtx))
	Paint(t, s, buf, full, Scene(stx))
	Compare(t, buf, target, 1)

	update := ARGB()
	for i := range update {
		update[i] = 0x10
	}
	damage := image.Rect(2, 1, 5, 3)
	for _, tx := range []render.Texture{rtx.ARGB, stx.ARGB} {
		if err := tx.Update(damage, TexSize.X*4, update[damage.Min.Y*TexSize.X*4+damage.Min.X*4:]); err != nil {
			t.Fatal(err)
		}
	}
	partial := render.Region{image.Rect(0, 0, 20, 12), image.Rect(30, 20, 64, 48)}
	Paint(t, r, target, partial, Scene(rtx))
	Paint(t, s, buf, partial, Scene(stx))
	Compare(t, buf, target, 1)

	Paint(t, r, target, full, MaskScene(rtx))
	Paint(t, s, buf, full, MaskScene(stx))
	Compare(t, buf, target, 1)

	Paint(t, r, target, full, GradientScene(rtx))
	Paint(t, s, buf, full, GradientScene(stx))
	Compare(t, buf, target, GradientTolerance)

	offscreen(t, r, target, rtx, s, buf, stx)

	cleared := render.Region{image.Rect(3, 4, 21, 15)}
	clear := func(p render.Pass) {
		p.Clear(render.Color{B: 0x20, A: 0x40})
		p.FillRect(image.Rect(0, 0, 10, 10), render.Color{R: 0x40, A: 0x80}, render.Mask{})
	}
	Paint(t, r, target, cleared, clear)
	Paint(t, s, buf, cleared, clear)
	Compare(t, buf, target, 1)
}

func PaintMarked(t *testing.T, r render.Renderer, target render.Target) {
	t.Helper()
	Paint(t, r, target, render.Region{image.Rect(0, 0, 5, 3)}, func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 5, 3), render.Color{B: 0xff, A: 0xff}, render.Mask{})
		p.FillRect(image.Rect(1, 0, 2, 1), render.Color{R: 0xff, A: 0xff}, render.Mask{})
		p.FillRect(image.Rect(4, 2, 5, 3), render.Color{G: 0xff, A: 0xff}, render.Mask{})
	})
}

const MarkedStride = 32

func CheckMarked(t *testing.T, got []byte) {
	t.Helper()
	for _, c := range []struct {
		at   image.Point
		want [4]byte
	}{{image.Pt(0, 0), [4]byte{0xff, 0, 0, 0xff}}, {image.Pt(1, 0), [4]byte{0, 0, 0xff, 0xff}}, {image.Pt(4, 2), [4]byte{0, 0xff, 0, 0xff}}, {image.Pt(3, 1), [4]byte{0xff, 0, 0, 0xff}}} {
		if px := [4]byte(got[c.at.Y*MarkedStride+c.at.X*4:]); px != c.want {
			t.Errorf("%v: %v, want %v", c.at, px, c.want)
		}
	}
	for y := range 3 {
		if pad := got[y*MarkedStride+20 : (y+1)*MarkedStride]; !bytes.Equal(pad, bytes.Repeat([]byte{0xaa}, len(pad))) {
			t.Errorf("row %d padding was overwritten: %v", y, pad)
		}
	}
}

func ReadKeepsTheStridePadding(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	PaintMarked(t, r, target)
	got := bytes.Repeat([]byte{0xaa}, MarkedStride*3)
	if err := target.Read(got, MarkedStride); err != nil {
		t.Fatal(err)
	}
	CheckMarked(t, got)
	if err := target.Read(make([]byte, 21*3), 21); err == nil {
		t.Error("a stride that is not a whole number of pixels was accepted")
	}
}

func Encode(f render.Format, c render.Color) []byte {
	lo, hi := c.B, c.R
	if f.RGBOrder() {
		lo, hi = hi, lo
	}
	a := c.A
	if f.Opaque() {
		a = 0
	}
	if !f.TenBit() {
		return []byte{lo, c.G, hi, a}
	}
	ten := func(v uint8) uint32 { return uint32(v) * 0x3ff / 0xff }
	return binary.LittleEndian.AppendUint32(nil, uint32(a>>6)<<30|ten(hi)<<20|ten(c.G)<<10|ten(lo))
}

func EveryFormat(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	c := render.Color{R: 0xff, G: 0xaa, B: 0x55, A: 0xff}
	want := [4]byte{c.B, c.G, c.R, c.A}
	if !slices.Contains(r.Formats(), render.XBGR2101010) {
		t.Errorf("%T offers %v, without 10-bit formats", r, r.Formats())
	}
	for _, f := range r.Formats() {
		tex, err := r.NewTexture(f, image.Pt(1, 1), 4, Encode(f, c))
		if err != nil {
			t.Fatal(err)
		}
		if got := drawn(t, r, target, tex); got != want {
			t.Errorf("%T: format %d drew %v, want %v", r, f, got, want)
		}
		if err := tex.Update(image.Rect(0, 0, 1, 1), 4, Encode(f, render.Color{A: 0xff})); err != nil {
			t.Fatal(err)
		}
		if got := drawn(t, r, target, tex); got != [4]byte{0, 0, 0, 0xff} {
			t.Errorf("%T: format %d updated to black drew %v", r, f, got)
		}
		if err := tex.Destroy(); err != nil {
			t.Fatal(err)
		}
	}
}

func drawn(t *testing.T, r render.Renderer, target render.ReadTarget, tex render.Texture) [4]byte {
	t.Helper()
	Paint(t, r, target, render.Region{image.Rect(0, 0, 1, 1)}, func(p render.Pass) {
		p.DrawTexture(tex, render.FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), render.Normal, 1, render.Mask{})
	})
	pix := make([]byte, 4)
	if err := target.Read(pix, 4); err != nil {
		t.Fatal(err)
	}
	return [4]byte(pix)
}
