package compositor

import (
	"github.com/stubbedev/lys-wm/internal/ipc"
	ssb "github.com/stubbedev/lys-wm/internal/protocol/server/systembell"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const bellVersion = 1

func (c *Compositor) bindBell(cl *server.Client, id wire.ObjectID, version uint32) error {
	_, err := ssb.NewXdgSystemBellV1(cl, id, version, bell{c})
	return err
}

type bell struct{ c *Compositor }

func (bell) Destroy(*ssb.XdgSystemBellV1) error { return nil }

func (b bell) Ring(_ *ssb.XdgSystemBellV1, wl *sw.WlSurface) error {
	if wl == nil {
		b.c.announceBell(nil)
		return nil
	}
	s, err := surface.From(wl)
	if err != nil {
		return err
	}
	w := b.c.windowWithSurface(s)
	b.c.announceBell(w)
	if w == nil {
		return nil
	}
	return b.c.markUrgent(w)
}

func (c *Compositor) announceBell(w *window) {
	if c.ipcEvents == nil {
		return
	}
	e := ipc.BellEvent{}
	if w != nil {
		id := w.id
		e.Window = &id
	}
	c.ipcEvents.Announce(e)
}
