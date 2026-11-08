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
	clip          image.Rectangle
	stalled       bool
	stalls        int
	member        *member
	tile          image.Rectangle
	rect          image.Rectangle
	visible       bool
	offscreen     bool
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
	scratch       bool
	pinned        bool
	wsLook        wm.WorkspaceLook
	looked        bool
	border        render.Border
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
	initialAppID  string
	initialTitle  string
	stated        wm.Decision
}

type decided struct {
	decider  wm.Decider
	override *wm.Decision
	lookGen  uint64
	looked   bool
	look     wm.Look
	mica     bool
}

func (c *Compositor) subject(w *window) wm.Subject {
	s := subjectOf(w.top, w.layer == wm.LayerFloating)
	s.InitialAppID, s.InitialTitle = w.initialAppID, w.initialTitle
	s.State.Focused, s.State.Fullscreen, s.State.Urgent, s.State.Captured = w == c.active, w.layer == wm.LayerFullscreen, w.urgent, c.shared(w)
	return s
}

func subjectOf(t toplevel, floating bool) wm.Subject {
	_, x := t.(xToplevel)
	return wm.Subject{AppID: t.AppID(), Title: t.Title(), Tag: t.Tag(), InitialAppID: t.AppID(), InitialTitle: t.Title(), XWayland: x, State: wm.WindowState{Floating: floating}}
}

func (c *Compositor) decision(w *window) *wm.Decision {
	k := &w.decided
	d, changed := k.decider.Decide(c.rules, c.rulesGen, c.subject(w), k.override)
	if changed || !k.looked || k.lookGen != c.rulesGen {
		k.look, k.mica, k.lookGen, k.looked = d.Look.Over(c.decor.Blur.Look), micaOf(d, c.decor.Blur.Mica.Enabled), c.rulesGen, true
	}
	return d
}

func (c *Compositor) windowOrigin(w *window) origin {
	if _, ok := w.top.(xToplevel); ok {
		return c.xOrigin(c.toX(w.rect).Min)
	}
	return flat(w.rect.Min.Add(c.letterboxed(w)).Sub(w.top.Geometry().Min))
}

func (c *Compositor) letterboxed(w *window) image.Point {
	if w.layer != wm.LayerFullscreen {
		return image.Point{}
	}
	gap := w.rect.Size().Sub(w.top.Geometry().Size())
	return image.Pt(max(gap.X, 0)/2, max(gap.Y, 0)/2)
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

func (c *Compositor) wanted(rect image.Rectangle, p wm.Placement, activated bool) want {
	w := want{rect: rect, layer: p.Layer, told: p.Told(), activated: activated}
	on := clipped(rect, p)
	if o := c.outputAtRect(on); o != nil {
		w.placed, w.scale, w.bounds = true, c.scaleOver(on), c.usable[o].Size()
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
	c.wm = s.FollowFocus(c.spacing(), c.settings.Scroll.Center)
	c.stateUpdates++
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
			return c.update(c.wm.FocusWindow(m.id, wm.Raise))
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
		if target != nil {
			c.setPeek(target.output, false, now)
		}
		c.active = target
		c.focusMoved()
		if target != nil {
			if target.urgent {
				c.stateChanged()
			}
			c.layerFocus, target.urgent = nil, false
			err = errors.Join(err, target.top.ping())
		}
	}
	c.refreshLooks(now)
	c.publishWorkArea()
	if !placed && !focusMoved {
		return err
	}
	return errors.Join(err, c.publish())
}

func (c *Compositor) publish() error {
	return errors.Join(c.syncOverview(c.clock()), c.refocus(), c.refreshPointer(), c.scheduleAll(), c.syncForeign(), c.syncWorkspaces(), c.syncCaptureSessions(), c.updateInhibition(), c.runOnCreatedEmpty())
}

func errUnknownWindow(id wm.WindowID) error {
	return fmt.Errorf("compositor: the window manager placed unknown window %d", id)
}

func (c *Compositor) install(a wm.Arrangement, now time.Duration) error {
	var err error
	c.tabBars, c.rings = a.TabBars, a.Rings
	c.stack = c.stack[:0]
	for _, p := range a.Windows {
		w := c.windowByID(p.ID)
		if w == nil {
			continue
		}
		c.stack = append(c.stack, w)
		rect := content(p.Rect, p.Layer, c.frameBorder(c.decision(w), p.Look), c.titlebarHeight(w.top, p.Layer))
		moved := rect != w.rect
		switch {
		case c.overview != nil:
		case p.Minimized && !w.minimized:
			err = errors.Join(err, c.startMinimize(w, now))
		case !p.Minimized && w.minimized:
			err = errors.Join(err, c.startRestore(w, p.Rect, now))
		}
		c.move(w, p, now)
		w.tile, w.rect, w.visible, w.offscreen, w.laid, w.workspace, w.layer, w.minimized, w.scratch, w.pinned, w.wsLook, w.clip = p.Rect, rect, p.Visible, p.Offscreen, p.Laid, p.Workspace, p.Layer, p.Minimized, p.Scratch, p.Pinned, p.Look, p.Clip
		if o := c.outputAtRect(clipped(p.Rect, p)); o != nil {
			w.output = o
		}
		if moved && w.visible {
			err = errors.Join(err, c.reconstrain(w))
		}
	}
	c.trackSwitches(now)
	c.trackScratchpads(now)
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
	case !w.slides(p, now) || (c.interaction != nil && c.interaction.w == w) || c.gesture.view.tracking:
		w.motion.Jump(p.Rect)
	case p.Rect != w.motion.Target():
		w.motion.Set(p.Rect, now, c.animations.Curve(anim.WindowsMove))
	}
}

func (w *window) shown(now time.Duration) bool {
	return w.visible || w.offscreen && !w.motion.Done(now)
}

func (w *window) slides(p wm.Placement, now time.Duration) bool {
	if p.Visible {
		return w.visible || w.offscreen
	}
	return p.Offscreen && w.shown(now)
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
		return w.shown(now) && (!w.motion.Done(now) || !w.fade.Done(now) || w.looking(now) || c.borderMoving(w, now) || w.wobble != nil || w.genie != nil) && w.motion.Source().Union(w.motion.Target()).Overlaps(o.Logical())
	}) || c.overviewAnimating(o, now) || c.switcherWaiting(o) || c.switcherDirty(o) || slices.ContainsFunc(c.closing, func(cl *closing) bool { return cl.output == o }) || c.switchAnimates(o) || c.scratchSlideOf[o] != nil || c.peekAnimates(o, now) || c.shaderAnimates(o) || c.zoomMoves(o, now) || c.cursorGrows(o, now) || c.previewMoves(o, now) || c.lockFading(now) || slices.ContainsFunc(c.layers, func(e *layer) bool {
		return e.output == o && e.appeared && (!e.motion.Done(now) || !e.fade.Done(now))
	})
}

func content(tile image.Rectangle, layer wm.PlacementLayer, border, bar int) image.Rectangle {
	if layer == wm.LayerFullscreen {
		return tile
	}
	r := tile.Inset(border)
	r.Min.Y = min(r.Min.Y+bar, r.Max.Y)
	return r
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

func (c *Compositor) ruled(t toplevel) (wm.Decision, bool) {
	wants := t.parent() != nil || t.prefersFloating() || fixedSize(t.sizeLimits())
	s := subjectOf(t, wants)
	if w := c.windowOf(t); w != nil {
		s.InitialAppID, s.InitialTitle = w.initialAppID, w.initialTitle
	}
	d := wm.Decide(c.rules, s)
	return d, d.Mode == wm.RuleFloat || (wants && d.Mode != wm.RuleTile) || c.draggedToplevel(t)
}

func fixedSize(minimum, maximum image.Point) bool {
	return minimum != (image.Point{}) && minimum == maximum
}

func (c *Compositor) ToplevelInitialCommit(st *shell.Toplevel) error {
	t := xdgToplevel{st}
	s, err := c.mapped(t, c.nextWindow, false)
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(c.spacing()).Windows {
		if p.ID == c.nextWindow {
			d, _ := c.ruled(t)
			return t.apply(c.wanted(content(p.Rect, p.Layer, c.borderWidthOf(&d), c.titlebarHeight(t, p.Layer)), p, true))
		}
	}
	return fmt.Errorf("compositor: the window manager did not place new window %d", c.nextWindow)
}

func (c *Compositor) mapped(t toplevel, id wm.WindowID, swallow bool) (wm.State, error) {
	d, float := c.ruled(t)
	o := wm.Opening{
		Float:    float,
		Cover:    c.settings.NewOverCover,
		Autotile: c.settings.Autotile,
		Mode:     openingMode(t.wantsMode(), d),
		Group:    d.Group,
		Pin:      d.Pin != nil && *d.Pin,
		NoFocus:  d.Focus != nil && !*d.Focus,
	}
	if i := c.launchToken(t); i >= 0 {
		tok := c.tokens[i]
		o.Workspace, o.Scratchpad, o.Silent = tok.workspace, tok.scratchpad, true
		o.Scratch = c.settings.Scratchpads[tok.scratchpad]
	}
	if d.Output != nil {
		if out := c.outputMatching(*d.Output); out != nil {
			o.Output, o.Workspace, o.Scratchpad = out.Backend().Name(), 0, ""
		}
	}
	if d.Workspace != nil {
		o.Workspace, o.Scratchpad, o.Silent = d.Workspace.ID, "", d.Workspace.Silent
	}
	if float {
		o.Rect = c.floatRect(t, d, c.openingArea(t, o))
	}
	if swallow && o.Workspace == 0 && o.Output == "" && o.Scratchpad == "" && o.Group == nil && o.Mode == wm.ModeNormal {
		o.Swallow = c.swallowTarget(t, float)
	}
	return c.wm.Open(id, o)
}

func openingMode(wants wm.WindowMode, d wm.Decision) wm.WindowMode {
	switch {
	case d.Fullscreen != nil && *d.Fullscreen:
		return wm.ModeFullscreen
	case d.Maximized != nil && *d.Maximized:
		return wm.ModeMaximized
	case wants == wm.ModeFullscreen && (d.Fullscreen != nil || d.Suppress.Has(wm.SuppressFullscreen)):
		return wm.ModeNormal
	case wants == wm.ModeMaximized && (d.Maximized != nil || d.Suppress.Has(wm.SuppressMaximize)):
		return wm.ModeNormal
	}
	return wants
}

func (c *Compositor) outputMatching(m wm.OutputMatch) *output.Output {
	for _, o := range c.outputs {
		if m.Matches(o.Backend().Name(), o.Backend().Description()) {
			return o
		}
	}
	return nil
}

func (c *Compositor) openingArea(t toplevel, o wm.Opening) image.Rectangle {
	name := o.Output
	if o.Workspace != 0 {
		if ws, ok := c.wm.Workspace(o.Workspace); ok {
			name = ws.Output
		}
	}
	for _, out := range c.outputs {
		if out.Backend().Name() == name {
			return c.usable[out].Add(out.Position())
		}
	}
	return c.floatArea(t)
}

func (c *Compositor) floatRect(t toplevel, d wm.Decision, area image.Rectangle) image.Rectangle {
	border := c.borderWidthOf(&d)
	size := floatSize(t, d.Size.In(area.Size())).Add(image.Pt(2*border, 2*border))
	switch d.Position.Kind {
	case wm.PositionCursor:
		at := c.cursor.pixel().Sub(size.Div(2))
		return clampInto(image.Rectangle{Min: at, Max: at.Add(size)}, area)
	case wm.PositionAt:
		at := area.Min.Add(image.Pt(d.Position.X.Of(area.Dx()), d.Position.Y.Of(area.Dy())))
		return image.Rectangle{Min: at, Max: at.Add(size)}
	}
	return wm.Centered(area, size)
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

func (c *Compositor) frameLimits(w *window) wm.Limits {
	minimum, maximum := w.top.sizeLimits()
	d := c.decision(w)
	minimum, maximum = ruledLimits(d, minimum, maximum)
	frame := 2 * c.borderWidthOf(d)
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
	s, err := c.wm.SetLimits(w.id, c.frameLimits(w))
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
	if w == nil {
		return nil
	}
	if err := c.restate(w); err != nil || c.wm.Minimized(w.id) || c.wm.Floats(w.id) != w.floated {
		return err
	}
	d, float := c.ruled(t)
	if float == w.floated {
		return nil
	}
	w.floated = float
	if float {
		return c.update(c.wm.Float(w.id, c.floatRect(t, d, c.floatArea(t))))
	}
	return c.update(c.wm.Tile(w.id, c.settings.Autotile))
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
	case c.active != nil && !c.peekHidesInput(c.active):
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
	w := &window{id: c.nextWindow, top: t, motion: anim.NewRectMotion(image.Rectangle{}), fade: anim.NewTween(1.0, anim.Lerp), look: newFocusLook(), initialAppID: t.AppID(), initialTitle: t.Title()}
	if p, ok := c.plannedWindow(t); ok {
		w.id = p.ID
	} else {
		c.nextWindow++
	}
	_, w.floated = c.ruled(t)
	w.stated = *c.decision(w)
	s, err := c.mapped(t, w.id, true)
	if err != nil {
		return err
	}
	if s, err = s.SetLimits(w.id, c.frameLimits(w)); err != nil {
		return err
	}
	for _, p := range s.Arrange(c.spacing()).Windows {
		if p.ID == w.id && t.scaled() {
			w.scale = c.scaleOver(p.Rect)
		}
	}
	c.windows = append(c.windows, w)
	c.consumeLaunch(t)
	if err := c.update(s, nil); err != nil {
		return err
	}
	if err := c.followDrag(); err != nil {
		return err
	}
	return c.endPeek(w.output)
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
	closeErr = errors.Join(closeErr, c.castGone(w, nil))
	c.dropToplevelDrag(t)
	focused := c.active == w
	if focused {
		c.active = nil
	}
	c.forgetFocus(w)
	if err := c.switcherForget(w); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if c.hovered == w {
		c.hovered = nil
	}
	if c.interaction != nil && c.interaction.w == w {
		c.interaction = nil
		c.hidePreview(c.clock())
	}
	if err := c.update(c.wm.UnmapWindow(w.id)); err != nil || !focused {
		return errors.Join(closeErr, err)
	}
	return errors.Join(closeErr, c.focusUnderPointer())
}

func (c *Compositor) ToplevelChanged(t *shell.Toplevel, change shell.Change) error {
	c.stateChanged()
	switch change {
	case shell.ChangedSizeLimits:
		return errors.Join(c.refreshLimits(xdgToplevel{t}), c.redecide(xdgToplevel{t}))
	case shell.ChangedTitle, shell.ChangedAppID, shell.ChangedTag, shell.ChangedParent:
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
	if a, ok := r.(shell.Activate); ok {
		return c.presentToken(t, a.Token)
	}
	w := c.windowOf(xdgToplevel{t})
	if w == nil {
		return nil
	}
	switch r := r.(type) {
	case shell.SetFullscreen:
		return c.clientFullscreen(w, r.On)
	case shell.SetMaximized:
		return c.clientMaximize(w, r.On)
	case shell.SetMinimized:
		return c.minimize(w)
	case shell.Move:
		return c.xdgDrag(w, t, r.Serial, wm.DragMove, 0)
	case shell.Resize:
		return c.xdgDrag(w, t, r.Serial, wm.DragResize, xdgEdges(r.Edges))
	case shell.ShowWindowMenu:
		return c.showWindowMenu(w, t, r)
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

func (c *Compositor) clientFullscreen(w *window, on bool) error {
	if c.decision(w).Suppress.Has(wm.SuppressFullscreen) {
		return nil
	}
	return c.update(c.wm.ClientRequestsFullscreen(w.id, on))
}

func (c *Compositor) clientMaximize(w *window, on bool) error {
	if c.decision(w).Suppress.Has(wm.SuppressMaximize) {
		return nil
	}
	return c.update(c.wm.SetMaximized(w.id, on))
}

func (c *Compositor) shared(w *window) bool {
	return slices.ContainsFunc(c.captureSessions, func(s *captureSession) bool {
		switch src := s.source.(type) {
		case windowSource:
			return src.window == w
		case outputSource:
			return src.output == w.output
		}
		return false
	})
}

func (c *Compositor) RuleEvaluations() int {
	n := 0
	for _, w := range c.windows {
		n += w.decided.decider.Evaluations
	}
	return n
}
