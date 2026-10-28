package drm

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	kdrm "github.com/stubbedev/lys-wm/internal/drm"
	"github.com/stubbedev/lys-wm/internal/edid"
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
	f, err := cp.BeginCursor()
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rectangle{Max: cp.CursorSize()}})
	if err != nil {
		t.Fatal(err)
	}
	p.Clear(render.Color{})
	p.FillRect(image.Rect(0, 0, 8, 8), render.Color{R: 0xff, A: 0xff})
	if err := errors.Join(p.End(), cp.SetCursor(image.Pt(1, 1)), cp.MoveCursor(image.Pt(50, 50))); err != nil {
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
	l, ok := o.primary.depth(backend.Depth10)
	if !ok {
		t.Logf("%s scans out no 10-bit format the renderer draws", o.Name())
		return
	}
	s, err := o.b.alloc.allocate(image.Pt(int(o.mode.HDisplay), int(o.mode.VDisplay)), l.format, l.modifiers)
	if err != nil {
		t.Fatalf("%s offers %v with %v for 10-bit frames, but they do not allocate: %v", o.Name(), l.format, l.modifiers, err)
	}
	if err := s.free(); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s draws 10-bit frames as %v", o.Name(), l.format)
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
		t.Run(filepath.Base(card), func(t *testing.T) { startWithoutMaster(t, card) })
	}
}

func startWithoutMaster(t *testing.T, card string) {
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
	b, err := New(l, dev, Options{Paths: gpu.PathsFromEnv(os.Getenv), Vendors: vendors, Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Skipf("no EGL vendor drives this card's GBM device here (unset __EGL_VENDOR_LIBRARY_DIRS on a GPU machine): %v", err)
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
