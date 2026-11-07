package drm

import (
	"errors"
	"fmt"
	"slices"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"

	"github.com/stubbedev/lys-wm/internal/backend"
)

var _ backend.AdaptiveSync = (*Output)(nil)

const (
	propVRRCapable = "vrr_capable"
	propVRREnabled = "VRR_ENABLED"
	defaultVRRHold = time.Second / 24
)

func (b *Backend) vrrHold(c kdrm.Connector, data []byte) time.Duration {
	r, err := edid.ParseVRR(data)
	switch {
	case errors.Is(err, edid.ErrNoVRRRange):
		return defaultVRRHold
	case err != nil:
		b.report(fmt.Errorf("drm: %s holds a cursor move at most %v under adaptive sync, its EDID's refresh range is unreadable: %w", c.Name(), defaultVRRHold, err))
		return defaultVRRHold
	}
	return r.MaxFrameDuration()
}

func (o *Output) AdaptiveSyncCapable() bool {
	if o.connector.props[propVRRCapable].Value != 1 || !o.bound() || slices.ContainsFunc(o.tiles, func(t *tile) bool { return t.connector.props[propVRRCapable].Value != 1 }) {
		return false
	}
	_, ok := o.crtc.props[propVRREnabled]
	return ok
}

func (o *Output) AdaptiveSync() bool {
	return o.vrr
}

func (o *Output) SetAdaptiveSync(on bool) error {
	on = on && o.AdaptiveSyncCapable()
	if on == o.vrr {
		return nil
	}
	o.vrr = on
	clear(o.tested)
	return o.ScheduleFrame()
}

func (o *Output) FillFrame(filled bool) {
	o.filled = filled
}

func (o *Output) cursorWait() time.Duration {
	switch {
	case !o.vrrSent:
		return o.planesWait()
	case !o.filled:
		return 0
	}
	return o.lastFlip + o.vrrHold - o.b.now()
}

func (o *Output) vrrDirty() bool {
	return o.vrr != o.vrrSent
}

func (o *Output) addVRR(r *kdrm.Request) {
	if _, ok := o.crtc.props[propVRREnabled]; !ok || !o.vrrDirty() && o.kms == kmsFlip {
		return
	}
	var on uint64
	if o.vrr {
		on = 1
	}
	o.setCRTCs(r, propVRREnabled, on)
}

func (o *Output) CapFrameRate(hz int) {
	o.frameCap = hz
}

func (o *Output) paceInterval() time.Duration {
	return modeOf(o.mode).PacedInterval(o.frameCap, o.vrrSent)
}
