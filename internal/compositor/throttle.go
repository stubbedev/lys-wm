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
	if c.hidden.armed {
		return nil
	}
	c.hidden.armed = true
	return c.hidden.timer.Arm(c.hidden.every)
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
	return errs
}

func (c *Compositor) HiddenFrames() uint64 {
	return c.hidden.fired
}
