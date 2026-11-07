package drm

import (
	"slices"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/backend"
)

var _ backend.AdaptiveSync = (*Output)(nil)

const (
	propVRRCapable = "vrr_capable"
	propVRREnabled = "VRR_ENABLED"
)

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
