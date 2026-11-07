package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/data"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	sxtd "github.com/stubbedev/lys-wm/internal/protocol/server/xdgtopleveldrag"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const toplevelDragVersion = 1

type toplevelDrag struct {
	c      *Compositor
	res    *sxtd.XdgToplevelDragV1
	source *sw.WlDataSource
	top    *shell.Toplevel
	offset image.Point
}

func (c *Compositor) addToplevelDrag(d *server.Display) error {
	_, err := d.AddGlobal(sxtd.XdgToplevelDragManagerV1Interface, toplevelDragVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := sxtd.NewXdgToplevelDragManagerV1(cl, id, version, toplevelDrags{c})
		return err
	})
	return err
}

type toplevelDrags struct{ c *Compositor }

func (toplevelDrags) Destroy(*sxtd.XdgToplevelDragManagerV1) error { return nil }

func (m toplevelDrags) GetXdgToplevelDrag(obj *sxtd.XdgToplevelDragManagerV1, res *sxtd.XdgToplevelDragV1, source *sw.WlDataSource) (sxtd.XdgToplevelDragV1Handler, error) {
	refuse := func() error {
		return obj.ProtocolError(sxtd.XdgToplevelDragManagerV1ErrorInvalidSource, "wl_data_source@%d is reserved for a toplevel drag", source.Base().ID())
	}
	if err := data.ReserveForDrag(source, refuse); errors.Is(err, data.ErrSourceUsed) {
		return nil, obj.ProtocolError(sxtd.XdgToplevelDragManagerV1ErrorInvalidSource, "wl_data_source@%d was already used", source.Base().ID())
	} else if err != nil {
		return nil, err
	}
	td := &toplevelDrag{c: m.c, res: res, source: source}
	m.c.toplevelDrags = append(m.c.toplevelDrags, td)
	server.OnDestroy(res, func() error {
		m.c.toplevelDrags = slices.DeleteFunc(m.c.toplevelDrags, func(x *toplevelDrag) bool { return x == td })
		return nil
	})
	server.OnDestroy(source, func() error {
		td.top = nil
		return nil
	})
	return td, nil
}

func (td *toplevelDrag) dragging() bool {
	return td.c.drag != nil && td.c.drag.Carries(td.source)
}

func (td *toplevelDrag) Destroy(obj *sxtd.XdgToplevelDragV1) error {
	if td.dragging() {
		return obj.ProtocolError(sxtd.XdgToplevelDragV1ErrorOngoingDrag, "the drag has not ended")
	}
	return nil
}

func (td *toplevelDrag) Attach(obj *sxtd.XdgToplevelDragV1, res *sxdg.XdgToplevel, x, y int32) error {
	t, ok := res.Handler().(*shell.Toplevel)
	if !ok {
		return fmt.Errorf("compositor: xdg_toplevel@%d has handler %T", res.Base().ID(), res.Handler())
	}
	if td.top != nil {
		return obj.ProtocolError(sxtd.XdgToplevelDragV1ErrorToplevelAttached, "a toplevel is already attached")
	}
	td.top, td.offset = t, image.Pt(int(x), int(y))
	server.OnDestroy(res, func() error {
		if td.top == t {
			td.top = nil
		}
		return nil
	})
	if !td.dragging() {
		return nil
	}
	return td.c.followDrag()
}

func (c *Compositor) activeToplevelDrag() *toplevelDrag {
	if c.drag == nil {
		return nil
	}
	if i := slices.IndexFunc(c.toplevelDrags, func(td *toplevelDrag) bool { return td.top != nil && c.drag.Carries(td.source) }); i >= 0 {
		return c.toplevelDrags[i]
	}
	return nil
}

func (c *Compositor) draggedToplevel(t toplevel) bool {
	x, ok := t.(xdgToplevel)
	return ok && slices.ContainsFunc(c.toplevelDrags, func(td *toplevelDrag) bool { return td.top == x.Toplevel && td.dragging() })
}

func (c *Compositor) dragCarried() *window {
	td := c.activeToplevelDrag()
	if td == nil {
		return nil
	}
	return c.windowOf(xdgToplevel{td.top})
}

func (c *Compositor) followDrag() error {
	td := c.activeToplevelDrag()
	if td == nil {
		return nil
	}
	w := c.windowOf(xdgToplevel{td.top})
	if w == nil || w.minimized {
		return nil
	}
	size := w.tile.Size()
	inset := w.rect.Min.Sub(w.tile.Min)
	at := c.drag.by.at(c).pixel().Sub(td.offset).Sub(inset)
	rect := image.Rectangle{Min: at, Max: at.Add(size)}
	if !c.wm.Floats(w.id) {
		return c.update(c.wm.Float(w.id, rect))
	}
	if r, ok := c.wm.FloatingRect(w.id); ok && r == rect {
		return nil
	}
	return c.update(c.wm.SetFloatingRect(w.id, rect))
}

func (c *Compositor) dropToplevelDrag(t toplevel) {
	x, ok := t.(xdgToplevel)
	if !ok {
		return
	}
	for _, td := range c.toplevelDrags {
		if td.top == x.Toplevel {
			td.top = nil
		}
	}
}

func (c *Compositor) DataDragging() bool {
	return c.drag != nil
}
