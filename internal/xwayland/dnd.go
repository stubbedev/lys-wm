package xwayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"

	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/lys-wm/internal/data"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/surface"
)

const (
	xdndVersion   = 5
	xdndInlineMax = 3
)

type dndOut struct {
	drag    *data.Drag
	window  *Window
	target  xproto.Window
	version uint32
	at      image.Point
	ready   bool
	dropped bool
}

var xdndActions = []struct {
	action data.Action
	atom   atom
}{
	{sw.WlDataDeviceManagerDndActionCopy, atomXdndActionCopy},
	{sw.WlDataDeviceManagerDndActionMove, atomXdndActionMove},
	{sw.WlDataDeviceManagerDndActionAsk, atomXdndActionAsk},
}

func (m *wm) xdndAction(actions data.Action) xproto.Atom {
	for _, a := range xdndActions {
		if actions&a.action != 0 {
			return m.atoms[a.atom]
		}
	}
	return 0
}

func (m *wm) dataAction(x xproto.Atom) data.Action {
	for _, a := range xdndActions {
		if m.atoms[a.atom] == x {
			return a.action
		}
	}
	return sw.WlDataDeviceManagerDndActionCopy
}

func (m *wm) windowShowing(s *surface.Surface) *Window {
	for _, w := range m.windows {
		if w.shown && w.surface == s {
			return w
		}
	}
	return nil
}

func (m *wm) windowProperty(win xproto.Window, prop, typ xproto.Atom) (uint32, bool) {
	r, err := xproto.GetProperty(m.x, false, win, prop, typ, 0, 1).Reply()
	if err != nil || r.Format != 32 || len(r.Value) < 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(r.Value), true
}

func (m *wm) xdndTarget(win xproto.Window) (xproto.Window, uint32) {
	version, ok := m.windowProperty(win, m.atoms[atomXdndAware], xproto.AtomAtom)
	if !ok || version == 0 {
		return 0, 0
	}
	target := win
	if proxy, ok := m.windowProperty(win, m.atoms[atomXdndProxy], xproto.AtomWindow); ok {
		if self, ok := m.windowProperty(xproto.Window(proxy), m.atoms[atomXdndProxy], xproto.AtomWindow); ok && self == proxy {
			target = xproto.Window(proxy)
		}
	}
	return target, min(version, xdndVersion)
}

func (m *wm) xdnd(out *dndOut, name atom, data [5]uint32) {
	ev := xproto.ClientMessageEvent{Format: 32, Window: out.window.id, Type: m.atoms[name], Data: xproto.ClientMessageDataUnionData32New(data[:])}
	xproto.SendEvent(m.x, false, out.target, xproto.EventMaskNoEvent, string(ev.Bytes()))
}

func (m *wm) dragEnter(d *data.Drag, s *surface.Surface, x, y float64) error {
	w := m.windowShowing(s)
	out := &dndOut{drag: d, window: w}
	m.dnd = out
	if w == nil {
		return nil
	}
	out.at = w.rect.Min.Add(image.Pt(int(x), int(y)))
	src := d.Source()
	names := map[string]string{}
	for _, mime := range src.Mimes() {
		if name, ok := atomOf(mime); ok {
			names[name] = mime
		}
	}
	sorted := slices.Sorted(maps.Keys(names))
	go func() {
		atoms, err := intern(m.x, sorted)
		target, version := m.xdndTarget(w.id)
		m.post(func() error {
			if err != nil {
				m.report(fmt.Errorf("xwayland: offer a drag to X: %w", err))
				return nil
			}
			if m.dnd != out || version == 0 {
				return nil
			}
			out.target, out.version, out.ready = target, version, true
			sel := m.dndSel
			sel.wayland, sel.owned = src, true
			sel.targets = make(map[xproto.Atom]string, len(atoms))
			for i, a := range atoms {
				sel.targets[a] = names[sorted[i]]
			}
			m.changeAtoms(m.sel, m.atoms[atomXdndTypeList], xproto.AtomAtom, atoms)
			xproto.SetSelectionOwner(m.x, m.sel, m.atoms[atomXdndSelection], xproto.TimeCurrentTime)
			enter := [5]uint32{uint32(m.sel), version << 24}
			if len(atoms) > xdndInlineMax {
				enter[1] |= 1
			}
			inline := enter[2:]
			for i, a := range atoms[:min(len(atoms), len(inline))] {
				inline[i] = uint32(a)
			}
			m.xdnd(out, atomXdndEnter, enter)
			m.position(out)
			return nil
		})
	}()
	return nil
}

func (m *wm) position(out *dndOut) {
	at := uint32(uint16(out.at.X))<<16 | uint32(uint16(out.at.Y)) //nolint:gosec // X root coordinates are 16-bit
	m.xdnd(out, atomXdndPosition, [5]uint32{uint32(m.sel), 0, at, uint32(xproto.TimeCurrentTime), uint32(m.xdndAction(out.drag.Actions()))})
}

func (m *wm) dragMotion(x, y float64) error {
	out := m.dnd
	if out == nil || out.window == nil {
		return nil
	}
	out.at = out.window.rect.Min.Add(image.Pt(int(x), int(y)))
	if out.ready {
		m.position(out)
	}
	return nil
}

func (m *wm) releaseDnd() {
	sel := m.dndSel
	if sel.owned {
		xproto.SetSelectionOwner(m.x, 0, m.atoms[atomXdndSelection], xproto.TimeCurrentTime)
	}
	sel.wayland, sel.targets, sel.owned = nil, nil, false
}

func (m *wm) dragLeave() error {
	out := m.dnd
	if out == nil {
		return nil
	}
	m.dnd = nil
	if out.ready {
		m.xdnd(out, atomXdndLeave, [5]uint32{uint32(m.sel)})
	}
	m.releaseDnd()
	return nil
}

func (m *wm) dragDrop() error {
	out := m.dnd
	if out == nil || !out.ready {
		m.dnd = nil
		m.releaseDnd()
		if out == nil {
			return nil
		}
		return out.drag.Finished(false)
	}
	out.dropped = true
	m.xdnd(out, atomXdndDrop, [5]uint32{uint32(m.sel), 0, uint32(xproto.TimeCurrentTime)})
	if m.dndStall == nil {
		t, err := m.loop.AddTimer(m.dndStalled)
		if err != nil {
			return fmt.Errorf("xwayland: watch a drop on X: %w", err)
		}
		m.dndStall = t
	}
	return m.dndStall.Arm(m.transfer)
}

func (m *wm) dndStalled() error {
	out := m.dnd
	if out == nil || !out.dropped {
		return nil
	}
	m.report(fmt.Errorf("xwayland: the X window %#x did not finish a drop within %v", out.window.id, m.transfer))
	m.dnd = nil
	m.releaseDnd()
	return out.drag.Finished(false)
}

func (m *wm) dndFrom(win xproto.Window) *dndOut {
	out := m.dnd
	if out == nil || !out.ready || (win != out.window.id && win != out.target) {
		return nil
	}
	return out
}

func (m *wm) dndStatus(win xproto.Window, accept bool, action xproto.Atom) error {
	out := m.dndFrom(win)
	if out == nil || out.dropped {
		return nil
	}
	return out.drag.Status(accept, m.dataAction(action))
}

func (m *wm) dndFinished(win xproto.Window, ok bool) error {
	out := m.dndFrom(win)
	if out == nil || !out.dropped {
		return nil
	}
	m.dnd = nil
	m.releaseDnd()
	var err error
	if m.dndStall != nil {
		err = m.dndStall.Disarm()
	}
	return errors.Join(err, out.drag.Finished(ok || out.version < xdndVersion))
}

func (m *wm) closeDnd() error {
	out := m.dnd
	m.dnd = nil
	var errs []error
	if m.dndStall != nil {
		errs = append(errs, m.dndStall.Remove())
		m.dndStall = nil
	}
	if out != nil && out.dropped {
		errs = append(errs, out.drag.Finished(false))
	}
	return errors.Join(errs...)
}
