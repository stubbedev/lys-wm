package compositor

import (
	"errors"
	"image"
	"math"
	"time"

	"github.com/stubbedev/lys-lib/third_party/gomath"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	zoomStopsPerOctave = 1024
	crispCursorStep    = 1.25
)

type outputZoom struct {
	level  anim.Motion
	target float64
	focus  [2]float64
	shown  render.FRect
	cursor frame
}

func zoomStops(factor float64) float64 {
	return gomath.Log2(factor) * zoomStopsPerOctave
}

func (z *outputZoom) factor(now time.Duration) float64 {
	return max(gomath.Exp2(z.level.At(now)/zoomStopsPerOctave), 1)
}

func (z *outputZoom) atRest(now time.Duration) bool {
	return z.target == 1 && z.level.Done(now)
}

func zoomView(bounds image.Point, focus [2]float64, factor float64) render.FRect {
	shrink := 1 - 1/factor
	return render.FRect{X: float64(focus[0] * shrink), Y: float64(focus[1] * shrink), W: float64(bounds.X) / factor, H: float64(bounds.Y) / factor}
}

func (c *Compositor) zoomed(o *output.Output) bool {
	sc, ok := c.scenes[o]
	return ok && sc.zoom != nil
}

func (c *Compositor) zoomTarget(o *output.Output) float64 {
	if sc, ok := c.scenes[o]; ok && sc.zoom != nil {
		return sc.zoom.target
	}
	return 1
}

func (c *Compositor) applyZoom(a wm.ZoomAction) error {
	o := c.outputAt(c.cursor)
	if o == nil {
		return nil
	}
	return c.setZoom(o, a.Next(c.zoomTarget(o)))
}

func (c *Compositor) setZoom(o *output.Output, factor float64) error {
	sc := c.sceneOf(o)
	z := sc.zoom
	switch {
	case z == nil && factor == 1:
		return nil
	case z == nil:
		bounds := o.Backend().State().Mode.Size
		z = &outputZoom{level: anim.NewMotion(0), target: 1, focus: [2]float64{float64(bounds.X) / 2, float64(bounds.Y) / 2}}
		sc.zoom = z
		sc.invalidate()
	case z.target == factor:
		return nil
	}
	z.target = factor
	z.level.Set(zoomStops(factor), c.clock(), c.animations.Curve(anim.Zoom))
	c.stateChanged()
	return c.schedule(o)
}

func (c *Compositor) zoomFollows(o *output.Output, z *outputZoom) {
	if !c.cursor.pixel().In(o.Logical()) {
		return
	}
	local := c.cursor.sub(o.Position())
	scale := o.Scale().Float()
	z.focus = [2]float64{float64(local.X * scale), float64(local.Y * scale)}
}

func (c *Compositor) settleZoom(sc *scene) error {
	if sc.zoom == nil || !sc.zoom.atRest(c.frameTime) {
		return nil
	}
	sc.zoom = nil
	sc.invalidate()
	return sc.dropWorking()
}

func (c *Compositor) zoomMoves(o *output.Output, now time.Duration) bool {
	sc, ok := c.scenes[o]
	return ok && sc.zoom != nil && !sc.zoom.level.Done(now)
}

func (c *Compositor) zoomScene(o *output.Output, sc *scene, bounds image.Rectangle) error {
	if err := c.ensureWorking(o, sc, bounds); err != nil {
		return err
	}
	if sc.damage.Empty() {
		return nil
	}
	p, err := c.renderer.Begin(sc.working, render.Region(sc.damage.Rects()))
	if err != nil {
		return err
	}
	(&frame{ops: sc.cur.ops[:sc.cur.cursorFrom]}).replay(p, c.sceneOut(o))
	return p.End()
}

func (c *Compositor) drawZoom(p render.Pass, o *output.Output, sc *scene, bounds image.Rectangle) error {
	z := sc.zoom
	to := colorOut{plans: &c.plans}
	if hc := c.hdr[o.Backend()]; hc != nil {
		p.SetColor(c.plans.Of(hc.working.Description(), hc.signal))
		to.to = hc.signal
	}
	p.Clear(opaque)
	p.DrawTexture(sc.working.Texture(), sc.zoom.shown, bounds, render.Normal, 1, render.Mask{})
	if err := c.zoomCursor(o, sc, z); err != nil {
		return err
	}
	z.cursor.replay(p, to)
	return nil
}

func (c *Compositor) zoomCursor(o *output.Output, sc *scene, z *outputZoom) error {
	z.cursor.ops = append(z.cursor.ops[:0], sc.cur.ops[sc.cur.cursorFrom:]...)
	if len(z.cursor.ops) == 0 {
		return nil
	}
	factor := z.factor(c.frameTime)
	tip := c.cursorTip(o)
	if _, surface := c.seat.Cursor().(seat.SurfaceCursor); !surface && z.level.Done(c.frameTime) {
		base := o.Scale().ToDevice(c.cursors.Size)
		px := crispCursorPx(base, factor)
		ct, err := c.cursorTextureAt(c.cursorShape(), px)
		if err != nil {
			return err
		}
		k := factor * float64(base) / float64(px)
		size := ct.tex.Size()
		op := &z.cursor.ops[0]
		op.tex = ct.tex
		op.src = render.FRect{W: float64(size.X), H: float64(size.Y)}
		corner := tip.Sub(scalePoint(ct.hotspot, k))
		op.dst = image.Rectangle{Min: corner, Max: corner.Add(scalePoint(ct.size, k))}
		return nil
	}
	for i := range z.cursor.ops {
		d := &z.cursor.ops[i].dst
		*d = image.Rectangle{Min: tip.Add(scalePoint(d.Min.Sub(tip), factor)), Max: tip.Add(scalePoint(d.Max.Sub(tip), factor))}
	}
	return nil
}

func scalePoint(p image.Point, k float64) image.Point {
	return image.Pt(int(math.Round(float64(p.X)*k)), int(math.Round(float64(p.Y)*k)))
}

func crispCursorPx(base int, factor float64) int {
	stops := math.Ceil(gomath.Log(factor) / gomath.Log(crispCursorStep))
	return int(math.Ceil(float64(float64(base) * gomath.Pow(crispCursorStep, stops))))
}

func (c *Compositor) renderZoomed(o *output.Output, sc *scene, bounds image.Rectangle) error {
	if out, ok := o.Backend().(backend.Scanner); ok {
		if err := c.fill(o, nil, out); err != nil {
			return err
		}
	}
	if err := c.zoomScene(o, sc, bounds); err != nil {
		return err
	}
	f, err := o.Backend().BeginFrame()
	if err != nil {
		return err
	}
	whole := render.Region{bounds}
	p, err := f.Renderer.Begin(f.Target, whole)
	if err != nil {
		o.Backend().AbortFrame()
		return err
	}
	if err := errors.Join(c.drawZoom(p, o, sc, bounds), p.End()); err != nil {
		o.Backend().AbortFrame()
		return err
	}
	if err := o.Backend().Present(whole); err != nil {
		return err
	}
	sc.presented()
	return errors.Join(c.lockBlanked(o), c.runCaptures(o))
}

func (c *Compositor) zoomedImage(o *output.Output, sc *scene, bounds image.Rectangle) error {
	if err := c.zoomScene(o, sc, bounds); err != nil {
		return err
	}
	p, err := c.renderer.Begin(sc.image, render.Region{bounds})
	if err != nil {
		return err
	}
	return errors.Join(c.drawZoom(p, o, sc, bounds), p.End())
}

func (c *Compositor) touchPoint(p point) point {
	o := c.outputAt(p)
	if o == nil || !c.zoomed(o) {
		return p
	}
	z := c.scenes[o].zoom
	bounds := o.Backend().State().Mode.Size
	view := zoomView(bounds, z.focus, z.factor(c.clock()))
	scale := o.Scale().Float()
	local := p.sub(o.Position())
	pos := o.Position()
	return point{
		X: float64(pos.X) + (view.X+float64(float64(local.X*scale)/float64(bounds.X)*view.W))/scale,
		Y: float64(pos.Y) + (view.Y+float64(float64(local.Y*scale)/float64(bounds.Y)*view.H))/scale,
	}
}
