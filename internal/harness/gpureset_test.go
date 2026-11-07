package harness

import (
	"errors"
	"image"
	"strings"
	"testing"

	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type resetRig struct {
	t     *testing.T
	h     *Harness
	c     *testclient.Client
	child *cw.WlSurface
	top   *cw.WlSurface
	buf   *cw.WlBuffer
	got   *reports
}

func newResetRig(t *testing.T, start gpuStarter, dmabuf bool) *resetRig {
	t.Helper()
	rig := &resetRig{t: t, got: &reports{}}
	rig.h = start(t, func(cfg *compositor.Config) { cfg.Report = rig.got.report }, small)
	rig.c = testclient.Connect(t, rig.h.Socket())
	w := rig.c.Window("reset", testclient.Fill(0, 0, 0xff))
	rig.c.DispatchUntil("the window to map", w.Mapped)
	rig.child, rig.top = rig.c.Surface(), w.Surface
	sub, err := rig.c.Subcompositor.GetSubsurface(rig.child, w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	blue := testclient.Fill(0xff, 0, 0)
	if dmabuf {
		rig.buf = rig.c.DMABUF(image.Pt(16, 16), blue).WL
	} else {
		rig.buf = rig.c.Buffer(image.Pt(16, 16), 0, blue)
	}
	if err := errors.Join(sub.SetPosition(10, 10), rig.child.Attach(rig.buf, 0, 0), rig.child.DamageBuffer(0, 0, 16, 16), rig.child.Commit(), w.Surface.Commit()); err != nil {
		t.Fatal(err)
	}
	rig.c.Sync()
	rig.h.Eventually("the window and its subsurface", rig.shown)
	rig.got.take()
	return rig
}

func (rig *resetRig) shown(b *headless.Backend, c *compositor.Compositor) bool {
	return showing(120, 80, [3]byte{0xff, 0, 0})(b, c) && showing(18, 18, [3]byte{0, 0, 0xff})(b, c)
}

func (rig *resetRig) loseInFrame() uint64 {
	rig.t.Helper()
	var presents uint64
	rig.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		presents = b.Outputs()[0].Presents()
		b.Outputs()[0].LoseDeviceInFrame()
		return nil
	})
	if err := errors.Join(rig.child.Attach(rig.buf, 0, 0), rig.child.DamageBuffer(0, 0, 4, 4), rig.child.Commit(), rig.top.Commit()); err != nil {
		rig.t.Fatal(err)
	}
	rig.c.Sync()
	return presents
}

func TestAGPUResetMidFrameRebuildsEverythingAndKeepsTheSession(t *testing.T) {
	t.Run("soft", func(t *testing.T) { testAGPUReset(t, StartWith, false) })
	onEachDMABUFRenderer(t, func(t *testing.T, start gpuStarter) { testAGPUReset(t, start, true) })
}

func testAGPUReset(t *testing.T, start gpuStarter, dmabuf bool) {
	rig := newResetRig(t, start, dmabuf)
	var imports uint64
	rig.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		imports = b.Renderer().Counts().Imports
		return nil
	})
	presents := rig.loseInFrame()
	rig.h.Eventually("the reset recovered and the next frame presented", func(b *headless.Backend, c *compositor.Compositor) bool {
		return c.GPUResets() == 1 && b.Outputs()[0].Presents() >= presents+2
	})
	rig.h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		if got := b.Outputs()[0].LastDamage().AreaIn(image.Rectangle{Max: small.Size}); got != small.Size.X*small.Size.Y {
			t.Errorf("the frame after the reset repainted %d px, want the whole %v output", got, small.Size)
		}
		shm, dmabufs := uint64(2), uint64(0)
		if dmabuf {
			shm, dmabufs = 1, 1
		}
		if got := c.Reuploads(); got != shm {
			t.Errorf("the reset uploaded %d shm surfaces again, want %d", got, shm)
		}
		if got := b.Renderer().Counts().Imports - imports; got != dmabufs {
			t.Errorf("the reset imported %d dmabufs again, want the %d live", got, dmabufs)
		}
		if err := b.Renderer().Lost(); err != nil {
			t.Errorf("the renderer is still lost: %v", err)
		}
		return nil
	})
	rig.h.Eventually("the window and its subsurface after the reset", rig.shown)
	rig.c.Sync()
	errs := rig.got.take()
	resets := 0
	for _, err := range errs {
		if strings.Contains(err.Error(), "the GPU was reset") {
			resets++
		}
	}
	if resets != 1 {
		t.Errorf("reported %v, want one GPU reset", errs)
	}
}

func TestAFourthGPUResetWithinTheWindowEndsTheSessionReportingAll(t *testing.T) {
	rig := newResetRig(t, StartWith, false)
	for i := range lgpu.LossLimit {
		presents := rig.loseInFrame()
		rig.h.Eventually("the reset recovered", func(b *headless.Backend, c *compositor.Compositor) bool {
			return c.GPUResets() == uint64(i+1) && b.Outputs()[0].Presents() >= presents+2
		})
	}
	rig.loseInFrame()
	err := rig.h.Ended()
	if !errors.Is(err, backend.ErrDeviceLost) || !backend.Fatal(err) {
		t.Fatalf("the session ended with %v, want a fatal lost device", err)
	}
	if n := strings.Count(err.Error(), "raised by LoseDevice"); n != lgpu.LossLimit+1 {
		t.Fatalf("the end reported %d losses, want all %d: %v", n, lgpu.LossLimit+1, err)
	}
}
