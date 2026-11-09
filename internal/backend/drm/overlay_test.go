package drm

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func yuvOverlayRig(t *testing.T, planeProps bool) (*rig, *Output) {
	t.Helper()
	r := boot(t, func(d *fakeDevice) allocator {
		d.overlayZpos, d.overlayYUV = 2, planeProps
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	o.overlays[0].clients = []gpu.FormatModifier{{Format: scanoutFormat, Modifier: fourcc.Linear}, {Format: fourcc.NV12, Modifier: fourcc.Linear}}
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
	if !testOverlay(o, key, d, dst) {
		t.Fatal("a BT.709 limited NV12 overlay was refused")
	}
	if e, rng, ok := colorOf(r.dev.commits[len(r.dev.commits)-1]); !ok || e != 1 || rng != 0 {
		t.Fatalf("the overlay test set encoding %d and range %d (%v), want BT.709 limited", e, rng, ok)
	}
	if err := o.SetOverlays([]backend.Overlay{overlayLayer(key, d, dst, func() error { return nil })}); err != nil {
		t.Fatal(err)
	}
	if flip, err := o.FlipOverlays(); err != nil || flip != backend.OverlaysFlipped {
		t.Fatalf("FlipOverlay = %d, %v", flip, err)
	}
	if e, rng, ok := colorOf(r.dev.commits[len(r.dev.commits)-1]); !ok || e != 1 || rng != 0 {
		t.Fatalf("the overlay flip set encoding %d and range %d (%v), want BT.709 limited", e, rng, ok)
	}
	r.dev.flip(t, 10, 2)
	r.until("the overlay's flip", func() bool { return !o.flipping })
	r.b.driver = nvidiaDriver
	auto := nv12(10, gpu.YUVEncoding{})
	if !testOverlay(o, &fakeBuffer{auto}, auto, dst) {
		t.Fatal("an NV12 overlay without a colour representation was refused")
	}
	if e, rng, ok := colorOf(r.dev.commits[len(r.dev.commits)-1]); !ok || e != 1 || rng != 1 {
		t.Fatalf("an unspecified 720p NV12 overlay on NVIDIA set encoding %d and range %d (%v), want BT.709 full", e, rng, ok)
	}
}

func TestAYUVOverlayNeedsAPlaneThatTakesItsEncoding(t *testing.T) {
	r, o := yuvOverlayRig(t, false)
	d := nv12(9, gpu.YUVEncoding{Coefficients: gpu.BT709, Range: gpu.LimitedRange})
	if testOverlay(o, &fakeBuffer{d}, d, image.Rect(0, 0, 1280, 720)) || len(r.dev.commits) != 0 {
		t.Fatalf("a YUV overlay on a plane without COLOR_ENCODING passed after %d commits", len(r.dev.commits))
	}
	rgb := gpu.DMABUF{Size: image.Pt(640, 360), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: 11, Stride: 640 * 4}}}
	if !testOverlay(o, &fakeBuffer{rgb}, rgb, image.Rect(0, 0, 640, 360)) {
		t.Fatal("an RGB overlay was refused on a plane without COLOR_ENCODING")
	}
	if _, _, ok := colorOf(r.dev.commits[len(r.dev.commits)-1]); ok {
		t.Fatal("an RGB overlay carried a YUV encoding")
	}
}

func overlayLayer(key backend.ScanoutKey, d gpu.DMABUF, dst image.Rectangle, release func() error) backend.Overlay {
	return backend.Overlay{Key: key, Buffer: d, At: backend.Placement{Src: render.FRect{W: float64(d.Size.X), H: float64(d.Size.Y)}, Dst: dst}, Release: release}
}

func testOverlay(o *Output, key backend.ScanoutKey, d gpu.DMABUF, dst image.Rectangle) bool {
	return o.TestOverlays([]backend.Overlay{overlayLayer(key, d, dst, nil)}) == 1
}

func twoOverlayRig(t *testing.T, budget int) (*rig, *Output) {
	t.Helper()
	r := boot(t, func(d *fakeDevice) allocator {
		d.overlayZpos, d.overlayYUV, d.secondOverlay, d.overlayBudget = 2, true, true, budget
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	if len(o.overlays) != 2 {
		t.Fatalf("%d overlay planes claimed, want 2", len(o.overlays))
	}
	for _, p := range o.overlays {
		p.clients = []gpu.FormatModifier{{Format: scanoutFormat, Modifier: fourcc.Linear}, {Format: fourcc.NV12, Modifier: fourcc.Linear}}
	}
	return r, o
}

func videos(n int) []backend.Overlay {
	layers := make([]backend.Overlay, n)
	for i := range layers {
		d := nv12(20+i, gpu.YUVEncoding{Coefficients: gpu.BT709, Range: gpu.LimitedRange})
		dst := image.Rect(100+700*i, 100, 740+700*i, 460)
		layers[i] = backend.Overlay{Key: &fakeBuffer{d}, Buffer: d, At: backend.Placement{Src: render.FRect{W: 1280, H: 720}, Dst: dst}, Release: func() error { return nil }}
	}
	return layers
}

func (r *rig) testedOverlays(o *Output, layers []backend.Overlay) (uint, int) {
	n := len(r.dev.commits)
	mask := o.TestOverlays(layers)
	return mask, tests(r.dev.commits[n:], 0)
}

func TestTwoScaledVideosTakeBothOverlayPlanesOneTestAFrame(t *testing.T) {
	r, o := twoOverlayRig(t, 0)
	layers := videos(3)
	var mask uint
	for frame := range 6 {
		var n int
		mask, n = r.testedOverlays(o, layers)
		if n > 1 {
			t.Fatalf("frame %d made %d test commits, want at most one", frame, n)
		}
	}
	if mask != 0b11 {
		t.Fatalf("two scaled NV12 videos settled on overlay mask %b, want both planes", mask)
	}
	if _, n := r.testedOverlays(o, layers); n != 0 {
		t.Fatalf("a settled pair of overlays made %d test commits", n)
	}
	if err := o.SetOverlays(layers[:2]); err != nil {
		t.Fatal(err)
	}
	if flip, err := o.FlipOverlays(); err != nil || flip != backend.OverlaysFlipped {
		t.Fatalf("FlipOverlays = %d, %v", flip, err)
	}
	c := r.dev.commits[len(r.dev.commits)-1]
	for _, plane := range []uint32{42, 44} {
		if c.values[[2]uint32{plane, propFB}] == 0 || c.values[[2]uint32{plane, propSrcW}] != 1280<<16 || c.values[[2]uint32{plane, propCRTCW}] != 640 {
			t.Fatalf("plane %d in the overlay flip: %+v", plane, c.values)
		}
	}
	if _, touched := c.values[[2]uint32{40, propFB}]; touched {
		t.Fatalf("the overlay flip touched the primary plane: %+v", c)
	}
}

func TestAPairThePlanesCannotShowTogetherKeepsTheTopOne(t *testing.T) {
	r, o := twoOverlayRig(t, 1)
	layers := videos(2)
	total := 0
	var mask uint
	for frame := range 10 {
		var n int
		mask, n = r.testedOverlays(o, layers)
		if n > 1 {
			t.Fatalf("frame %d made %d test commits, want at most one", frame, n)
		}
		total += n
	}
	if mask != 0b01 || total != 3 {
		t.Fatalf("a pair over the planes' budget settled on mask %b after %d tests, want the top video alone after 3", mask, total)
	}
}
