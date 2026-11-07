package compositor

import (
	"slices"

	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type mru struct {
	order  []wm.WindowID
	stamps map[wm.WindowID]uint64
	seq    uint64
}

func (m *mru) record(id wm.WindowID) {
	m.seq++
	if m.stamps == nil {
		m.stamps = map[wm.WindowID]uint64{}
	}
	m.stamps[id] = m.seq
	if i := slices.Index(m.order, id); i >= 0 {
		copy(m.order[1:i+1], m.order[:i])
		m.order[0] = id
		return
	}
	m.order = slices.Insert(m.order, 0, id)
}

func (m *mru) forget(id wm.WindowID) {
	m.order = slices.DeleteFunc(m.order, func(x wm.WindowID) bool { return x == id })
	delete(m.stamps, id)
}

func (m *mru) stamp(id wm.WindowID) uint64 {
	return m.stamps[id]
}

type mruWalk struct {
	windows []wm.WindowID
	at      int
}

func (c *Compositor) recordFocus(w *window) {
	c.mru.record(w.id)
	c.stateChanged()
}

func (c *Compositor) noteFocus() {
	c.mruPending = nil
	if c.active == nil || c.walk != nil || c.switcher != nil || c.overviewHolds() {
		return
	}
	switch c.focusBy {
	case wm.FocusHover, wm.FocusKeyboard:
		c.mruPending = c.active
	case wm.FocusClick, wm.FocusOther:
		c.recordFocus(c.active)
	}
}

func (c *Compositor) settleFocus() {
	p := c.mruPending
	c.mruPending = nil
	if p != nil && p == c.active {
		c.recordFocus(p)
	}
}

func (c *Compositor) forgetFocus(w *window) {
	c.mru.forget(w.id)
	if c.mruPending == w {
		c.mruPending = nil
	}
}

func (c *Compositor) inScope(w, ref *window, scope wm.MRUScope) bool {
	switch scope {
	case wm.ScopeAll:
		return true
	case wm.ScopeApp:
		return ref != nil && w.top.AppID() == ref.top.AppID()
	case wm.ScopeWorkspace:
		return ref != nil && w.workspace == ref.workspace
	case wm.ScopeOutput:
		return ref != nil && w.output == ref.output
	}
	return false
}

func (c *Compositor) mruWindows(scope wm.MRUScope) []*window {
	var into []*window
	ref := c.active
	for _, id := range c.mru.order {
		if w := c.windowByID(id); w != nil && c.inScope(w, ref, scope) {
			into = append(into, w)
		}
	}
	for _, w := range c.windows {
		if c.mru.stamp(w.id) == 0 && c.inScope(w, ref, scope) {
			into = append(into, w)
		}
	}
	return into
}

func (c *Compositor) focusLast(by wm.FocusOrigin) error {
	for _, w := range c.mruWindows(wm.ScopeAll) {
		if w != c.active {
			return c.activate(w, by)
		}
	}
	return nil
}

func (c *Compositor) focusUrgent(by wm.FocusOrigin) error {
	for _, w := range c.mruWindows(wm.ScopeAll) {
		if w.urgent {
			return c.activate(w, by)
		}
	}
	return c.focusLast(by)
}

func (c *Compositor) cycle(a wm.CycleAction, by wm.FocusOrigin) error {
	if c.walk == nil {
		list := c.mruWindows(a.Scope)
		if len(list) == 0 {
			return nil
		}
		walk := &mruWalk{windows: make([]wm.WindowID, len(list))}
		for i, w := range list {
			walk.windows[i] = w.id
		}
		if c.active != nil {
			walk.at = max(slices.Index(walk.windows, c.active.id), 0)
		}
		c.walk = walk
		if mods := c.binds.Held(c.seat.DepressedModifiers()); mods != 0 {
			c.OnModifiersReleased(mods, c.endWalk)
		}
	}
	walk := c.walk
	n := len(walk.windows)
	for range n {
		walk.at = ((walk.at+a.Step.Delta())%n + n) % n
		if w := c.windowByID(walk.windows[walk.at]); w != nil {
			return c.activate(w, by)
		}
	}
	return c.endWalk()
}

func (c *Compositor) endWalk() error {
	if c.walk == nil {
		return nil
	}
	c.walk = nil
	if c.active != nil && c.switcher == nil && !c.overviewHolds() {
		c.recordFocus(c.active)
	}
	return nil
}

func (c *Compositor) endWalkUnless(a wm.Action) error {
	if _, cycling := a.(wm.CycleAction); cycling {
		return nil
	}
	return c.endWalk()
}

func (c *Compositor) keyEndsWalk(code uint32) bool {
	_, modifier := binds.TapOf(c.seat.Keysym(code))
	return c.walk != nil && !modifier
}

func (c *Compositor) FocusTimestamp(id wm.WindowID) uint64 {
	return c.mru.stamp(id)
}
