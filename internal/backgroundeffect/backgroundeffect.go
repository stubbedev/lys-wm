// Package backgroundeffect implements ext_background_effect_v1: clients name
// the region of a surface whose background the compositor blurs. A region is
// double-buffered and applies with the surface's content update.
package backgroundeffect

import (
	sbe "github.com/stubbedev/lys-wm/internal/protocol/server/backgroundeffect"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

type Manager struct {
	regions *surface.Latched[*region.Region]
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{regions: surface.NewLatched[*region.Region](nil)}
	if _, err := d.AddGlobal(sbe.ExtBackgroundEffectManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) BlurRegion(s *surface.Surface) region.Region {
	if r := m.regions.Get(s); r != nil {
		return *r
	}
	return region.Region{}
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	obj, err := sbe.NewExtBackgroundEffectManagerV1(c, id, v, requests{m})
	if err != nil {
		return err
	}
	return obj.SendCapabilities(sbe.ExtBackgroundEffectManagerV1CapabilityBlur)
}

type requests struct{ m *Manager }

func (requests) Destroy(*sbe.ExtBackgroundEffectManagerV1) error { return nil }

func (r requests) GetBackgroundEffect(obj *sbe.ExtBackgroundEffectManagerV1, res *sbe.ExtBackgroundEffectSurfaceV1, wl *sw.WlSurface) (sbe.ExtBackgroundEffectSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	v, ok := r.m.regions.Attach(s)
	if !ok {
		return nil, obj.ProtocolError(sbe.ExtBackgroundEffectManagerV1ErrorBackgroundEffectExists, "wl_surface@%d already has a background effect", wl.Base().ID())
	}
	server.OnDestroy(res, v.Detach)
	return effect{v}, nil
}

type effect struct {
	v *surface.LatchedValue[*region.Region]
}

func (effect) Destroy(*sbe.ExtBackgroundEffectSurfaceV1) error { return nil }

func (e effect) SetBlurRegion(obj *sbe.ExtBackgroundEffectSurfaceV1, g *sw.WlRegion) error {
	if e.v.Gone() {
		return obj.ProtocolError(sbe.ExtBackgroundEffectSurfaceV1ErrorSurfaceDestroyed, "the wl_surface was destroyed")
	}
	if g == nil {
		e.v.Set(nil)
		return nil
	}
	shape, err := surface.RegionValue(g)
	if err != nil {
		return err
	}
	e.v.Set(&shape)
	return nil
}
