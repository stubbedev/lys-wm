package compositor

import (
	"errors"
	"fmt"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type VRRPolicy uint8

const (
	VRROff VRRPolicy = iota
	VRROn
	VRRFullscreen
)

func (c *Compositor) wantsVRR(policy VRRPolicy, s *surface.Surface, alone bool) bool {
	switch policy {
	case VRROn:
		return true
	case VRRFullscreen:
		if !alone {
			return false
		}
		t := c.contentType(s)
		return t == wm.ContentGame || t == wm.ContentVideo
	}
	return false
}

func (c *Compositor) showAdaptiveSync(o *output.Output, s *surface.Surface, alone bool) error {
	b := o.Backend()
	as, ok := b.(backend.AdaptiveSync)
	if !ok || !as.AdaptiveSyncCapable() {
		return nil
	}
	on := c.wantsVRR(c.outputRule(b).VRR, s, alone)
	if on == as.AdaptiveSync() {
		return nil
	}
	return errors.Join(as.SetAdaptiveSync(on), c.outputConfig.Changed())
}

func (c *Compositor) commitAdaptiveSync(b backend.Output, policy VRRPolicy) error {
	as, ok := b.(backend.AdaptiveSync)
	if !ok || !as.AdaptiveSyncCapable() {
		if policy != VRROff {
			c.report(fmt.Errorf("compositor: output %s keeps a fixed refresh rate: it has no adaptive sync, which needs a vrr_capable connector on a CRTC with VRR_ENABLED", b.Name()))
		}
		return nil
	}
	if policy == VRRFullscreen {
		return nil
	}
	return as.SetAdaptiveSync(policy == VRROn)
}
