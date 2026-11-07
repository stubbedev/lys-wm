package compositor

import (
	"errors"
	"image"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type peek struct {
	pos anim.Progress
}

func (c *Compositor) showDesktop(a wm.ShowDesktopAction) error {
	targets := c.outputs
	if !a.All {
		o := c.outputAt(c.cursor)
		if o == nil {
			return nil
		}
		targets = []*output.Output{o}
	}
	now := c.clock()
	for _, o := range targets {
		c.setPeek(o, a.Switch.From(c.peeking(o)), now)
	}
	return c.peekChanged()
}

func (c *Compositor) peeking(o *output.Output) bool {
	p := c.peeks[o]
	return p != nil && p.pos.Target() == 1
}

func (c *Compositor) setPeek(o *output.Output, on bool, now time.Duration) {
	p := c.peeks[o]
	if p == nil && !on || o == nil {
		return
	}
	target := 0.0
	if on {
		target = 1
	}
	curve := c.animations.Curve(anim.WindowsMove)
	switch {
	case curve == anim.Instant && !on:
		delete(c.peeks, o)
	case curve == anim.Instant:
		c.peeks[o] = &peek{pos: anim.NewProgress(1)}
	case p == nil:
		p = &peek{pos: anim.NewProgress(0)}
		p.pos.Set(target, now, curve)
		c.peeks[o] = p
	default:
		p.pos.Set(target, now, curve)
	}
	c.stateChanged()
}

func (c *Compositor) endPeek(o *output.Output) error {
	if !c.peeking(o) {
		return nil
	}
	c.setPeek(o, false, c.clock())
	return c.peekChanged()
}

func (c *Compositor) peekChanged() error {
	return errors.Join(c.refocus(), c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) finishPeek(o *output.Output, now time.Duration) {
	if p := c.peeks[o]; p != nil && p.pos.Target() == 0 && p.pos.Done(now) {
		delete(c.peeks, o)
	}
}

func (c *Compositor) peekAnimates(o *output.Output, now time.Duration) bool {
	p := c.peeks[o]
	return p != nil && !p.pos.Done(now)
}

func peekShift(area, r image.Rectangle, sliver int) image.Point {
	switch nearestEdge(area, r) {
	case 0:
		return image.Pt(area.Min.X+sliver-r.Max.X, 0)
	case 1:
		return image.Pt(area.Max.X-sliver-r.Min.X, 0)
	case 2:
		return image.Pt(0, area.Min.Y+sliver-r.Max.Y)
	}
	return image.Pt(0, area.Max.Y-sliver-r.Min.Y)
}

func (c *Compositor) sliver(o *output.Output) int {
	return max(o.Logical().Dx()*c.settings.Peek/100, 1)
}

func (c *Compositor) peekOffset(o *output.Output, w *window) image.Point {
	p := c.peeks[o]
	if p == nil || w.output != o || w.scratch {
		return image.Point{}
	}
	full := peekShift(o.Logical(), w.tile, c.sliver(o))
	at := p.pos.At(c.frameTime)
	return image.Pt(int(float64(full.X)*at), int(float64(full.Y)*at))
}

func (c *Compositor) sliverAt(o *output.Output, at image.Point) *window {
	for _, w := range c.stack {
		if w.visible && w.output == o && !w.scratch && at.In(w.tile.Add(peekShift(o.Logical(), w.tile, c.sliver(o))).Intersect(o.Logical())) {
			return w
		}
	}
	return nil
}

func (c *Compositor) peekHidesInput(w *window) bool {
	return c.peeking(w.output) && !w.scratch
}
