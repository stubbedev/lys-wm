// Package hyprlandsurface implements hyprland_surface_manager_v1: clients
// fade a surface by an overall opacity and name the part of its buffer that
// holds visible content, which the compositor draws alone.
package hyprlandsurface

import (
	"errors"

	shs "github.com/stubbedev/lys-wm/internal/protocol/server/hyprlandsurface"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 2

type Manager struct {
	opacity *surface.Latched[float32]
	visible *surface.Latched[*region.Region]
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{opacity: surface.NewLatched[float32](1), visible: surface.NewLatched[*region.Region](nil)}
	if _, err := d.AddGlobal(shs.HyprlandSurfaceManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Opacity(s *surface.Surface) float32 {
	return m.opacity.Get(s)
}

func (m *Manager) VisibleRegion(s *surface.Surface) region.Region {
	if r := m.visible.Get(s); r != nil {
		return *r
	}
	return region.Region{}
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := shs.NewHyprlandSurfaceManagerV1(c, id, v, requests{m})
	return err
}

type requests struct{ m *Manager }

func (requests) Destroy(*shs.HyprlandSurfaceManagerV1) error { return nil }

func (r requests) GetHyprlandSurface(obj *shs.HyprlandSurfaceManagerV1, res *shs.HyprlandSurfaceV1, wl *sw.WlSurface) (shs.HyprlandSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	opacity, ok := r.m.opacity.Attach(s)
	if !ok {
		return nil, obj.ProtocolError(shs.HyprlandSurfaceManagerV1ErrorAlreadyConstructed, "wl_surface@%d already has a hyprland surface", wl.Base().ID())
	}
	visible, _ := r.m.visible.Attach(s)
	server.OnDestroy(res, func() error {
		return errors.Join(opacity.Detach(), visible.Detach())
	})
	return properties{opacity: opacity, visible: visible}, nil
}

type properties struct {
	opacity *surface.LatchedValue[float32]
	visible *surface.LatchedValue[*region.Region]
}

func (properties) Destroy(*shs.HyprlandSurfaceV1) error { return nil }

func (p properties) SetOpacity(obj *shs.HyprlandSurfaceV1, opacity wire.Fixed) error {
	if p.opacity.Gone() {
		return obj.ProtocolError(shs.HyprlandSurfaceV1ErrorNoSurface, "the wl_surface was destroyed")
	}
	f := opacity.Float()
	if f < 0 || f > 1 {
		return obj.ProtocolError(shs.HyprlandSurfaceV1ErrorOutOfRange, "opacity %v is outside 0 to 1", f)
	}
	p.opacity.Set(float32(f))
	return nil
}

func (p properties) SetVisibleRegion(obj *shs.HyprlandSurfaceV1, g *sw.WlRegion) error {
	if p.visible.Gone() {
		return obj.ProtocolError(shs.HyprlandSurfaceV1ErrorNoSurface, "the wl_surface was destroyed")
	}
	if g == nil {
		p.visible.Set(nil)
		return nil
	}
	shape, err := surface.RegionValue(g)
	if err != nil {
		return err
	}
	if shape.Empty() {
		p.visible.Set(nil)
		return nil
	}
	p.visible.Set(&shape)
	return nil
}
