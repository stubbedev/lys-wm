package drm

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
)

type added struct {
	backend.Handler
	outputs []backend.Output
}

func (a *added) OutputAdded(o backend.Output) error {
	a.outputs = append(a.outputs, o)
	return nil
}

func (a *added) OutputRemoved(backend.Output) error { return nil }

func connected(t *testing.T, card string) int {
	t.Helper()
	statuses, err := filepath.Glob(filepath.Join("/sys/class/drm", card+"-*", "status"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range statuses {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(b)) == "connected" {
			n++
		}
	}
	return n
}

func cursorImage(t *testing.T, o *Output) {
	t.Helper()
	cp, err := o.CursorPlane()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cp.BeginCursor(); !errors.Is(err, backend.ErrCursorReadback) {
		t.Fatalf("%s: a cursor drawn on the GPU renderer: %v, want ErrCursorReadback", o.Name(), err)
	}
	red := make([]byte, 8*8*4)
	for i := 0; i < len(red); i += 4 {
		red[i+2], red[i+3] = 0xff, 0xff
	}
	if err := errors.Join(cp.LoadCursor(red, 8*4, image.Pt(8, 8), image.Pt(1, 1)), cp.MoveCursor(image.Pt(50, 50))); err != nil {
		t.Fatal(err)
	}
	if len(o.cursor.buffers) != cursorBuffers || o.cursor.pending.fb == 0 || o.cursor.refused != nil {
		t.Fatalf("%s cursor: %d buffers, pending %+v, refused %v", o.Name(), len(o.cursor.buffers), o.cursor.pending, o.cursor.refused)
	}
	pix, pitch := o.cursor.pending.dumb.Pix, int(o.cursor.pending.dumb.Pitch)
	if [4]byte(pix[:4]) != [4]byte{0, 0, 0xff, 0xff} || [4]byte(pix[7*pitch+28:7*pitch+32]) != [4]byte{0, 0, 0xff, 0xff} || [4]byte(pix[8*pitch:8*pitch+4]) != [4]byte{} {
		t.Fatalf("%s cursor buffer: %v, %v, %v", o.Name(), pix[:4], pix[7*pitch+28:7*pitch+32], pix[8*pitch:8*pitch+4])
	}
	t.Logf("%s cursor plane %d, %v", o.Name(), o.cursor.plane.id, cp.CursorSize())
}

func gammaRamps(t *testing.T, o *Output) {
	t.Helper()
	size := o.GammaSize()
	if size == 0 {
		t.Logf("%s has no GAMMA_LUT", o.Name())
		return
	}
	ramps := make([]uint16, 3*size)
	for i := range ramps {
		ramps[i] = uint16(i % size * 0xffff / (size - 1))
	}
	if err := o.SetGamma(ramps); err != nil {
		t.Fatal(err)
	}
	if c := o.color[gammaLUT]; c.next == 0 || !c.dirty {
		t.Fatalf("%s gamma: %+v", o.Name(), c)
	}
	if err := o.SetGamma(nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s gamma LUT of %d entries", o.Name(), size)
}

func deepFrames(t *testing.T, o *Output) {
	t.Helper()
	layouts := o.primary.layouts(backend.Depth10)
	if len(layouts) == 0 {
		t.Logf("%s scans out no 10-bit format the renderer draws", o.Name())
		return
	}
	size := image.Pt(int(o.mode.HDisplay), int(o.mode.VDisplay))
	for _, l := range layouts {
		s, err := o.b.alloc.allocate(size, l)
		if err != nil {
			t.Logf("%s offers %v with %v for 10-bit frames, but they do not allocate: %v", o.Name(), l.format, l.modifiers, err)
			continue
		}
		if err := s.free(o.b.dev); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s draws 10-bit frames as %v", o.Name(), l.format)
		return
	}
	t.Fatalf("%s allocates none of the 10-bit layouts it offers: %+v", o.Name(), layouts)
}

func colorTransform(t *testing.T, o *Output) {
	t.Helper()
	if !o.HasColorTransform() {
		t.Logf("%s has no CTM", o.Name())
		return
	}
	if err := o.SetColorTransform(backend.Matrix{1, 0, 0, 0, 0.8, 0, 0, 0, 0.6}); err != nil {
		t.Fatal(err)
	}
	if c := o.color[colorMatrix]; c.next == 0 || !c.dirty {
		t.Fatalf("%s CTM: %+v", o.Name(), c)
	}
	if err := o.SetColorTransform(backend.Identity); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s takes a CTM", o.Name())
}

func TestStartOnEveryCardWithoutMaster(t *testing.T) {
	cards, err := filepath.Glob("/dev/dri/card*")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) == 0 {
		t.Skip("no DRM card")
	}
	for _, card := range cards {
		for _, vulkan := range []bool{false, true} {
			name := filepath.Base(card) + "/gles"
			if vulkan {
				name = filepath.Base(card) + "/vulkan"
			}
			t.Run(name, func(t *testing.T) { startWithoutMaster(t, card, vulkan) })
		}
	}
}

func scanoutFrame(t *testing.T, b *Backend, o *Output) {
	t.Helper()
	s := o.buffers[0]
	read, ok := s.target.(render.ReadTarget)
	if !ok {
		t.Fatalf("%s: a %T scanout buffer cannot be read back", o.Name(), s.target)
	}
	testFrame(t, b.Renderer(), read, o.Name())
}

func testFrame(t *testing.T, r render.Renderer, target render.ReadTarget, what string) {
	t.Helper()
	size := target.Size()
	p, err := r.Begin(target, render.Region{{Max: size}})
	if err != nil {
		t.Fatal(err)
	}
	p.FillRect(image.Rectangle{Max: size}, render.Color{G: 0x80, B: 0xff, A: 0xff}, render.Mask{})
	p.FillRect(image.Rect(0, 0, 8, 8), render.Color{R: 0xff, A: 0xff}, render.Mask{})
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
	pix := make([]byte, size.X*size.Y*4)
	if err := target.Read(pix, size.X*4); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		at   image.Point
		want [3]byte
	}{{image.Pt(1, 1), [3]byte{0, 0, 0xff}}, {size.Sub(image.Pt(2, 2)), [3]byte{0xff, 0x80, 0}}} {
		i := c.at.Y*size.X*4 + c.at.X*4
		if got := [3]byte(pix[i:]); got != c.want {
			t.Errorf("%s: scanout pixel %v is %v, want %v", what, c.at, got, c.want)
		}
	}
}

func startWithoutMaster(t *testing.T, card string, vulkan bool) {
	dev, err := kdrm.Open(card)
	if err != nil {
		t.Skipf("cannot open %s: %v", card, err)
	}
	defer dev.Close()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	vendors, err := edid.LoadVendors(edid.PNPPath(os.Getenv))
	if err != nil {
		t.Skipf("no PNP ID database: %v", err)
	}
	b, err := New(l, dev, Options{Paths: gpu.PathsFromEnv(os.Getenv), Vendors: vendors, Report: func(err error) { t.Error(err) }, Vulkan: vulkan, Master: WithoutMaster})
	if err != nil {
		t.Skipf("no EGL vendor or Vulkan driver drives this card here (unset __EGL_VENDOR_LIBRARY_DIRS and VK_DRIVER_FILES on a GPU machine): %v", err)
	}
	h := &added{}
	if err := b.Start(h); err != nil {
		t.Fatal(err)
	}
	if n := connected(t, filepath.Base(card)); len(h.outputs) != n {
		t.Fatalf("%d outputs for %d connected connectors", len(h.outputs), n)
	}
	for _, o := range b.Outputs() {
		if len(o.buffers) != buffersPerOut || o.buffers[0].fb == 0 || !o.State().Enabled() || !o.State().Mode.Preferred {
			t.Errorf("%s: %d buffers, state %+v", o.Name(), len(o.buffers), o.State())
		}
		if o.Description() == o.Name() {
			t.Errorf("%s has no EDID description", o.Name())
		}
		t.Logf("%s (%s) on CRTC %d and plane %d with modifiers %v, %+v", o.Name(), o.Description(), o.crtc.id, o.primary.id, o.primary.modifiers, o.State().Mode)
		scanoutFrame(t, b, o)
		cursorImage(t, o)
		gammaRamps(t, o)
		colorTransform(t, o)
		deepFrames(t, o)
	}
	t.Logf("atomic async page flips: %v", b.async)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMesaAndKMSShareNoHandlesOnHardware(t *testing.T) {
	cards, err := filepath.Glob("/dev/dri/card*")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) == 0 {
		t.Skip("no DRM card")
	}
	for _, card := range cards {
		t.Run(filepath.Base(card), func(t *testing.T) {
			dev, err := kdrm.Open(card)
			if err != nil {
				t.Skipf("cannot open %s: %v", card, err)
			}
			defer dev.Close()
			a, err := newGLESAllocator(dev, gpu.PathsFromEnv(os.Getenv))
			if err != nil {
				t.Skipf("no GLES on %s: %v", card, err)
			}
			defer func() {
				if err := a.close(); err != nil {
					t.Error(err)
				}
			}()
			size := image.Pt(64, 64)
			mods, err := a.renderable(scanoutFormat)
			if err != nil {
				t.Fatal(err)
			}
			files, err := renderFiles(dev)
			if err != nil {
				t.Fatal(err)
			}
			clientDev, err := files[0]()
			if err != nil {
				t.Fatal(err)
			}
			defer clientDev.Close()
			clientLib, err := gpu.LoadGBM(gpu.PathsFromEnv(os.Getenv))
			if err != nil {
				t.Fatal(err)
			}
			defer clientLib.Close()
			client, err := clientLib.Open(clientDev.FD())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			bo, err := client.Allocate(size, scanoutFormat, mods, gpu.UseRendering|gpu.UseScanout)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := bo.Close(); err != nil {
					t.Error(err)
				}
			}()
			tex, err := a.gpu.ImportTexture(bo.DMABUF)
			if err != nil {
				t.Fatal(err)
			}
			id, err := importFB(dev, bo.DMABUF, a.modifiers)
			if err != nil {
				t.Fatal(err)
			}
			if err := tex.Destroy(); err != nil {
				t.Fatal(err)
			}
			again, err := importFB(dev, bo.DMABUF, a.modifiers)
			if err != nil {
				t.Fatalf("importing the buffer again after Mesa let go of it: %v", err)
			}
			if err := errors.Join(dev.RemoveFB(id), dev.RemoveFB(again)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTenBitScanoutBuffersDrawOnEveryRenderNodeOnHardware(t *testing.T) {
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Skip("no render node")
	}
	for _, node := range nodes {
		for _, stack := range []struct {
			name string
			open func(*kdrm.Device, gpu.Paths) (*gbmAllocator, error)
		}{{"gles", newGLESAllocator}, {"vulkan", newVulkanAllocator}} {
			t.Run(filepath.Base(node)+"/"+stack.name, func(t *testing.T) { tenBitOnRenderNode(t, node, stack.open) })
		}
	}
}

func tenBitOnRenderNode(t *testing.T, node string, open func(*kdrm.Device, gpu.Paths) (*gbmAllocator, error)) {
	unpin := gpu.PinThread()
	defer func() {
		if err := unpin(); err != nil {
			t.Error(err)
		}
	}()
	a := stackOn(t, node, open)
	t.Cleanup(func() {
		if err := a.close(); err != nil {
			t.Error(err)
		}
	})
	size := image.Pt(64, 32)
	drawn := 0
	for _, f := range deepFormats {
		mods, err := a.renderable(f)
		if err != nil {
			t.Fatal(err)
		}
		bo, err := a.gbm.Allocate(size, f, mods, gpu.UseRendering|gpu.UseScanout)
		if err != nil {
			t.Logf("%s: GBM allocates no %v scanout buffer with the renderer's %d modifiers: %v", node, f, len(mods), err)
			continue
		}
		target, err := a.gpu.ImportTarget(bo.DMABUF)
		if err != nil {
			t.Errorf("%s: GBM allocated a %v buffer with modifier %v, but the renderer cannot draw into it: %v", node, f, bo.Modifier, err)
			if err := bo.Close(); err != nil {
				t.Error(err)
			}
			continue
		}
		testFrame(t, a.gpu, target, fmt.Sprintf("%s %v", node, f))
		if err := errors.Join(target.Destroy(), bo.Close()); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s draws 10-bit scanout buffers as %v with modifier %v", node, f, bo.Modifier)
		drawn++
	}
	if drawn == 0 {
		t.Fatalf("%s draws none of the 10-bit scanout formats %v", node, deepFormats)
	}
}
