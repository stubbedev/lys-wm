package gles_test

import (
	"errors"
	"image"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
	"github.com/stubbedev/lys-wm/internal/render/rendertest"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

func renderer(t *testing.T) *gles.Renderer {
	t.Helper()
	return rendererOn(t, func(lib *gpu.Library) (*gpu.Display, error) { return lib.Surfaceless() })
}

func rendererOn(t *testing.T, open func(*gpu.Library) (*gpu.Display, error)) *gles.Renderer {
	t.Helper()
	d, err := open(testenv.GPU(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	r, err := gles.New(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func framebuffer(t *testing.T, r *gles.Renderer, size image.Point) *gles.Framebuffer {
	t.Helper()
	f, err := r.NewFramebuffer(size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Destroy(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func TestOverlappingDamageBlendsOnce(t *testing.T) {
	r := renderer(t)
	rendertest.OverlappingDamageBlendsOnce(t, r, framebuffer(t, r, rendertest.DamageSize))
}

func TestParityWithTheSoftwareRenderer(t *testing.T) {
	parity(t, renderer(t))
}

func hardware(t *testing.T) (*gles.Renderer, string) {
	t.Helper()
	var node string
	r := rendererOn(t, func(lib *gpu.Library) (*gpu.Display, error) {
		devices, err := lib.Devices()
		if err != nil {
			return nil, err
		}
		for _, dev := range devices {
			if dev.RenderNode() != "" && !dev.Software() {
				if d, err := lib.OnDevice(dev); err == nil {
					node = dev.RenderNode()
					return d, nil
				}
			}
		}
		t.Skip("no EGL vendor drives a render node here; unset __EGL_VENDOR_LIBRARY_DIRS on a GPU machine to run this")
		return nil, nil
	})
	return r, node
}

func TestParityOnHardware(t *testing.T) {
	r, _ := hardware(t)
	parity(t, r)
}

func TestDMABUFTexturesOnHardware(t *testing.T) {
	g, node := hardware(t)
	rendertest.DMABUFTextures(t, g, node)
}

func TestScaledDrawsKeepFlatColor(t *testing.T) {
	g := renderer(t)
	rendertest.ScaledDrawsKeepFlatColor(t, g, framebuffer(t, g, rendertest.ScaledSize))
}

func TestMaskEdgesMatchTheSoftwareRendererAcrossA4KOutput(t *testing.T) {
	g := renderer(t)
	rendertest.MaskEdgesAcrossA4KOutput(t, g, framebuffer(t, g, rendertest.FourKStrip))
}

func TestReadKeepsTheStridePadding(t *testing.T) {
	g := renderer(t)
	rendertest.ReadKeepsTheStridePadding(t, g, framebuffer(t, g, image.Pt(5, 3)))
}

func TestAnAsyncReadSeesTheFrameItWasStartedAfter(t *testing.T) {
	rendertest.AsyncReadSeesItsFrame(t, renderer(t))
}

func TestMisuseFailsPrecisely(t *testing.T) {
	g := renderer(t)
	other := renderer(t)
	if _, err := g.Begin(soft.NewBuffer(4, 4), nil); err == nil || !strings.Contains(err.Error(), "cannot render into *soft.Buffer") {
		t.Fatalf("a soft target: %v", err)
	}
	if _, err := g.NewTexture(render.ARGB8888, image.Pt(4, 4), 8, make([]byte, 64)); err == nil {
		t.Fatal("a stride shorter than a row was accepted")
	}
	tx := rendertest.Load(t, other).ARGB
	fb := framebuffer(t, g, image.Pt(8, 8))
	p, err := g.Begin(fb, render.Region{image.Rect(0, 0, 8, 8)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Begin(fb, nil); err == nil {
		t.Fatal("a second Begin was accepted")
	}
	p.DrawTexture(tx, render.FRect{W: 7, H: 5}, image.Rect(0, 0, 7, 5), render.Normal, 1, render.Mask{})
	if err := p.End(); err == nil || !strings.Contains(err.Error(), "another renderer") {
		t.Fatalf("a foreign texture: %v", err)
	}
	p, err = g.Begin(fb, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.FillRect(image.Rect(0, 0, 1, 1), render.Color{R: 0xff, A: 0x10}, render.Mask{})
	p.DrawTexture(rendertest.Load(t, g).ARGB, render.FRect{W: 8, H: 5}, image.Rect(0, 0, 8, 5), render.Normal, 1, render.Mask{})
	if err := p.End(); err == nil || !strings.Contains(err.Error(), "not premultiplied") {
		t.Fatalf("a straight-alpha color: %v", err)
	}
}

func TestExternalYUVTexturesOnHardware(t *testing.T) {
	g, node := hardware(t)
	rendertest.ExternalYUV(t, g, node)
}

func TestYUVHintsSelectTheConversionOnHardware(t *testing.T) {
	g, node := hardware(t)
	rendertest.YUVHintsSelectTheConversion(t, g, node)
}

func TestAPinnedThreadKeepsTheContextCurrentBetweenFrames(t *testing.T) {
	lib := testenv.GPU(t)
	surfaceless := func(*gpu.Library) (*gpu.Display, error) { return lib.Surfaceless() }
	r := rendererOn(t, surfaceless)
	other := rendererOn(t, surfaceless)
	tx := rendertest.Load(t, r)
	fb := framebuffer(t, r, rendertest.Size)
	otherFB := framebuffer(t, other, rendertest.Size)
	pix := make([]byte, rendertest.Size.X*rendertest.Size.Y*4)
	frame := func(r *gles.Renderer, fb *gles.Framebuffer) error {
		if r != other {
			if err := tx.ARGB.Update(image.Rectangle{Max: rendertest.TexSize}, rendertest.TexSize.X*4, rendertest.ARGB()); err != nil {
				return err
			}
		}
		p, err := r.Begin(fb, render.Region{{Max: rendertest.Size}})
		if err != nil {
			return err
		}
		p.FillRect(image.Rectangle{Max: rendertest.Size}, render.Color{R: 0xff, A: 0xff}, render.Mask{})
		if err := p.End(); err != nil {
			return err
		}
		return fb.Read(pix, rendertest.Size.X*4)
	}
	done := make(chan error, 1)
	var got struct{ first, steady, switched uint64 }
	go func() {
		unpin := gpu.PinThread()
		start := lib.ContextSwitches()
		err := frame(r, fb)
		got.first = lib.ContextSwitches() - start
		start = lib.ContextSwitches()
		for range 10 {
			err = errors.Join(err, frame(r, fb))
		}
		got.steady = lib.ContextSwitches() - start
		start = lib.ContextSwitches()
		err = errors.Join(err, frame(other, otherFB), frame(r, fb))
		got.switched = lib.ContextSwitches() - start
		done <- errors.Join(err, unpin())
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got.first > 1 || got.steady != 0 || got.switched != 2 {
		t.Fatalf("eglMakeCurrent calls: %d for the first frame, %d for ten more, %d for a frame on another display and one back; want at most 1, 0 and 2", got.first, got.steady, got.switched)
	}
	if err := frame(r, fb); err != nil {
		t.Fatalf("after the thread was unpinned: %v", err)
	}
	before := lib.ContextSwitches()
	if err := errors.Join(frame(r, fb), frame(r, fb)); err != nil {
		t.Fatal(err)
	}
	if n := lib.ContextSwitches() - before; n < 2 {
		t.Fatalf("an unpinned thread made the context current %d times in two frames, want once per call", n)
	}
}

func TestASteadyPassDoesNotAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates on its own")
	}
	g := renderer(t)
	tex := rendertest.Load(t, g)
	fb := framebuffer(t, g, rendertest.Size)
	damage := render.Region{image.Rect(0, 0, 32, 32)}
	var failed error
	draw := func() {
		p, err := g.Begin(fb, damage)
		if err != nil {
			failed = err
			return
		}
		p.FillRect(image.Rect(0, 0, 32, 32), render.Color{A: 0xff}, render.Mask{})
		p.DrawTexture(tex.ARGB, render.FRect{W: float64(rendertest.TexSize.X), H: float64(rendertest.TexSize.Y)}, image.Rect(0, 0, rendertest.TexSize.X, rendertest.TexSize.Y), render.Normal, 1, render.Mask{})
		p.DrawTexture(tex.XRGB, render.FRect{W: float64(rendertest.TexSize.X), H: float64(rendertest.TexSize.Y)}, image.Rect(8, 8, 8+rendertest.TexSize.X, 8+rendertest.TexSize.Y), render.Normal, 1, render.Mask{})
		if err := p.End(); err != nil && failed == nil {
			failed = err
		}
	}
	allocs := make(chan float64, 1)
	unpinned := make(chan error, 1)
	go func() {
		unpin := gpu.PinThread()
		draw()
		allocs <- testing.AllocsPerRun(50, draw)
		unpinned <- unpin()
	}()
	n := <-allocs
	if err := errors.Join(failed, <-unpinned); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a steady pass on a pinned thread allocated %v times", n)
	}
}

func TestASteadyTextureUpdateDoesNotAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates on its own")
	}
	g := renderer(t)
	size := image.Pt(256, 256)
	stride := size.X * 4
	pixels := make([]byte, stride*size.Y)
	tex, err := g.NewTexture(render.ARGB8888, size, stride, pixels)
	if err != nil {
		t.Fatal(err)
	}
	var failed error
	update := func() {
		if err := tex.Update(image.Rectangle{Max: size}, stride, pixels); err != nil && failed == nil {
			failed = err
		}
	}
	allocs := make(chan float64, 1)
	unpinned := make(chan error, 1)
	go func() {
		unpin := gpu.PinThread()
		update()
		allocs <- testing.AllocsPerRun(50, update)
		unpinned <- unpin()
	}()
	n := <-allocs
	if err := errors.Join(failed, <-unpinned); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a steady texture update on a pinned thread allocated %v times", n)
	}
}

func TestFrameFencesReportWhenTheGPUFinishedOnHardware(t *testing.T) {
	g, _ := hardware(t)
	rendertest.FrameFencesReportTheGPUFinishing(t, g)
}

func parity(t *testing.T, g *gles.Renderer) {
	t.Helper()
	rendertest.Parity(t, g, framebuffer(t, g, rendertest.Size))
}

func TestEveryTextureFormatDrawsItsColour(t *testing.T) {
	g := renderer(t)
	rendertest.EveryFormat(t, g, framebuffer(t, g, image.Pt(1, 1)))
}

func TestUploadsAcrossBands(t *testing.T) {
	g := renderer(t)
	rendertest.UploadsAcrossBands(t, g, framebuffer(t, g, rendertest.UploadTargetSize()))
}

func TestUploadsAcrossBandsWithoutAnUnpackBuffer(t *testing.T) {
	g := renderer(t)
	gles.DisableUnpackBuffer(g)
	rendertest.UploadsAcrossBands(t, g, framebuffer(t, g, rendertest.UploadTargetSize()))
}

func TestSurvivesAFaultingUpload(t *testing.T) {
	g := renderer(t)
	rendertest.SurvivesAFaultingUpload(t, g, framebuffer(t, g, image.Pt(1, 1)))
}

func TestSurvivesAFaultingUploadWithoutAnUnpackBuffer(t *testing.T) {
	g := renderer(t)
	gles.DisableUnpackBuffer(g)
	rendertest.SurvivesAFaultingUpload(t, g, framebuffer(t, g, image.Pt(1, 1)))
}

func TestStreamedUpdatesStayInOrder(t *testing.T) {
	g := renderer(t)
	rendertest.StreamedUpdatesStayInOrder(t, g, framebuffer(t, g, image.Pt(1, 1)))
}

func TestTheRendererKnowsItsNameOutsideItsContext(t *testing.T) {
	if name := renderer(t).Name(); name == "" {
		t.Fatal("no GL_RENDERER: glGetString was asked without a current context")
	}
}
