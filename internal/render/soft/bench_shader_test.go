package soft_test

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/shader"
)

func BenchmarkScreenShader1080p(b *testing.B) {
	paths, err := filepath.Glob("../../../docs/shaders/*.frag")
	if err != nil || len(paths) == 0 {
		b.Fatalf("no example shaders: %v", err)
	}
	size := image.Pt(1920, 1080)
	r := soft.New()
	src, err := r.NewTextureTarget(size)
	if err != nil {
		b.Fatal(err)
	}
	dst := soft.NewBuffer(size.X, size.Y)
	whole := render.Region{image.Rectangle{Max: size}}
	for _, path := range paths {
		text, err := os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
		p, err := shader.Compile(text)
		if err != nil {
			b.Fatal(err)
		}
		s, err := r.NewScreenShader(p)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(filepath.Base(path), func(b *testing.B) {
			for b.Loop() {
				pass, err := r.Begin(dst, whole)
				if err != nil {
					b.Fatal(err)
				}
				pass.DrawScreenShader(s, src.Texture(), image.Rectangle{Max: size}, 1)
				if err := pass.End(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
