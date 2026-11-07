package compositor

import (
	"fmt"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	overviewRadius      = lookUnit + 4
	overviewRingWidth   = 2
	overviewRingGap     = 2
	overviewTrayPad     = 12
	overviewLabelPx     = 12
	overviewLabelHeight = 22
	overviewLabelGap    = 6
	overviewPlusArm     = 2
	overviewGlassTint   = 0.55
	overviewRimStrength = 0.12
	overviewHoverInk    = 0.1
)

var overviewGlass wm.Look

func fadeOrDrop(pass *frame, from int, a float32) {
	switch {
	case a <= 0:
		pass.ops = pass.ops[:from]
	case a < 1:
		pass.fade(from, a)
	}
}

func (c *Compositor) drawOverview(pass *frame, o *output.Output, v *overviewView, shown []*surface.Surface) ([]*surface.Surface, error) {
	ov := c.overview
	p := ov.progress.At(c.frameTime)
	in := float32(min(max(p, 0), 1))
	shown = c.drawLayers(pass, o, belowWindows, shown)
	pass.background = len(pass.ops)
	pass.sharedBlur = true
	c.drawOverviewBackdrop(pass, o, in)
	from := len(pass.ops)
	shown = c.drawLayers(pass, o, aboveWindows, shown)
	fadeOrDrop(pass, from, 1-in)
	if in < 1 {
		from = len(pass.ops)
		c.drawTabBars(pass, o)
		fadeOrDrop(pass, from, 1-in)
	}
	if c.switches[o] == nil {
		v.leaving = nil
	}
	c.drawOverviewPane(pass, o, v, p, in)
	shown = c.drawOverviewEntries(pass, o, v, p, shown)
	c.drawOverviewStrip(pass, o, v, p, in)
	c.drawClosing(pass, o, windowLevel)
	if in > 0 {
		for _, t := range v.spaces {
			if t.id != v.workspace {
				shown = append(shown, t.surfaces...)
			}
		}
	}
	if err := c.drawOverviewSelection(pass, o, v, p, in); err != nil {
		return shown, err
	}
	shown = c.drawOverviewDragged(pass, o, p, shown)
	if err := c.evictLabels(o); err != nil {
		return shown, err
	}
	shown = c.drawLayers(pass, o, overlay, shown)
	for _, e := range c.layers {
		from = len(pass.ops)
		shown = c.drawPopups(pass, o, c.layerPopups(e), shown, popupBlur{regions: true, whole: c.blursLayer(e), look: &c.decor.Blur.Look})
		if !isOverlay(e.surface) {
			fadeOrDrop(pass, from, 1-in)
		}
	}
	shown, err := c.drawInputPopups(pass, o, shown)
	if err != nil {
		return shown, err
	}
	from = len(pass.ops)
	shown = c.drawUnmanaged(pass, o, shown)
	fadeOrDrop(pass, from, 1-in)
	return c.drawDragIcon(pass, o, shown), nil
}

func isOverlay(l *shell.LayerSurface) bool {
	return l.Current().Layer == overlay[0]
}

func (c *Compositor) drawOverviewBackdrop(pass *frame, o *output.Output, in float32) {
	if in <= 0 {
		return
	}
	bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
	if c.overviewStyle.Blur {
		c.drawBlur(pass, o, glassDraw{shape: c.sceneOf(o).blurs.one(bounds), extent: bounds, alpha: in, look: &overviewGlass, mica: true})
	}
	if d := c.overviewStyle.Dim; d > 0 {
		pass.FillRect(bounds, render.Color{A: uint8(math.Round(float64(d * in * 0xff)))})
	}
}

func (c *Compositor) drawOverviewEntries(pass *frame, o *output.Output, v *overviewView, p float64, shown []*surface.Surface) []*surface.Surface {
	sw := c.switches[o]
	if sw != nil && len(v.leaving) > 0 {
		out, _ := sw.viewOf(v.left)
		for _, e := range v.leaving {
			shown = c.drawOverviewEntry(pass, o, e, p, out, shown)
		}
	}
	view := identity
	if sw != nil {
		view, _ = sw.viewOf(v.workspace)
	}
	for _, e := range v.order {
		if pr := c.overview.press; pr != nil && pr.dragging && pr.item == e.item {
			continue
		}
		shown = c.drawOverviewEntry(pass, o, e, p, view, shown)
	}
	return shown
}

func (c *Compositor) drawOverviewEntry(pass *frame, o *output.Output, e overviewEntry, p float64, v view, shown []*surface.Surface) []*surface.Surface {
	w := e.w
	alpha := w.fade.At(c.frameTime) * v.alpha
	if !e.front {
		alpha *= min(max(p, 0), 1)
	}
	if alpha <= 0 {
		return shown
	}
	return c.drawOverviewWindow(pass, o, w, v.rect(e.rect(o, p, c.frameTime), o.Logical()), p, alpha, shown)
}

func (c *Compositor) drawOverviewWindow(pass *frame, o *output.Output, w *window, at image.Rectangle, p, alpha float64, shown []*surface.Surface) []*surface.Surface {
	if !c.drawable(w.top.Surface()) {
		r := deviceRect(o, at)
		pass.fillMasked(r, scaleColor(c.decor.Tab.Inactive.Background, float32(alpha)), render.Mask{Keep: render.RoundRect{Rect: r, Radius: deviceLength(o, overviewRadius)}})
		return shown
	}
	painted := len(pass.ops)
	shown = c.paintWindow(pass, o, w, at, alpha, true, shown)
	if w.layer == wm.LayerFullscreen && c.decor.Rounding > 0 {
		pass.roundBare(painted, deviceRect(o, at), float32(float64(deviceLength(o, c.decor.Rounding))*min(max(p, 0), 1)))
	}
	from := len(pass.ops)
	shown = c.drawOwnPopups(pass, o, w, shown)
	if at != w.tile {
		pass.remap(from, deviceRect(o, w.tile), deviceRect(o, at))
	}
	if alpha < 1 {
		pass.fade(from, float32(alpha))
	}
	return shown
}

func (c *Compositor) drawOverviewDragged(pass *frame, o *output.Output, p float64, shown []*surface.Surface) []*surface.Surface {
	pr := c.overview.press
	if pr == nil || !pr.dragging {
		return shown
	}
	for from, v := range c.overview.views {
		for _, e := range v.order {
			if e.item != pr.item {
				continue
			}
			at := e.rect(from, p, c.frameTime)
			if !at.Overlaps(o.Logical()) {
				continue
			}
			shown = c.drawOverviewWindow(pass, o, e.w, at, p, e.w.fade.At(c.frameTime), shown)
		}
	}
	return shown
}

func stripSlide(o *output.Output, r image.Rectangle, p float64) image.Rectangle {
	return anim.LerpRect(r.Sub(image.Pt(0, r.Max.Y-o.Logical().Min.Y)), r, p)
}

func (c *Compositor) drawOverviewPane(pass *frame, o *output.Output, v *overviewView, p float64, in float32) {
	if in <= 0 || v.plus.Empty() || len(v.spaces) == 0 {
		return
	}
	pane := stripSlide(o, v.spaces[0].rect.Union(v.plus).Inset(-overviewTrayPad), p)
	c.drawOverviewGlass(pass, o, deviceRect(o, pane), deviceLength(o, overviewRadius+overviewTrayPad), in)
}

func (c *Compositor) drawOverviewStrip(pass *frame, o *output.Output, v *overviewView, p float64, in float32) {
	if in <= 0 {
		return
	}
	slide := func(r image.Rectangle) image.Rectangle { return stripSlide(o, r, p) }
	radius := deviceLength(o, overviewRadius)
	for _, t := range v.spaces {
		if t.rect.Empty() {
			continue
		}
		if v.redraw == nil && t.due(deviceRect(o, t.rect).Size(), c.frameTime) {
			v.redraw = t
		}
		dst := deviceRect(o, slide(t.rect))
		if t.version == 0 {
			pass.fillMasked(dst, scaleColor(Background, in), render.Mask{Keep: render.RoundRect{Rect: dst, Radius: radius}})
			continue
		}
		drawn := t.target.Size()
		pass.ops = append(pass.ops, drawOp{
			tex: t.target.Texture(), src: render.FRect{W: float64(drawn.X), H: float64(drawn.Y)}, dst: dst, alpha: in, version: t.version,
			mask: render.Mask{Keep: render.RoundRect{Rect: dst, Radius: radius}},
		})
		c.drawOverviewRim(pass, o, dst, radius, in)
		if t.id == v.workspace {
			c.drawOverviewRing(pass, o, slide(t.rect), radius, in)
		}
	}
	if v.plus.Empty() {
		return
	}
	dst := deviceRect(o, slide(v.plus))
	c.drawOverviewGlass(pass, o, dst, radius, in)
	arm := max(o.Scale().ToDevice(overviewPlusArm), 1)
	span := min(dst.Dx(), dst.Dy()) / 4
	mid := dst.Min.Add(dst.Size().Div(2))
	ink := scaleColor(c.decor.Tab.Inactive.Text, in)
	pass.FillRect(image.Rect(mid.X-span/2, mid.Y-arm/2, mid.X+span/2, mid.Y-arm/2+arm), ink)
	pass.FillRect(image.Rect(mid.X-arm/2, mid.Y-span/2, mid.X-arm/2+arm, mid.Y+span/2), ink)
}

func (c *Compositor) overviewInk(strength, in float32) render.Color {
	return scaleColor(c.decor.Tab.Inactive.Text.Premultiply(), strength*in)
}

func (c *Compositor) drawOverviewGlass(pass *frame, o *output.Output, r image.Rectangle, radius, in float32) {
	pass.fillMasked(r, scaleColor(c.decor.Tab.Inactive.Background.Premultiply(), overviewGlassTint*in), render.Mask{Keep: render.RoundRect{Rect: r, Radius: radius}})
	c.drawOverviewRim(pass, o, r, radius, in)
}

func (c *Compositor) drawOverviewRim(pass *frame, o *output.Output, r image.Rectangle, radius, in float32) {
	inner := r.Inset(max(o.Scale().ToDevice(1), 1))
	pass.fillMasked(r, c.overviewInk(overviewRimStrength, in), render.Mask{Keep: render.RoundRect{Rect: r, Radius: radius}, Cut: render.RoundRect{Rect: inner, Radius: max(radius-float32(inner.Min.X-r.Min.X), 0)}})
}

func (c *Compositor) drawOverviewRing(pass *frame, o *output.Output, r image.Rectangle, radius, a float32) {
	gap, width := o.Scale().ToDevice(overviewRingGap), o.Scale().ToDevice(overviewRingWidth)
	inner := deviceRect(o, r).Inset(-gap)
	outer := inner.Inset(-width)
	cut := radius + float32(gap)
	ring := render.Mask{Keep: render.RoundRect{Rect: outer, Radius: cut + float32(width)}, Cut: render.RoundRect{Rect: inner, Radius: cut}}
	c.paintBorder(pass, c.decor.ActiveBorder, outer, inner, ring, a, 0)
}

func (c *Compositor) redrawThumb(o *output.Output) error {
	if c.overview == nil {
		return nil
	}
	v := c.overview.views[o]
	if v == nil || v.redraw == nil {
		return nil
	}
	t := v.redraw
	v.redraw = nil
	if !slices.Contains(v.spaces, t) {
		return nil
	}
	if err := c.refreshThumb(o, v, t); err != nil {
		return fmt.Errorf("the thumbnail of workspace %d: %w", t.id, err)
	}
	return c.schedule(o)
}

func thumbStep(i int, size, screen image.Point) (image.Point, bool) {
	at := image.Pt(screen.X>>i, screen.Y>>i)
	if i == 0 {
		return at, size.X < screen.X && size.Y < screen.Y
	}
	return at, at.X >= size.X && at.Y >= size.Y
}

func (c *Compositor) thumbFilter(o *output.Output, v *overviewView, size image.Point) error {
	screen := o.Backend().State().Mode.Size
	n, same := 0, true
	for {
		at, ok := thumbStep(n, size, screen)
		if !ok {
			break
		}
		same = same && n < len(v.filter) && v.filter[n].Size() == at
		n++
	}
	if same && n == len(v.filter) {
		return nil
	}
	if err := c.dropFilter(v); err != nil {
		return err
	}
	for i := range n {
		at, _ := thumbStep(i, size, screen)
		t, err := c.newSceneTarget(o, at)
		if err != nil {
			return err
		}
		v.filter = append(v.filter, t)
	}
	return nil
}

func (c *Compositor) drawThumbMembers(f *frame, o *output.Output, members []thumbMember, fullscreen bool, shown []*surface.Surface) []*surface.Surface {
	for _, m := range members {
		if m.fullscreen == fullscreen {
			shown = c.drawClippedTree(f, o, m.w.top.Surface(), c.windowOrigin(m.w), shown, c.windowMask(o, m.w), c.opacity(m.w))
		}
	}
	return shown
}

func (c *Compositor) refreshThumb(o *output.Output, v *overviewView, t *overviewThumb) error {
	started := c.clock()
	size := deviceRect(o, t.rect).Size()
	if t.target != nil && t.target.Size() != size {
		err := c.releaseTarget(t.target)
		t.target, t.version = nil, 0
		if err != nil {
			return err
		}
	}
	if t.target == nil {
		target, err := c.newSceneTarget(o, size)
		if err != nil {
			return err
		}
		t.target = target
	}
	f := &c.overview.scratch
	f.ops = f.ops[:0]
	bounds := deviceRect(o, o.Logical())
	for e := range c.layersAt(o, belowWindows) {
		c.drawTree(f, o, e.surface.Surface(), flat(e.bounds().Min), nil)
	}
	shown := c.drawThumbMembers(f, o, t.members, false, t.surfaces[:0])
	for e := range c.layersAt(o, aboveWindows) {
		c.drawTree(f, o, e.surface.Surface(), flat(e.bounds().Min), nil)
	}
	t.surfaces = c.drawThumbMembers(f, o, t.members, true, shown)
	if err := c.thumbFilter(o, v, size); err != nil {
		return err
	}
	draw, chain := t.target, v.filter
	if len(chain) > 0 {
		draw = chain[0]
	}
	f.remap(0, bounds, image.Rectangle{Max: draw.Size()})
	region := c.overview.region[:]
	region[0] = image.Rectangle{Max: draw.Size()}
	pass, err := c.renderer.Begin(draw, region)
	if err != nil {
		return err
	}
	pass.Clear(Background)
	f.replayFrom(pass, image.Point{}, c.sceneOut(o))
	if err := pass.End(); err != nil {
		return err
	}
	for i, from := range chain {
		to := t.target
		if i+1 < len(chain) {
			to = chain[i+1]
		}
		region[0] = image.Rectangle{Max: to.Size()}
		step, err := c.renderer.Begin(to, region)
		if err != nil {
			return err
		}
		step.Clear(render.Color{})
		c.kawase(step, from.Texture(), image.Rectangle{Max: to.Size()}, region, 1, false)
		if err := step.End(); err != nil {
			return err
		}
	}
	c.jellyGen++
	t.version, t.dirty = c.jellyGen, false
	t.next = c.frameTime + thumbShare*(c.clock()-started)
	c.tallied.thumbs++
	return nil
}

func (c *Compositor) drawOverviewSelection(pass *frame, o *output.Output, v *overviewView, p float64, in float32) error {
	if in <= 0 {
		return nil
	}
	switch t := c.overview.selected.(type) {
	case overviewWindow:
		for _, e := range v.order {
			if e.w != t.w {
				continue
			}
			r := e.rect(o, p, c.frameTime)
			c.drawOverviewRing(pass, o, r, deviceLength(o, c.decor.Rounding), in)
			dev := deviceRect(o, r)
			return c.drawOverviewLabel(pass, o, t.w.top.Title(), t.w.top.Icon(), dev.Max.Y-o.Scale().ToDevice(overviewLabelHeight+overviewLabelGap), dev, in)
		}
	case overviewSpace:
		if t.t.o == o {
			return c.drawOverviewHover(pass, o, t.t.rect, t.t.label, in)
		}
	case overviewPlus:
		if t.v == v {
			return c.drawOverviewHover(pass, o, v.plus, "New workspace", in)
		}
	}
	return nil
}

func (c *Compositor) drawOverviewHover(pass *frame, o *output.Output, r image.Rectangle, text string, in float32) error {
	dev := deviceRect(o, r)
	pass.fillMasked(dev, c.overviewInk(overviewHoverInk, in), render.Mask{Keep: render.RoundRect{Rect: dev, Radius: deviceLength(o, overviewRadius)}})
	return c.drawOverviewLabel(pass, o, text, nil, dev.Max.Y+o.Scale().ToDevice(overviewLabelGap), dev, in)
}

func (c *Compositor) overviewLabelWidth(text string, px int) int {
	m := &c.measuredLabel
	if m.text != text || m.px != px {
		*m = measuredLabel{text: text, px: px, w: int(math.Ceil(c.font.Shape(text, float64(px)).Advance()))}
	}
	return m.w
}

func (c *Compositor) drawOverviewLabel(pass *frame, o *output.Output, text string, icon *shell.Icon, y int, under image.Rectangle, in float32) error {
	if text == "" {
		return nil
	}
	s := o.Scale()
	px, pad, h := s.ToDevice(overviewLabelPx), s.ToDevice(c.decor.Tab.TextPadding), s.ToDevice(overviewLabelHeight)
	w := c.overviewLabelWidth(text, px) + 2*pad
	if icon != nil {
		w += h
	}
	screen := o.Backend().State().Mode.Size
	w = min(w, screen.X/2)
	tex := c.label(o, labelKey{text: text, icon: icon, theme: c.icons, size: image.Pt(w, h), px: px, colors: c.decor.Tab.Active}, labelSlot{})
	if tex == nil {
		return nil
	}
	x := under.Min.X + (under.Dx()-w)/2
	x = min(max(x, 0), screen.X-w)
	dst := image.Rect(x, y, x+w, y+h)
	size := tex.Size()
	pass.ops = append(pass.ops, drawOp{tex: tex, src: render.FRect{W: float64(size.X), H: float64(size.Y)}, dst: dst, alpha: in, mask: render.Mask{Keep: render.RoundRect{Rect: dst, Radius: float32(h) / 2}}})
	return nil
}
