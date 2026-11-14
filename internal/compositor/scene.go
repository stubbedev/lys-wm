package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/frametrace"
	"github.com/stubbedev/lys-wm/internal/output"
	spt "github.com/stubbedev/lys-wm/internal/protocol/server/presentationtime"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) Frame(b backend.Output) error {
	p, err := c.placed(b, "frame on")
	if err != nil || c.parked {
		return err
	}
	return p.frame(c, b)
}

func (c *Compositor) liveFrame(o *output.Output) error {
	if c.frames == nil {
		return c.frame(o)
	}
	b := o.Backend()
	c.frames.Begin(b.Name())
	err := c.frame(o)
	c.frames.End(b.Name(), c.animating(o, c.frameTime), c.frameCost(o))
	return err
}

type tally struct {
	thumbs, filterPx, repaintPx, copied uint64
}

func (c *Compositor) kawase(p render.Pass, from render.Texture, dst image.Rectangle, damage render.Region, offset float32, up bool) {
	c.tallied.filterPx += uint64(damage.AreaIn(dst)) //nolint:gosec // an area is not negative
	p.Kawase(from, dst, offset, up)
}

func (c *Compositor) frameCost(o *output.Output) frametrace.Cost {
	sc := c.sceneOf(o)
	now := c.renderer.Counts()
	copied := c.copiedBytes.Load()
	cost := frametrace.Cost{
		Draws:       uint64(len(sc.prev.ops)),
		Passes:      now.Passes - c.tracedCounts.Passes,
		Filters:     now.Filters - c.tracedCounts.Filters,
		Uploads:     now.Uploads - c.tracedCounts.Uploads,
		GPUWaits:    now.GPUWaits - c.tracedCounts.GPUWaits,
		Thumbs:      c.tallied.thumbs - c.traced.thumbs,
		DamageRects: uint64(max(len(sc.damage.Rects()), len(sc.repaint.Rects()))),
		Submits:     now.Submits - c.tracedCounts.Submits,
		Targets:     now.Targets - c.tracedCounts.Targets,
		FilterPx:    c.tallied.filterPx - c.traced.filterPx,
		RepaintPx:   c.tallied.repaintPx - c.traced.repaintPx,
		Copied:      copied - c.traced.copied,
		Overview:    c.OverviewProgress(c.frameTime),
	}
	c.tracedCounts, c.traced = now, c.tallied
	c.traced.copied = copied
	return cost
}

func (c *Compositor) frame(o *output.Output) error {
	b := o.Backend()
	if c.dragPending {
		if err := c.recovered(c.flushDrag()); err != nil {
			return err
		}
	}
	if c.layoutDirty && c.txn == nil {
		if err := c.recovered(c.reconcile()); err != nil {
			return err
		}
	}
	if err := c.render(o); err != nil {
		return c.dropLiveFrame(o, err)
	}
	if err := errors.Join(c.redrawThumb(o), c.redrawSwitcherThumbs(o)); err != nil {
		return c.dropLiveFrame(o, err)
	}
	if err := c.checkGPU(); err != nil {
		return c.dropLiveFrame(o, err)
	}
	if err := c.finishClosing(o, c.frameTime); err != nil {
		return err
	}
	c.finishSwitches(o, c.frameTime)
	c.finishScratchSlides(o, c.frameTime)
	c.finishPeek(o, c.frameTime)
	if err := c.stepCursorGrowth(o, c.frameTime); err != nil {
		return err
	}
	c.settleGenies(c.frameTime)
	c.settlePreview(c.frameTime)
	if err := c.settleLockFade(c.frameTime); err != nil {
		return err
	}
	if err := c.settleWobbles(); err != nil {
		return err
	}
	animated := c.animating(o, c.frameTime) || c.debugDraw.fading(c.sceneOf(o))
	c.sceneOf(o).animated = animated
	if animated {
		if err := c.schedule(o); err != nil {
			return err
		}
	}
	c.drew(b)
	return errors.Join(c.updateEntered(o), c.updateInhibition(), c.fifo.Latched(c.shown[o]))
}

func (c *Compositor) dropLiveFrame(o *output.Output, err error) error {
	b := o.Backend()
	return c.dropFrame(b, err, func() error {
		c.frames.Drop(b.Name())
		c.sceneOf(o).invalidate()
		return c.schedule(o)
	})
}

func (c *Compositor) dropFrame(b backend.Output, err error, retry func() error) error {
	err = fmt.Errorf("compositor: frame on %s: %w", b.Name(), err)
	if backend.Fatal(err) {
		return err
	}
	if lost := c.renderer.Lost(); lost != nil {
		if rerr := c.recoverGPU(lost); backend.Fatal(rerr) {
			return rerr
		} else if rerr != nil {
			c.report(rerr)
		}
	}
	if c.dropped[b] == 0 {
		c.report(fmt.Errorf("%w; dropping frames until one draws", err))
	}
	c.dropped[b]++
	return retry()
}

func (c *Compositor) drew(b backend.Output) {
	if n := c.dropped[b]; n > 0 {
		delete(c.dropped, b)
		c.report(fmt.Errorf("compositor: %s draws again after %d dropped frames", b.Name(), n))
	}
}

func (c *Compositor) updateInhibition() error {
	if c.rulesInhibitIdle() {
		return c.idle.SetInhibited(true)
	}
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
	c.frameTime = c.aimed(o, c.clock())
	c.prepareSwitch(o)
	if err := c.settleOverview(c.frameTime); err != nil {
		return err
	}
	sc := c.sceneOf(o)
	if err := c.settleZoom(sc); err != nil {
		return err
	}
	bounds := image.Rectangle{Max: o.PixelSize()}
	shading := c.shading(o)
	if err := sc.useShader(shading); err != nil {
		return err
	}
	pass := sc.begin()
	pass.FillRect(bounds, Background)
	shown := sc.shown[:0]
	var err error
	locked, lockAlpha := c.shell.Locked(), c.lockOpacity(c.frameTime)
	if !locked || lockAlpha < 1 {
		if shown, err = c.drawDesktop(pass, o, shown); err != nil {
			return err
		}
	}
	if locked {
		shown = c.drawLocked(pass, o, bounds, shown, lockAlpha)
	} else {
		if err := c.drawConfigNotice(pass, o); err != nil {
			return err
		}
		c.drawLockSnapshot(pass, o, bounds, lockAlpha)
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
	c.noteFirstShown(shown)
	for _, s := range shown {
		s.Latch()
	}
	sc.noteDrawn(&sc.cur)
	sc.cur.clip()
	c.resolveBlurs(o, &sc.cur, 0)
	sc.noteKept(&sc.cur)
	if err := c.drawnNow(o, shown); err != nil {
		return err
	}
	if err := c.throttleLeft(o, sc.shown); err != nil {
		return err
	}
	sc.cur.dropHiddenRims()
	sc.zeroCopy = [1 + backend.MaxOverlayPlanes]*surface.Surface{}
	if err := c.assignOverlays(o, sc, bounds, shading != nil); err != nil {
		c.report(fmt.Errorf("compositor: an overlay plane on %s: %w", o.Backend().Name(), err))
	}
	if err := sc.blurs.dropUnused(); err != nil {
		c.report(fmt.Errorf("compositor: drop the blurs unused on %s: %w", o.Backend().Name(), err))
	}
	viewMoved := false
	if z := sc.zoom; z != nil {
		c.zoomFollows(o, z)
		view := zoomView(bounds.Max, z.focus, z.factor(c.frameTime))
		viewMoved, z.shown = view != z.shown, view
	}
	damage := sc.diff(bounds)
	if c.debugDraw != nil {
		c.debugDraw.draw(sc)
	}
	if damage.Empty() && !viewMoved && (shading == nil || !shading.animated) {
		return c.lockBlanked(o)
	}
	op, alone := c.showFrameHints(o, sc, bounds, shading == nil && sc.zoom == nil)
	if shading != nil {
		return c.renderShaded(o, sc, shading, bounds)
	}
	if sc.zoom != nil {
		return c.renderZoomed(o, sc, bounds)
	}
	if direct, err := c.scanout(o, sc, op, alone); err != nil || direct {
		if err != nil {
			return err
		}
		c.frames.Scanout(o.Backend().Name())
		sc.presented()
		sc.targetStale = true
		return errors.Join(c.lockBlanked(o), c.runCaptures(o))
	}
	if flipped, err := c.flipOverlays(o, sc); err != nil || flipped {
		if err != nil {
			return err
		}
		c.frames.Scanout(o.Backend().Name())
		sc.presented()
		return errors.Join(c.lockBlanked(o), c.runCaptures(o))
	}
	if sc.targetStale {
		sc.targetStale = false
		sc.damage.Add(bounds)
	}
	f, err := c.beginFrame(o, sc)
	if err != nil {
		return err
	}
	repaint, err := c.paintFrame(o, sc, f, bounds)
	if err != nil {
		o.Backend().AbortFrame()
		return err
	}
	c.frames.Submit(o.Backend().Name())
	if err := c.present(o, sc, repaint); err != nil {
		return err
	}
	sc.presented()
	return errors.Join(c.lockBlanked(o), c.runCaptures(o))
}

var windowLayers = []wm.PlacementLayer{wm.LayerTiled, wm.LayerFloating, wm.LayerMaximized}

func (c *Compositor) drawDesktop(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	if c.overview != nil {
		if v := c.overview.views[o]; v != nil {
			return c.drawOverview(pass, o, v, shown)
		}
	}
	shown = c.drawLayers(pass, o, belowWindows, shown)
	pass.background = len(pass.ops)
	for _, layer := range windowLayers {
		for _, w := range c.stack {
			if w.layer == layer && !w.scratch {
				shown = c.drawWindow(pass, o, w, shown)
			}
		}
		if layer == wm.LayerTiled {
			c.drawTabBars(pass, o)
			c.drawRings(pass, o)
			c.drawDragPreview(pass, o)
		}
	}
	c.drawGooSwitch(pass, o)
	c.drawClosing(pass, o, windowLevel)
	for _, w := range c.stack {
		if w.layer != wm.LayerFullscreen && !w.scratch {
			shown = c.drawWindowPopups(pass, o, w, shown)
		}
	}
	shown = c.drawLayers(pass, o, aboveWindows, shown)
	for _, w := range c.stack {
		if w.layer == wm.LayerFullscreen && !w.scratch {
			shown = c.drawWindow(pass, o, w, shown)
			shown = c.drawWindowPopups(pass, o, w, shown)
		}
	}
	shown = c.drawScratchpad(pass, o, shown)
	shown, err := c.drawSwitcher(pass, o, shown)
	if err != nil {
		return shown, err
	}
	if err := c.evictLabels(o); err != nil {
		return shown, err
	}
	shown = c.drawLayers(pass, o, overlay, shown)
	for _, e := range c.layers {
		shown = c.drawPopups(pass, o, c.layerPopups(e), shown, popupBlur{regions: true, whole: c.blursLayer(e), look: &c.decor.Blur.Look})
	}
	shown, err = c.drawInputPopups(pass, o, shown)
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
	if !drawn || !at.Overlaps(o.Logical()) || !w.clip.Empty() && !w.clip.Overlaps(o.Logical()) {
		return shown
	}
	if c.decision(w).DimAround {
		pass.FillRect(deviceRect(o, o.Logical()), c.dimAroundColor())
	}
	if !w.bud.Empty() && !w.motion.Done(c.frameTime) {
		c.drawBud(pass, o, w.bud, at, 1-travelled(w.motion.Source(), w.motion.At(c.frameTime), w.motion.Target()))
	}
	from := len(pass.ops)
	shown = c.paintWindow(pass, o, w, at, w.fade.At(c.frameTime)*v.alpha, false, shown)
	if err := errors.Join(c.drawWobble(pass, o, w, from), c.drawMorph(pass, o, w, v, from), c.drawWindowGenie(pass, o, w, from)); err != nil {
		c.report(fmt.Errorf("compositor: deform window %d: %w", w.id, err))
	}
	return shown
}

func (c *Compositor) paintWindow(pass *frame, o *output.Output, w *window, at image.Rectangle, alpha float64, wholeBorder bool, shown []*surface.Surface) []*surface.Surface {
	from := len(pass.ops)
	c.drawShadow(pass, o, w)
	mask := c.windowMask(o, w)
	if !wholeBorder {
		mask = c.drawnMask(o, w)
	}
	top := w.top.Surface()
	look, mica := &w.decided.look, w.decided.mica
	rims := len(pass.rims)
	if (c.blurred(w) || mica) && c.effects.BlurRegion(top).Empty() && c.translucent(top, c.shownOpacity(w)) {
		under := deviceRect(o, w.rect)
		c.drawBlur(pass, o, glassDraw{surface: top, shape: c.sceneOf(o).blurs.one(under), extent: under, mask: mask, alpha: c.alphaOf(top), look: look, mica: mica, tiled: w.layer == wm.LayerTiled})
	}
	if _, x := w.top.(xToplevel); !x && c.letterboxed(w) != (image.Point{}) {
		pass.FillRect(deviceRect(o, w.rect), letterbox)
	}
	tree := len(pass.ops)
	if c.honoursBlurRegions(w) {
		shown = c.drawBlurredTree(pass, o, top, c.windowOrigin(w), shown, mask, c.shownOpacity(w), look)
	} else {
		shown = c.drawClippedTree(pass, o, top, c.windowOrigin(w), shown, mask, c.shownOpacity(w))
	}
	c.stretchStale(pass, o, tree, w)
	if c.dimsInactive(w) {
		pass.fillMasked(deviceRect(o, w.rect), c.shownDim(w), mask)
	}
	if err := c.drawRims(pass, o, rims); err != nil {
		c.report(fmt.Errorf("compositor: the rim of window %d: %w", w.id, err))
	}
	c.drawTitlebar(pass, o, w, c.drawnMask(o, w))
	if !wholeBorder {
		c.drawOutlined(pass, o, w)
	}
	if at != w.tile {
		pass.remap(from, deviceRect(o, w.tile), deviceRect(o, at))
	}
	if wholeBorder {
		outer := deviceRect(o, at)
		c.drawBorderIn(pass, o, w, outer, outer.Inset(o.Scale().ToDevice(c.borderWidth(w))))
	}
	if alpha < 1 {
		pass.fade(from, float32(max(alpha, 0)))
	}
	return shown
}

func (c *Compositor) drawOwnPopups(pass *frame, o *output.Output, w *window, shown []*surface.Surface) []*surface.Surface {
	return c.drawPopups(pass, o, c.popups(w), shown, popupBlur{regions: c.honoursBlurRegions(w), whole: c.blurred(w) || w.decided.mica, look: &w.decided.look})
}

func (c *Compositor) drawWindowPopups(pass *frame, o *output.Output, w *window, shown []*surface.Surface) []*surface.Surface {
	from := len(pass.ops)
	shown = c.drawOwnPopups(pass, o, w, shown)
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

type popupBlur struct {
	regions bool
	whole   bool
	look    *wm.Look
}

func (c *Compositor) drawPopups(pass *frame, o *output.Output, popups []placedPopup, shown []*surface.Surface, blur popupBlur) []*surface.Surface {
	for _, p := range popups {
		if !p.bounds().Overlaps(o.Logical()) {
			continue
		}
		s := p.popup.Surface()
		rims := len(pass.rims)
		if blur.whole && c.effects.BlurRegion(s).Empty() && c.translucent(s, 1) {
			under := deviceRect(o, p.geometry())
			c.drawBlur(pass, o, glassDraw{surface: s, shape: c.sceneOf(o).blurs.one(under), extent: under, mask: c.popupMask(o, under), alpha: c.alphaOf(s), look: blur.look})
		}
		if blur.regions {
			shown = c.drawBlurredTree(pass, o, s, flat(p.origin), shown, render.Mask{}, 1, blur.look)
		} else {
			shown = c.drawTree(pass, o, s, flat(p.origin), shown)
		}
		if err := c.drawRims(pass, o, rims); err != nil {
			c.report(fmt.Errorf("compositor: the rim of a popup: %w", err))
		}
	}
	return shown
}

func (c *Compositor) popupMask(o *output.Output, under image.Rectangle) render.Mask {
	if c.decor.Rounding <= 0 {
		return render.Mask{}
	}
	return render.Mask{Keep: render.RoundRect{Rect: under, Radius: deviceLength(o, c.decor.Rounding)}}
}

func (c *Compositor) drawTree(pass *frame, o *output.Output, s *surface.Surface, org origin, shown []*surface.Surface) []*surface.Surface {
	return c.drawClippedTree(pass, o, s, org, shown, render.Mask{}, 1)
}

func (c *Compositor) drawClippedTree(pass *frame, o *output.Output, s *surface.Surface, org origin, shown []*surface.Surface, m render.Mask, alpha float32) []*surface.Surface {
	return c.drawSurfaces(pass, o, s, org, shown, treeStyle{mask: m, alpha: alpha})
}

func (c *Compositor) drawBlurredTree(pass *frame, o *output.Output, s *surface.Surface, org origin, shown []*surface.Surface, m render.Mask, alpha float32, look *wm.Look) []*surface.Surface {
	return c.drawSurfaces(pass, o, s, org, shown, treeStyle{mask: m, alpha: alpha, regions: true, look: look})
}

type treeStyle struct {
	mask    render.Mask
	alpha   float32
	regions bool
	look    *wm.Look
}

func (c *Compositor) drawSurfaces(pass *frame, o *output.Output, s *surface.Surface, org origin, shown []*surface.Surface, st treeStyle) []*surface.Surface {
	for member := range s.Stack() {
		if member != s {
			if sub := member.Subsurface(); sub != nil {
				shown = c.drawSurfaces(pass, o, member, org.child(sub.Position()), shown, st)
			}
			continue
		}
		t, ok := c.textures[s]
		if !ok {
			continue
		}
		if st.regions {
			c.drawBlurRegion(pass, o, s, org, st.mask, st.look)
		}
		dst, alpha := org.device(o, s.Size()), c.alphaOf(s)*st.alpha
		if f, fill := t.(filled); fill {
			pass.fillSurface(s, dst, scaleColor(f.color, alpha), st.mask)
		} else {
			pass.drawSurface(s, c.surfaceSpace(s), t.texture(), s.BufferSource(), dst, s.Transform().Invert(), alpha, c.opaque(s), st.mask)
		}
		pass.keepVisible(s, c.hyprSurfaces.VisibleRegion(s))
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
		if c.colorManager != nil {
			errs = append(errs, c.colorManager.PreferredChanged(s))
		}
	}
	for _, e := range c.entered {
		if slices.Contains(e.outputs, o) && !slices.Contains(shown, e.s) {
			e.outputs = slices.DeleteFunc(e.outputs, func(x *output.Output) bool { return x == o })
			errs = append(errs, e.s.Leave(o.Resources(e.s.Client())), e.s.SetPreferredScale(uint32(maxScale(e.outputs))))
			if c.colorManager != nil {
				errs = append(errs, c.colorManager.PreferredChanged(e.s))
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) Presented(b backend.Output, p backend.Presentation) error {
	o := c.output(b)
	if o == nil {
		return nil
	}
	c.frames.Presented(b.Name(), p)
	if c.adopting != nil {
		c.adopting.shown[o] = true
	}
	sc := c.sceneOf(o)
	kind := spt.WpPresentationFeedbackKindVsync
	if p.Idle || p.Torn {
		kind = 0
	}
	if p.Hardware {
		kind |= spt.WpPresentationFeedbackKindHwClock | spt.WpPresentationFeedbackKindHwCompletion
	}
	if !p.Idle {
		sc.last = p
	}
	var errs error
	for _, s := range c.shown[o] {
		c.outputResources = o.AppendResources(c.outputResources[:0], s.Client())
		var frame error
		if sc.culled[s] {
			frame = c.throttleFrames(s)
		} else {
			frame = s.FrameDone(wire.Millis(p.Time))
		}
		errs = errors.Join(errs, frame, s.Presented(c.outputResources, p.Time, p.Refresh, p.Variable, p.Seq, sc.feedbackKind(s, kind, p.Idle)))
	}
	if p.Idle {
		return errors.Join(errs, c.heldFrame(o))
	}
	return errors.Join(errs, c.timing.Presented(), c.heldFrame(o))
}
