package compositor

import (
	"fmt"
	"image"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	swlr "github.com/stubbedev/lys-wm/internal/protocol/server/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

func (c *Compositor) layerWithSurface(s *surface.Surface) *layer {
	for _, e := range c.layers {
		if e.surface.Surface() == s {
			return e
		}
	}
	return nil
}

func anchoredSlide(area, r image.Rectangle, anchor swlr.ZwlrLayerSurfaceV1Anchor) image.Rectangle {
	top, bottom := anchor&swlr.ZwlrLayerSurfaceV1AnchorTop != 0, anchor&swlr.ZwlrLayerSurfaceV1AnchorBottom != 0
	left, right := anchor&swlr.ZwlrLayerSurfaceV1AnchorLeft != 0, anchor&swlr.ZwlrLayerSurfaceV1AnchorRight != 0
	switch {
	case top && !bottom:
		return r.Add(image.Pt(0, area.Min.Y-r.Max.Y))
	case bottom && !top:
		return r.Add(image.Pt(0, area.Max.Y-r.Min.Y))
	case left && !right:
		return r.Add(image.Pt(area.Min.X-r.Max.X, 0))
	case right && !left:
		return r.Add(image.Pt(area.Max.X-r.Min.X, 0))
	}
	return slideOff(area, r)
}

func (c *Compositor) layerStyled(e anim.Event, l *layer) (image.Rectangle, float64) {
	goal := l.bounds()
	switch st := c.animations.Style(e); st.Kind {
	case anim.Slide:
		return anchoredSlide(l.output.Logical(), goal, l.surface.Current().Anchor), 1
	case anim.Popin:
		return c.styled(e, l.output, goal), 1
	}
	return goal, 0
}

func (c *Compositor) layerAppear(l *layer, now time.Duration) {
	l.appeared = true
	goal := l.bounds()
	l.motion = anim.NewTween(goal, anim.LerpRect)
	l.fade = anim.NewTween(1.0, anim.Lerp)
	curve := c.animations.Curve(anim.LayersIn)
	if curve == anim.Instant {
		return
	}
	start, alpha := c.layerStyled(anim.LayersIn, l)
	l.motion.Jump(start)
	l.motion.Set(goal, now, curve)
	l.fade.Jump(alpha)
	l.fade.Set(1, now, curve)
}

func (c *Compositor) layerSnapshot(l *layer) (render.TextureTarget, error) {
	if !l.placed || l.snapshot != nil {
		return nil, nil
	}
	var f frame
	c.drawTree(&f, l.output, l.surface.Surface(), flat(l.bounds().Min), nil)
	if len(f.ops) == 0 {
		return nil, nil
	}
	tt, err := c.renderOffscreen(&f, deviceRect(l.output, l.bounds()))
	if err != nil {
		return nil, fmt.Errorf("compositor: snapshot the %q layer surface: %w", l.surface.Namespace(), err)
	}
	return tt, nil
}

func (c *Compositor) layerClosing(l *layer, now time.Duration) error {
	l.appeared = false
	curve := c.animations.Curve(anim.LayersOut)
	if curve == anim.Instant {
		return nil
	}
	tt := l.snapshot
	if tt == nil {
		var err error
		if tt, err = c.layerSnapshot(l); err != nil || tt == nil {
			return err
		}
	}
	l.snapshot = nil
	end, alpha := c.layerStyled(anim.LayersOut, l)
	cl := &closing{snapshot: tt, output: l.output, level: level{layer: l.surface.Current().Layer}, motion: anim.NewTween(l.motion.At(now), anim.LerpRect), fade: anim.NewTween(1.0, anim.Lerp)}
	cl.motion.Set(end, now, curve)
	cl.fade.Set(alpha, now, curve)
	c.closing = append(c.closing, cl)
	return c.scheduleAll()
}
