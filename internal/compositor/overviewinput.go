package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	overviewDragSlop = 8
	overviewButton   = 272

	keysymEscape  = 0xff1b
	keysymReturn  = 0xff0d
	keysymKPEnter = 0xff8d
	keysymSpace   = 0x20
	keysymLeft    = 0xff51
	keysymUp      = 0xff52
	keysymRight   = 0xff53
	keysymDown    = 0xff54
)

type overviewPress struct {
	button   uint32
	touch    int32
	touched  bool
	at       point
	target   overviewTarget
	item     *overviewItem
	dragging bool
	grab     image.Point
}

func (c *Compositor) overviewSurfaceAt(p point) (*surface.Surface, point, *window) {
	if s, local := c.hitInputPopups(p); s != nil {
		return s, local, nil
	}
	for _, e := range slices.Backward(c.layers) {
		if isOverlay(e.surface) {
			if s, local := hitPopups(c.layerPopups(e), p); s != nil {
				return s, local, nil
			}
		}
	}
	s, local := c.hitLayers(c.outputAt(p), overlay, p)
	return s, local, nil
}

func (c *Compositor) overviewTargetAt(p point) overviewTarget {
	ov := c.overview
	o := c.outputAt(p)
	v := ov.views[o]
	if v == nil {
		return nil
	}
	at := p.pixel()
	for _, t := range v.spaces {
		if at.In(t.rect) {
			return overviewSpace{t}
		}
	}
	if at.In(v.plus) {
		return overviewPlus{v}
	}
	now := c.clock()
	progress := ov.progress.At(now)
	for _, e := range slices.Backward(v.order) {
		if pr := ov.press; pr != nil && pr.dragging && pr.item == e.item {
			continue
		}
		if at.In(e.rect(o, progress, now)) {
			return overviewWindow{e.w}
		}
	}
	return nil
}

func (c *Compositor) overviewPressed(button uint32, touch int32, touched bool, at point) error {
	ov := c.overview
	if ov.press != nil || !touched && button != overviewButton {
		return nil
	}
	pr := &overviewPress{button: button, touch: touch, touched: touched, at: at, target: c.overviewTargetAt(at)}
	if t, ok := pr.target.(overviewWindow); ok {
		pr.item = c.overviewItemOf(t.w)
	}
	ov.press = pr
	return nil
}

func (c *Compositor) overviewMoved(at point) error {
	ov := c.overview
	if pr := ov.press; pr != nil && pr.item != nil && !pr.item.minimized {
		if !pr.dragging && movedPast(pr.at, at, overviewDragSlop) {
			now := c.clock()
			f := pr.item.frame(c.outputAt(pr.at), ov.progress.At(now), now)
			pr.dragging, pr.grab = true, pr.at.pixel().Sub(f.Min)
			pr.item.slot.Jump(f)
		}
		if pr.dragging {
			f := pr.item.slot.At(c.clock())
			pr.item.slot.Jump(f.Add(at.pixel().Sub(pr.grab).Sub(f.Min)))
		}
	}
	hover := c.overviewTargetAt(at)
	if hover == ov.selected && (ov.press == nil || !ov.press.dragging) {
		return nil
	}
	ov.selected = hover
	return c.scheduleAll()
}

func movedPast(a, b point, slop float64) bool {
	dx, dy := a.X-b.X, a.Y-b.Y
	return float64(dx*dx)+float64(dy*dy) >= float64(slop*slop)
}

func (c *Compositor) overviewReleased(at point) error {
	ov := c.overview
	pr := ov.press
	ov.press = nil
	if pr.dragging {
		return errors.Join(c.overviewDrop(pr, at), c.cursorMoved())
	}
	if t := c.overviewTargetAt(at); t != pr.target {
		return nil
	}
	return c.overviewActivate(pr.target)
}

func (c *Compositor) overviewActivate(t overviewTarget) error {
	switch t := t.(type) {
	case overviewWindow:
		if err := c.activate(t.w); err != nil {
			return err
		}
		return c.closeOverview()
	case overviewSpace:
		name := t.t.o.Backend().Name()
		if active, ok := c.wm.ActiveOn(name); ok && active == t.t.id {
			return c.closeOverview()
		}
		return c.overviewShowSpace(name, t.t.id)
	case overviewPlus:
		return c.overviewShowSpace(t.v.output.Backend().Name(), c.wm.UnusedWorkspace())
	}
	return c.closeOverview()
}

func (c *Compositor) overviewShowSpace(name string, id wm.WorkspaceID) error {
	s, err := c.wm.FocusOutput(name)
	if err != nil {
		return err
	}
	return c.update(s.SwitchWorkspace(id))
}

func (c *Compositor) overviewDrop(pr *overviewPress, at point) error {
	ov := c.overview
	now := c.clock()
	it := pr.item
	home := c.overviewItemOutput(it)
	s := c.wm
	var dest wm.WorkspaceID
	var err error
	var into image.Rectangle
	switch t := c.overviewTargetAt(at).(type) {
	case overviewSpace:
		dest, into = t.t.id, t.t.rect
	case overviewPlus:
		if s, err = s.FocusOutput(t.v.output.Backend().Name()); err != nil {
			return err
		}
		dest, into = s.UnusedWorkspace(), t.v.plus
	default:
		if o := c.outputAt(at); o != nil && o != home {
			dest, _ = s.ActiveOn(o.Backend().Name())
		}
	}
	current, _ := c.wm.WorkspaceOf(it.root.id)
	if dest == 0 || dest == current {
		it.slot.Set(it.goal, now, c.animations.Curve(anim.Overview))
		return c.scheduleAll()
	}
	ov.carried = &overviewCarried{root: it.root, at: it.slot.At(now)}
	if !into.Empty() {
		if err := c.flyInto(it, c.outputAt(at), into, now); err != nil {
			return err
		}
	}
	for _, m := range slices.Clone(it.members) {
		if s, err = s.MoveWindowToWorkspace(m.id, dest, c.settings.Autotile); err != nil {
			return err
		}
	}
	return c.update(s, nil)
}

func (c *Compositor) flyInto(it *overviewItem, o *output.Output, into image.Rectangle, now time.Duration) error {
	tt, err := c.snapshot(it.root)
	if err != nil || tt == nil || o == nil {
		return err
	}
	curve := c.animations.Curve(anim.Overview)
	if curve == anim.Instant {
		return tt.Destroy()
	}
	cl := &closing{snapshot: tt, output: o, level: windowLevel, motion: anim.NewTween(it.slot.At(now), anim.LerpRect), fade: anim.NewTween(1.0, anim.Lerp)}
	cl.motion.Set(into, now, curve)
	cl.fade.Set(0, now, curve)
	c.closing = append(c.closing, cl)
	return nil
}

func (c *Compositor) abandonOverviewPress() {
	ov := c.overview
	pr := ov.press
	ov.press = nil
	if pr != nil && pr.dragging {
		pr.item.slot.Set(pr.item.goal, c.clock(), c.animations.Curve(anim.Overview))
	}
}

func (c *Compositor) overviewItemOutput(it *overviewItem) *output.Output {
	for o, v := range c.overview.views {
		if slices.Contains(v.items, it) {
			return o
		}
	}
	return nil
}

func (c *Compositor) overviewTargets() ([]overviewTarget, []image.Rectangle) {
	targets, rects := c.overview.targets[:0], c.overview.rects[:0]
	for _, o := range c.outputs {
		v := c.overview.views[o]
		if v == nil {
			continue
		}
		for _, t := range v.spaces {
			targets, rects = append(targets, overviewSpace{t}), append(rects, t.rect)
		}
		targets, rects = append(targets, overviewPlus{v}), append(rects, v.plus)
		for _, it := range v.items {
			targets, rects = append(targets, overviewWindow{it.root}), append(rects, it.goal)
		}
	}
	c.overview.targets, c.overview.rects = targets, rects
	return targets, rects
}

func (c *Compositor) overviewStep(d wm.Direction) error {
	ov := c.overview
	targets, rects := c.overviewTargets()
	selected := ov.selected
	if t, ok := selected.(overviewWindow); ok {
		if it := c.overviewItemOf(t.w); it != nil {
			selected = overviewWindow{it.root}
		}
	}
	from := slices.Index(targets, selected)
	if from < 0 {
		for _, t := range targets {
			if _, ok := t.(overviewWindow); ok {
				ov.selected = t
				return c.scheduleAll()
			}
		}
		return nil
	}
	if next, ok := wm.OverviewNeighbour(rects, from, d); ok {
		ov.selected = targets[next]
		return c.scheduleAll()
	}
	return nil
}

func (c *Compositor) overviewKey(code uint32) (bool, error) {
	if c.seat.Focus() != nil || c.seat.DepressedModifiers() != 0 {
		return false, nil
	}
	taken, err := c.overviewSym(c.seat.Keysym(code), code)
	if taken {
		c.held = append(c.held, code)
	}
	return taken, err
}

func arrowOf(sym uint32) (wm.Direction, bool) {
	switch sym {
	case keysymLeft:
		return wm.Left, true
	case keysymRight:
		return wm.Right, true
	case keysymUp:
		return wm.Up, true
	case keysymDown:
		return wm.Down, true
	}
	return 0, false
}

func (c *Compositor) overviewSym(sym, code uint32) (bool, error) {
	if d, ok := arrowOf(sym); ok {
		return true, errors.Join(c.overviewStep(d), c.repeatOverview(code, d))
	}
	switch sym {
	case keysymEscape:
		return true, c.closeOverview()
	case keysymReturn, keysymKPEnter, keysymSpace:
		return true, c.overviewActivate(c.overview.selected)
	}
	return false, nil
}

func (c *Compositor) repeatOverview(code uint32, d wm.Direction) error {
	if c.seatConfig.RepeatRate == 0 {
		return nil
	}
	return c.repeatKey(code, func() {
		var err error
		if c.overviewHolds() {
			err = c.overviewStep(d)
		} else {
			err = c.repeat.stop()
		}
		if err != nil {
			c.report(fmt.Errorf("compositor: repeat an arrow in the overview: %w", err))
		}
	})
}

func (c *Compositor) overviewDragging() bool {
	return c.overview != nil && c.overview.press != nil && c.overview.press.dragging
}

func (c *Compositor) overviewTouching(id int32) bool {
	if !c.overviewHolds() {
		return false
	}
	pr := c.overview.press
	return pr != nil && pr.touched && pr.touch == id
}
