package compositor

import (
	"errors"
	"slices"

	shfg "github.com/stubbedev/lys-wm/internal/protocol/server/hyprlandfocusgrab"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const focusGrabVersion = 1

func (c *Compositor) bindFocusGrab(cl *server.Client, id wire.ObjectID, version uint32) error {
	_, err := shfg.NewHyprlandFocusGrabManagerV1(cl, id, version, focusGrabManager{c})
	return err
}

type focusGrabManager struct{ c *Compositor }

func (focusGrabManager) Destroy(*shfg.HyprlandFocusGrabManagerV1) error { return nil }

func (m focusGrabManager) CreateGrab(_ *shfg.HyprlandFocusGrabManagerV1, res *shfg.HyprlandFocusGrabV1) (shfg.HyprlandFocusGrabV1Handler, error) {
	g := &focusGrab{c: m.c, res: res}
	server.OnDestroy(res, g.destroyed)
	return g, nil
}

type focusGrab struct {
	c        *Compositor
	res      *shfg.HyprlandFocusGrabV1
	pending  []*surface.Surface
	surfaces []*surface.Surface
	gone     bool
}

func (g *focusGrab) has(s *surface.Surface) bool {
	return s != nil && slices.Contains(g.surfaces, s.Root())
}

func (g *focusGrab) AddSurface(_ *shfg.HyprlandFocusGrabV1, wl *sw.WlSurface) error {
	s, err := surface.From(wl)
	if err != nil || slices.Contains(g.pending, s) {
		return err
	}
	g.pending = append(g.pending, s)
	server.OnDestroy(wl, func() error { return g.forget(s) })
	return nil
}

func (g *focusGrab) RemoveSurface(_ *shfg.HyprlandFocusGrabV1, wl *sw.WlSurface) error {
	s, err := surface.From(wl)
	if err != nil {
		return err
	}
	g.pending = slices.DeleteFunc(g.pending, func(p *surface.Surface) bool { return p == s })
	return nil
}

func (g *focusGrab) Commit(*shfg.HyprlandFocusGrabV1) error {
	g.surfaces = slices.Clone(g.pending)
	return g.settle()
}

func (*focusGrab) Destroy(*shfg.HyprlandFocusGrabV1) error { return nil }

func (g *focusGrab) destroyed() error {
	g.gone = true
	g.pending, g.surfaces = nil, nil
	return g.settle()
}

func (g *focusGrab) forget(s *surface.Surface) error {
	if g.gone {
		return nil
	}
	drop := func(p *surface.Surface) bool { return p == s }
	g.pending = slices.DeleteFunc(g.pending, drop)
	g.surfaces = slices.DeleteFunc(g.surfaces, drop)
	return g.settle()
}

func (g *focusGrab) settle() error {
	c := g.c
	var err error
	switch {
	case len(g.surfaces) == 0 && c.focusGrab == g:
		c.focusGrab = nil
	case len(g.surfaces) == 0:
		return nil
	case c.focusGrab != g:
		if old := c.focusGrab; old != nil {
			err = old.clear()
		}
		c.focusGrab = g
	}
	return errors.Join(err, c.GrabChanged())
}

func (g *focusGrab) clear() error {
	g.pending, g.surfaces = nil, nil
	if g.c.focusGrab == g {
		g.c.focusGrab = nil
	}
	return g.res.SendCleared()
}

func (g *focusGrab) keyboardTarget(focus *surface.Surface) *surface.Surface {
	if g.has(focus) {
		return focus
	}
	return g.surfaces[len(g.surfaces)-1]
}

func (c *Compositor) dismissGrabs() error {
	var err error
	if g := c.focusGrab; g != nil {
		err = errors.Join(g.clear(), c.GrabChanged())
	}
	return errors.Join(err, c.shell.DismissGrab())
}
