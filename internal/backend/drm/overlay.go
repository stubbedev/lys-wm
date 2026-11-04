package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
)

const testedOverlays = 16

type overlayUse struct {
	client  *clientFB
	dst     image.Rectangle
	release func() error
}

type overlayTest struct {
	fb  uint32
	dst image.Rectangle
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
	cf, err := o.b.clientFB(key, d)
	if err != nil {
		return false
	}
	test := overlayTest{cf.fb, dst}
	if ok, seen := o.overlayTested[test]; seen {
		return ok
	}
	r := &o.b.req
	r.Reset()
	o.placeOverlay(cf.fb, dst)
	ok := o.test(planeFlags) == nil
	if o.overlayTested == nil || len(o.overlayTested) >= testedOverlays {
		o.overlayTested = map[overlayTest]bool{}
	}
	o.overlayTested[test] = ok
	return ok
}

func (o *Output) placeOverlay(fb uint32, dst image.Rectangle) {
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
}

func (o *Output) SetOverlay(key backend.ScanoutKey, d gpu.DMABUF, dst image.Rectangle, release func() error) error {
	if o.overlay == nil {
		return fmt.Errorf("drm: %s has no overlay plane", o.Name())
	}
	cf, err := o.b.clientFB(key, d)
	if err != nil {
		return fmt.Errorf("drm: an overlay framebuffer on %s: %w", o.Name(), err)
	}
	o.overlayNext = &overlayUse{client: cf, dst: dst, release: release}
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
		o.placeOverlay(u.client.fb, u.dst)
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
	o.overlayTested[overlayTest{u.client.fb, u.dst}] = false
	o.overlayNext = o.overlayShown
}
