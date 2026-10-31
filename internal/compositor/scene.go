package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	spt "github.com/stubbedev/lys-wm/internal/protocol/server/presentationtime"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) Frame(b backend.Output) error {
	o := c.output(b)
	if o == nil {
		return backend.Broken("compositor: frame on unknown output %s", b.Name())
	}
	if err := c.render(o); err != nil {
		return c.dropFrame(o, fmt.Errorf("compositor: frame on %s: %w", b.Name(), err))
	}
	if err := c.finishClosing(o, c.frameTime); err != nil {
		return err
	}
	c.finishSwitches(o, c.frameTime)
	if err := c.settleWobbles(); err != nil {
		return err
	}
	if c.animating(o, c.frameTime) {
		if err := c.schedule(o); err != nil {
			return err
		}
	}
	if n := c.dropped[o]; n > 0 {
		delete(c.dropped, o)
		c.report(fmt.Errorf("compositor: %s draws again after %d dropped frames", b.Name(), n))
	}
	return errors.Join(c.updateEntered(o), c.updateInhibition(), c.fifo.Latched(c.shown[o]))
}

func (c *Compositor) dropFrame(o *output.Output, err error) error {
	if backend.Fatal(err) {
		return err
	}
	if c.dropped[o] == 0 {
		c.report(fmt.Errorf("%w; dropping frames until one draws", err))
	}
	c.dropped[o]++
	c.sceneOf(o).invalidate()
	return c.schedule(o)
}

func (c *Compositor) updateInhibition() error {
	for i := range c.idle.Inhibitors() {
		for _, shown := range c.shown {
			if slices.Contains(shown, i.Surface()) {
				return c.idle.SetInhibited(true)
			}
		}
	}
	return c.idle.SetInhibited(false)
}

func (c *Compositor) sceneOf(o *output.Output) *scene {
	sc, ok := c.scenes[o]
	if !ok {
		sc = newScene()
		c.scenes[o] = sc
	}
	return sc
}

func (c *Compositor) render(o *output.Output) error {
	c.frameTime = c.clock()
	sc := c.sceneOf(o)
	bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
	pass := sc.begin()
	pass.FillRect(bounds, Background)
	shown := sc.shown[:0]
	var err error
	if c.shell.Locked() {
		shown = c.drawLocked(pass, o, bounds, shown)
	} else if shown, err = c.drawDesktop(pass, o, shown); err != nil {
		return err
	}
	if held, err := c.holdsInherited(o, len(shown) > 0); held || err != nil {
		return err
	}
	pass.cursorFrom = len(pass.ops)
	placed, err := c.placeCursor(o)
	if err != nil {
		return err
	}
	if hw, ok := c.hwCursors[o]; ok {
		hw.inFrame = !placed
	}
	if placed {
		shown = append(shown, c.cursorShown...)
	} else if shown, err = c.drawCursor(pass, o, shown); err != nil {
		return err
	}
	c.shown[o], sc.shown = shown, c.shown[o]
	for _, s := range shown {
		s.Latch()
	}
	sc.cur.cull(&sc.covered)
	if sc.diff(bounds).Empty() {
		return c.lockBlanked(o)
	}
	if direct, err := c.scanout(o, sc, bounds); err != nil || direct {
		if err != nil {
			return err
		}
		sc.presented()
		sc.invalidate()
		return errors.Join(c.lockBlanked(o), c.runCaptures(o))
	}
	f, err := o.Backend().BeginFrame()
	if err != nil {
		return err
	}
	repaint := render.Region(sc.repaintFor(f, bounds).Rects())
	p, err := f.Renderer.Begin(f.Target, repaint)
	if err != nil {
		o.Backend().AbortFrame()
		return err
	}
	sc.cur.replay(p)
	if err := p.End(); err != nil {
		o.Backend().AbortFrame()
		return err
	}
	if err := o.Backend().Present(repaint); err != nil {
		return err
	}
	sc.presented()
	return errors.Join(c.lockBlanked(o), c.runCaptures(o))
}

var windowLayers = []wm.PlacementLayer{wm.LayerTiled, wm.LayerFloating}

func (c *Compositor) drawDesktop(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	shown = c.drawLayers(pass, o, belowWindows, shown)
	pass.background = len(pass.ops)
	for _, layer := range windowLayers {
		for _, w := range c.stack {
			if w.layer == layer {
				shown = c.drawWindow(pass, o, w, shown)
			}
		}
		if layer == wm.LayerTiled {
			if err := c.drawTabBars(pass, o); err != nil {
				return shown, err
			}
		}
	}
	c.drawGooSwitch(pass, o)
	c.drawClosing(pass, o, windowLevel)
	for _, w := range c.stack {
		if w.layer != wm.LayerFullscreen {
			shown = c.drawWindowPopups(pass, o, w, shown)
		}
	}
	shown = c.drawLayers(pass, o, aboveWindows, shown)
	for _, w := range c.stack {
		if w.layer == wm.LayerFullscreen {
			shown = c.drawWindow(pass, o, w, shown)
			shown = c.drawWindowPopups(pass, o, w, shown)
		}
	}
	if err := c.evictLabels(o); err != nil {
		return shown, err
	}
	shown = c.drawLayers(pass, o, overlay, shown)
	for _, e := range c.layers {
		shown = c.drawPopups(pass, o, c.layerPopups(e), shown)
	}
	shown, err := c.drawInputPopups(pass, o, shown)
	if err != nil {
		return shown, err
	}
	shown = c.drawUnmanaged(pass, o, shown)
	shown = c.drawDragIcon(pass, o, shown)
	return shown, nil
}

func (c *Compositor) drawWindow(pass *frame, o *output.Output, w *window, shown []*surface.Surface) []*surface.Surface {
	v, drawn := c.workspaceView(o, w)
	at := v.rect(w.motion.At(c.frameTime), o.Logical())
	if !drawn || !at.Overlaps(o.Logical()) {
		return shown
	}
	if c.decision(w).DimAround {
		pass.FillRect(deviceRect(o, o.Logical()), c.dimAroundColor())
	}
	if !w.bud.Empty() && !w.motion.Done(c.frameTime) {
		c.drawBud(pass, o, w.bud, at, 1-travelled(w.motion.Source(), w.motion.At(c.frameTime), w.motion.Target()))
	}
	from := len(pass.ops)
	c.drawShadow(pass, o, w)
	mask := c.windowMask(o, w)
	if c.blurred(w) && (c.shownOpacity(w) < 1 || !c.opaque(w.top.Surface())) {
		if err := c.drawBlur(pass, o, &w.blur, deviceRect(o, w.rect), mask); err != nil {
			c.report(fmt.Errorf("compositor: blur behind window %d: %w", w.id, err))
		}
	}
	shown = c.drawClippedTree(pass, o, w.top.Surface(), c.windowOrigin(w), shown, mask, c.shownOpacity(w))
	if c.decor.Dim.Inactive {
		pass.fillMasked(deviceRect(o, w.rect), c.shownDim(w), mask)
	}
	c.drawBorder(pass, o, w)
	if at != w.tile {
		pass.remap(from, deviceRect(o, w.tile), deviceRect(o, at))
	}
	if a := w.fade.At(c.frameTime) * v.alpha; a < 1 {
		pass.fade(from, float32(max(a, 0)))
	}
	if err := errors.Join(c.drawWobble(pass, o, w, from), c.drawMorph(pass, o, w, v, from)); err != nil {
		c.report(fmt.Errorf("compositor: deform window %d: %w", w.id, err))
	}
	return shown
}

func (c *Compositor) drawWindowPopups(pass *frame, o *output.Output, w *window, shown []*surface.Surface) []*surface.Surface {
	from := len(pass.ops)
	shown = c.drawPopups(pass, o, c.popups(w), shown)
	v, _ := c.workspaceView(o, w)
	if at := v.rect(w.motion.At(c.frameTime), o.Logical()); at.Min != w.tile.Min {
		goal := deviceRect(o, w.tile)
		pass.remap(from, goal, goal.Add(deviceRect(o, at).Min.Sub(goal.Min)))
	}
	return shown
}

func deviceRect(o *output.Output, r image.Rectangle) image.Rectangle {
	return o.Scale().Rect(r.Sub(o.Position()))
}

func (c *Compositor) drawPopups(pass *frame, o *output.Output, popups []placedPopup, shown []*surface.Surface) []*surface.Surface {
	for _, p := range popups {
		if p.bounds().Overlaps(o.Logical()) {
			shown = c.drawTree(pass, o, p.popup.Surface(), flat(p.origin), shown)
		}
	}
	return shown
}

func (c *Compositor) drawTree(pass *frame, o *output.Output, s *surface.Surface, org origin, shown []*surface.Surface) []*surface.Surface {
	return c.drawClippedTree(pass, o, s, org, shown, render.Mask{}, 1)
}

func (c *Compositor) drawClippedTree(pass *frame, o *output.Output, s *surface.Surface, org origin, shown []*surface.Surface, m render.Mask, alpha float32) []*surface.Surface {
	for member := range s.Stack() {
		if member != s {
			if sub := member.Subsurface(); sub != nil {
				shown = c.drawClippedTree(pass, o, member, org.child(sub.Position()), shown, m, alpha)
			}
			continue
		}
		tex := c.textureOf(s)
		if tex == nil {
			continue
		}
		pass.drawSurface(s, tex, s.BufferSource(), org.device(o, s.Size()), s.Transform().Invert(), c.alpha.Alpha(s)*alpha, c.opaque(s), m)
		shown = append(shown, s)
	}
	return shown
}

func (c *Compositor) enteredOf(s *surface.Surface) *entered {
	for _, e := range c.entered {
		if e.s == s {
			return e
		}
	}
	e := &entered{s: s}
	c.entered = append(c.entered, e)
	return e
}

func (c *Compositor) updateEntered(o *output.Output) error {
	var errs []error
	shown := c.shown[o]
	for _, s := range shown {
		e := c.enteredOf(s)
		if slices.Contains(e.outputs, o) {
			continue
		}
		e.outputs = append(e.outputs, o)
		errs = append(errs, s.Enter(o.Resources(s.Client())), s.SetPreferredScale(uint32(maxScale(e.outputs))))
	}
	for _, e := range c.entered {
		if slices.Contains(e.outputs, o) && !slices.Contains(shown, e.s) {
			e.outputs = slices.DeleteFunc(e.outputs, func(x *output.Output) bool { return x == o })
			errs = append(errs, e.s.Leave(o.Resources(e.s.Client())), e.s.SetPreferredScale(uint32(maxScale(e.outputs))))
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) Presented(b backend.Output, p backend.Presentation) error {
	o := c.output(b)
	if o == nil {
		return nil
	}
	sc := c.sceneOf(o)
	kind, refresh, seq := spt.WpPresentationFeedbackKindVsync, p.Refresh, p.Seq
	switch {
	case p.Idle:
		kind, refresh, seq = 0, 0, sc.last.Seq
	case p.Torn:
		kind = 0
	}
	if !p.Idle {
		sc.last = p
	}
	var errs error
	for _, s := range c.shown[o] {
		c.outputResources = o.AppendResources(c.outputResources[:0], s.Client())
		errs = errors.Join(errs,
			s.FrameDone(wire.Millis(p.Time)),
			s.Presented(c.outputResources, p.Time, refresh, seq, kind))
	}
	if p.Idle {
		return errs
	}
	return errors.Join(errs, c.timing.Presented())
}
