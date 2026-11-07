package compositor

import (
	"errors"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const DefaultHiddenFrame = time.Second / 15

type throttle struct {
	timer   *loop.Timer
	every   time.Duration
	armed   bool
	waiting map[*surface.Surface]struct{}
	fired   uint64
}

func (sc *scene) noteDrawn(f *frame) {
	clear(sc.culled)
	for _, op := range f.ops[:f.cursorFrom] {
		if op.surface != nil {
			sc.culled[op.surface] = true
		}
	}
}

func (sc *scene) noteKept(f *frame) {
	for _, op := range f.ops {
		delete(sc.culled, op.surface)
	}
	for _, p := range f.blurs {
		if p.resolved {
			for _, op := range f.below[p.from:p.to] {
				delete(sc.culled, op.surface)
			}
		}
	}
}

func (c *Compositor) drawnOn(o *output.Output, s *surface.Surface) bool {
	return slices.Contains(c.shown[o], s) && !c.sceneOf(o).culled[s]
}

func (c *Compositor) drawnAnywhere(s *surface.Surface) bool {
	for _, o := range c.outputs {
		if c.Powered(o) && c.drawnOn(o, s) {
			return true
		}
	}
	return false
}

func (c *Compositor) throttleFrames(s *surface.Surface) error {
	if !s.WantsFrame() || c.hidden.timer == nil {
		return nil
	}
	if c.hidden.waiting == nil {
		c.hidden.waiting = map[*surface.Surface]struct{}{}
	}
	c.hidden.waiting[s] = struct{}{}
	return c.armHidden()
}

func (c *Compositor) armHidden() error {
	if c.hidden.armed || c.hidden.timer == nil {
		return nil
	}
	c.hidden.armed = true
	return c.hidden.timer.Arm(c.hidden.every)
}

func (c *Compositor) drawnNow(o *output.Output, shown []*surface.Surface) error {
	if len(c.hidden.waiting) == 0 && !c.hidden.armed {
		return nil
	}
	culled := c.sceneOf(o).culled
	for _, s := range shown {
		if !culled[s] {
			delete(c.hidden.waiting, s)
		}
	}
	if len(c.hidden.waiting) > 0 || !c.hidden.armed || c.fifo.Hidden() {
		return nil
	}
	c.hidden.armed = false
	return c.hidden.timer.Disarm()
}

func (c *Compositor) throttleLeft(o *output.Output, was []*surface.Surface) error {
	var errs error
	for _, s := range was {
		if !slices.Contains(c.shown[o], s) && !c.drawnAnywhere(s) {
			errs = errors.Join(errs, c.throttleFrames(s))
		}
	}
	return errs
}

func (c *Compositor) hiddenFrame() error {
	c.hidden.armed = false
	c.hidden.fired++
	ms := wire.Millis(backend.Now())
	var errs error
	for s := range c.hidden.waiting {
		delete(c.hidden.waiting, s)
		if !c.drawnAnywhere(s) {
			errs = errors.Join(errs, s.FrameDone(ms))
		}
	}
	return errors.Join(errs, c.fifo.Expired())
}

func (c *Compositor) HiddenFrames() uint64 {
	return c.hidden.fired
}

func (c *Compositor) captured(w *window, at time.Duration) error {
	errs := c.capturedTree(w.top.Surface(), at)
	for _, pp := range c.popups(w) {
		errs = errors.Join(errs, c.capturedTree(pp.popup.Surface(), at))
	}
	return errs
}

func (c *Compositor) capturedTree(root *surface.Surface, at time.Duration) error {
	var errs error
	for member := range root.Stack() {
		if member != root {
			errs = errors.Join(errs, c.capturedTree(member, at))
			continue
		}
		if c.drawnAnywhere(member) {
			continue
		}
		delete(c.hidden.waiting, member)
		member.Latch()
		errs = errors.Join(errs, member.FrameDone(wire.Millis(at)), member.Presented(nil, at, 0, false, 0, 0))
	}
	return errs
}
