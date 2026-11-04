package compositor

import (
	"errors"
	"image"
	"math"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
)

const wobbleGrid = 8

func (c *Compositor) wobbleSpring() anim.Spring {
	if s, ok := c.animations.Curve(anim.WindowsMove).(anim.Spring); ok {
		return s
	}
	return anim.Wobbly
}

func (c *Compositor) wobbleWith(w *window, goal image.Rectangle, now time.Duration) {
	d := goal.Min.Sub(w.tile.Min)
	if d == (image.Point{}) || w.tile.Empty() {
		return
	}
	if w.wobble == nil {
		w.wobble = anim.NewWobble(c.wobbleSpring(), wobbleGrid, wobbleGrid, now)
	}
	w.wobble.Advance(now)
	grab := c.interaction.last.Sub(w.tile.Min)
	u := min(max(float64(grab.X)/float64(w.tile.Dx()), 0), 1)
	v := min(max(float64(grab.Y)/float64(w.tile.Dy()), 0), 1)
	w.wobble.Shift(float64(d.X), float64(d.Y), u, v)
}

func (c *Compositor) drawWobble(pass *frame, o *output.Output, w *window, from int) error {
	if w.wobble == nil {
		return nil
	}
	w.wobble.Advance(c.frameTime)
	if w.wobble.Resting() && (c.interaction == nil || c.interaction.w != w) {
		return nil
	}
	tile := deviceRect(o, w.tile)
	if tile.Empty() {
		return nil
	}
	if err := c.renderJelly(pass, o, w, tile, from); err != nil {
		return err
	}
	cols, rows := w.wobble.Grid()
	scale := o.Scale().Float()
	node := func(i, j int) (float64, float64) {
		dx, dy := w.wobble.Offset(i, j)
		return float64(tile.Min.X) + float64(tile.Dx()*i)/float64(cols) + float64(dx*scale), float64(tile.Min.Y) + float64(tile.Dy()*j)/float64(rows) + float64(dy*scale)
	}
	round := func(v float64) int { return int(math.Round(v)) }
	for j := range rows {
		for i := range cols {
			lx0, ly0 := node(i, j)
			lx1, ly1 := node(i, j+1)
			rx0, ry0 := node(i+1, j)
			rx1, ry1 := node(i+1, j+1)
			dst := image.Rect(round((lx0+lx1)/2), round((ly0+ry0)/2), round((rx0+rx1)/2), round((ly1+ry1)/2))
			if dst.Empty() {
				continue
			}
			src := render.FRect{X: float64(tile.Dx()*i) / float64(cols), Y: float64(tile.Dy()*j) / float64(rows), W: float64(tile.Dx()) / float64(cols), H: float64(tile.Dy()) / float64(rows)}
			pass.ops = append(pass.ops, drawOp{tex: w.jelly.Texture(), src: src, dst: dst, alpha: 1, version: w.jellyVersion})
		}
	}
	return nil
}

func (c *Compositor) renderJelly(pass *frame, o *output.Output, w *window, area image.Rectangle, from int) error {
	if w.jelly == nil || w.jelly.Size() != area.Size() {
		if err := c.dropJelly(w); err != nil {
			return err
		}
		tt, err := c.newSceneTarget(o, area.Size())
		if err != nil {
			return err
		}
		w.jelly = tt
	}
	c.resolveBlurs(o, pass, from)
	ops, out := pass.ops[from:], c.sceneOut(o)
	if w.jellyVersion != 0 && w.jellyArea == area && w.jellyOut == out && slices.Equal(w.jellyOps, ops) && !c.sceneOf(o).contentChanged(ops, area) {
		pass.ops = pass.ops[:from]
		return nil
	}
	f := frame{ops: ops}
	p, err := c.renderer.Begin(w.jelly, render.Region{{Max: area.Size()}})
	if err != nil {
		return err
	}
	p.Clear(render.Color{})
	f.replayFrom(p, area.Min, out)
	if err := p.End(); err != nil {
		return err
	}
	c.jellyGen++
	w.jellyVersion, w.jellyArea, w.jellyOut = c.jellyGen, area, out
	w.jellyOps = append(w.jellyOps[:0], ops...)
	pass.ops = pass.ops[:from]
	return nil
}

func (c *Compositor) JellyRedraws() uint64 {
	return c.jellyGen
}

func (c *Compositor) dropJelly(w *window) error {
	if w.jelly == nil {
		return nil
	}
	var errs []error
	for o, sc := range c.scenes {
		if sc.draws(w.jelly.Texture()) {
			sc.stale = true
			if c.outputLive(o) {
				errs = append(errs, c.schedule(o))
			}
		}
	}
	errs = append(errs, w.jelly.Destroy())
	w.jelly, w.jellyOps, w.jellyVersion = nil, nil, 0
	return errors.Join(errs...)
}

func (c *Compositor) settleWobbles() error {
	var errs []error
	for _, w := range c.windows {
		dragged := c.interaction != nil && c.interaction.w == w
		if w.wobble != nil && !dragged && w.wobble.Resting() {
			w.wobble = nil
		}
		if w.jelly != nil && w.wobble == nil && w.genie == nil && w.motion.Done(c.frameTime) {
			errs = append(errs, c.dropJelly(w))
		}
	}
	return errors.Join(errs...)
}
