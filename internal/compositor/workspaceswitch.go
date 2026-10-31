package compositor

import (
	"image"
	"math"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type workspaceSwitch struct {
	from, to wm.WorkspaceID
	forward  bool
	style    anim.Style
	progress anim.Tween[float64]
}

type view struct {
	offset image.Point
	scale  float64
	alpha  float64
}

var identity = view{scale: 1, alpha: 1}

func (v view) rect(r, area image.Rectangle) image.Rectangle {
	if v.scale != 1 {
		c := area.Min.Add(area.Size().Div(2))
		at := func(p image.Point) image.Point {
			return image.Pt(c.X+int(math.Round(float64(p.X-c.X)*v.scale)), c.Y+int(math.Round(float64(p.Y-c.Y)*v.scale)))
		}
		r = image.Rectangle{Min: at(r.Min), Max: at(r.Max)}
	}
	return r.Add(v.offset)
}

func (sw *workspaceSwitch) view(area image.Rectangle, incoming bool, now time.Duration) view {
	p := min(max(sw.progress.At(now), 0), 1)
	sign := 1
	if !sw.forward {
		sign = -1
	}
	shift := func(span int) int {
		if incoming {
			return int(math.Round(float64(sign*span) * (1 - p)))
		}
		return -int(math.Round(float64(sign*span) * p))
	}
	switch sw.style.Kind {
	case anim.Slide:
		return view{offset: image.Pt(shift(area.Dx()), 0), scale: 1, alpha: 1}
	case anim.SlideVert:
		return view{offset: image.Pt(0, shift(area.Dy())), scale: 1, alpha: 1}
	case anim.Popin:
		if incoming {
			return view{scale: sw.style.Percent/100 + (1-sw.style.Percent/100)*p, alpha: p}
		}
		return view{scale: 1, alpha: 1 - p}
	}
	if incoming {
		return view{scale: 1, alpha: p}
	}
	return view{scale: 1, alpha: 1 - p}
}

func (c *Compositor) trackSwitches(now time.Duration) {
	curve := c.animations.Curve(anim.Workspaces)
	for _, o := range c.outputs {
		name := o.Backend().Name()
		active, ok := c.wm.ActiveOn(name)
		prev, had := c.shownWorkspace[name]
		c.shownWorkspace[name] = active
		if !ok || !had || prev == active || curve == anim.Instant {
			continue
		}
		sw := &workspaceSwitch{from: prev, to: active, forward: active > prev, style: c.animations.Style(anim.Workspaces), progress: anim.NewTween(0.0, anim.Lerp)}
		sw.progress.Set(1, now, curve)
		c.switches[o] = sw
	}
}

func (c *Compositor) workspaceView(o *output.Output, w *window) (view, bool) {
	sw, ok := c.switches[o]
	if !ok || w.layer == wm.LayerFullscreen && !w.visible {
		return identity, w.visible
	}
	ws, ok := c.wm.WorkspaceOf(w.id)
	switch {
	case !ok:
		return identity, w.visible
	case w.visible && ws == sw.to:
		return sw.view(o.Logical(), true, c.frameTime), true
	case !w.visible && ws == sw.from && w.output == o:
		return sw.view(o.Logical(), false, c.frameTime), true
	}
	return identity, w.visible
}

func (c *Compositor) finishSwitches(o *output.Output, now time.Duration) {
	if sw, ok := c.switches[o]; ok && sw.progress.Done(now) {
		delete(c.switches, o)
	}
}
