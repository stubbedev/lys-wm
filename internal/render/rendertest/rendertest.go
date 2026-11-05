// Package rendertest holds the scene and checks every renderer's tests share,
// so each GPU renderer is held to the software renderer's pixels.
package rendertest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"testing"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/shader"
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
		melt := render.Mask{Keep: render.RoundRect{Rect: image.Rect(36, 4, 46, 16), Radius: 3}, Merge: image.Rect(50, 6, 60, 14), Smooth: 8}
		for x := 36; x < 60; x += 7 {
			p.DrawTexture(tx.XRGB, render.FRect{W: 7, H: 5}, image.Rect(x, 4, x+7, 9), render.Normal, 1, melt)
			p.DrawTexture(tx.XRGB, render.FRect{W: 7, H: 5}, image.Rect(x, 9, x+7, 14), render.Normal, 1, melt)
		}
		p.FillGoo(image.Rect(0, 24, 64, 48), render.Color{R: 0x60, G: 0x30, B: 0x10, A: 0xa0}, render.Goo{
			A:      render.RoundRect{Rect: image.Rect(4, 28, 20, 44), Radius: 5},
			B:      render.RoundRect{Rect: image.Rect(28, 32, 60, 42), Radius: 3},
			Smooth: 9,
		})
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

const GlassTolerance = 3

func GlassScene(tx render.Texture) func(render.Pass) {
	src := render.FRect{W: float64(Size.X), H: float64(Size.Y)}
	all := image.Rectangle{Max: Size}
	return func(p render.Pass) {
		p.Clear(render.Color{})
		p.DrawGlass(tx, src, all, 1, render.Mask{}, render.Glass{Saturation: 1.6, Brightness: 0.8, Contrast: 1.3})
		p.DrawGlass(tx, src, image.Rect(4, 4, 44, 28), 0.75, render.Mask{Keep: render.RoundRect{Rect: image.Rect(6, 5, 40, 26), Radius: 7}}, render.Glass{Saturation: 1, Brightness: 1, Contrast: 1, Bezel: 8, Amount: 6})
		p.DrawGlass(tx, src, image.Rect(30, 20, 60, 44), 1, render.Mask{}, render.Glass{Saturation: 0.4, Brightness: 1.1, Contrast: 0.9, Bezel: 5, Amount: 3})
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
	glass(t, r, target, s, buf)

	cleared := render.Region{image.Rect(3, 4, 21, 15)}
	clear := func(p render.Pass) {
		p.Clear(render.Color{B: 0x20, A: 0x40})
		p.FillRect(image.Rect(0, 0, 10, 10), render.Color{R: 0x40, A: 0x80}, render.Mask{})
	}
	Paint(t, r, target, cleared, clear)
	Paint(t, s, buf, cleared, clear)
	Compare(t, buf, target, 1)
}

func glass(t *testing.T, r render.Renderer, target render.ReadTarget, s *soft.Renderer, buf *soft.Buffer) {
	t.Helper()
	full := render.Region{image.Rectangle{Max: Size}}
	backdrop := func(rd render.Renderer) render.TextureTarget {
		tt, err := rd.NewTextureTarget(Size)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := tt.Destroy(); err != nil {
				t.Error(err)
			}
		})
		Paint(t, rd, tt, full, func(p render.Pass) {
			p.Clear(render.Color{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
			p.FillRect(image.Rect(0, 0, Size.X/2, Size.Y/2), render.Color{R: 0xe0, G: 0x40, B: 0x10, A: 0xff}, render.Mask{})
			p.FillRect(image.Rect(Size.X/2, Size.Y/2, Size.X, Size.Y), render.Color{R: 0x30, G: 0xa0, B: 0x80, A: 0xff}, render.Mask{})
			p.FillRect(image.Rect(10, 8, 38, 28), render.Color{R: 0x20, G: 0x10, B: 0x30, A: 0x80}, render.Mask{})
		})
		return tt
	}
	Paint(t, r, target, full, GlassScene(backdrop(r).Texture()))
	Paint(t, s, buf, full, GlassScene(backdrop(s).Texture()))
	Compare(t, buf, target, GlassTolerance)
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

var uploadSize = image.Pt(640, 512)

func uploadPixel(x, y int, seed byte) render.Color {
	return render.Color{R: seed ^ byte(y>>8), G: byte(y), B: byte(x), A: 0xff} //nolint:gosec // the pattern keeps the low bits
}

func paddedPixels(size image.Point, stride int, seed byte) []byte {
	data := bytes.Repeat([]byte{0xee}, stride*size.Y)
	for y := range size.Y {
		for x := range size.X {
			c := uploadPixel(x, y, seed)
			copy(data[y*stride+x*4:], []byte{c.B, c.G, c.R, c.A})
		}
	}
	return data
}

// UploadsAcrossBands holds a texture larger than one GPU upload step to
// its client's pixels, whole and in part, from memory with a padded
// stride. The target is UploadTargetSize.
func UploadsAcrossBands(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	stride := uploadSize.X*4 + 8
	tex, err := r.NewTexture(render.ARGB8888, uploadSize, stride, paddedPixels(uploadSize, stride, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tex.Destroy() }()
	checkUpload(t, r, target, tex, func(image.Point) byte { return 1 })

	part := image.Rect(50, 20, 610, 500)
	if err := tex.Update(part, stride, paddedPixels(uploadSize, stride, 2)[part.Min.Y*stride+part.Min.X*4:]); err != nil {
		t.Fatal(err)
	}
	checkUpload(t, r, target, tex, func(p image.Point) byte {
		if p.In(part) {
			return 2
		}
		return 1
	})
}

// UploadTargetSize is the target UploadsAcrossBands draws on.
func UploadTargetSize() image.Point { return uploadSize }

func checkUpload(t *testing.T, r render.Renderer, target render.ReadTarget, tex render.Texture, seed func(image.Point) byte) {
	t.Helper()
	all := image.Rectangle{Max: uploadSize}
	Paint(t, r, target, render.Region{all}, func(p render.Pass) {
		p.DrawTexture(tex, render.FRect{W: float64(uploadSize.X), H: float64(uploadSize.Y)}, all, render.Normal, 1, render.Mask{})
	})
	got := make([]byte, uploadSize.X*uploadSize.Y*4)
	if err := target.Read(got, uploadSize.X*4); err != nil {
		t.Fatal(err)
	}
	for y := range uploadSize.Y {
		for x := range uploadSize.X {
			c := uploadPixel(x, y, seed(image.Pt(x, y)))
			at := (y*uploadSize.X + x) * 4
			if want := [4]byte{c.B, c.G, c.R, c.A}; [4]byte(got[at:at+4]) != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got[at:at+4], want)
			}
		}
	}
}

// faultingMemory is a mapping past the end of its file, as a client
// leaves its pool by shrinking it: every read raises SIGBUS.
func faultingMemory(t *testing.T, n int) []byte {
	t.Helper()
	fd, err := unix.MemfdCreate("rendertest", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Ftruncate(fd, int64(n)); err != nil {
		t.Fatal(err)
	}
	data, err := unix.Mmap(fd, 0, n, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Munmap(data) })
	if err := unix.Ftruncate(fd, 0); err != nil {
		t.Fatal(err)
	}
	return data
}

func wantFault(t *testing.T, what string, f func()) {
	t.Helper()
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("%s read faulting memory without a Go fault", what)
			return
		}
		if _, ok := r.(interface{ Addr() uintptr }); !ok {
			panic(r)
		}
	}()
	f()
}

// SurvivesAFaultingUpload holds a renderer to the Texture contract: a
// client's shrunk file faults in Go, and the renderer draws on. The
// target is 1x1.
func SurvivesAFaultingUpload(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	size := image.Pt(16, 16)
	data := faultingMemory(t, size.X*size.Y*4)
	c := render.Color{R: 0xff, G: 0xaa, B: 0x55, A: 0xff}
	good := bytes.Repeat(Encode(render.ARGB8888, c), size.X*size.Y)
	tex, err := r.NewTexture(render.ARGB8888, size, size.X*4, good)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tex.Destroy() }()
	wantFault(t, "Update", func() { _ = tex.Update(image.Rectangle{Max: size}, size.X*4, data) })
	wantFault(t, "NewTexture", func() { _, _ = r.NewTexture(render.ARGB8888, size, size.X*4, data) })
	if err := tex.Update(image.Rectangle{Max: size}, size.X*4, good); err != nil {
		t.Fatalf("update after a fault: %v", err)
	}
	if got, want := drawn(t, r, target, tex), [4]byte{c.B, c.G, c.R, c.A}; got != want {
		t.Errorf("drew %v after a fault, want %v", got, want)
	}
}

// StreamedUpdatesStayInOrder draws after every one of many updates of
// one texture, past any upload buffer's wrap: each draw sees exactly
// the last update, not one a later write overwrote. The target is 1x1.
func StreamedUpdatesStayInOrder(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	size := image.Pt(256, 256)
	tex, err := r.NewTexture(render.ARGB8888, size, size.X*4, make([]byte, size.X*size.Y*4))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tex.Destroy() }()
	for i := range 64 {
		c := render.Color{R: byte(i * 3), G: byte(255 - i), B: byte(i), A: 0xff}
		if err := tex.Update(image.Rectangle{Max: size}, size.X*4, bytes.Repeat(Encode(render.ARGB8888, c), size.X*size.Y)); err != nil {
			t.Fatal(err)
		}
		if got, want := drawn(t, r, target, tex), [4]byte{c.B, c.G, c.R, c.A}; got != want {
			t.Fatalf("update %d drew %v, want %v", i, got, want)
		}
	}
}

var (
	DamageSize = image.Pt(4, 1)
	ScaledSize = image.Pt(20, 20)
	FourKStrip = image.Pt(3840, 48)
)

func OverlappingDamageBlendsOnce(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	all := image.Rectangle{Max: DamageSize}
	Paint(t, r, target, render.Region{all}, func(p render.Pass) { p.FillRect(all, render.Color{A: 0xff}, render.Mask{}) })
	Paint(t, r, target, render.Region{image.Rect(0, 0, 3, 1), image.Rect(1, 0, 4, 1), image.Rect(1, 0, 2, 1)}, func(p render.Pass) {
		p.FillRect(all, render.Color{R: 0x80, A: 0x80}, render.Mask{})
	})
	pix := make([]byte, 16)
	if err := target.Read(pix, 16); err != nil {
		t.Fatal(err)
	}
	for x := range 4 {
		if got := pix[4*x : 4*x+4]; got[2] < 0x7f || got[2] > 0x81 || got[3] != 0xff {
			t.Fatalf("pixel %d is %v, want one blend of half-transparent red over black", x, got)
		}
	}
}

func ScaledDrawsKeepFlatColor(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	pix := make([]byte, 4*4*4)
	for i := range pix {
		pix[i] = []byte{0x20, 0x40, 0x60, 0xff}[i%4]
	}
	tx, err := r.NewTexture(render.ARGB8888, image.Pt(4, 4), 16, pix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Destroy(); err != nil {
			t.Error(err)
		}
	})
	Paint(t, r, target, render.Region{image.Rectangle{Max: ScaledSize}}, func(p render.Pass) {
		p.FillRect(image.Rectangle{Max: ScaledSize}, render.Color{A: 0xff}, render.Mask{})
		p.DrawTexture(tx, render.FRect{X: 0.5, Y: 0.5, W: 3, H: 3}, image.Rect(2, 2, 12, 17), render.Rotate270, 1, render.Mask{})
	})
	got := make([]byte, 20*20*4)
	if err := target.Read(got, 80); err != nil {
		t.Fatal(err)
	}
	for _, at := range []image.Point{{3, 3}, {7, 9}, {11, 16}} {
		if px := got[at.Y*80+at.X*4:][:4]; px[0] != 0x20 || px[1] != 0x40 || px[2] != 0x60 || px[3] != 0xff {
			t.Errorf("%v: %v", at, px)
		}
	}
	if px := got[18*80+18*4:][:4]; px[0] != 0 || px[3] != 0xff {
		t.Errorf("outside the draw: %v", px)
	}
}

func MaskEdgesAcrossA4KOutput(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	s, buf := soft.New(), soft.NewBuffer(FourKStrip.X, FourKStrip.Y)
	ring := render.Mask{
		Keep: render.RoundRect{Rect: image.Rect(3794, 3, 3821, 45), Radius: 9},
		Cut:  render.RoundRect{Rect: image.Rect(3797, 6, 3818, 42), Radius: 6},
	}
	draw := func(p render.Pass) {
		p.Clear(render.Color{A: 0xff})
		p.FillRect(ring.Keep.Rect, render.Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, ring)
	}
	full := render.Region{image.Rectangle{Max: FourKStrip}}
	Paint(t, r, target, full, draw)
	Paint(t, s, buf, full, draw)
	Compare(t, buf, target, 1)
}

func AsyncReadSeesItsFrame(t *testing.T, r render.AsyncReader) {
	t.Helper()
	if !r.AsyncReads() {
		t.Skip("this renderer reads back only synchronously")
	}
	rr, ok := r.(render.Renderer)
	if !ok {
		t.Fatalf("%T reads asynchronously but does not render", r)
	}
	target, err := r.NewAsyncTarget(image.Pt(5, 3))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := target.Destroy(); err != nil {
			t.Error(err)
		}
	})
	for round := range 2 {
		PaintMarked(t, rr, target)
		pending, err := target.ReadAsync()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := target.ReadAsync(); err == nil {
			t.Fatal("a second read was started while one was pending")
		}
		Paint(t, rr, target, render.Region{image.Rect(0, 0, 5, 3)}, func(p render.Pass) {
			p.FillRect(image.Rect(0, 0, 5, 3), render.Color{R: 0xff, G: 0xff, A: 0xff}, render.Mask{})
		})
		if err := pending.Wait(); err != nil {
			t.Fatal(err)
		}
		m, stride, err := pending.Map()
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if _, _, err := pending.Map(); err == nil {
			t.Fatal("a mapped read was mapped again")
		}
		got := bytes.Repeat([]byte{0xaa}, MarkedStride*3)
		for y := range 3 {
			copy(got[y*MarkedStride:], m[y*stride:y*stride+20])
		}
		if err := pending.Release(); err != nil {
			t.Fatal(err)
		}
		CheckMarked(t, got)
	}
	pending, err := target.ReadAsync()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(pending.Wait(), pending.Release()); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ReadAsync(); err != nil {
		t.Fatalf("a read after one released unmapped: %v", err)
	}
}

func check(t *testing.T, f func() error) {
	t.Helper()
	if err := f(); err != nil {
		t.Error(err)
	}
}

type GPU interface {
	render.Renderer
	ImportTarget(b gpu.DMABUF) (render.ReadTarget, error)
	ImportTexture(b gpu.DMABUF) (render.Texture, error)
	RenderableModifiers(format fourcc.Format) ([]fourcc.Modifier, error)
	NativeFences() bool
	SyncFile() (int, error)
	Finish() error
}

func near(a, b [4]byte) bool {
	for i := range a {
		if d := int(a[i]) - int(b[i]); d < -1 || d > 1 {
			return false
		}
	}
	return true
}

func DMABUFTextures(t *testing.T, g GPU, node string) {
	t.Helper()
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, func() error { return unix.Close(fd) })
	lib, err := gpu.LoadGBM(gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := lib.Open(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	modifiers, err := g.RenderableModifiers(fourcc.ARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	bo, err := dev.Allocate(image.Pt(8, 8), fourcc.ARGB8888, modifiers, gpu.UseRendering)
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, bo.Close)
	client, err := g.ImportTarget(bo.DMABUF)
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, client.Destroy)
	Paint(t, g, client, render.Region{image.Rect(0, 0, 8, 8)}, func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 8, 8), render.Color{B: 0x80, A: 0x80}, render.Mask{})
		p.FillRect(image.Rect(0, 0, 4, 8), render.Color{R: 0xff, A: 0xff}, render.Mask{})
	})
	if err := g.Finish(); err != nil {
		t.Fatal(err)
	}
	tex, err := g.ImportTexture(bo.DMABUF)
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, tex.Destroy)
	if err := tex.Update(image.Rect(0, 0, 8, 8), 32, make([]byte, 256)); err == nil {
		t.Fatal("a dmabuf texture accepted an upload")
	}
	fb, err := g.NewTarget(image.Pt(16, 8))
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, fb.Destroy)
	Paint(t, g, fb, render.Region{image.Rect(0, 0, 16, 8)}, func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 16, 8), render.Color{G: 0xff, A: 0xff}, render.Mask{})
		p.DrawTexture(tex, render.FRect{W: 8, H: 8}, image.Rect(8, 0, 16, 8), render.Flipped, 1, render.Mask{})
	})
	got := make([]byte, 16*8*4)
	if err := fb.Read(got, 64); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		x    int
		want [4]byte
	}{{2, [4]byte{0, 0xff, 0, 0xff}}, {10, [4]byte{0x80, 0x7f, 0, 0xff}}, {14, [4]byte{0, 0, 0xff, 0xff}}} {
		px := [4]byte(got[4*64+c.x*4:])
		if !near(px, c.want) {
			t.Errorf("x=%d: %v, want %v", c.x, px, c.want)
		}
	}
}

const nv12ChromaOffset = 64 << 10

func sampleNV12(t *testing.T, g GPU, node string, color *render.ColorTransform, y, cb, cr byte, yuv gpu.YUVEncoding) [4]byte {
	t.Helper()
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, func() error { return unix.Close(fd) })
	lib, err := gpu.LoadGBM(gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := lib.Open(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	carrier, err := dev.Allocate(image.Pt(256, 1024), fourcc.XRGB8888, nil, gpu.UseLinear)
	if err != nil {
		t.Fatalf("%s cannot allocate the linear buffer that carries the NV12 planes: %v", dev.Backend(), err)
	}
	defer check(t, carrier.Close)
	plane := carrier.Planes[0]
	stride := int(plane.Stride)
	m, err := unix.Mmap(plane.FD, int64(plane.Offset), nv12ChromaOffset+8*stride, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatalf("map the NV12 carrier: %v", err)
	}
	for row := range 16 {
		for col := range 16 {
			m[row*stride+col] = y
		}
	}
	for row := range 8 {
		for col := 0; col < 16; col += 2 {
			m[nv12ChromaOffset+row*stride+col] = cb
			m[nv12ChromaOffset+row*stride+col+1] = cr
		}
	}
	if err := unix.Munmap(m); err != nil {
		t.Fatal(err)
	}
	d := gpu.DMABUF{
		Size:     image.Pt(16, 16),
		Format:   fourcc.NV12,
		Modifier: fourcc.Linear,
		Planes:   []gpu.Plane{plane, {FD: plane.FD, Offset: plane.Offset + nv12ChromaOffset, Stride: plane.Stride}},
		YUV:      yuv,
	}
	tex, err := g.ImportTexture(d)
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, tex.Destroy)
	fb, err := g.NewTarget(image.Pt(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, fb.Destroy)
	Paint(t, g, fb, render.Region{image.Rect(0, 0, 16, 16)}, func(p render.Pass) {
		p.SetColor(color)
		p.DrawTexture(tex, render.FRect{W: 16, H: 16}, image.Rect(0, 0, 16, 16), render.Normal, 1, render.Mask{})
	})
	got := make([]byte, 16*16*4)
	if err := fb.Read(got, 64); err != nil {
		t.Fatal(err)
	}
	return [4]byte(got[8*64+8*4:])
}

func ExternalYUV(t *testing.T, g GPU, node string) {
	t.Helper()
	px := sampleNV12(t, g, node, nil, 126, 128, 128, gpu.YUVEncoding{})
	for c := range 3 {
		if d := int(px[c]) - 128; d < -4 || d > 4 {
			t.Fatalf("mid-gray NV12 sampled as %v", px)
		}
	}
}

func YUVHintsSelectTheConversion(t *testing.T, g GPU, node string) {
	t.Helper()
	for _, c := range []struct {
		yuv   gpu.YUVEncoding
		green int
	}{
		{gpu.YUVEncoding{Coefficients: gpu.BT601, Range: gpu.LimitedRange}, 0},
		{gpu.YUVEncoding{Coefficients: gpu.BT709, Range: gpu.LimitedRange}, 24},
	} {
		px := sampleNV12(t, g, node, nil, 81, 90, 240, c.yuv)
		if d := int(px[1]) - c.green; d < -6 || d > 6 || px[2] < 0xf0 {
			t.Errorf("Y=81 Cb=90 Cr=240 tagged %+v sampled as %v, want green near %d and full red", c.yuv, px, c.green)
		}
	}
}

func FrameFencesReportTheGPUFinishing(t *testing.T, g GPU) {
	t.Helper()
	if !g.NativeFences() {
		t.Skip("no native fences on this driver")
	}
	fb, err := g.NewTarget(Size)
	if err != nil {
		t.Fatal(err)
	}
	defer check(t, fb.Destroy)
	start := backend.Now()
	Paint(t, g, fb, render.Region{{Max: Size}}, func(p render.Pass) {
		p.FillRect(image.Rectangle{Max: Size}, render.Color{G: 0xff, A: 0xff}, render.Mask{})
	})
	fd, err := g.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	if fd < 0 {
		return
	}
	defer check(t, func() error { return unix.Close(fd) })
	if _, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 1000); err != nil {
		t.Fatal(err)
	}
	done, ok, err := kdrm.SyncFileSignalTime(fd)
	if err != nil || !ok {
		t.Fatalf("a signalled frame fence: %v, %v", ok, err)
	}
	if done < start || done > backend.Now() {
		t.Fatalf("the fence signalled at %v, outside the frame's %v..%v", done, start, backend.Now())
	}
	t.Logf("the GPU finished the frame %v after it started", done-start)
}

const ScreenShaderTolerance = 3

// ScreenShaderProgram is a shader that reaches for most of the language, for
// the renderers to agree on.
const ScreenShaderProgram = `
precision highp float;
uniform sampler2D tex;
uniform float time;
uniform vec2 resolution;
varying vec2 v_texcoord;

const mat3 tint = mat3(0.9, 0.1, 0.0, 0.0, 0.8, 0.2, 0.1, 0.0, 0.9);

vec3 bent(vec3 c, float k) {
    if (k > 0.5) {
        return c * c;
    }
    return sqrt(c);
}

void main() {
    vec2 px = 1.0 / resolution;
    vec3 sum = vec3(0.0);
    for (int i = -1; i <= 1; i++) {
        sum += texture2D(tex, v_texcoord + vec2(float(i) * 1.5 * px.x, 0.0)).rgb;
    }
    vec3 c = tint * (sum / 3.0);
    float band = 0.5 + 0.5 * sin(gl_FragCoord.y * 0.3 + time);
    c = mix(bent(c, v_texcoord.x), c.bgr, smoothstep(0.2, 0.8, v_texcoord.y));
    c *= 0.75 + 0.25 * band;
    gl_FragColor = vec4(clamp(c, 0.0, 1.0), 0.0);
}
`

func ScreenShaders(t *testing.T, r render.Renderer, target render.ReadTarget, sources ...string) {
	t.Helper()
	s := soft.New()
	rtt, stt := newTextureTarget(t, r), newTextureTarget(t, s)
	full := render.Region{image.Rectangle{Max: Size}}
	Paint(t, r, rtt, full, Scene(Load(t, r)))
	Paint(t, s, stt, full, Scene(Load(t, s)))
	want := soft.NewBuffer(Size.X, Size.Y)
	for _, source := range append(sources, ScreenShaderProgram) {
		p, err := shader.Compile([]byte(source))
		if err != nil {
			t.Fatalf("%v\n%s", err, source)
		}
		rs, err := r.NewScreenShader(p)
		if err != nil {
			t.Fatalf("%v\n%s", err, source)
		}
		ss, err := s.NewScreenShader(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(rs.Prepare(target), ss.Prepare(want)); err != nil {
			t.Fatal(err)
		}
		draw := func(sh render.ScreenShader, tex render.Texture) func(render.Pass) {
			return func(p render.Pass) {
				p.Clear(render.Color{A: 0xff})
				p.DrawScreenShader(sh, tex, image.Rectangle{Max: Size}, 1.25)
			}
		}
		Paint(t, r, target, full, draw(rs, rtt.Texture()))
		Paint(t, s, want, full, draw(ss, stt.Texture()))
		Compare(t, want, target, ScreenShaderTolerance)
		if err := errors.Join(rs.Destroy(), ss.Destroy()); err != nil {
			t.Fatal(err)
		}
	}
}

func ExampleShaders(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("../../../docs/shaders/*.frag")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no example shaders: %v", err)
	}
	out := make([]string, len(paths))
	for i, path := range paths {
		src, err := os.ReadFile(path) //nolint:gosec // a test reads the repository's own examples
		if err != nil {
			t.Fatal(err)
		}
		out[i] = string(src)
	}
	return out
}

type Finisher interface {
	render.Renderer
	Finish() error
}

func GPUWaitsCountOnlyWaits(t *testing.T, r Finisher, target render.Target) {
	t.Helper()
	fill := func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 8, 8), render.Color{R: 0xff, A: 0xff}, render.Mask{})
	}
	Paint(t, r, target, render.Region{image.Rect(0, 0, 8, 8)}, fill)
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	before := r.Counts().GPUWaits
	for range 3 {
		Paint(t, r, target, render.Region{image.Rect(0, 0, 8, 8)}, fill)
	}
	if got := r.Counts().GPUWaits; got != before {
		t.Fatalf("three passes counted %d GPU waits, want none", got-before)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if got := r.Counts().GPUWaits; got <= before {
		t.Fatalf("a Finish after three passes counted %d GPU waits, want at least one", got-before)
	}
}

const (
	BenchWindowCount = 30
	BenchRadius      = 12
)

var BenchOutput = image.Pt(1920, 1080)

func benchTexture(b *testing.B, g Finisher, format render.Format, size image.Point) render.Texture {
	b.Helper()
	pix := make([]byte, size.X*size.Y*4)
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = byte(i), byte(i>>8), byte(i>>16), 0xff
	}
	t, err := g.NewTexture(format, size, size.X*4, pix)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := t.Destroy(); err != nil {
			b.Error(err)
		}
	})
	return t
}

func benchFrame(b *testing.B, g Finisher, target render.Target, tex render.Texture, size image.Point, windows int, mask func(image.Rectangle) render.Mask) {
	b.Helper()
	bounds := image.Rectangle{Max: BenchOutput}
	src := render.FRect{W: float64(size.X), H: float64(size.Y)}
	var recording, finishing time.Duration
	for b.Loop() {
		start := time.Now()
		p, err := g.Begin(target, render.Region{bounds})
		if err != nil {
			b.Fatal(err)
		}
		p.FillRect(bounds, render.Color{R: 0x20, G: 0x20, B: 0x20, A: 0xff}, render.Mask{})
		for i := range windows {
			at := image.Pt(i*(BenchOutput.X-size.X)/windows, i*(BenchOutput.Y-size.Y)/windows)
			dst := image.Rectangle{Min: at, Max: at.Add(size)}
			p.DrawTexture(tex, src, dst, render.Normal, 1, mask(dst))
		}
		if err := p.End(); err != nil {
			b.Fatal(err)
		}
		recorded := time.Now()
		if err := g.Finish(); err != nil {
			b.Fatal(err)
		}
		recording += recorded.Sub(start)
		finishing += time.Since(recorded)
	}
	b.ReportMetric(float64(recording.Microseconds())/float64(b.N), "record-us/frame")
	b.ReportMetric(float64(finishing.Microseconds())/float64(b.N), "finish-us/frame")
}

// BenchWindows records and finishes frames of BenchWindowCount RGBX windows of
// 800x600 over a background into target, each window under mask, and reports
// the time spent on each half per frame.
func BenchWindows(b *testing.B, g Finisher, target render.Target, mask func(image.Rectangle) render.Mask) {
	size := image.Pt(800, 600)
	benchFrame(b, g, target, benchTexture(b, g, render.XRGB8888, size), size, BenchWindowCount, mask)
}

func BenchRounded(dst image.Rectangle) render.Mask {
	return render.Mask{Keep: render.RoundRect{Rect: dst, Radius: BenchRadius}}
}

func BenchUnmasked(image.Rectangle) render.Mask { return render.Mask{} }

func OpaqueRunsWithoutBlending(t *testing.T, r render.Renderer, target render.ReadTarget, runs func() uint64) {
	t.Helper()
	size := image.Pt(64, 64)
	before := runs()
	Paint(t, r, target, render.Region{image.Rectangle{Max: size}}, func(p render.Pass) {
		p.Clear(render.Color{B: 0xff, A: 0xff})
		p.FillRect(image.Rect(0, 0, 32, 64), render.Color{R: 0xff, A: 0xff}, render.Mask{})
		p.FillRect(image.Rect(16, 0, 48, 64), render.Color{R: 0x40, A: 0x80}, render.Mask{})
	})
	if got := runs() - before; got != 1 {
		t.Fatalf("%d runs of opaque ops, want the one", got)
	}
	pix := make([]byte, size.X*size.Y*4)
	if err := target.Read(pix, size.X*4); err != nil {
		t.Fatal(err)
	}
	if got := pix[16*4+2]; got == 0xff {
		t.Fatalf("the translucent fill over the opaque one did not blend: R = %#x", got)
	}
	if got := pix[60*4]; got != 0xff {
		t.Fatalf("the background changed: B = %#x", got)
	}
}

func TargetsAndSubmitsCount(t *testing.T, r render.Renderer, target render.Target, submitsPerPass uint64) {
	t.Helper()
	before := r.Counts()
	rt, err := r.NewTarget(image.Pt(8, 8))
	if err != nil {
		t.Fatal(err)
	}
	tt, err := r.NewTextureTarget(image.Pt(8, 8))
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(rt.Destroy(), tt.Destroy()); err != nil {
		t.Fatal(err)
	}
	if got := r.Counts().Targets - before.Targets; got != 2 {
		t.Fatalf("two new targets counted %d", got)
	}
	before = r.Counts()
	for range 3 {
		Paint(t, r, target, render.Region{image.Rect(0, 0, 8, 8)}, func(p render.Pass) {
			p.FillRect(image.Rect(0, 0, 8, 8), render.Color{R: 0xff, A: 0xff}, render.Mask{})
		})
	}
	now := r.Counts()
	if got := now.Submits - before.Submits; got != 3*submitsPerPass {
		t.Fatalf("three passes counted %d submits, want %d", got, 3*submitsPerPass)
	}
	if now.Targets != before.Targets {
		t.Fatalf("painting counted %d new targets", now.Targets-before.Targets)
	}
}
