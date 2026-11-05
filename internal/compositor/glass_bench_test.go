package compositor

import (
	"errors"
	"image"
	"testing"

	gtext "github.com/stubbedev/lys-lib/render"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

type finisher interface{ Finish() error }

func benchRenderer(b *testing.B, name string, run func(b *testing.B, r render.Renderer)) {
	b.Run(name, func(b *testing.B) {
		switch name {
		case "soft":
			run(b, soft.New())
		case "gles":
			d, err := testenv.GPU(b).Surfaceless()
			if err != nil {
				b.Fatal(err)
			}
			r, err := gles.New(d)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = errors.Join(r.Close(), d.Close()) }()
			run(b, r)
		case "vulkan":
			run(b, testenv.Vulkan(b))
		}
	})
}

func benchBackdrop(b *testing.B, r render.Renderer, size image.Point) render.Texture {
	b.Helper()
	pix := make([]byte, size.X*size.Y*4)
	for y := range size.Y {
		for x := range size.X {
			copy(pix[(y*size.X+x)*4:], []byte{byte(x), byte(y), byte(x + y), 0xff})
		}
	}
	t, err := r.NewTexture(render.ARGB8888, size, size.X*4, pix)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = t.Destroy() })
	return t
}

func BenchmarkBlurChainRedraw(b *testing.B) {
	for _, name := range []string{"soft", "gles", "vulkan"} {
		benchRenderer(b, name, func(b *testing.B, r render.Renderer) {
			area := image.Rect(0, 0, 1000, 600)
			tex := benchBackdrop(b, r, area.Size())
			c := &Compositor{renderer: r}
			pb := &pendingBlur{cache: &blurCache{}, spec: chainSpec{offset: 8, passes: 2}, area: area}
			below := []drawOp{{tex: tex, src: render.FRect{W: 1000, H: 600}, dst: area, alpha: 1}}
			b.Cleanup(func() { _ = pb.cache.destroy() })
			for b.Loop() {
				if err := c.renderBlur(r.NewTextureTarget, pb, below, colorOut{plans: &c.plans}, nil); err != nil {
					b.Fatal(err)
				}
				if f, ok := r.(finisher); ok {
					if err := f.Finish(); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func BenchmarkGlassComposite(b *testing.B) {
	full := render.Glass{Saturation: 1.5, Brightness: 1.05, Contrast: 1.1, Bezel: 16, Amount: 8}
	colour := render.Glass{Saturation: 1.5, Brightness: 1.05, Contrast: 1.1}
	for _, name := range []string{"soft", "gles", "vulkan"} {
		benchRenderer(b, name, func(b *testing.B, r render.Renderer) {
			size := image.Pt(1000, 600)
			tex := benchBackdrop(b, r, size)
			dst, err := r.NewTextureTarget(size)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = dst.Destroy() })
			m := render.Mask{Keep: render.RoundRect{Rect: image.Rectangle{Max: size}, Radius: 12}}
			src := render.FRect{W: 1000, H: 600}
			whole := render.Region{{Max: size}}
			for _, d := range []struct {
				name string
				draw func(render.Pass)
			}{
				{"plain", func(p render.Pass) { p.DrawTexture(tex, src, image.Rectangle{Max: size}, render.Normal, 1, m) }},
				{"colour", func(p render.Pass) { p.DrawGlass(tex, src, image.Rectangle{Max: size}, 1, m, colour) }},
				{"refraction", func(p render.Pass) { p.DrawGlass(tex, src, image.Rectangle{Max: size}, 1, m, full) }},
			} {
				b.Run(d.name, func(b *testing.B) {
					for b.Loop() {
						p, err := r.Begin(dst, whole)
						if err != nil {
							b.Fatal(err)
						}
						d.draw(p)
						if err := p.End(); err != nil {
							b.Fatal(err)
						}
						if f, ok := r.(finisher); ok {
							if err := f.Finish(); err != nil {
								b.Fatal(err)
							}
						}
					}
				})
			}
		})
	}
}

func BenchmarkGrainTile(b *testing.B) {
	for b.Loop() {
		gtext.GrainTile(1920, 1080)
	}
}
