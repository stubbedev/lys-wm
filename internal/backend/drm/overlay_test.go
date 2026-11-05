package drm

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func yuvOverlayRig(t *testing.T, planeProps bool) (*rig, *Output) {
	t.Helper()
	r := boot(t, func(d *fakeDevice) allocator {
		d.overlayZpos, d.overlayYUV = 2, planeProps
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	o.overlay.clients = []gpu.FormatModifier{{Format: scanoutFormat, Modifier: fourcc.Linear}, {Format: fourcc.NV12, Modifier: fourcc.Linear}}
	return r, o
}

func nv12(fd int, yuv gpu.YUVEncoding) gpu.DMABUF {
	return gpu.DMABUF{Size: image.Pt(1280, 720), Format: fourcc.NV12, Modifier: fourcc.Linear, YUV: yuv, Planes: []gpu.Plane{{FD: fd, Stride: 1280}, {FD: fd, Offset: 1280 * 720, Stride: 1280}}}
}

func colorOf(c commit) (encoding, rng uint64, ok bool) {
	encoding, hasEncoding := c.values[[2]uint32{42, propColorEncodingID}]
	rng, hasRange := c.values[[2]uint32{42, propColorRangeID}]
	return encoding, rng, hasEncoding && hasRange
}

func TestAYUVOverlayCarriesItsColourEncodingAndRange(t *testing.T) {
	r, o := yuvOverlayRig(t, true)
	dst := image.Rect(0, 0, 1280, 720)
	d := nv12(9, gpu.YUVEncoding{Coefficients: gpu.BT709, Range: gpu.LimitedRange})
	key := &fakeBuffer{d}
	if !o.TestOverlay(key, d, dst) {
		t.Fatal("a BT.709 limited NV12 overlay was refused")
	}
	if e, rng, ok := colorOf(r.dev.commits[len(r.dev.commits)-1]); !ok || e != 1 || rng != 0 {
		t.Fatalf("the overlay test set encoding %d and range %d (%v), want BT.709 limited", e, rng, ok)
	}
	if err := o.SetOverlay(key, d, dst, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if flipped, err := o.FlipOverlay(); err != nil || !flipped {
		t.Fatalf("FlipOverlay = %t, %v", flipped, err)
	}
	if e, rng, ok := colorOf(r.dev.commits[len(r.dev.commits)-1]); !ok || e != 1 || rng != 0 {
		t.Fatalf("the overlay flip set encoding %d and range %d (%v), want BT.709 limited", e, rng, ok)
	}
	r.dev.flip(t, 10, 2)
	r.until("the overlay's flip", func() bool { return !o.flipping })
	r.b.driver = nvidiaDriver
	auto := nv12(10, gpu.YUVEncoding{})
	if !o.TestOverlay(&fakeBuffer{auto}, auto, dst) {
		t.Fatal("an NV12 overlay without a colour representation was refused")
	}
	if e, rng, ok := colorOf(r.dev.commits[len(r.dev.commits)-1]); !ok || e != 1 || rng != 1 {
		t.Fatalf("an unspecified 720p NV12 overlay on NVIDIA set encoding %d and range %d (%v), want BT.709 full", e, rng, ok)
	}
}

func TestAYUVOverlayNeedsAPlaneThatTakesItsEncoding(t *testing.T) {
	r, o := yuvOverlayRig(t, false)
	d := nv12(9, gpu.YUVEncoding{Coefficients: gpu.BT709, Range: gpu.LimitedRange})
	if o.TestOverlay(&fakeBuffer{d}, d, image.Rect(0, 0, 1280, 720)) || len(r.dev.commits) != 0 {
		t.Fatalf("a YUV overlay on a plane without COLOR_ENCODING passed after %d commits", len(r.dev.commits))
	}
	rgb := gpu.DMABUF{Size: image.Pt(640, 360), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: 11, Stride: 640 * 4}}}
	if !o.TestOverlay(&fakeBuffer{rgb}, rgb, image.Rect(0, 0, 640, 360)) {
		t.Fatal("an RGB overlay was refused on a plane without COLOR_ENCODING")
	}
	if _, _, ok := colorOf(r.dev.commits[len(r.dev.commits)-1]); ok {
		t.Fatal("an RGB overlay carried a YUV encoding")
	}
}
