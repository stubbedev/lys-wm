package vulkan_test

import (
	"image"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/rendertest"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

var _ render.Renderer = (*vulkan.Renderer)(nil)

func target(t *testing.T, r *vulkan.Renderer, size image.Point) render.ReadTarget {
	t.Helper()
	tg, err := r.NewTarget(size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tg.Destroy(); err != nil {
			t.Error(err)
		}
	})
	return tg
}

func TestParityWithTheSoftwareRenderer(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.Parity(t, r, target(t, r, rendertest.Size))
}

func TestEveryTextureFormatDrawsItsColour(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.EveryFormat(t, r, target(t, r, image.Pt(1, 1)))
}

func TestReadKeepsTheStridePadding(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.ReadKeepsTheStridePadding(t, r, target(t, r, image.Pt(5, 3)))
}

func TestMisuseFailsPrecisely(t *testing.T) {
	r := testenv.Vulkan(t)
	other := testenv.Vulkan(t)
	if _, err := r.Begin(soft.NewBuffer(4, 4), nil); err == nil || !strings.Contains(err.Error(), "cannot render into *soft.Buffer") {
		t.Fatalf("a soft target: %v", err)
	}
	if _, err := r.NewTexture(render.ARGB8888, image.Pt(4, 4), 8, make([]byte, 64)); err == nil {
		t.Fatal("a stride shorter than a row was accepted")
	}
	tg := target(t, r, image.Pt(8, 8))
	if _, err := r.Begin(target(t, other, image.Pt(8, 8)), nil); err == nil || !strings.Contains(err.Error(), "another renderer") {
		t.Fatalf("another renderer's target: %v", err)
	}
	p, err := r.Begin(tg, render.Region{image.Rect(0, 0, 8, 8)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Begin(tg, nil); err == nil {
		t.Fatal("a second Begin was accepted")
	}
	p.DrawTexture(rendertest.Load(t, other).ARGB, render.FRect{W: 7, H: 5}, image.Rect(0, 0, 7, 5), render.Normal, 1, render.Mask{})
	if err := p.End(); err == nil || !strings.Contains(err.Error(), "another renderer") {
		t.Fatalf("a foreign texture: %v", err)
	}
	for name, draw := range map[string]func(render.Pass){
		"not premultiplied": func(p render.Pass) { p.FillRect(image.Rect(0, 0, 1, 1), render.Color{R: 0xff, A: 0x10}, render.Mask{}) },
		"source outside texture": func(p render.Pass) {
			p.DrawTexture(rendertest.Load(t, r).ARGB, render.FRect{W: 8, H: 5}, image.Rect(0, 0, 8, 5), render.Normal, 1, render.Mask{})
		},
		"alpha above one": func(p render.Pass) {
			p.DrawTexture(rendertest.Load(t, r).ARGB, render.FRect{W: 7, H: 5}, image.Rect(0, 0, 7, 5), render.Normal, 2, render.Mask{})
		},
	} {
		p, err := r.Begin(tg, nil)
		if err != nil {
			t.Fatal(err)
		}
		draw(p)
		if err := p.End(); err == nil {
			t.Errorf("%s: End succeeded", name)
		}
	}
}

func TestScanoutTargetsRoundTripAsDMABUFsOnHardware(t *testing.T) {
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Skip("no render node")
	}
	r, err := vulkan.OpenRenderNode(nodes[0])
	if err != nil {
		t.Skipf("no Vulkan device for %s: %v", nodes[0], err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	if node, err := r.RenderNode(); err != nil || node != nodes[0] {
		t.Fatalf("RenderNode() = %q, %v; want %q", node, err, nodes[0])
	}
	formats, err := r.DMABUFFormats()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(formats, func(f gpu.FormatModifier) bool { return f.Format == fourcc.XRGB8888 }) {
		t.Fatalf("%s imports no XRGB8888 dmabufs: %v", r.Device().Name(), formats)
	}
	mods, err := r.RenderableModifiers(fourcc.XRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	size := image.Pt(37, 21)
	scanout, err := r.NewScanoutTarget(size, fourcc.XRGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := scanout.Destroy(); err != nil {
			t.Error(err)
		}
	})
	rendertest.Paint(t, r, scanout, render.Region{{Max: size}}, func(p render.Pass) {
		p.FillRect(image.Rectangle{Max: size}, render.Color{G: 0x80, B: 0xff, A: 0xff}, render.Mask{})
		p.FillRect(image.Rect(0, 0, 10, 10), render.Color{R: 0xff, A: 0xff}, render.Mask{})
	})
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	tex, err := r.ImportTexture(scanout.Dmabuf())
	if err != nil {
		t.Fatalf("import the scanout target's %v dmabuf: %v", scanout.Dmabuf().Modifier, err)
	}
	t.Cleanup(func() {
		if err := tex.Destroy(); err != nil {
			t.Error(err)
		}
	})
	copied := target(t, r, size)
	rendertest.Paint(t, r, copied, render.Region{{Max: size}}, func(p render.Pass) {
		p.DrawTexture(tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Max: size}, render.Normal, 1, render.Mask{})
	})
	pix := make([]byte, size.X*size.Y*4)
	if err := copied.Read(pix, size.X*4); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		at   image.Point
		want [4]byte
	}{{image.Pt(1, 1), [4]byte{0, 0, 0xff, 0xff}}, {image.Pt(30, 15), [4]byte{0xff, 0x80, 0, 0xff}}} {
		if got := [4]byte(pix[c.at.Y*size.X*4+c.at.X*4:]); got != c.want {
			t.Errorf("%v: %v, want %v", c.at, got, c.want)
		}
	}
}
