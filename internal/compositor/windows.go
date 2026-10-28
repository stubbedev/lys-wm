package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type window struct {
	id      wm.WindowID
	top     toplevel
	tile    image.Rectangle
	rect    image.Rectangle
	visible bool
	layer   wm.PlacementLayer
	output  *output.Output
	urgent  bool
	hung    bool
	scale   output.Scale
	floated bool
}

func (c *Compositor) windowOrigin(w *window) origin {
	if _, ok := w.top.(xToplevel); ok {
		return c.xOrigin(c.toX(w.rect).Min)
	}
	return flat(w.rect.Min.Sub(w.top.Geometry().Min))
}

func (c *Compositor) firstOutput() *output.Output {
	if len(c.outputs) == 0 {
		return nil
	}
	return c.outputs[0]
}

func (c *Compositor) findWindow(match func(w *window) bool) *window {
	if i := slices.IndexFunc(c.windows, match); i >= 0 {
		return c.windows[i]
	}
	return nil
}

func (c *Compositor) windowOf(t toplevel) *window {
	return c.findWindow(func(w *window) bool { return w.top == t })
}

func (c *Compositor) windowByID(id wm.WindowID) *window {
	return c.findWindow(func(w *window) bool { return w.id == id })
}

func (c *Compositor) windowWithSurface(s *surface.Surface) *window {
	return c.findWindow(func(w *window) bool { return w.top.Surface() == s })
}

func (c *Compositor) live(w *window) bool {
	return slices.Contains(c.windows, w)
}

func (c *Compositor) Toplevels() []*shell.Toplevel {
	out := make([]*shell.Toplevel, 0, len(c.windows))
	for _, w := range c.windows {
		if t, ok := w.top.(xdgToplevel); ok {
			out = append(out, t.Toplevel)
		}
	}
	return out
}

func (c *Compositor) outputAtRect(r image.Rectangle) *output.Output {
	for _, o := range c.outputs {
		if r.Min.In(o.Logical()) {
			return o
		}
	}
	return nil
}

func (c *Compositor) wanted(rect image.Rectangle, layer wm.PlacementLayer, activated bool) want {
	w := want{rect: rect, layer: layer, activated: activated}
	if o := c.outputAtRect(rect); o != nil {
		w.placed, w.scale, w.bounds = true, c.scaleOver(rect), c.usable[o].Size()
	}
	return w
}

func maxScale(outputs []*output.Output) output.Scale {
	var s output.Scale
	for _, o := range outputs {
		s = max(s, o.Scale())
	}
	return s
}

func (c *Compositor) scaleOver(r image.Rectangle) output.Scale {
	var covered []*output.Output
	for _, o := range c.outputs {
		if o.Logical().Overlaps(r) {
			covered = append(covered, o)
		}
	}
	return maxScale(covered)
}

func (c *Compositor) update(s wm.State, err error) error {
	if err != nil {
		return err
	}
	c.wm = s
	return c.reconcile()
}

func (c *Compositor) syncAreas() error {
	s := c.wm
	for _, o := range c.outputs {
		full, area := o.Logical(), c.usable[o].Add(o.Position())
		for _, have := range s.Outputs() {
			if have.Name == o.Backend().Name() && (have.Area != area || have.Full != full) {
				var err error
				if s, err = s.SetGeometry(have.Name, full, area); err != nil {
					return err
				}
			}
		}
	}
	return c.update(s, nil)
}

func (c *Compositor) modalChild(w *window) *window {
	for _, x := range slices.Backward(c.windows) {
		if x != w && x.top.modal() && x.top.parent() == w.top {
			if deeper := c.modalChild(x); deeper != nil {
				return deeper
			}
			return x
		}
	}
	return nil
}

func (c *Compositor) reconcile() error {
	focused, hasFocus := c.wm.Focused()
	if hasFocus {
		if m := c.modalChild(c.windowByID(focused)); m != nil {
			return c.update(c.wm.FocusWindow(m.id))
		}
	}
	var errs []error
	a := c.wm.Arrange(c.decor.reserved())
	c.tabBars = a.TabBars
	c.stack = c.stack[:0]
	for _, p := range a.Windows {
		w := c.windowByID(p.ID)
		if w == nil {
			return fmt.Errorf("compositor: the window manager placed unknown window %d", p.ID)
		}
		c.stack = append(c.stack, w)
		rect := c.content(p.Rect, p.Layer)
		moved := rect != w.rect
		w.tile, w.rect, w.visible, w.layer = p.Rect, rect, p.Visible, p.Layer
		if o := c.outputAtRect(p.Rect); o != nil {
			w.output = o
		}
		if moved && w.visible {
			errs = append(errs, c.reconstrain(w))
		}
		errs = append(errs, c.settle(w, hasFocus && focused == w.id))
	}
	var target *window
	if hasFocus {
		target = c.windowByID(focused)
	}
	if target != c.active {
		c.active = target
		if target != nil {
			c.layerFocus, target.urgent = nil, false
			errs = append(errs, target.top.ping())
		}
	}
	return errors.Join(append(errs, c.refocus(), c.refreshPointer(), c.scheduleAll(), c.syncForeign(), c.syncWorkspaces(), c.syncCaptureSessions())...)
}

func (c *Compositor) content(tile image.Rectangle, layer wm.PlacementLayer) image.Rectangle {
	if layer == wm.LayerFullscreen {
		return tile
	}
	return tile.Inset(c.decor.BorderWidth)
}

func sameState(a, b shell.State) bool {
	return a.Size == b.Size && a.Bounds == b.Bounds && slices.Equal(a.States, b.States)
}

func (c *Compositor) settle(w *window, activated bool) error {
	want := c.wanted(w.rect, w.layer, activated)
	if w.top.settled(want) && w.scale == want.scale {
		return nil
	}
	w.scale = want.scale
	return w.top.apply(want)
}

func (c *Compositor) floats(t toplevel) (bool, image.Point) {
	d := wm.Decide(c.rules, t.AppID(), t.Title())
	wants := t.parent() != nil || t.prefersFloating() || fixedSize(t.sizeLimits())
	return d.Mode == wm.RuleFloat || (wants && d.Mode != wm.RuleTile), d.Size
}

func fixedSize(minimum, maximum image.Point) bool {
	return minimum != (image.Point{}) && minimum == maximum
}

func (c *Compositor) ToplevelInitialCommit(st *shell.Toplevel) error {
	t := xdgToplevel{st}
	if float, size := c.floats(t); float {
		return t.apply(want{rect: image.Rectangle{Max: floatSize(t, size)}, activated: true})
	}
	s, err := c.wm.MapWindow(c.nextWindow, c.settings.Autotile)
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(c.decor.reserved()).Windows {
		if p.ID == c.nextWindow {
			return t.apply(c.wanted(c.content(p.Rect, p.Layer), p.Layer, true))
		}
	}
	return fmt.Errorf("compositor: the window manager did not place new window %d", c.nextWindow)
}

func (c *Compositor) floatRect(t toplevel, size image.Point) image.Rectangle {
	size = floatSize(t, size).Add(image.Pt(2*c.decor.BorderWidth, 2*c.decor.BorderWidth))
	over := image.Rectangle{}
	if p := c.windowOf(t.parent()); p != nil {
		over = p.tile
	} else if o := c.outputAt(c.cursor); o != nil {
		over = c.usable[o].Add(o.Position())
	}
	corner := over.Min.Add(over.Size().Sub(size).Div(2))
	return image.Rectangle{Min: corner, Max: corner.Add(size)}
}

func floatSize(t toplevel, ruled image.Point) image.Point {
	size := ruled
	if size == (image.Point{}) {
		size = t.Geometry().Size()
	}
	minimum, maximum := t.sizeLimits()
	return wm.Limits{Min: minimum, Max: maximum}.Fit(size)
}

func (c *Compositor) frameLimits(t toplevel) wm.Limits {
	minimum, maximum := t.sizeLimits()
	frame := 2 * c.decor.BorderWidth
	framed := func(p image.Point) image.Point {
		if p.X > 0 {
			p.X += frame
		}
		if p.Y > 0 {
			p.Y += frame
		}
		return p
	}
	return wm.Limits{Min: framed(minimum), Max: framed(maximum)}
}

func (c *Compositor) refreshLimits(t toplevel) error {
	w := c.windowOf(t)
	if w == nil {
		return nil
	}
	return c.update(c.wm.SetLimits(w.id, c.frameLimits(t)))
}

func (c *Compositor) redecide(t toplevel) error {
	w := c.windowOf(t)
	if w == nil || c.wm.Floats(w.id) != w.floated {
		return nil
	}
	float, size := c.floats(t)
	if float == w.floated {
		return nil
	}
	w.floated = float
	if float {
		return c.update(c.wm.Float(w.id, c.floatRect(t, size)))
	}
	return c.update(c.wm.Tile(w.id, c.settings.Autotile))
}

func (c *Compositor) focusWindow(w *window) error {
	c.layerFocus = nil
	return c.update(c.wm.FocusWindow(w.id))
}

func (c *Compositor) keyboardTarget() *surface.Surface {
	if c.shell.Locked() {
		return c.lockTarget()
	}
	exclusive := c.exclusiveLayer()
	if top := c.grabTop(); top != nil && (exclusive == nil || top.Client() == exclusive.Surface().Client()) {
		return top
	}
	switch {
	case exclusive != nil:
		return exclusive.Surface()
	case c.layerFocus != nil && c.layerFocus.Mapped():
		return c.layerFocus.Surface()
	case c.active != nil:
		return c.active.top.Surface()
	}
	return nil
}

func (c *Compositor) refocus() error {
	return c.seat.SetKeyboardFocus(c.keyboardTarget())
}

func (c *Compositor) ToplevelMapped(t *shell.Toplevel) error {
	return c.mapToplevel(xdgToplevel{t})
}

func (c *Compositor) mapToplevel(t toplevel) error {
	w := &window{id: c.nextWindow, top: t}
	c.nextWindow++
	var s wm.State
	var err error
	float, size := c.floats(t)
	w.floated = float
	if float {
		s, err = c.wm.MapFloating(w.id, c.floatRect(t, size))
	} else {
		s, err = c.wm.MapWindow(w.id, c.settings.Autotile)
	}
	if err != nil {
		return err
	}
	if s, err = s.SetLimits(w.id, c.frameLimits(t)); err != nil {
		return err
	}
	for _, p := range s.Arrange(c.decor.reserved()).Windows {
		if p.ID == w.id && t.scaled() {
			w.scale = c.scaleOver(p.Rect)
		}
	}
	c.windows = append(c.windows, w)
	return c.update(s, nil)
}

func (c *Compositor) ToplevelUnmapped(t *shell.Toplevel) error {
	return c.unmapToplevel(xdgToplevel{t})
}

func (c *Compositor) unmapToplevel(t toplevel) error {
	w := c.windowOf(t)
	if w == nil {
		return nil
	}
	c.windows = slices.DeleteFunc(c.windows, func(x *window) bool { return x == w })
	if c.active == w {
		c.active = nil
	}
	if c.interaction != nil && c.interaction.w == w {
		c.interaction = nil
	}
	return c.update(c.wm.UnmapWindow(w.id))
}

func (c *Compositor) ToplevelChanged(t *shell.Toplevel, change shell.Change) error {
	switch change {
	case shell.ChangedSizeLimits:
		return errors.Join(c.refreshLimits(xdgToplevel{t}), c.redecide(xdgToplevel{t}))
	case shell.ChangedTitle, shell.ChangedAppID, shell.ChangedParent:
		return errors.Join(c.redecide(xdgToplevel{t}), c.syncForeign())
	case shell.ChangedModal:
		return errors.Join(c.reconcile(), c.syncForeign())
	case shell.ChangedIcon:
		return c.scheduleAll()
	}
	return c.syncForeign()
}

func (c *Compositor) markUrgent(w *window) error {
	if w == c.active || w.urgent {
		return nil
	}
	w.urgent = true
	return errors.Join(c.scheduleAll(), c.syncWorkspaces())
}

func (c *Compositor) ToplevelRequest(t *shell.Toplevel, r shell.Request) error {
	w := c.windowOf(xdgToplevel{t})
	if w == nil {
		return nil
	}
	switch r := r.(type) {
	case shell.Activate:
		return c.markUrgent(w)
	case shell.SetFullscreen:
		return c.update(c.wm.SetFullscreen(w.id, r.On))
	case shell.SetMaximized:
		_, err := t.Configure(t.Requested())
		return err
	}
	return nil
}

func (c *Compositor) WM() wm.State {
	return c.wm
}

func (c *Compositor) Urgent(t *shell.Toplevel) bool {
	w := c.windowOf(xdgToplevel{t})
	return w != nil && w.urgent
}

func (c *Compositor) ClientResponsive(cl *server.Client, responsive bool) error {
	for _, w := range c.windows {
		if s := w.top.Surface(); s != nil && s.Client() == cl {
			w.hung = !responsive
		}
	}
	return nil
}
