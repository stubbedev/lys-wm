package compositor

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

func reportLatencies(b *testing.B, samples []time.Duration) {
	b.Helper()
	slices.Sort(samples)
	at := func(q float64) float64 {
		return float64(samples[int(q*float64(len(samples)-1))]) / float64(time.Millisecond)
	}
	b.ReportMetric(at(0.5), "p50-ms")
	b.ReportMetric(at(0.99), "p99-ms")
	b.ReportMetric(at(1), "max-ms")
}

func BenchmarkStallShmUpload(b *testing.B) {
	full4K := image.Rect(0, 0, 3840, 2160)
	damages := []struct {
		name string
		size image.Point
		rect image.Rectangle
	}{
		{"4K-full", full4K.Max, full4K},
		{"1080p-full", image.Pt(1920, 1080), image.Rect(0, 0, 1920, 1080)},
		{"4K-terminal-line", full4K.Max, image.Rect(100, 800, 2100, 840)},
	}
	for _, name := range []string{"gles", "vulkan"} {
		benchRenderer(b, name, func(b *testing.B, r render.Renderer) {
			for _, d := range damages {
				b.Run(d.name, func(b *testing.B) {
					stride := d.size.X * 4
					pix := make([]byte, stride*d.size.Y)
					for i := range pix {
						pix[i] = byte(i)
					}
					tex, err := r.NewTexture(render.ARGB8888, d.size, stride, pix)
					if err != nil {
						b.Fatal(err)
					}
					defer func() { _ = tex.Destroy() }()
					target, err := r.NewTarget(image.Pt(16, 16))
					if err != nil {
						b.Fatal(err)
					}
					defer func() { _ = target.Destroy() }()
					data := pix[d.rect.Min.Y*stride+d.rect.Min.X*4:]
					samples := make([]time.Duration, 0, 1024)
					b.SetBytes(int64(d.rect.Dx() * d.rect.Dy() * 4))
					b.ReportAllocs()
					for b.Loop() {
						at := time.Now()
						if err := tex.Update(d.rect, stride, data); err != nil {
							b.Fatal(err)
						}
						samples = append(samples, time.Since(at))
						p, err := r.Begin(target, render.Region{image.Rectangle{Max: target.Size()}})
						if err != nil {
							b.Fatal(err)
						}
						p.DrawTexture(tex, render.FRect{W: 16, H: 16}, image.Rectangle{Max: target.Size()}, render.Normal, 1, render.Mask{})
						if err := p.End(); err != nil {
							b.Fatal(err)
						}
					}
					reportLatencies(b, samples)
				})
			}
		})
	}
}

func dropPageCache(b *testing.B, path string) {
	b.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		b.Fatalf("open %s to drop its pages: %v", path, err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED); err != nil {
		b.Fatalf("drop the pages of %s: %v", path, err)
	}
}

func BenchmarkStallCursorThemeMiss(b *testing.B) {
	theme := testenv.CursorTheme(b)
	file := filepath.Join(os.Getenv("LYS_WM_TEST_XCURSOR_PATH"), "Vanilla-DMZ", "cursors", "left_ptr")
	shapes := []struct {
		name string
		px   int
		cold bool
	}{
		{"native-24", 24, false},
		{"native-24-cold", 24, true},
		{"resampled-30", 30, false},
		{"resampled-36", 36, false},
		{"resampled-36-cold", 36, true},
		{"resampled-72", 72, false},
	}
	for _, name := range []string{"soft", "gles", "vulkan"} {
		benchRenderer(b, name, func(b *testing.B, r render.Renderer) {
			for _, s := range shapes {
				b.Run(s.name, func(b *testing.B) {
					c := &Compositor{cursors: CursorConfig{Theme: theme, Size: s.px}, renderer: r, pointers: map[cursorKey]*cursorTexture{}}
					samples := make([]time.Duration, 0, 1024)
					b.ReportAllocs()
					for b.Loop() {
						for _, ct := range c.pointers {
							if err := ct.tex.Destroy(); err != nil {
								b.Fatal(err)
							}
						}
						clear(c.pointers)
						if s.cold {
							dropPageCache(b, file)
						}
						at := time.Now()
						if _, err := c.cursorTextureAt("default", s.px); err != nil {
							b.Fatal(fmt.Errorf("the %s cursor: %w", s.name, err))
						}
						samples = append(samples, time.Since(at))
					}
					reportLatencies(b, samples)
				})
			}
		})
	}
}
