// Package tearingcontrol implements wp_tearing_control_manager_v1: a surface
// hints that its content may be presented with tearing, which the compositor
// honours where the user allows it and the surface fills an output.
package tearingcontrol

import (
	"fmt"

	stc "github.com/stubbedev/lys-wm/internal/protocol/server/tearingcontrol"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

type Hint uint8

const (
	Unset Hint = iota
	Vsync
	Async
)

type Manager struct {
	hints *surface.Latched[Hint]
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{hints: surface.NewLatched(Unset)}
	if _, err := d.AddGlobal(stc.WpTearingControlManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Hint(s *surface.Surface) Hint {
	return m.hints.Get(s)
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := stc.NewWpTearingControlManagerV1(c, id, v, requests{m})
	return err
}

type requests struct{ m *Manager }

func (requests) Destroy(*stc.WpTearingControlManagerV1) error { return nil }

func (r requests) GetTearingControl(obj *stc.WpTearingControlManagerV1, res *stc.WpTearingControlV1, wl *sw.WlSurface) (stc.WpTearingControlV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	v, ok := r.m.hints.Attach(s)
	if !ok {
		return nil, obj.ProtocolError(stc.WpTearingControlManagerV1ErrorTearingControlExists, "wl_surface@%d already has a tearing control", wl.Base().ID())
	}
	server.OnDestroy(res, v.Detach)
	return hint{v}, nil
}

type hint struct{ v *surface.LatchedValue[Hint] }

func (hint) Destroy(*stc.WpTearingControlV1) error { return nil }

func (h hint) SetPresentationHint(_ *stc.WpTearingControlV1, v stc.WpTearingControlV1PresentationHint) error {
	switch v {
	case stc.WpTearingControlV1PresentationHintVsync:
		h.v.Set(Vsync)
	case stc.WpTearingControlV1PresentationHintAsync:
		h.v.Set(Async)
	default:
		return fmt.Errorf("tearingcontrol: presentation hint %d is neither vsync nor async", v)
	}
	return nil
}
