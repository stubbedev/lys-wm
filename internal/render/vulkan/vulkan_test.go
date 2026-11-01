package vulkan_test

import (
	"image"
	"strings"
	"testing"

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

func TestUploadsAcrossBands(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.UploadsAcrossBands(t, r, target(t, r, rendertest.UploadTargetSize()))
}

func TestSurvivesAFaultingUpload(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.SurvivesAFaultingUpload(t, r, target(t, r, image.Pt(1, 1)))
}

func TestStreamedUpdatesStayInOrder(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.StreamedUpdatesStayInOrder(t, r, target(t, r, image.Pt(1, 1)))
}
