// Package fifo implements wp_fifo_manager_v1: a content update can set a
// barrier on its surface when it applies, and a later update can wait for the
// next latching deadline to clear it, so Vulkan FIFO presentation shows every
// frame for at least one refresh.
package fifo

import (
	"errors"
	"slices"

	sff "github.com/stubbedev/lys-wm/internal/protocol/server/fifo"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

type Manager struct {
	shown  func(*surface.Surface) bool
	hidden func() error
	fifos  []*fifo
	waking []*fifo
}

func New(d *server.Display, shown func(*surface.Surface) bool, hidden func() error) (*Manager, error) {
	m := &Manager{shown: shown, hidden: hidden}
	if _, err := d.AddGlobal(sff.WpFifoManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Latched(latched []*surface.Surface) error {
	return m.clear(func(f *fifo) bool { return slices.Contains(latched, f.s) && m.shown(f.s) })
}

func (m *Manager) Expired() error {
	return m.clear(func(f *fifo) bool { return !m.shown(f.s) })
}

func (m *Manager) Hidden() bool {
	return slices.ContainsFunc(m.fifos, func(f *fifo) bool { return f.barrier && !m.shown(f.s) })
}

func (m *Manager) clear(passed func(*fifo) bool) error {
	m.waking = m.waking[:0]
	hidden := false
	for _, f := range m.fifos {
		switch {
		case !f.barrier:
		case passed(f):
			f.barrier = false
			m.waking = append(m.waking, f)
		case !m.shown(f.s):
			hidden = true
		}
	}
	var errs error
	for _, f := range m.waking {
		errs = errors.Join(errs, f.s.Retry())
	}
	clear(m.waking)
	if hidden {
		errs = errors.Join(errs, m.hidden())
	}
	return errs
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sff.NewWpFifoManagerV1(c, id, v, requests{m, c})
	return err
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*sff.WpFifoManagerV1) error { return nil }

func (r requests) GetFifo(obj *sff.WpFifoManagerV1, res *sff.WpFifoV1, wl *sw.WlSurface) (sff.WpFifoV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(r.m.fifos, func(f *fifo) bool { return f.s == s })
	if i >= 0 && r.m.fifos[i].live {
		return nil, obj.ProtocolError(sff.WpFifoManagerV1ErrorAlreadyExists, "wl_surface@%d already has a fifo", wl.Base().ID())
	}
	var f *fifo
	if i >= 0 {
		f = r.m.fifos[i]
	} else {
		f = &fifo{m: r.m, s: s}
		f.passed, f.raise = f.barrierPassed, f.raiseBarrier
		f.hooks = &surface.Hooks{Commit: f.commit, Destroyed: f.surfaceGone}
		s.AddHooks(f.hooks)
		r.m.fifos = append(r.m.fifos, f)
	}
	f.live = true
	server.OnDestroy(res, func() error {
		f.live = false
		return nil
	})
	return f, nil
}

type fifo struct {
	m       *Manager
	s       *surface.Surface
	hooks   *surface.Hooks
	live    bool
	gone    bool
	set     bool
	wait    bool
	barrier bool
	passed  func() bool
	raise   func() error
}

func (*fifo) Destroy(*sff.WpFifoV1) error { return nil }

func (f *fifo) SetBarrier(obj *sff.WpFifoV1) error {
	if f.gone {
		return obj.ProtocolError(sff.WpFifoV1ErrorSurfaceDestroyed, "set_barrier after the wl_surface was destroyed")
	}
	f.set = true
	return nil
}

func (f *fifo) WaitBarrier(obj *sff.WpFifoV1) error {
	if f.gone {
		return obj.ProtocolError(sff.WpFifoV1ErrorSurfaceDestroyed, "wait_barrier after the wl_surface was destroyed")
	}
	f.wait = true
	return nil
}

func (f *fifo) commit(u *surface.Update) error {
	set, wait := f.set, f.wait
	f.set, f.wait = false, false
	if sub := f.s.Subsurface(); wait && (sub == nil || !sub.Synchronized()) {
		u.Gate(f.passed)
	}
	if set {
		u.OnApply(f.raise)
	}
	return nil
}

func (f *fifo) barrierPassed() bool {
	return !f.barrier
}

func (f *fifo) raiseBarrier() error {
	f.barrier = true
	if f.m.shown(f.s) {
		return nil
	}
	return f.m.hidden()
}

func (f *fifo) surfaceGone() error {
	f.gone = true
	f.s.RemoveHooks(f.hooks)
	f.m.fifos = slices.DeleteFunc(f.m.fifos, func(x *fifo) bool { return x == f })
	return nil
}
