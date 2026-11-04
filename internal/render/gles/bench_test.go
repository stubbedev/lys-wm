package gles_test

import (
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
)

const (
	benchWindows = 30
	benchRadius  = 12
)

var benchOutput = image.Pt(1920, 1080)

func benchTexture(b *testing.B, g *gles.Renderer, format render.Format, size image.Point) render.Texture {
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

func benchFrame(b *testing.B, g *gles.Renderer, target render.Target, tex render.Texture, size image.Point, windows int, mask func(image.Rectangle) render.Mask) {
	b.Helper()
	bounds := image.Rectangle{Max: benchOutput}
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
			at := image.Pt(i*(benchOutput.X-size.X)/windows, i*(benchOutput.Y-size.Y)/windows)
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

func benchWindowsOnHardware(b *testing.B, mask func(image.Rectangle) render.Mask) {
	g, _ := hardware(b)
	f := framebuffer(b, g, benchOutput)
	size := image.Pt(800, 600)
	benchFrame(b, g, f, benchTexture(b, g, render.XRGB8888, size), size, benchWindows, mask)
}

func BenchmarkRoundedWindowsOnHardware(b *testing.B) {
	benchWindowsOnHardware(b, func(dst image.Rectangle) render.Mask {
		return render.Mask{Keep: render.RoundRect{Rect: dst, Radius: benchRadius}}
	})
}

func BenchmarkUnmaskedWindowsOnHardware(b *testing.B) {
	benchWindowsOnHardware(b, func(image.Rectangle) render.Mask { return render.Mask{} })
}
