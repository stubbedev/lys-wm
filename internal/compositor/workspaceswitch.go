package compositor

import (
	"image"
	"math"
	"time"

	"github.com/stubbedev/lys-lib/third_party/gomath"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type workspaceSwitch struct {
	from, to wm.WorkspaceID
	forward  bool
	style    anim.Style
	pos      anim.Progress
	incoming view
	outgoing view
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

func (sw *workspaceSwitch) sign() float64 {
	if sw.forward {
		return 1
	}
	return -1
}

func (sw *workspaceSwitch) travelled(now time.Duration) float64 {
	return float64(sw.pos.At(now) * sw.sign())
}

func (sw *workspaceSwitch) view(area image.Rectangle, incoming bool, now time.Duration) view {
	p := sw.travelled(now)
	shift := func(span int) int {
		if incoming {
			return int(math.Round(float64(float64(sw.sign())*float64(span)) * (1 - p)))
		}
		return -int(math.Round(float64(float64(sw.sign())*float64(span)) * p))
	}
	switch sw.style.Kind {
	case anim.Slide:
		return view{offset: image.Pt(shift(area.Dx()), 0), scale: 1, alpha: 1}
	case anim.SlideVert:
		return view{offset: image.Pt(0, shift(area.Dy())), scale: 1, alpha: 1}
	}
	p = min(max(p, 0), 1)
	if sw.style.Kind == anim.Popin {
		if incoming {
			return view{scale: sw.style.Percent/100 + float64((1-sw.style.Percent/100)*p), alpha: p}
		}
		return view{scale: 1, alpha: 1 - p}
	}
	if incoming {
		return view{scale: 1, alpha: p}
	}
	return view{scale: 1, alpha: 1 - p}
}

func (sw *workspaceSwitch) prepare(area image.Rectangle, now time.Duration) {
	sw.incoming, sw.outgoing = sw.view(area, true, now), sw.view(area, false, now)
}

func (sw *workspaceSwitch) viewOf(ws wm.WorkspaceID) (view, bool) {
	switch ws {
	case sw.to:
		return sw.incoming, true
	case sw.from:
		return sw.outgoing, true
	}
	return identity, false
}

func (c *Compositor) prepareSwitch(o *output.Output) {
	c.drawnSwitch = c.switches[o]
	if c.drawnSwitch != nil {
		c.drawnSwitch.prepare(o.Logical(), c.frameTime)
	}
}

func (sw *workspaceSwitch) restOn(ws wm.WorkspaceID) float64 {
	if ws == sw.to {
		return sw.sign()
	}
	return 0
}

func (c *Compositor) switchAnimates(o *output.Output) bool {
	sw, ok := c.switches[o]
	return ok && !sw.pos.Tracking()
}

func (c *Compositor) drawGooSwitch(pass *frame, o *output.Output) {
	sw, ok := c.switches[o]
	if !ok || sw.style.Kind != anim.Goo {
		return
	}
	p := min(max(sw.travelled(c.frameTime), 0), 1)
	strength := gomath.Sin(math.Pi * p)
	if strength <= 0 {
		return
	}
	out, in := c.gooFrom[:0], c.gooTo[:0]
	for _, w := range c.stack {
		switch {
		case !w.laid || w.layer == wm.LayerFullscreen || w.minimized:
		case w.workspace == sw.to && w.tile.Overlaps(o.Logical()):
			in = append(in, w)
		case w.workspace == sw.from && w.output == o:
			out = append(out, w)
		}
	}
	c.gooFrom, c.gooTo = out, in
	if len(in) == 0 {
		return
	}
	for _, w := range out {
		target := nearest(in, w.tile)
		a := deviceRect(o, anim.LerpRect(w.tile, target.tile, p))
		b := deviceRect(o, target.tile)
		if a.Empty() || b.Empty() {
			continue
		}
		color := scaleColor(c.borderTarget(target).Colors()[0].Premultiply(), float32(strength))
		radius := c.outerRadius(o, target)
		smooth := float32(min(a.Dx(), a.Dy(), b.Dx(), b.Dy())) / 2
		pass.fillGoo(a.Union(b), color, render.Goo{A: render.RoundRect{Rect: a, Radius: radius}, B: render.RoundRect{Rect: b, Radius: radius}, Smooth: smooth})
	}
}

func nearest(ws []*window, r image.Rectangle) *window {
	c := r.Min.Add(r.Size().Div(2))
	best, dist := ws[0], math.MaxFloat64
	for _, w := range ws {
		d := w.tile.Min.Add(w.tile.Size().Div(2)).Sub(c)
		if v := gomath.Hypot(float64(d.X), float64(d.Y)); v < dist {
			best, dist = w, v
		}
	}
	return best
}

func (c *Compositor) trackSwitches(now time.Duration) {
	curve := c.animations.Curve(anim.Workspaces)
	for _, o := range c.outputs {
		name := o.Backend().Name()
		active, ok := c.wm.ActiveOn(name)
		prev, had := c.shownWorkspace[name]
		c.shownWorkspace[name] = active
		if !ok || !had || prev == active {
			continue
		}
		c.setPeek(o, false, now)
		sw, switching := c.switches[o]
		if switching && (active == sw.from || active == sw.to) {
			sw.pos.Set(sw.restOn(active), now, curve)
			continue
		}
		if curve == anim.Instant {
			delete(c.switches, o)
			continue
		}
		sw = &workspaceSwitch{from: prev, to: active, forward: active > prev, style: c.animations.Style(anim.Workspaces), pos: anim.NewProgress(0)}
		sw.pos.Set(sw.sign(), now, curve)
		c.switches[o] = sw
	}
}

func (c *Compositor) workspaceView(o *output.Output, w *window) (view, bool) {
	v, drawn := c.switchView(o, w)
	v.offset = v.offset.Add(c.peekOffset(o, w))
	return v, drawn
}

func (c *Compositor) switchView(o *output.Output, w *window) (view, bool) {
	if w.pinned {
		return identity, w.visible
	}
	if w.scratch {
		if v, drawn, sliding := c.scratchView(o, w); sliding {
			return v, drawn
		}
		return identity, w.visible
	}
	sw := c.drawnSwitch
	if sw == nil || !w.laid || (!w.visible && w.output != o) {
		return identity, w.visible
	}
	if v, ok := sw.viewOf(w.workspace); ok {
		return v, true
	}
	return identity, w.visible
}

func (c *Compositor) finishSwitches(o *output.Output, now time.Duration) {
	if sw, ok := c.switches[o]; ok && sw.pos.Done(now) {
		delete(c.switches, o)
	}
}
