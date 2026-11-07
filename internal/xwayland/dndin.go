package xwayland

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/lys-wm/internal/data"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/surface"
)

const (
	xdndAccept       = 1
	xdndWantPosition = 2
	proxySize        = 8192
)

type dndIn struct {
	drag     *data.Drag
	src      *xDragSource
	from     xproto.Window
	offered  data.Action
	accepted bool
	action   data.Action
	dropped  bool
}

type xDragSource struct {
	*xSource
	in *dndIn
}

func (s *xDragSource) Actions() data.Action {
	return s.in.offered
}

func (s *xDragSource) Target(mime *string) error {
	s.in.accepted = mime != nil
	s.m.dndInStatus(s.in)
	return nil
}

func (s *xDragSource) Action(a data.Action) error {
	s.in.action = a
	s.m.dndInStatus(s.in)
	return nil
}

func (*xDragSource) DropPerformed() error {
	return nil
}

func (s *xDragSource) Finished() error {
	s.m.dndInFinish(s.in, true)
	return nil
}

func (s *xDragSource) Cancel() error {
	if s.in.dropped {
		s.m.dndInFinish(s.in, false)
	}
	return s.xSource.Cancel()
}

func (m *wm) xDragOwned(owner xproto.Window) error {
	dragging := owner != 0 && owner != m.sel
	if dragging == m.xDragging {
		return nil
	}
	m.xDragging = dragging
	if dragging {
		return m.listener.XDragOver(m.pointerOver)
	}
	m.showProxy(false)
	var err error
	if in := m.in; in != nil && !in.dropped {
		m.in = nil
		err = in.drag.Cancel()
	}
	return errors.Join(err, m.listener.XDragOver(nil))
}

func (m *wm) pointerOver(s *surface.Surface, held bool) error {
	m.showProxy(m.xDragging && held && (s == nil || s.Client() != m.client))
	return nil
}

func (m *wm) showProxy(on bool) {
	if on == m.proxyShown {
		return
	}
	m.proxyShown = on
	if !on {
		xproto.UnmapWindow(m.x, m.proxy)
		return
	}
	xproto.ConfigureWindow(m.x, m.proxy, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	xproto.MapWindow(m.x, m.proxy)
}

func (m *wm) xdndToSource(from xproto.Window, name atom, d [5]uint32) {
	ev := xproto.ClientMessageEvent{Format: 32, Window: from, Type: m.atoms[name], Data: xproto.ClientMessageDataUnionData32New(d[:])}
	xproto.SendEvent(m.x, false, from, xproto.EventMaskNoEvent, string(ev.Bytes()))
}

func (m *wm) dndInEnter(from xproto.Window, offered map[string]xproto.Atom) error {
	if old := m.in; old != nil {
		m.in = nil
		if err := old.drag.Cancel(); err != nil {
			return err
		}
	}
	in := &dndIn{from: from, offered: sw.WlDataDeviceManagerDndActionCopy}
	src := &xDragSource{xSource: &xSource{m: m, sel: m.dndSel, mimes: slices.Sorted(maps.Keys(offered)), atoms: offered}, in: in}
	in.src = src
	drag, err := m.listener.ForeignDrag(src)
	if err != nil {
		return err
	}
	in.drag = drag
	m.dndSel.x = src.xSource
	m.in = in
	return nil
}

func (m *wm) dndInPosition(from xproto.Window, at time.Duration, action xproto.Atom) error {
	in := m.in
	if in == nil || in.from != from || in.dropped {
		m.xdndToSource(from, atomXdndStatus, [5]uint32{uint32(m.proxy)})
		return nil
	}
	in.offered = m.dataAction(action)
	target, x, y := m.listener.XDragTarget()
	if target != nil && target.Client() == m.client {
		target = nil
	}
	if err := in.drag.Motion(target, x, y, at); err != nil {
		return err
	}
	m.dndInStatus(in)
	return nil
}

func (m *wm) dndInStatus(in *dndIn) {
	if m.in != in || in.dropped {
		return
	}
	accept := in.accepted && in.action != sw.WlDataDeviceManagerDndActionNone
	flags, action := uint32(xdndWantPosition), xproto.Atom(0)
	if accept {
		flags |= xdndAccept
		action = m.xdndAction(in.action)
	}
	m.xdndToSource(in.from, atomXdndStatus, [5]uint32{uint32(m.proxy), flags, 0, 0, uint32(action)})
}

func (m *wm) dndInLeave(from xproto.Window) error {
	in := m.in
	if in == nil || in.from != from || in.dropped {
		return nil
	}
	m.in = nil
	return in.drag.Cancel()
}

func (m *wm) dndInDrop(from xproto.Window) error {
	in := m.in
	if in == nil || in.from != from {
		m.xdndToSource(from, atomXdndFinished, [5]uint32{uint32(m.proxy)})
		return nil
	}
	in.dropped = true
	return in.drag.Drop()
}

func (m *wm) dndInFinish(in *dndIn, ok bool) {
	if m.in == in {
		m.in = nil
	}
	if !in.dropped {
		return
	}
	in.dropped = false
	flags, action := uint32(0), xproto.Atom(0)
	if ok {
		flags, action = xdndAccept, m.xdndAction(in.action)
	}
	m.xdndToSource(in.from, atomXdndFinished, [5]uint32{uint32(m.proxy), flags, uint32(action)})
	if m.dndSel.x == in.src.xSource {
		m.dndSel.x = nil
	}
}

func (c *xconn) dndEnter(d []uint32) {
	from := xproto.Window(d[0])
	var types []xproto.Atom
	if d[1]&1 != 0 {
		r, err := xproto.GetProperty(c.x, false, from, c.atoms[atomXdndTypeList], xproto.AtomAtom, 0, maxProperty).Reply()
		if err != nil {
			c.report(fmt.Errorf("xwayland: read the XdndTypeList of %#x: %w", from, err))
			return
		}
		if types, err = atomList(r.Value, atomXdndTypeList); err != nil {
			c.report(fmt.Errorf("xwayland: the XdndTypeList of %#x: %w", from, err))
			return
		}
	} else {
		for _, a := range d[2:5] {
			if a != 0 {
				types = append(types, xproto.Atom(a))
			}
		}
	}
	offered := c.named(types)
	c.post(func(m *wm) error { return m.dndInEnter(from, offered) })
}
