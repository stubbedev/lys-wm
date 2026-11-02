package compositor

import (
	"image"
	"math"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const dragPreviewRing = 2

type DragPreview struct {
	Enabled      bool
	Opacity      float32
	Blur         bool
	SnapDistance int
}

type dragPreview struct {
	motion anim.RectMotion
	fade   anim.Tween[float64]
	target image.Rectangle
}

func (c *Compositor) dropTarget(it *interaction) (image.Rectangle, bool, error) {
	if !c.decor.DragPreview.Enabled || it.mode != wm.DragMove {
		return image.Rectangle{}, false, nil
	}
	at := c.cursor.pixel()
	switch it.w.layer {
	case wm.LayerFloating:
		o := c.outputAt(c.cursor)
		if o == nil || at == it.start {
			return image.Rectangle{}, false, nil
		}
		r, ok := c.wm.SnapTarget(it.w.id, c.usable[o].Add(o.Position()), at, c.decor.DragPreview.SnapDistance, c.settings.Gaps, c.sharedEdges(o))
		return r, ok, nil
	case wm.LayerTiled:
		if it.w.tile.Empty() {
			return image.Rectangle{}, false, nil
		}
		r, err := c.tiledDropTarget(it, at)
		return r, err == nil && r != it.w.tile, err
	}
	return image.Rectangle{}, false, nil
}

type tiledDrop struct {
	zones  wm.DropZones
	ready  bool
	stale  bool
	known  bool
	zone   wm.DropZone
	target image.Rectangle
}

func (c *Compositor) tiledDropTarget(it *interaction, at image.Point) (image.Rectangle, error) {
	d := &it.drop
	if d.stale || !d.ready {
		var err error
		if d.zones, err = c.wm.DropZones(it.w.id, d.zones); err != nil {
			return image.Rectangle{}, err
		}
		d.ready, d.stale, d.known = true, false, false
	}
	zone := d.zones.At(at)
	if d.known && zone == d.zone {
		return d.target, nil
	}
	target, err := c.wm.DropTarget(it.w.id, at, c.settings.Autotile, c.spacing())
	if err != nil {
		return image.Rectangle{}, err
	}
	d.zone, d.target, d.known = zone, target, true
	return target, nil
}

func (c *Compositor) sharedEdges(o *output.Output) wm.SharedEdges {
	r := o.Logical()
	var shared wm.SharedEdges
	for _, other := range c.outputs {
		n := other.Logical()
		if other == o {
			continue
		}
		overlapsY, overlapsX := n.Min.Y < r.Max.Y && r.Min.Y < n.Max.Y, n.Min.X < r.Max.X && r.Min.X < n.Max.X
		shared.Left = shared.Left || (n.Max.X == r.Min.X && overlapsY)
		shared.Right = shared.Right || (n.Min.X == r.Max.X && overlapsY)
		shared.Top = shared.Top || (n.Max.Y == r.Min.Y && overlapsX)
		shared.Bottom = shared.Bottom || (n.Min.Y == r.Max.Y && overlapsX)
	}
	return shared
}

func (c *Compositor) trackDrop(it *interaction, now time.Duration) error {
	target, ok, err := c.dropTarget(it)
	if err != nil {
		return err
	}
	if !ok {
		c.hidePreview(now)
		return nil
	}
	p := c.preview
	if p == nil {
		p = &dragPreview{motion: anim.NewRectMotion(it.w.tile), fade: anim.NewTween(0.0, anim.Lerp)}
		c.preview = p
	}
	if p.target != target {
		p.target = target
		p.motion.Set(target, now, c.animations.Curve(anim.Snap))
	}
	if p.fade.Target() != 1 {
		p.fade.Set(1, now, c.animations.Curve(anim.FadeSnap))
	}
	return nil
}

func (c *Compositor) hidePreview(now time.Duration) {
	p := c.preview
	if p == nil || p.target.Empty() {
		return
	}
	p.target = image.Rectangle{}
	p.fade.Set(0, now, c.animations.Curve(anim.FadeSnap))
}

func (c *Compositor) settlePreview(now time.Duration) {
	if p := c.preview; p != nil && p.target.Empty() && p.fade.Done(now) {
		c.preview = nil
	}
}

func (c *Compositor) previewMoves(o *output.Output, now time.Duration) bool {
	p := c.preview
	return p != nil && (!p.motion.Done(now) || !p.fade.Done(now) || p.target.Empty()) && p.motion.At(now).Overlaps(o.Logical())
}

func (c *Compositor) drawDragPreview(pass *frame, o *output.Output) {
	p := c.preview
	if p == nil {
		return
	}
	rect := p.motion.At(c.frameTime)
	strength := float32(min(max(p.fade.At(c.frameTime), 0), 1))
	if strength <= 0 || rect.Empty() || !rect.Overlaps(o.Logical()) {
		return
	}
	d := deviceRect(o, rect)
	radius := min(deviceLength(o, c.decor.Rounding), float32(min(d.Dx(), d.Dy()))/2)
	keep := render.Mask{Keep: render.RoundRect{Rect: d, Radius: radius}}
	cfg := c.decor.DragPreview
	if cfg.Blur {
		under := [1]image.Rectangle{d}
		c.drawBlur(pass, o, glassDraw{shape: under[:], extent: d, mask: keep, alpha: strength, look: &c.decor.Blur.Look})
	}
	border := c.decor.ActiveBorder
	pass.fillMasked(d, scaleColor(border.colors[0].Premultiply(), strength*cfg.Opacity), keep)
	width := int(math.Ceil(float64(deviceLength(o, dragPreviewRing))))
	inner := d.Inset(width)
	if inner.Empty() {
		return
	}
	ring := render.Mask{Keep: keep.Keep, Cut: render.RoundRect{Rect: inner, Radius: max(radius-float32(width), 0)}}
	c.paintBorder(pass, border, d, inner, ring, strength, 0)
}

func (c *Compositor) endDrop(it *interaction) error {
	target, ok, err := c.dropTarget(it)
	if err != nil || !ok || it.w.layer != wm.LayerFloating {
		return err
	}
	return c.update(c.wm.SetFloatingRect(it.w.id, target))
}
