package compositor

import (
	"errors"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type DragSwitch struct {
	Width  int
	Frames int
}

type holdKind uint8

const (
	holdNone holdKind = iota
	holdEdge
	holdSpace
	holdTile
	holdTab
)

type holdTarget struct {
	kind  holdKind
	o     *output.Output
	space wm.WorkspaceID
	w     *window
}

type dragHold struct {
	target holdTarget
	frames int
	looks  uint64
}

func (c *Compositor) DragHoldLooks() uint64 {
	return c.hold.looks
}

func (c *Compositor) dragPoint() (point, bool) {
	switch {
	case c.drag != nil:
		return c.drag.by.at(c), true
	case c.interaction != nil && c.interaction.mode == wm.DragMove:
		return c.interaction.by.at(c), true
	}
	return point{}, false
}

func (c *Compositor) trackDragHold() error {
	p, ok := c.dragPoint()
	if !ok {
		c.dropDragHold()
		return nil
	}
	c.hold.looks++
	t := c.holdTargetAt(p)
	if t == c.hold.target {
		return nil
	}
	c.hold.target, c.hold.frames = t, 0
	if t.kind == holdNone {
		return nil
	}
	return c.schedule(t.o)
}

func (c *Compositor) dropDragHold() {
	c.hold.target, c.hold.frames = holdTarget{}, 0
}

func (c *Compositor) holdTargetAt(p point) holdTarget {
	o := c.outputAt(p)
	if o == nil {
		return holdTarget{}
	}
	if c.overviewHolds() {
		switch t := c.overviewTargetAt(p).(type) {
		case overviewSpace:
			if active, ok := c.wm.ActiveOn(t.t.o.Backend().Name()); !ok || active != t.t.id {
				return holdTarget{kind: holdSpace, o: t.t.o, space: t.t.id}
			}
		case overviewWindow:
			if c.drag != nil {
				return holdTarget{kind: holdTile, o: o, w: t.w}
			}
		}
		return holdTarget{}
	}
	at := p.pixel()
	if c.drag != nil {
		if id, ok := c.backgroundTabAt(at); ok {
			if w := c.windowByID(id); w != nil {
				return holdTarget{kind: holdTab, o: o, w: w}
			}
		}
	}
	near := c.freeEdgesNear(o, at, c.decor.DragPreview.Switch.Width)
	back, ahead := wm.EdgeLeft, wm.EdgeRight
	if c.animations.Style(anim.Workspaces).Kind == anim.SlideVert {
		back, ahead = wm.EdgeTop, wm.EdgeBottom
	}
	forward := near.Has(ahead)
	if !forward && !near.Has(back) {
		return holdTarget{}
	}
	active, ok := c.wm.ActiveOn(o.Backend().Name())
	if !ok {
		return holdTarget{}
	}
	id, ok := c.wm.EdgeWorkspace(o.Backend().Name(), active, forward)
	if !ok {
		return holdTarget{}
	}
	return holdTarget{kind: holdEdge, o: o, space: id}
}

func (c *Compositor) heldFrame(o *output.Output) error {
	h := &c.hold
	if h.target.kind == holdNone || h.target.o != o {
		return nil
	}
	h.frames++
	if h.frames < c.decor.DragPreview.Switch.Frames {
		return c.schedule(o)
	}
	t := h.target
	c.dropDragHold()
	return errors.Join(c.holdFire(t), c.trackDragHold())
}

func (c *Compositor) holdFire(t holdTarget) error {
	name := t.o.Backend().Name()
	switch t.kind {
	case holdEdge, holdSpace:
		if it := c.interaction; it != nil && t.kind == holdEdge {
			return c.carryToWorkspace(name, it.w, t.space)
		}
		return c.overviewShowSpace(name, t.space)
	case holdTile:
		return c.enterTile(t.w)
	case holdTab:
		if c.windowByID(t.w.id) != t.w {
			return nil
		}
		return c.dispatch(wm.FocusWindowAction{ID: t.w.id}, nil, wm.FocusOther)
	}
	return nil
}

func (c *Compositor) carryToWorkspace(name string, w *window, id wm.WorkspaceID) error {
	s, err := c.wm.FocusOutput(name)
	if err != nil {
		return err
	}
	if s, err = s.MoveWindowToWorkspace(w.id, id, c.settings.Autotile); err != nil {
		return err
	}
	if s, err = s.SwitchWorkspace(id); err != nil {
		return err
	}
	return c.update(s.FocusWindow(w.id, wm.Raise))
}

func (c *Compositor) enterTile(w *window) error {
	if c.windowByID(w.id) != w || c.drag == nil {
		return nil
	}
	tile, ok := c.overviewRect(w, c.clock())
	if !ok || tile.Empty() {
		return nil
	}
	p := c.drag.by.at(c)
	fx, fy := (p.X-float64(tile.Min.X))/float64(tile.Dx()), (p.Y-float64(tile.Min.Y))/float64(tile.Dy())
	if err := errors.Join(c.activate(w, wm.FocusOther), c.closeOverview()); err != nil {
		return err
	}
	if _, ok := c.drag.by.(pointerInput); !ok {
		return nil
	}
	r := w.rect
	return c.moveCursor(c.clock(), point{X: float64(r.Min.X) + float64(fx*float64(r.Dx())), Y: float64(r.Min.Y) + float64(fy*float64(r.Dy()))})
}
