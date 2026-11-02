package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"
	"strconv"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	overviewRadius      = 8
	overviewRing        = 3
	overviewLabelPx     = 12
	overviewLabelHeight = 22
	overviewLabelGap    = 6
	overviewPlusArm     = 2
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
		if err := c.drawTabBars(pass, o); err != nil {
			return shown, err
		}
		fadeOrDrop(pass, from, 1-in)
	}
	if c.switches[o] == nil {
		v.leaving = nil
	}
	shown = c.drawOverviewEntries(pass, o, v, p, shown)
	c.drawClosing(pass, o, windowLevel)
	if err := c.drawOverviewStrip(pass, o, v, p, in); err != nil {
		return shown, err
	}
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
		shown = c.drawPopups(pass, o, c.layerPopups(e), shown, popupBlur{regions: true, whole: c.blursLayer(e.surface), look: &c.decor.Blur.Look})
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
		sc := c.sceneOf(o)
		sc.blurs.shape = append(sc.blurs.shape[:0], bounds)
		c.drawBlur(pass, o, glassDraw{shape: sc.blurs.shape, extent: bounds, alpha: in, look: &overviewGlass, mica: true})
	}
	if d := c.overviewStyle.Dim; d > 0 {
		pass.FillRect(bounds, render.Color{A: uint8(math.Round(float64(d * in * 0xff)))})
	}
}

func (c *Compositor) drawOverviewEntries(pass *frame, o *output.Output, v *overviewView, p float64, shown []*surface.Surface) []*surface.Surface {
	sw := c.switches[o]
	area := o.Logical()
	if sw != nil && len(v.leaving) > 0 {
		out, _ := sw.viewOf(v.left, area, c.frameTime)
		for _, e := range v.leaving {
			shown = c.drawOverviewEntry(pass, o, e, p, out, shown)
		}
	}
	view := identity
	if sw != nil {
		view, _ = sw.viewOf(v.workspace, area, c.frameTime)
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
	at := v.rect(e.rect(o, p, c.frameTime), o.Logical())
	if c.textureOf(w.top.Surface()) == nil {
		r := deviceRect(o, at)
		pass.fillMasked(r, scaleColor(c.decor.Tab.Inactive.Background, float32(alpha)), render.Mask{Keep: render.RoundRect{Rect: r, Radius: deviceLength(o, overviewRadius)}})
		return shown
	}
	shown = c.paintWindow(pass, o, w, at, alpha, shown)
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
			alpha := e.w.fade.At(c.frameTime)
			at := e.rect(from, p, c.frameTime)
			if !at.Overlaps(o.Logical()) {
				continue
			}
			shown = c.paintWindow(pass, o, e.w, at, alpha, shown)
		}
	}
	return shown
}

func (c *Compositor) drawOverviewStrip(pass *frame, o *output.Output, v *overviewView, p float64, in float32) error {
	if in <= 0 {
		return nil
	}
	top := o.Logical().Min.Y
	slide := func(r image.Rectangle) image.Rectangle {
		return anim.LerpRect(r.Sub(image.Pt(0, r.Max.Y-top)), r, p)
	}
	radius := deviceLength(o, overviewRadius)
	for _, t := range v.spaces {
		if t.rect.Empty() {
			continue
		}
		if err := c.refreshThumb(o, t); err != nil {
			return fmt.Errorf("the thumbnail of workspace %d: %w", t.id, err)
		}
		dst := deviceRect(o, slide(t.rect))
		size := t.target.Size()
		pass.ops = append(pass.ops, drawOp{
			tex: t.target.Texture(), src: render.FRect{W: float64(size.X), H: float64(size.Y)}, dst: dst, alpha: in, version: t.version,
			mask: render.Mask{Keep: render.RoundRect{Rect: dst, Radius: radius}},
		})
		if t.id == v.workspace {
			c.drawOverviewRing(pass, o, slide(t.rect), in)
		}
	}
	if v.plus.Empty() {
		return nil
	}
	dst := deviceRect(o, slide(v.plus))
	pass.fillMasked(dst, scaleColor(c.decor.Tab.Inactive.Background, in), render.Mask{Keep: render.RoundRect{Rect: dst, Radius: radius}})
	arm := max(o.Scale().ToDevice(overviewPlusArm), 1)
	span := min(dst.Dx(), dst.Dy()) / 3
	mid := dst.Min.Add(dst.Size().Div(2))
	ink := scaleColor(c.decor.Tab.Inactive.Text, in)
	pass.FillRect(image.Rect(mid.X-span/2, mid.Y-arm/2, mid.X+span/2, mid.Y-arm/2+arm), ink)
	pass.FillRect(image.Rect(mid.X-arm/2, mid.Y-span/2, mid.X-arm/2+arm, mid.Y+span/2), ink)
	return nil
}

func (c *Compositor) drawOverviewRing(pass *frame, o *output.Output, r image.Rectangle, a float32) {
	inner := deviceRect(o, r)
	outer := inner.Inset(-o.Scale().ToDevice(overviewRing))
	radius := deviceLength(o, overviewRadius)
	ring := render.Mask{Keep: render.RoundRect{Rect: outer, Radius: radius + float32(inner.Min.X-outer.Min.X)}, Cut: render.RoundRect{Rect: inner, Radius: radius}}
	c.paintBorder(pass, c.decor.ActiveBorder, outer, inner, ring, a, 0)
}

func (c *Compositor) refreshThumb(o *output.Output, t *overviewThumb) error {
	size := deviceRect(o, t.rect).Size()
	if t.target == nil || t.target.Size() != size {
		if t.target != nil {
			if err := c.releaseTarget(t.target); err != nil {
				return err
			}
		}
		target, err := c.newSceneTarget(o, size)
		if err != nil {
			t.target = nil
			return err
		}
		t.target, t.dirty = target, true
	}
	if !t.dirty {
		return nil
	}
	f := &c.overview.scratch
	f.ops = f.ops[:0]
	bounds := deviceRect(o, o.Logical())
	f.FillRect(bounds, Background)
	for e := range c.layersAt(o, belowWindows) {
		c.drawTree(f, o, e.surface.Surface(), flat(e.bounds().Min), nil)
	}
	shown := t.surfaces[:0]
	for _, m := range t.members {
		shown = c.drawClippedTree(f, o, m.w.top.Surface(), c.windowOrigin(m.w), shown, c.windowMask(o, m.w), c.opacity(m.w))
	}
	t.surfaces = shown
	f.remap(0, bounds, image.Rectangle{Max: size})
	pass, err := c.renderer.Begin(t.target, render.Region{{Max: size}})
	if err != nil {
		return err
	}
	pass.Clear(Background)
	f.replayFrom(pass, image.Point{}, c.sceneOut(o))
	if err := pass.End(); err != nil {
		return err
	}
	c.jellyGen++
	t.version, t.dirty = c.jellyGen, false
	c.thumbRedraws++
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
			c.drawOverviewRing(pass, o, r, in)
			dev := deviceRect(o, r)
			return c.drawOverviewLabel(pass, o, t.w.top.Title(), t.w.top.Icon(), dev.Max.Y-o.Scale().ToDevice(overviewLabelHeight+overviewLabelGap), dev, in)
		}
	case overviewSpace:
		if t.o != o {
			return nil
		}
		for _, sp := range v.spaces {
			if sp.id == t.id {
				return c.drawOverviewHover(pass, o, sp.rect, "Workspace "+strconv.Itoa(int(sp.id)), in)
			}
		}
	case overviewPlus:
		if t.o == o {
			return c.drawOverviewHover(pass, o, v.plus, "New workspace", in)
		}
	}
	return nil
}

func (c *Compositor) drawOverviewHover(pass *frame, o *output.Output, r image.Rectangle, text string, in float32) error {
	dev := deviceRect(o, r)
	pass.fillMasked(dev, scaleColor(render.Color{R: 0x30, G: 0x30, B: 0x30, A: 0x30}, in), render.Mask{Keep: render.RoundRect{Rect: dev, Radius: deviceLength(o, overviewRadius)}})
	return c.drawOverviewLabel(pass, o, text, nil, dev.Max.Y+o.Scale().ToDevice(overviewLabelGap), dev, in)
}

func (c *Compositor) drawOverviewLabel(pass *frame, o *output.Output, text string, icon *shell.Icon, y int, under image.Rectangle, in float32) error {
	if text == "" {
		return nil
	}
	s := o.Scale()
	px, pad, h := s.ToDevice(overviewLabelPx), s.ToDevice(c.decor.Tab.TextPadding), s.ToDevice(overviewLabelHeight)
	w := int(math.Ceil(c.font.Shape(text, float64(px)).Advance())) + 2*pad
	if icon != nil {
		w += h
	}
	screen := o.Backend().State().Mode.Size
	w = min(w, screen.X/2)
	tex, err := c.label(o, labelKey{text: text, icon: icon, theme: c.icons, size: image.Pt(w, h), px: px, colors: c.decor.Tab.Active})
	if err != nil {
		return errors.Join(fmt.Errorf("the label %q", text), err)
	}
	x := under.Min.X + (under.Dx()-w)/2
	x = min(max(x, 0), screen.X-w)
	dst := image.Rect(x, y, x+w, y+h)
	size := tex.Size()
	pass.ops = append(pass.ops, drawOp{tex: tex, src: render.FRect{W: float64(size.X), H: float64(size.Y)}, dst: dst, alpha: in, mask: render.Mask{Keep: render.RoundRect{Rect: dst, Radius: float32(h) / 2}}})
	return nil
}
