package drm

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
)

func gbmOn(t *testing.T, card string) *gbmAllocator {
	t.Helper()
	dev, err := kdrm.Open(card)
	if err != nil {
		t.Skipf("cannot open %s: %v", card, err)
	}
	t.Cleanup(func() { _ = dev.Close() })
	a, err := newGBMAllocator(dev, gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		t.Skipf("no EGL vendor drives this card's GBM device here (unset __EGL_VENDOR_LIBRARY_DIRS on a GPU machine): %v", err)
	}
	return a
}

var quadrantColors = [4]render.Color{
	{R: 0xff, A: 0xff},
	{G: 0xff, A: 0xff},
	{B: 0xff, A: 0xff},
	{R: 0xff, G: 0xff, A: 0xff},
}

func quadrant(i int, size image.Point) image.Rectangle {
	half := size.Div(2)
	r := image.Rectangle{Max: half}
	if i%2 == 1 {
		r.Min.X, r.Max.X = half.X, size.X
	}
	if i/2 == 1 {
		r.Min.Y, r.Max.Y = half.Y, size.Y
	}
	return r
}

func drawQuadrants(t *testing.T, r render.Renderer, target render.Target) {
	t.Helper()
	size := target.Size()
	p, err := r.Begin(target, render.Region{{Max: size}})
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range quadrantColors {
		p.FillRect(quadrant(i, size), c, render.Mask{})
	}
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
}

func blitPair(t *testing.T, a *blitAllocator, size image.Point) (*scanout, *shared) {
	t.Helper()
	mods, err := a.renderable(scanoutFormat)
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.local.allocate(size, scanoutFormat, mods)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := out.free(); err != nil {
			t.Error(err)
		}
	})
	s, err := a.share(size, scanoutFormat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.release(); err != nil {
			t.Error(err)
		}
	})
	if s.copy == nil {
		t.Fatal("the shared frame has no Vulkan copy")
	}
	return out, s
}

func TestABlitThroughAVulkanLinearCopyShowsTheFrameOnHardware(t *testing.T) {
	cards, err := filepath.Glob("/dev/dri/card*")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) == 0 {
		t.Skip("no DRM card")
	}
	unpin := gpu.PinThread()
	defer func() {
		if err := unpin(); err != nil {
			t.Error(err)
		}
	}()
	primary, local := gbmOn(t, cards[0]), gbmOn(t, cards[0])
	a := &blitAllocator{primary: primary, local: local}
	t.Cleanup(func() {
		if err := errors.Join(a.close(), primary.close()); err != nil {
			t.Error(err)
		}
	})
	t.Logf("%s rendering into a linear buffer: %v", primary.stack.GBM.Backend(), a.probe())
	if err := a.copyThroughVulkan(); err != nil {
		t.Fatalf("a linear copy through Vulkan on %s: %v", cards[0], err)
	}
	size := image.Pt(301, 203)
	out, s := blitPair(t, a, size)
	drawQuadrants(t, primary.stack.Renderer, s.target)
	start := time.Now()
	if err := a.blit(s, out.target, size); err != nil {
		t.Fatal(err)
	}
	pix := make([]byte, size.X*size.Y*4)
	read, ok := out.target.(render.ReadTarget)
	if !ok {
		t.Fatalf("the scanout target %T cannot be read", out.target)
	}
	if err := read.Read(pix, size.X*4); err != nil {
		t.Fatal(err)
	}
	t.Logf("a %v frame copied, blitted and read back in %v", size, time.Since(start))
	for i, c := range quadrantColors {
		q := quadrant(i, size)
		for _, at := range []image.Point{q.Min, q.Max.Sub(image.Pt(1, 1))} {
			o := at.Y*size.X*4 + at.X*4
			if got := [3]byte{pix[o+2], pix[o+1], pix[o]}; got != [3]byte{c.R, c.G, c.B} {
				t.Errorf("quadrant %d shows %v at %v, want %v", i, got, at, [3]byte{c.R, c.G, c.B})
			}
		}
	}
	big := image.Pt(2560, 1440)
	out, s = blitPair(t, a, big)
	drawQuadrants(t, primary.stack.Renderer, s.target)
	const frames = 60
	start = time.Now()
	for range frames {
		if err := a.blit(s, out.target, big); err != nil {
			t.Fatal(err)
		}
		if err := local.stack.Renderer.Finish(); err != nil {
			t.Fatal(err)
		}
	}
	per := time.Since(start) / frames
	t.Logf("a %v frame copied and blitted in %v", big, per)
	if per > time.Second/60 {
		t.Errorf("a %v frame takes %v to copy and blit, too long for 60 Hz", big, per)
	}
}
