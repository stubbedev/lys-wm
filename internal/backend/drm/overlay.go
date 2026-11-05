package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
)

const (
	testedOverlays    = 16
	propColorEncoding = "COLOR_ENCODING"
	propColorRange    = "COLOR_RANGE"
	nvidiaDriver      = "nvidia"
)

var (
	colorEncodings = map[gpu.Coefficients]string{gpu.BT601: "ITU-R BT.601 YCbCr", gpu.BT709: "ITU-R BT.709 YCbCr", gpu.BT2020: "ITU-R BT.2020 YCbCr"}
	colorRanges    = map[gpu.Range]string{gpu.LimitedRange: "YCbCr limited range", gpu.FullRange: "YCbCr full range"}
)

type planeYUV struct {
	encoding, rng uint64
	set           bool
}

type overlayUse struct {
	client  *clientFB
	dst     image.Rectangle
	yuv     planeYUV
	release func() error
}

type overlayTest struct {
	fb  uint32
	dst image.Rectangle
	yuv planeYUV
}

type overlayFlip struct {
	err     error
	changed bool
}

func (o *Output) overlaysUsable() bool {
	return o.overlay != nil && o.enabled && o.lit && !o.off && !o.b.paused && o.kms == kmsFlip && !o.flipping && o.drawing == nil && o.front != nil
}

func (o *Output) TestOverlay(key backend.ScanoutKey, d gpu.DMABUF, dst image.Rectangle) bool {
	if !o.overlaysUsable() || !slices.Contains(o.overlay.clients, gpu.FormatModifier{Format: d.Format, Modifier: d.Modifier}) {
		return false
	}
	yuv, err := o.overlayYUV(d)
	if err != nil {
		return false
	}
	cf, err := o.b.clientFB(key, d)
	if err != nil {
		return false
	}
	test := overlayTest{fb: cf.fb, dst: dst, yuv: yuv}
	if ok, seen := o.overlayTested[test]; seen {
		return ok
	}
	r := &o.b.req
	r.Reset()
	o.placeOverlay(cf.fb, dst, yuv)
	ok := o.test(planeFlags) == nil
	if o.overlayTested == nil || len(o.overlayTested) >= testedOverlays {
		o.overlayTested = map[overlayTest]bool{}
	}
	o.overlayTested[test] = ok
	return ok
}

func (o *Output) overlayYUV(d gpu.DMABUF) (planeYUV, error) {
	if !d.Format.YUV() {
		return planeYUV{}, nil
	}
	y := d.YUV
	if y.Coefficients == gpu.CoefficientsUnspecified {
		y = gpu.DefaultYUV(d.Size)
		if o.b.driver == nvidiaDriver {
			y.Range = gpu.FullRange
		}
	}
	encoding, err := o.overlay.enum(o.b.dev, propColorEncoding, colorEncodings[y.Coefficients])
	if err != nil {
		return planeYUV{}, err
	}
	rng, err := o.overlay.enum(o.b.dev, propColorRange, colorRanges[y.Range])
	if err != nil {
		return planeYUV{}, err
	}
	return planeYUV{encoding: encoding, rng: rng, set: true}, nil
}

func (p *plane) enum(dev device, name, value string) (uint64, error) {
	prop, ok := p.props[name]
	if !ok {
		return 0, fmt.Errorf("drm: plane %d has no %s property", p.id, name)
	}
	info, err := dev.PropertyInfo(prop.ID)
	if err != nil {
		return 0, fmt.Errorf("drm: the %s property of plane %d: %w", name, p.id, err)
	}
	v, ok := info.Enums[value]
	if !ok {
		return 0, fmt.Errorf("drm: the %s property of plane %d has no %q", name, p.id, value)
	}
	return v, nil
}

func (o *Output) placeOverlay(fb uint32, dst image.Rectangle, yuv planeYUV) {
	p, r := o.overlay, &o.b.req
	w, h := uint64(dst.Dx()), uint64(dst.Dy()) //nolint:gosec // plane sizes are positive
	p.set(r, "FB_ID", uint64(fb))
	p.set(r, "CRTC_ID", uint64(o.crtc.id))
	p.set(r, "SRC_X", 0)
	p.set(r, "SRC_Y", 0)
	p.set(r, "SRC_W", w<<16)
	p.set(r, "SRC_H", h<<16)
	p.setPosition(r, dst.Min)
	p.set(r, "CRTC_W", w)
	p.set(r, "CRTC_H", h)
	if yuv.set {
		p.set(r, propColorEncoding, yuv.encoding)
		p.set(r, propColorRange, yuv.rng)
	}
}

func (o *Output) SetOverlay(key backend.ScanoutKey, d gpu.DMABUF, dst image.Rectangle, release func() error) error {
	if o.overlay == nil {
		return fmt.Errorf("drm: %s has no overlay plane", o.Name())
	}
	yuv, err := o.overlayYUV(d)
	if err != nil {
		return fmt.Errorf("drm: an overlay on %s: %w", o.Name(), err)
	}
	cf, err := o.b.clientFB(key, d)
	if err != nil {
		return fmt.Errorf("drm: an overlay framebuffer on %s: %w", o.Name(), err)
	}
	o.overlayNext = &overlayUse{client: cf, dst: dst, yuv: yuv, release: release}
	return nil
}

func (o *Output) ClearOverlay() {
	o.overlayNext = nil
}

func (o *Output) addOverlay() {
	p := o.overlay
	if p == nil || o.overlayNext == o.overlayShown {
		return
	}
	if u := o.overlayNext; u != nil {
		o.placeOverlay(u.client.fb, u.dst, u.yuv)
		return
	}
	p.set(&o.b.req, "FB_ID", 0)
	p.set(&o.b.req, "CRTC_ID", 0)
}

func (o *Output) sentOverlay() {
	if o.overlayNext == o.overlayShown {
		return
	}
	o.overlayQueued = o.overlayNext
	if o.overlayQueued != nil {
		o.overlayQueued.client.shown++
	}
}

func (o *Output) FlipOverlay() (bool, error) {
	if !o.overlaysUsable() {
		return false, nil
	}
	r := &o.b.req
	r.Reset()
	o.addOverlay()
	if err := o.b.dev.Commit(r, planeFlags, uint64(o.crtc.id)); err != nil {
		if refused(err) {
			return false, nil
		}
		return false, fmt.Errorf("drm: flip the overlay of %s: %w", o.Name(), err)
	}
	o.sentOverlay()
	o.flipping = true
	return true, nil
}

func (o *Output) overlayFlipped() overlayFlip {
	if o.overlayQueued == o.overlayShown {
		return overlayFlip{}
	}
	old := o.overlayShown
	o.overlayShown = o.overlayQueued
	if old == nil {
		return overlayFlip{changed: true}
	}
	old.client.leave()
	return overlayFlip{err: old.release(), changed: true}
}

func (o *Output) dropOverlays() error {
	if o.overlay == nil {
		return nil
	}
	var errs []error
	if u := o.overlayShown; u != nil {
		u.client.leave()
		errs = append(errs, u.release())
	}
	if u := o.overlayQueued; u != nil && u != o.overlayShown {
		u.client.leave()
		errs = append(errs, u.release())
	}
	o.overlayShown, o.overlayQueued, o.overlayNext = nil, nil, nil
	return errors.Join(errs...)
}

func (o *Output) overlayRefused() {
	u := o.overlayNext
	if u == nil || u == o.overlayShown {
		return
	}
	if o.overlayTested == nil {
		o.overlayTested = map[overlayTest]bool{}
	}
	o.overlayTested[overlayTest{fb: u.client.fb, dst: u.dst, yuv: u.yuv}] = false
	o.overlayNext = o.overlayShown
}
