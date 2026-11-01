package vulkan_test

import (
	"image"
	"path/filepath"
	"strings"
	"testing"

	lgpu "github.com/stubbedev/lys-lib/gpu"

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

func TestOverlappingDamageBlendsOnce(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.OverlappingDamageBlendsOnce(t, r, target(t, r, rendertest.DamageSize))
}

func TestScaledDrawsKeepFlatColor(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.ScaledDrawsKeepFlatColor(t, r, target(t, r, rendertest.ScaledSize))
}

func TestMaskEdgesMatchTheSoftwareRendererAcrossA4KOutput(t *testing.T) {
	r := testenv.Vulkan(t)
	rendertest.MaskEdgesAcrossA4KOutput(t, r, target(t, r, rendertest.FourKStrip))
}

func TestAnAsyncReadSeesTheFrameItWasStartedAfter(t *testing.T) {
	rendertest.AsyncReadSeesItsFrame(t, testenv.Vulkan(t))
}

const lysLibPassAllocs = 6

func TestASteadyPassAllocatesOnlyInTheDevice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates on its own")
	}
	r := testenv.Vulkan(t)
	tex := rendertest.Load(t, r)
	tg := target(t, r, rendertest.Size)
	damage := render.Region{image.Rect(0, 0, 32, 32)}
	whole := render.FRect{W: float64(rendertest.TexSize.X), H: float64(rendertest.TexSize.Y)}
	var failed error
	draw := func() {
		p, err := r.Begin(tg, damage)
		if err != nil {
			failed = err
			return
		}
		p.FillRect(image.Rect(0, 0, 32, 32), render.Color{A: 0xff}, render.Mask{})
		p.DrawTexture(tex.ARGB, whole, image.Rectangle{Max: rendertest.TexSize}, render.Normal, 1, render.Mask{})
		p.DrawTexture(tex.XRGB, whole, image.Rectangle{Max: rendertest.TexSize}.Add(image.Pt(8, 8)), render.Normal, 1, render.Mask{})
		if err := p.End(); err != nil && failed == nil {
			failed = err
		}
	}
	draw()
	n := testing.AllocsPerRun(50, draw)
	if failed != nil {
		t.Fatal(failed)
	}
	if n > lysLibPassAllocs {
		t.Fatalf("a steady pass allocated %v times, and lys-lib's device accounts for %d", n, lysLibPassAllocs)
	}
}

func TestChromaSitingReachesTheDevice(t *testing.T) {
	for _, c := range []struct {
		in   gpu.Siting
		want lgpu.Siting
	}{{gpu.SitingUnspecified, lgpu.SitingDefault}, {gpu.SitedAt0, lgpu.SitedAt0}, {gpu.SitedAtHalf, lgpu.SitedAtHalf}, {gpu.SitedAt1, lgpu.SitingDefault}} {
		got := vulkan.YCbCrOf(gpu.YUVEncoding{Chroma: gpu.ChromaSiting{Horizontal: c.in, Vertical: c.in}})
		if got.Chroma.X != c.want || got.Chroma.Y != c.want {
			t.Errorf("siting %v: %+v, want %v on both axes", c.in, got.Chroma, c.want)
		}
	}
}

func hardware(t *testing.T) (*vulkan.Renderer, string) {
	t.Helper()
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		r, err := vulkan.OpenRenderNode(node)
		if err != nil {
			continue
		}
		t.Cleanup(func() {
			if err := r.Close(); err != nil {
				t.Error(err)
			}
		})
		t.Logf("Vulkan device: %s on %s", r.Device().Name(), node)
		return r, node
	}
	t.Skip("no Vulkan driver drives a render node here; unset VK_DRIVER_FILES on a GPU machine to run this")
	return nil, ""
}

func TestParityOnHardware(t *testing.T) {
	r, _ := hardware(t)
	rendertest.Parity(t, r, target(t, r, rendertest.Size))
}

func TestDMABUFTexturesOnHardware(t *testing.T) {
	r, node := hardware(t)
	rendertest.DMABUFTextures(t, r, node)
}

func TestExternalYUVTexturesOnHardware(t *testing.T) {
	r, node := hardware(t)
	rendertest.ExternalYUV(t, r, node)
}

func TestYUVHintsSelectTheConversionOnHardware(t *testing.T) {
	r, node := hardware(t)
	rendertest.YUVHintsSelectTheConversion(t, r, node)
}

func TestFrameFencesReportWhenTheGPUFinishedOnHardware(t *testing.T) {
	r, _ := hardware(t)
	rendertest.FrameFencesReportTheGPUFinishing(t, r)
}

func TestARenderNodeNamesTheMainDeviceOnHardware(t *testing.T) {
	r, node := hardware(t)
	got, err := r.RenderNode()
	if err != nil {
		t.Fatal(err)
	}
	if got != node {
		t.Fatalf("a renderer opened on %s names %s", node, got)
	}
}
