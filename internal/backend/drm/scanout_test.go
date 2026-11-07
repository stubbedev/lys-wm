package drm

import (
	"image"
	"slices"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
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
	return r.scanoutAt(o, k, backend.Whole(k.d.Size), tear)
}

func (r *rig) scanoutAt(o *Output, k *fakeBuffer, at backend.Placement, tear bool) bool {
	r.t.Helper()
	ok, err := o.Scanout(k, k.d, at, func() error { return nil }, tear)
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

func placedBuffer(fd int, size image.Point) *fakeBuffer {
	return &fakeBuffer{gpu.DMABUF{Size: size, Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: fd, Stride: uint32(size.X) * 4}}}}
}

func primaryRects(c commit) (src [4]uint64, dst [4]uint64) {
	for i, p := range []uint32{propSrcX, propSrcY, propSrcW, propSrcH} {
		src[i] = c.values[[2]uint32{40, p}]
	}
	for i, p := range []uint32{propCRTCX, propCRTCY, propCRTCW, propCRTCH} {
		dst[i] = c.values[[2]uint32{40, p}]
	}
	return src, dst
}

func lastReal(commits []commit) commit {
	for _, c := range slices.Backward(commits) {
		if c.flags&kdrm.AtomicTestOnly == 0 {
			return c
		}
	}
	return commit{}
}

func TestScaledAndLetterboxedBuffersScanOutWithTheirRects(t *testing.T) {
	for _, tc := range []struct {
		name string
		size image.Point
		at   backend.Placement
		src  [4]uint64
		dst  [4]uint64
	}{
		{"a 1280x720 buffer scaled to the output", image.Pt(1280, 720), backend.Placement{Src: render.FRect{W: 1280, H: 720}, Dst: image.Rect(0, 0, 1920, 1080)}, [4]uint64{0, 0, 1280 << 16, 720 << 16}, [4]uint64{0, 0, 1920, 1080}},
		{"a 4:3 buffer pillarboxed", image.Pt(1440, 1080), backend.Placement{Src: render.FRect{W: 1440, H: 1080}, Dst: image.Rect(240, 0, 1680, 1080)}, [4]uint64{0, 0, 1440 << 16, 1080 << 16}, [4]uint64{240, 0, 1440, 1080}},
		{"a cropped half-pixel source", image.Pt(1920, 1080), backend.Placement{Src: render.FRect{X: 0.5, Y: 10, W: 1600, H: 900}, Dst: image.Rect(0, 0, 1920, 1080)}, [4]uint64{1 << 15, 10 << 16, 1600 << 16, 900 << 16}, [4]uint64{0, 0, 1920, 1080}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := start(t)
			o := r.lit()
			k := placedBuffer(7, tc.size)
			for i := range 3 {
				r.dev.commits = nil
				if !r.scanoutAt(o, k, tc.at, false) {
					t.Fatalf("scanout %d refused", i)
				}
				c := lastReal(r.dev.commits)
				src, dst := primaryRects(c)
				_, placed := c.values[[2]uint32{40, propSrcW}]
				if i == 0 && (src != tc.src || dst != tc.dst) {
					t.Fatalf("the primary plane got source %v and destination %v, want %v and %v", src, dst, tc.src, tc.dst)
				}
				if i > 0 && placed {
					t.Fatalf("scanout %d restated the unchanged rects: %+v", i, c)
				}
				r.dev.flip(t, 10, uint32(i+2))
				r.until("the flip", func() bool { return !o.flipping })
			}
			c := r.nextFrame(o)
			if src, dst := primaryRects(c); src != [4]uint64{0, 0, 1920 << 16, 1080 << 16} || dst != [4]uint64{0, 0, 1920, 1080} {
				t.Fatalf("the composited frame after it put the primary at source %v and destination %v, want the whole output", src, dst)
			}
		})
	}
}

func TestAPlaneThatCannotScaleIsTestedOncePerBufferAndRects(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.dev.refuseScaling = true
	k := placedBuffer(7, image.Pt(1280, 720))
	at := backend.Placement{Src: render.FRect{W: 1280, H: 720}, Dst: image.Rect(0, 0, 1920, 1080)}
	r.dev.commits = nil
	for range 100 {
		if r.scanoutAt(o, k, at, false) {
			t.Fatal("a scaled buffer scanned out on a plane that cannot scale")
		}
	}
	if n := tests(r.dev.commits, 0); n != 1 || len(r.dev.commits) != 1 {
		t.Fatalf("100 refused scaled scanouts made %d test commits in %d commits, want one", n, len(r.dev.commits))
	}
	other := backend.Placement{Src: render.FRect{W: 1280, H: 720}, Dst: image.Rect(0, 0, 1280, 720)}
	if !r.scanoutAt(o, k, other, false) {
		t.Fatal("the same buffer unscaled in a corner did not scan out")
	}
}

func TestAScaledTearingScanoutFlipsAsyncOnceItsRectsAreSet(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.asyncFlips = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.b.Outputs()[0]
	r.nextFrame(o)
	at := backend.Placement{Src: render.FRect{W: 1280, H: 720}, Dst: image.Rect(0, 0, 1920, 1080)}
	for i, want := range []bool{false, true, true} {
		r.dev.commits = nil
		if !r.scanoutAt(o, placedBuffer(7+i, image.Pt(1280, 720)), at, true) {
			t.Fatalf("scanout %d refused", i)
		}
		c := lastReal(r.dev.commits)
		if torn := c.flags&kdrm.AtomicPageFlipAsync != 0; torn != want {
			t.Fatalf("scaled tearing scanout %d flipped async %v, want %v: the rects change only in a vblank flip", i, torn, want)
		}
		r.dev.flip(t, 10, uint32(i+2))
		r.until("the flip", func() bool { return !o.flipping })
		if p := r.rec.presents[len(r.rec.presents)-1]; p.Torn != want {
			t.Fatalf("scaled tearing scanout %d presented torn %v, want %v", i, p.Torn, want)
		}
	}
}
