package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type closing struct {
	snapshot render.TextureTarget
	output   *output.Output
	level    level
	motion   anim.Tween[image.Rectangle]
	fade     anim.Tween[float64]
}

type level struct {
	layer  shell.Layer
	window bool
}

var windowLevel = level{window: true}

func (cl *closing) done(now time.Duration) bool {
	return cl.motion.Done(now) && cl.fade.Done(now)
}

func (c *Compositor) styled(e anim.Event, o *output.Output, goal image.Rectangle) image.Rectangle {
	switch st := c.animations.Style(e); st.Kind {
	case anim.Popin:
		k := st.Percent / 100
		size := image.Pt(int(math.Round(float64(goal.Dx())*k)), int(math.Round(float64(goal.Dy())*k)))
		corner := goal.Min.Add(goal.Size().Sub(size).Div(2))
		return image.Rectangle{Min: corner, Max: corner.Add(size)}
	case anim.Slide:
		return slideOff(o.Logical(), goal)
	}
	return goal
}

func slideOff(area, r image.Rectangle) image.Rectangle {
	c := r.Min.Add(r.Size().Div(2))
	gaps := [4]int{c.X - area.Min.X, area.Max.X - c.X, c.Y - area.Min.Y, area.Max.Y - c.Y}
	moves := [4]image.Point{{area.Min.X - r.Max.X, 0}, {area.Max.X - r.Min.X, 0}, {0, area.Min.Y - r.Max.Y}, {0, area.Max.Y - r.Min.Y}}
	i := 0
	for j, g := range gaps {
		if g < gaps[i] {
			i = j
		}
	}
	return r.Add(moves[i])
}

func (c *Compositor) appear(w *window, goal image.Rectangle, now time.Duration) {
	if o := c.outputAtRect(goal); o != nil {
		w.motion.Jump(c.styled(anim.WindowsIn, o, goal))
		w.motion.Set(goal, now, c.animations.Curve(anim.WindowsIn))
	}
	if fade := c.animations.Curve(anim.FadeIn); fade != anim.Instant {
		w.fade.Jump(0)
		w.fade.Set(1, now, fade)
	}
}

func (c *Compositor) closeAnimated() bool {
	return c.animations.Curve(anim.WindowsOut) != anim.Instant || c.animations.Curve(anim.FadeOut) != anim.Instant
}

func (c *Compositor) snapshot(w *window) (render.TextureTarget, error) {
	o := w.output
	if o == nil || !w.visible || w.snapshot != nil {
		return nil, nil
	}
	var f frame
	c.drawClippedTree(&f, o, w.top.Surface(), c.windowOrigin(w), nil, c.windowMask(o, w), c.opacity(w))
	if !slices.ContainsFunc(f.ops, func(op drawOp) bool { return op.surface != nil }) {
		return nil, nil
	}
	c.drawBorder(&f, o, w)
	tt, err := c.renderOffscreen(&f, deviceRect(o, w.tile))
	if err != nil {
		return nil, fmt.Errorf("compositor: snapshot window %d: %w", w.id, err)
	}
	return tt, nil
}

func (c *Compositor) renderOffscreen(f *frame, area image.Rectangle) (render.TextureTarget, error) {
	if area.Empty() {
		return nil, nil
	}
	tt, err := c.renderer.NewTextureTarget(area.Size())
	if err != nil {
		return nil, err
	}
	p, err := c.renderer.Begin(tt, render.Region{{Max: area.Size()}})
	if err != nil {
		return nil, errors.Join(err, tt.Destroy())
	}
	p.Clear(render.Color{})
	f.replayFrom(p, area.Min)
	if err := p.End(); err != nil {
		return nil, errors.Join(err, tt.Destroy())
	}
	return tt, nil
}

func (c *Compositor) keepSnapshot(s *surface.Surface) error {
	if e := c.layerWithSurface(s); e != nil && c.animations.Curve(anim.LayersOut) != anim.Instant {
		tt, err := c.layerSnapshot(e)
		if err != nil || tt == nil {
			return err
		}
		e.snapshot = tt
		return nil
	}
	w := c.windowWithSurface(s)
	if w == nil || !c.closeAnimated() {
		return nil
	}
	tt, err := c.snapshot(w)
	if err != nil || tt == nil {
		return err
	}
	w.snapshot = tt
	return nil
}

func (c *Compositor) startClosing(w *window, now time.Duration) error {
	if !c.closeAnimated() {
		return nil
	}
	tt := w.snapshot
	if tt == nil {
		var err error
		if tt, err = c.snapshot(w); err != nil || tt == nil {
			return err
		}
	}
	w.snapshot = nil
	cl := &closing{snapshot: tt, output: w.output, level: windowLevel, motion: anim.NewTween(w.motion.At(now), anim.LerpRect), fade: anim.NewTween(1.0, anim.Lerp)}
	cl.motion.Set(c.styled(anim.WindowsOut, w.output, w.tile), now, c.animations.Curve(anim.WindowsOut))
	cl.fade.Set(0, now, c.animations.Curve(anim.FadeOut))
	c.closing = append(c.closing, cl)
	return c.scheduleAll()
}

func (c *Compositor) drawClosing(pass *frame, o *output.Output, at level) {
	for _, cl := range c.closing {
		if cl.output != o || cl.level != at || cl.done(c.frameTime) {
			continue
		}
		size := cl.snapshot.Size()
		a := float32(min(max(cl.fade.At(c.frameTime), 0), 1))
		pass.DrawTexture(cl.snapshot.Texture(), render.FRect{W: float64(size.X), H: float64(size.Y)}, deviceRect(o, cl.motion.At(c.frameTime)), render.Normal, a)
	}
}

func (c *Compositor) dropClosing(cl *closing) error {
	var errs []error
	for o, sc := range c.scenes {
		if sc.draws(cl.snapshot.Texture()) {
			sc.stale = true
			if c.outputLive(o) {
				errs = append(errs, c.schedule(o))
			}
		}
	}
	return errors.Join(append(errs, cl.snapshot.Destroy())...)
}

func (c *Compositor) finishClosing(o *output.Output, now time.Duration) error {
	var errs []error
	c.closing = slices.DeleteFunc(c.closing, func(cl *closing) bool {
		if cl.output != o || !cl.done(now) {
			return false
		}
		errs = append(errs, c.dropClosing(cl))
		return true
	})
	return errors.Join(errs...)
}

func (c *Compositor) dropClosingOn(o *output.Output) error {
	var errs []error
	c.closing = slices.DeleteFunc(c.closing, func(cl *closing) bool {
		if cl.output != o {
			return false
		}
		errs = append(errs, cl.snapshot.Destroy())
		return true
	})
	return errors.Join(errs...)
}

func (c *Compositor) dropAllClosing() error {
	var errs []error
	for _, cl := range c.closing {
		errs = append(errs, cl.snapshot.Destroy())
	}
	c.closing = nil
	return errors.Join(errs...)
}

const morphLead = 60 * time.Millisecond

func (c *Compositor) drawMorph(pass *frame, o *output.Output, w *window, v view) {
	if c.animations.Style(anim.WindowsMove).Kind != anim.Morph || w.motion.Done(c.frameTime) {
		return
	}
	area := o.Logical()
	a := deviceRect(o, v.rect(w.motion.At(c.frameTime), area))
	b := deviceRect(o, v.rect(w.motion.At(c.frameTime+morphLead), area))
	if a == b || a.Empty() || b.Empty() {
		return
	}
	radius := c.outerRadius(o, w)
	color := c.borderTarget(w).colors[0].Premultiply()
	smooth := float32(min(a.Dx(), a.Dy(), b.Dx(), b.Dy())) / 2
	pass.fillGoo(a.Union(b), color, render.Goo{A: render.RoundRect{Rect: a, Radius: radius}, B: render.RoundRect{Rect: b, Radius: radius}, Smooth: smooth})
}
