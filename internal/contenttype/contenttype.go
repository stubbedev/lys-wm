// Package contenttype implements wp_content_type_manager_v1: a surface says
// whether it shows a photo, a video or a game, which the compositor uses where
// it decides presentation per surface.
package contenttype

import (
	"fmt"

	sct "github.com/stubbedev/lys-wm/internal/protocol/server/contenttype"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const version = 1

func parse(v sct.WpContentTypeV1Type) (wm.ContentType, error) {
	switch v {
	case sct.WpContentTypeV1TypeNone:
		return wm.ContentNone, nil
	case sct.WpContentTypeV1TypePhoto:
		return wm.ContentPhoto, nil
	case sct.WpContentTypeV1TypeVideo:
		return wm.ContentVideo, nil
	case sct.WpContentTypeV1TypeGame:
		return wm.ContentGame, nil
	}
	return wm.ContentNone, fmt.Errorf("contenttype: content type %d is none of none, photo, video and game", v)
}

type Manager struct {
	types *surface.Latched[wm.ContentType]
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{types: surface.NewLatched(wm.ContentNone)}
	if _, err := d.AddGlobal(sct.WpContentTypeManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Of(s *surface.Surface) wm.ContentType {
	return m.types.Get(s)
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sct.NewWpContentTypeManagerV1(c, id, v, requests{m})
	return err
}

type requests struct{ m *Manager }

func (requests) Destroy(*sct.WpContentTypeManagerV1) error { return nil }

func (r requests) GetSurfaceContentType(obj *sct.WpContentTypeManagerV1, res *sct.WpContentTypeV1, wl *sw.WlSurface) (sct.WpContentTypeV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	v, ok := r.m.types.Attach(s)
	if !ok {
		return nil, obj.ProtocolError(sct.WpContentTypeManagerV1ErrorAlreadyConstructed, "wl_surface@%d already has a content type object", wl.Base().ID())
	}
	server.OnDestroy(res, v.Detach)
	return hint{v}, nil
}

type hint struct {
	v *surface.LatchedValue[wm.ContentType]
}

func (hint) Destroy(*sct.WpContentTypeV1) error { return nil }

func (h hint) SetContentType(_ *sct.WpContentTypeV1, v sct.WpContentTypeV1Type) error {
	t, err := parse(v)
	if err != nil {
		return err
	}
	h.v.Set(t)
	return nil
}
