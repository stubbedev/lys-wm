package rendertest

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

const colorTolerance = 3

var colorOutput = colorspace.Output{SDRBrightness: 203, Peak: 1000, MinLum: 0.005, MaxAverage: 400, Panel: colorspace.DisplayP3}

func hdrSource() *colorspace.Description {
	d := *colorspace.WindowsBT2100
	d.MaxCLL = 4000
	return &d
}

func workingScene(tx Textures, hdr, flat render.Texture, sdr, content *render.ColorTransform) func(render.Pass) {
	return func(p render.Pass) {
		p.SetColor(sdr)
		p.FillRect(image.Rectangle{Max: Size}, render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}, render.Mask{})
		p.FillRect(image.Rect(4, 30, 40, 46), render.Color{R: 0x40, G: 0x20, B: 0x00, A: 0x80}, render.Mask{})
		p.DrawTexture(tx.ARGB, render.FRect{W: 7, H: 5}, image.Rect(1, 1, 8, 6), render.Normal, 1, render.Mask{})
		p.DrawTexture(tx.XRGB, render.FRect{W: 7, H: 5}, image.Rect(10, 1, 17, 6), render.Normal, 0.5, render.Mask{})
		round := render.Mask{Keep: render.RoundRect{Rect: image.Rect(20, 1, 36, 13), Radius: 4}}
		p.FillRect(image.Rect(20, 1, 36, 13), render.Color{G: 0xc0, A: 0xff}, round)
		p.FillGradient(image.Rect(40, 1, 60, 13), render.Gradient{Stops: tx.Stops, From: [2]float32{40, 0}, To: [2]float32{60, 0}, Alpha: 1}, render.Mask{})
		p.SetColor(content)
		p.DrawTexture(hdr, render.FRect{W: 3, H: 1}, image.Rect(4, 16, 7, 17), render.Normal, 1, render.Mask{})
		p.DrawTexture(flat, render.FRect{W: 1, H: 1}, image.Rect(8, 16, 38, 26), render.Normal, 1, render.Mask{})
		p.SetColor(sdr)
	}
}

func hdrPixels() []byte {
	return []byte{0x60, 0x60, 0x60, 0xff, 0x94, 0x94, 0x94, 0xff, 0xf0, 0xe0, 0xd0, 0xff}
}

type colorRig struct {
	r       render.Renderer
	tx      Textures
	hdr     render.Texture
	flat    render.Texture
	work    render.TextureTarget
	blurred render.TextureTarget
	target  render.Target
	sdr     *render.ColorTransform
	content *render.ColorTransform
	encode  *render.ColorTransform
	tonemap *render.ColorTransform
	wide    *render.ColorTransform
	full    render.Region
}

func newColorRig(t *testing.T, r render.Renderer, target render.Target) *colorRig {
	t.Helper()
	tx := Load(t, r)
	hdr, err := r.NewTexture(render.ARGB8888, image.Pt(3, 1), 12, hdrPixels())
	if err != nil {
		t.Fatal(err)
	}
	flat, err := r.NewTexture(render.ARGB8888, image.Pt(1, 1), 4, []byte{0xe0, 0xd0, 0xc0, 0xff})
	if err != nil {
		t.Fatal(err)
	}
	work, err := r.NewWorkingTarget(Size)
	if err != nil {
		t.Fatal(err)
	}
	blurred, err := r.NewWorkingTarget(Size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		check(t, hdr.Destroy)
		check(t, flat.Destroy)
		check(t, work.Destroy)
		check(t, blurred.Destroy)
	})
	w, sig := colorspace.Working(colorOutput), colorspace.Signal(colorOutput, [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1})
	source := hdrSource()
	wideSDR := *colorspace.SDR
	wideSDR.Primaries = colorspace.BT2020
	wideSDR.TargetPrimaries = colorspace.BT2020
	return &colorRig{
		r: r, tx: tx, hdr: hdr, flat: flat, work: work, blurred: blurred, target: target,
		sdr: colorspace.Plan(nil, w), content: colorspace.Plan(source, w), encode: colorspace.Plan(w.Description(), sig),
		tonemap: colorspace.Plan(source, colorspace.SDRTarget), wide: colorspace.Plan(&wideSDR, colorspace.SDRTarget),
		full: render.Region{image.Rectangle{Max: Size}},
	}
}

func (c *colorRig) hdrFrame(t *testing.T) {
	t.Helper()
	Paint(t, c.r, c.work, c.full, workingScene(c.tx, c.hdr, c.flat, c.sdr, c.content))
	Paint(t, c.r, c.blurred, c.full, func(p render.Pass) {
		p.Clear(render.Color{})
		p.Kawase(c.work.Texture(), image.Rectangle{Max: Size}, 2, false)
	})
	Paint(t, c.r, c.target, c.full, func(p render.Pass) {
		p.Clear(render.Color{A: 0xff})
		p.SetColor(c.encode)
		half := image.Pt(Size.X/2, Size.Y)
		p.DrawTexture(c.work.Texture(), render.FRect{W: float64(half.X), H: float64(half.Y)}, image.Rectangle{Max: half}, render.Normal, 1, render.Mask{})
		p.DrawTexture(c.blurred.Texture(), render.FRect{X: float64(half.X), W: float64(half.X), H: float64(half.Y)}, image.Rect(half.X, 0, Size.X, Size.Y), render.Normal, 1, render.Mask{})
	})
}

func (c *colorRig) sdrFrame(t *testing.T) {
	t.Helper()
	Paint(t, c.r, c.target, c.full, func(p render.Pass) {
		p.Clear(render.Color{R: 0x20, G: 0x20, B: 0x20, A: 0xff})
		p.SetColor(c.tonemap)
		p.DrawTexture(c.hdr, render.FRect{W: 3, H: 1}, image.Rect(0, 0, 3, 1), render.Normal, 1, render.Mask{Keep: render.RoundRect{Rect: image.Rect(0, 0, 3, 1), Radius: 0.5}})
		p.DrawTexture(c.flat, render.FRect{W: 1, H: 1}, image.Rect(0, 4, 30, 20), render.Normal, 1, render.Mask{Keep: render.RoundRect{Rect: image.Rect(0, 4, 30, 20), Radius: 5}})
		p.SetColor(c.wide)
		p.DrawTexture(c.tx.ARGB, render.FRect{W: 7, H: 5}, image.Rect(32, 2, 39, 7), render.Normal, 1, render.Mask{})
		p.FillRect(image.Rect(40, 2, 60, 20), render.Color{G: 0xc0, A: 0xc0}, render.Mask{})
		p.SetColor(nil)
		p.DrawTexture(c.tx.ARGB, render.FRect{W: 7, H: 5}, image.Rect(32, 30, 39, 35), render.Normal, 1, render.Mask{})
	})
}

func ColorParity(t *testing.T, r render.Renderer, target render.ReadTarget) {
	t.Helper()
	s := soft.New()
	buf := soft.NewBuffer(Size.X, Size.Y)
	got, want := newColorRig(t, r, target), newColorRig(t, s, buf)
	got.hdrFrame(t)
	want.hdrFrame(t)
	Compare(t, buf, target, colorTolerance)
	got.sdrFrame(t)
	want.sdrFrame(t)
	Compare(t, buf, target, colorTolerance)
}

func YUVThroughAColorTransform(t *testing.T, g GPU, node string) {
	t.Helper()
	yuv := gpu.YUVEncoding{Coefficients: gpu.BT2020, Range: gpu.FullRange}
	plain := sampleNV12(t, g, node, nil, 180, 100, 160, yuv)
	source := hdrSource()
	transform := colorspace.Plan(source, colorspace.SDRTarget)
	got := sampleNV12(t, g, node, transform, 180, 100, 160, yuv)
	want := transform.Apply([4]float32{float32(plain[2]) / 255, float32(plain[1]) / 255, float32(plain[0]) / 255, 1})
	for i, channel := range [3]int{2, 1, 0} {
		v := max(min(want[channel], 1), 0)
		if d := int(got[i]) - int(float32(v*255)+0.5); d > colorTolerance+2 || d < -colorTolerance-2 {
			t.Errorf("channel %d of a transformed NV12 sample is %d, want %v", i, got[i], v*255)
		}
	}
}
