package drm

import (
	"image"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func clientBuffer(fd int, modifier fourcc.Modifier) *fakeBuffer {
	return &fakeBuffer{gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: modifier, Planes: []gpu.Plane{{FD: fd, Stride: 1920 * 4}}}}
}

func tests(commits []commit, flag uint32) int {
	n := 0
	for _, c := range commits {
		if c.flags&kdrm.AtomicTestOnly != 0 && c.flags&flag == flag {
			n++
		}
	}
	return n
}

func (r *rig) scanout(o *Output, k *fakeBuffer, tear bool) bool {
	r.t.Helper()
	ok, err := o.Scanout(k, k.d, func() error { return nil }, tear)
	if err != nil {
		r.t.Fatal(err)
	}
	return ok
}

func TestARefusedScanoutIsTestedOncePerBuffer(t *testing.T) {
	r := start(t)
	o := r.lit()
	refused := clientBuffer(7, fourcc.Linear)
	r.dev.refuseFrames = func(fb uint64) bool { return r.dev.scanouts[uint32(fb)] == primeHandles+7 }
	for range 3 {
		if r.scanout(o, refused, false) {
			t.Fatal("a refused buffer scanned out")
		}
	}
	if n := tests(r.dev.commits, 0); n != 1 || len(r.dev.commits) != 1 {
		t.Fatalf("%d tests in %d commits for a buffer refused three times, want one", n, len(r.dev.commits))
	}
	if !r.scanout(o, clientBuffer(8, fourcc.Linear), false) {
		t.Fatal("another buffer was refused with the first")
	}
	r.dev.flip(t, 10, 2)
	r.until("the flip", func() bool { return !o.flipping })
	if err := o.ForgetScanout(refused); err != nil {
		t.Fatal(err)
	}
	r.dev.commits = nil
	if r.scanout(o, refused, false); tests(r.dev.commits, 0) != 1 {
		t.Fatalf("a buffer imported again after it was forgotten was not tested: %+v", r.dev.commits)
	}
}

func TestARefusedAsyncFlipIsTestedOncePerBuffer(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.asyncFlips, d.refuseAsync = true, true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	torn := clientBuffer(7, fourcc.Linear)
	for i := range 3 {
		if !r.scanout(o, torn, true) {
			t.Fatal("a buffer whose async flip is refused did not flip on the vblank")
		}
		r.dev.flip(t, 10, uint32(2+i))
		r.until("the flip", func() bool { return !o.flipping })
		if !r.scanout(o, clientBuffer(8, fourcc.Linear), false) {
			t.Fatal("the buffer between did not scan out")
		}
		r.dev.flip(t, 10, uint32(10+i))
		r.until("the flip", func() bool { return !o.flipping })
	}
	if n := tests(r.dev.commits, kdrm.AtomicPageFlipAsync); n != 1 {
		t.Fatalf("%d async flip tests for one buffer, want one", n)
	}
}

func TestAFailedImportIsAttemptedOnce(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.dev.refuseAddFB = true
	k := clientBuffer(7, fourcc.Linear)
	for range 3 {
		if r.scanout(o, k, false) {
			t.Fatal("a buffer the device cannot import scanned out")
		}
	}
	if r.dev.imports != 1 || len(r.dev.commits) != 0 {
		t.Fatalf("%d imports and %d commits for one unimportable buffer", r.dev.imports, len(r.dev.commits))
	}
	r.dev.refuseAddFB = false
	if err := o.ForgetScanout(k); err != nil {
		t.Fatal(err)
	}
	if !r.scanout(o, k, false) || r.dev.imports != 2 {
		t.Fatalf("a forgotten buffer was not imported again: %d imports", r.dev.imports)
	}
}

func TestAScanoutThePrimaryPlaneCannotTakeIsNotTried(t *testing.T) {
	r := start(t)
	o := r.lit()
	if r.scanout(o, clientBuffer(7, 9), false) || r.dev.imports != 0 || len(r.dev.commits) != 0 {
		t.Fatalf("a modifier the primary plane lacks: %d imports, %d commits", r.dev.imports, len(r.dev.commits))
	}
}

func TestAnAsyncFlipIsNotTestedForAFormatTheAsyncListLacks(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.asyncFlips, d.asyncFormats = true, true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	if !r.scanout(o, clientBuffer(7, fourcc.Linear), true) {
		t.Fatal("a linear buffer did not scan out")
	}
	if n := tests(r.dev.commits, kdrm.AtomicPageFlipAsync); n != 0 || r.dev.commits[len(r.dev.commits)-1].flags&kdrm.AtomicPageFlipAsync != 0 {
		t.Fatalf("a linear buffer the async list lacks was tested async %d times: %+v", n, r.dev.commits)
	}
	r.dev.flip(t, 10, 2)
	r.until("the flip", func() bool { return !o.flipping })
	if !r.scanout(o, clientBuffer(8, 7), true) || r.dev.commits[len(r.dev.commits)-1].flags&kdrm.AtomicPageFlipAsync == 0 {
		t.Fatalf("a buffer the async list holds did not flip async: %+v", r.dev.commits[len(r.dev.commits)-1])
	}
}

func TestARememberedAsyncFlipCarriesItsOwnBuffer(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.asyncFlips = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	torn, between := clientBuffer(7, fourcc.Linear), clientBuffer(8, fourcc.Linear)
	for i, k := range []*fakeBuffer{torn, between, torn} {
		if !r.scanout(o, k, k == torn) {
			t.Fatalf("scanout %d did not show", i)
		}
		r.dev.flip(t, 10, uint32(2+i))
		r.until("the flip", func() bool { return !o.flipping })
	}
	last := r.dev.commits[len(r.dev.commits)-1]
	if last.flags&kdrm.AtomicTestOnly != 0 || last.flags&kdrm.AtomicPageFlipAsync == 0 || len(last.values) != 1 || r.dev.scanouts[uint32(last.values[[2]uint32{40, propFB}])] != primeHandles+7 {
		t.Fatalf("the second async flip of a tested buffer committed %+v", last)
	}
}

func TestACompositedFrameOfATearingSurfaceFlipsAsync(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.asyncFlips = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.b.Outputs()[0]
	o.TearFrame(true)
	if first := r.nextFrame(o); first.flags&kdrm.AtomicPageFlipAsync != 0 {
		t.Fatalf("the modeset flipped async: %+v", first)
	}
	torn := r.nextFrame(o)
	if torn.flags != planeFlags|kdrm.AtomicPageFlipAsync || len(torn.values) != 2 || torn.values[[2]uint32{40, propFB}] == 0 || torn.values[[2]uint32{40, propInFence}] != uint64(r.alloc.fences[len(r.alloc.fences)-1]) {
		t.Fatalf("a composited frame of a tearing surface committed %+v, want an async flip of FB_ID and its fence", torn)
	}
	if p := r.rec.presents[len(r.rec.presents)-1]; !p.Torn {
		t.Fatalf("the async frame was presented as %+v, not torn", p)
	}
	o.TearFrame(false)
	if vsync := r.nextFrame(o); vsync.flags&kdrm.AtomicPageFlipAsync != 0 || r.rec.presents[len(r.rec.presents)-1].Torn {
		t.Fatalf("a frame after tearing stopped flipped async: %+v", vsync)
	}
	o.TearFrame(true)
	r.dev.refuseAsync = true
	if refused := r.nextFrame(o); refused.flags&kdrm.AtomicPageFlipAsync != 0 || refused.values[[2]uint32{40, propFB}] == 0 {
		t.Fatalf("a composited async flip the driver refuses did not fall back to a vblank flip: %+v", refused)
	}
}
