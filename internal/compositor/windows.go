package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type window struct {
	id            wm.WindowID
	top           toplevel
	goal          image.Rectangle
	stalled       bool
	member        *member
	tile          image.Rectangle
	rect          image.Rectangle
	visible       bool
	laid          bool
	workspace     wm.WorkspaceID
	layer         wm.PlacementLayer
	output        *output.Output
	urgent        bool
	hung          bool
	scale         output.Scale
	floated       bool
	motion        anim.RectMotion
	fade          anim.Tween[float64]
	appeared      bool
	firstShown    bool
	framesUnshown int
	look          focusLook
	looked        bool
	border        Border
	borderAngle   anim.Tween[float64]
	snapshot      render.TextureTarget
	frost         *frost
	decided       decided
	wobble        *anim.Wobble
	bud           image.Rectangle
	jelly         render.TextureTarget
	jellyOps      []drawOp
	jellyArea     image.Rectangle
	jellyOut      colorOut
	jellyVersion  uint64
	minimized     bool
	icon          iconHint
	genie         *genie
}

type decided struct {
	appID, title string
	rules        uint64
	d            wm.Decision
	look         wm.Look
	mica         bool
}

func (c *Compositor) decision(w *window) *wm.Decision {
	appID, title := w.top.AppID(), w.top.Title()
	k := &w.decided
	if k.rules != c.rulesGen || k.appID != appID || k.title != title {
		*k = decided{appID: appID, title: title, rules: c.rulesGen, d: wm.Decide(c.rules, appID, title)}
		k.look, k.mica = k.d.Look.Over(c.decor.Blur.Look), micaOf(&k.d, c.decor.Blur.Mica.Enabled)
	}
	return &k.d
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
	var s output.Scale
	for _, o := range c.outputs {
		if o.Logical().Overlaps(r) {
			s = max(s, o.Scale())
		}
	}
	return s
}

func (c *Compositor) update(s wm.State, err error) error {
	if err != nil {
		return err
	}
	c.wm = s
	c.stateChanged()
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
	if c.interaction != nil {
		c.interaction.drop.stale = true
	}
	focused, hasFocus := c.wm.Focused()
	if hasFocus {
		if m := c.modalChild(c.windowByID(focused)); m != nil {
			return c.update(c.wm.FocusWindow(m.id))
		}
	}
	c.layoutDirty, c.lagging = false, false
	now := c.clock()
	x := c.freeArranger()
	c.wm.ArrangeInto(x, c.spacing())
	a := x.Arrangement
	var err error
	if c.txn != nil && !sameArrangement(a, c.txn.goal) {
		err = c.land(false)
	}
	held := c.txn != nil
	awaited, sent := c.sendGoal(a)
	err = errors.Join(err, sent)
	placed := false
	switch {
	case held:
	case len(awaited) > 0 && c.holdable() && (c.holdOpening || !c.opening(a)):
		err = errors.Join(err, c.begin(x, awaited))
	default:
		placed = true
		c.shownArranger = x
		err = errors.Join(err, c.install(a, now))
	}
	var target *window
	if hasFocus {
		target = c.windowByID(focused)
	}
	focusMoved := target != c.active
	if focusMoved {
		c.active = target
		if target != nil {
			if target.urgent {
				c.stateChanged()
			}
			c.layerFocus, target.urgent = nil, false
			err = errors.Join(err, target.top.ping())
		}
	}
	c.refreshLooks(now)
	if !placed && !focusMoved {
		return err
	}
	return errors.Join(err, c.publish())
}

func (c *Compositor) publish() error {
	return errors.Join(c.syncOverview(c.clock()), c.refocus(), c.refreshPointer(), c.scheduleAll(), c.syncForeign(), c.syncWorkspaces(), c.syncCaptureSessions())
}

func errUnknownWindow(id wm.WindowID) error {
	return fmt.Errorf("compositor: the window manager placed unknown window %d", id)
}

func (c *Compositor) install(a wm.Arrangement, now time.Duration) error {
	var err error
	c.tabBars = a.TabBars
	c.stack = c.stack[:0]
	for _, p := range a.Windows {
		w := c.windowByID(p.ID)
		if w == nil {
			continue
		}
		c.stack = append(c.stack, w)
		rect := c.content(p.Rect, p.Layer)
		moved := rect != w.rect
		switch {
		case c.overview != nil:
		case p.Minimized && !w.minimized:
			err = errors.Join(err, c.startMinimize(w, now))
		case !p.Minimized && w.minimized:
			err = errors.Join(err, c.startRestore(w, p.Rect, now))
		}
		c.move(w, p, now)
		w.tile, w.rect, w.visible, w.laid, w.workspace, w.layer, w.minimized = p.Rect, rect, p.Visible, p.Laid, p.Workspace, p.Layer, p.Minimized
		if o := c.outputAtRect(p.Rect); o != nil {
			w.output = o
		}
		if moved && w.visible {
			err = errors.Join(err, c.reconstrain(w))
		}
	}
	c.trackSwitches(now)
	c.stateChanged()
	return err
}

func (c *Compositor) move(w *window, p wm.Placement, now time.Duration) {
	switch {
	case !w.appeared:
		w.appeared = true
		w.motion.Jump(p.Rect)
		if p.Visible {
			c.appear(w, p.Rect, now)
		}
	case c.interaction != nil && c.interaction.w == w && c.animations.Style(anim.WindowsMove).Kind == anim.Elastic:
		c.wobbleWith(w, p.Rect, now)
		w.motion.Jump(p.Rect)
	case c.interaction != nil && c.interaction.w == w && c.springMoves():
		w.motion.Set(p.Rect, now, c.animations.Curve(anim.WindowsMove))
	case !w.visible || !p.Visible || (c.interaction != nil && c.interaction.w == w):
		w.motion.Jump(p.Rect)
	case p.Rect != w.motion.Target():
		w.motion.Set(p.Rect, now, c.animations.Curve(anim.WindowsMove))
	}
}

func (c *Compositor) springMoves() bool {
	_, ok := c.animations.Curve(anim.WindowsMove).(anim.Spring)
	return ok
}

func (c *Compositor) Animating(o *output.Output) bool {
	return c.animating(o, c.clock())
}

func (c *Compositor) animating(o *output.Output, now time.Duration) bool {
	return slices.ContainsFunc(c.windows, func(w *window) bool {
		return w.visible && (!w.motion.Done(now) || !w.fade.Done(now) || w.looking(now) || c.borderMoving(w, now) || w.wobble != nil || w.genie != nil) && w.motion.Source().Union(w.motion.Target()).Overlaps(o.Logical())
	}) || c.overviewAnimating(o, now) || slices.ContainsFunc(c.closing, func(cl *closing) bool { return cl.output == o }) || c.switchAnimates(o) || c.shaderAnimates(o) || c.zoomMoves(o, now) || c.previewMoves(o, now) || c.lockFading(now) || slices.ContainsFunc(c.layers, func(e *layer) bool {
		return e.output == o && e.appeared && (!e.motion.Done(now) || !e.fade.Done(now))
	})
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

func (c *Compositor) settle(w *window, goal want) error {
	if w.top.settled(goal) && w.scale == goal.scale {
		return nil
	}
	w.scale = goal.scale
	return w.top.apply(goal)
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
	if float, size := c.floats(t); float && !t.wantsFullscreen() {
		return t.apply(want{rect: image.Rectangle{Max: floatSize(t, size)}, activated: true})
	}
	s, err := c.mapped(t, c.nextWindow)
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(c.spacing()).Windows {
		if p.ID == c.nextWindow {
			return t.apply(c.wanted(c.content(p.Rect, p.Layer), p.Layer, true))
		}
	}
	return fmt.Errorf("compositor: the window manager did not place new window %d", c.nextWindow)
}

func (c *Compositor) mapped(t toplevel, id wm.WindowID) (wm.State, error) {
	var s wm.State
	var err error
	if float, size := c.floats(t); float {
		s, err = c.wm.MapFloating(id, c.floatRect(t, size))
	} else {
		s, err = c.wm.MapWindow(id, c.settings.Autotile)
	}
	if err != nil || !t.wantsFullscreen() {
		return s, err
	}
	return s.SetFullscreen(id, true)
}

func (c *Compositor) floatRect(t toplevel, size image.Point) image.Rectangle {
	size = floatSize(t, size).Add(image.Pt(2*c.decor.BorderWidth, 2*c.decor.BorderWidth))
	return wm.Centered(c.floatArea(t), size)
}

func (c *Compositor) floatArea(t toplevel) image.Rectangle {
	if p := c.windowOf(t.parent()); p != nil {
		return p.tile
	}
	if o := c.outputAt(c.cursor); o != nil {
		return c.usable[o].Add(o.Position())
	}
	return image.Rectangle{}
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
	s, err := c.wm.SetLimits(w.id, c.frameLimits(t))
	if err != nil {
		return err
	}
	was, floating := c.wm.FloatingRect(w.id)
	now, _ := s.FloatingRect(w.id)
	if over := c.floatArea(t); floating && now != was && was == wm.Centered(over, was.Size()) {
		s, err = s.SetFloatingRect(w.id, wm.Centered(over, now.Size()))
	}
	return c.update(s, err)
}

func (c *Compositor) redecide(t toplevel) error {
	w := c.windowOf(t)
	if w == nil || c.wm.Minimized(w.id) || c.wm.Floats(w.id) != w.floated {
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
	if c.overviewHolds() {
		return c.overviewKeyboardTarget()
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
	w := &window{id: c.nextWindow, top: t, motion: anim.NewRectMotion(image.Rectangle{}), fade: anim.NewTween(1.0, anim.Lerp), look: newFocusLook()}
	if p, ok := c.plannedWindow(t); ok {
		w.id = p.ID
	} else {
		c.nextWindow++
	}
	w.floated, _ = c.floats(t)
	s, err := c.mapped(t, w.id)
	if err != nil {
		return err
	}
	if s, err = s.SetLimits(w.id, c.frameLimits(t)); err != nil {
		return err
	}
	for _, p := range s.Arrange(c.spacing()).Windows {
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
	closeErr := c.startClosing(w, c.clock())
	closeErr = errors.Join(closeErr, c.dropJelly(w))
	c.windows = slices.DeleteFunc(c.windows, func(x *window) bool { return x == w })
	c.stack = slices.DeleteFunc(c.stack, func(x *window) bool { return x == w })
	if c.active == w {
		c.active = nil
	}
	if c.interaction != nil && c.interaction.w == w {
		c.interaction = nil
		c.hidePreview(c.clock())
	}
	return errors.Join(closeErr, c.update(c.wm.UnmapWindow(w.id)))
}

func (c *Compositor) ToplevelChanged(t *shell.Toplevel, change shell.Change) error {
	c.stateChanged()
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
	if c.overview != nil {
		return errors.Join(c.scheduleAll(), c.syncForeign())
	}
	return c.syncForeign()
}

func (c *Compositor) markUrgent(w *window) error {
	if w == c.active || w.urgent {
		return nil
	}
	w.urgent = true
	c.stateChanged()
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
	case shell.SetMinimized:
		return c.minimize(w)
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
			c.stateChanged()
		}
	}
	return nil
}
