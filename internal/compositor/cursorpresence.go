package compositor

import (
	"errors"
	"math"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

type CursorBehavior struct {
	HideWhileTyping bool
	HideAfter       time.Duration
	ShakeToFind     bool
}

const (
	shakeTurns   = 4
	shakeWindow  = 800 * time.Millisecond
	shakeSegment = 24.0
	shakeSpan    = 600.0
	shakeGrowth  = 3.0
)

type shakeDetector struct {
	run   float64
	turns [shakeTurns]time.Duration
	n     int
}

func (s *shakeDetector) reset() {
	*s = shakeDetector{}
}

func (s *shakeDetector) motion(at time.Duration, dx float64) bool {
	if dx == 0 {
		return false
	}
	if s.run != 0 && (dx > 0) != (s.run > 0) {
		length := math.Abs(s.run)
		s.run = 0
		if length < shakeSegment || length > shakeSpan {
			s.n = 0
		} else {
			s.turns[s.n%shakeTurns] = at
			s.n++
		}
	}
	s.run += dx
	if math.Abs(s.run) > shakeSpan {
		s.n = 0
	}
	if s.n < shakeTurns || at-s.turns[(s.n-shakeTurns)%shakeTurns] > shakeWindow {
		return false
	}
	s.reset()
	return true
}

type cursorPresence struct {
	behavior     CursorBehavior
	typing, idle bool
	pushed       bool
	last         time.Duration
	timer        *loop.Timer
	armed        bool
	checks       uint64
	shake        shakeDetector
	grow         anim.Motion
	growing      bool
}

func (p *cursorPresence) hidden() bool {
	return p.typing || p.idle
}

func (c *Compositor) adoptCursorBehavior(b CursorBehavior) error {
	p := &c.presence
	wasHidden := p.hidden()
	p.behavior, p.last = b, c.clock()
	p.shake.reset()
	if !b.HideWhileTyping {
		p.typing = false
	}
	if b.HideAfter == 0 {
		p.idle = false
	}
	if err := p.timer.Disarm(); err != nil {
		return err
	}
	p.armed = false
	if wasHidden && !p.hidden() {
		return errors.Join(c.cursorMoved(), c.armCursorIdle())
	}
	return c.armCursorIdle()
}

func (c *Compositor) holdingPointer() bool {
	return c.interaction != nil || c.drag != nil || c.overviewDragging() || c.seat.PointerGrabbed() || (c.constraint != nil && c.constraint.locks())
}

func (c *Compositor) armCursorIdle() error {
	p := &c.presence
	if p.behavior.HideAfter == 0 || p.armed || p.idle {
		return nil
	}
	p.armed = true
	return p.timer.Arm(p.behavior.HideAfter - (c.clock() - p.last))
}

func (c *Compositor) cursorIdleExpired() error {
	p := &c.presence
	p.armed = false
	p.checks++
	if p.behavior.HideAfter == 0 {
		return nil
	}
	now := c.clock()
	if c.holdingPointer() {
		p.last = now
	}
	if now-p.last < p.behavior.HideAfter {
		return c.armCursorIdle()
	}
	p.idle = true
	return c.cursorMoved()
}

func (c *Compositor) CursorHidden() bool {
	return c.presence.hidden()
}

func (c *Compositor) CursorIdleChecks() uint64 {
	return c.presence.checks
}

func (c *Compositor) pointerActive() error {
	p := &c.presence
	p.last = c.clock()
	if !p.hidden() {
		return c.armCursorIdle()
	}
	p.typing, p.idle = false, false
	return c.armCursorIdle()
}

func (c *Compositor) typed(code uint32) error {
	p := &c.presence
	if !p.behavior.HideWhileTyping || p.typing || c.seat.Focus() == nil || c.holdingPointer() || xkb.IsModifier(c.seat.Keysym(code)) {
		return nil
	}
	p.typing = true
	return c.cursorMoved()
}

func (c *Compositor) shaken(at time.Duration, dx float64) {
	p := &c.presence
	if !p.behavior.ShakeToFind || !p.shake.motion(at, dx) || p.growing {
		return
	}
	p.growing = true
	p.grow.Set(shakeGrowth, c.clock(), anim.Smooth)
}

func (c *Compositor) cursorGrowth(now time.Duration) float64 {
	return c.presence.grow.At(now)
}

func (c *Compositor) cursorGrows(o *output.Output, now time.Duration) bool {
	p := &c.presence
	return (p.growing || !p.grow.Done(now)) && c.cursor.pixel().In(o.Logical())
}

func (c *Compositor) stepCursorGrowth(o *output.Output, now time.Duration) error {
	p := &c.presence
	if !c.cursorGrows(o, now) {
		return nil
	}
	if p.growing && p.grow.Done(now) {
		p.growing = false
		p.grow.Set(1, now, anim.Smooth)
	}
	return c.cursorMoved()
}

func (c *Compositor) CursorGrowth() float64 {
	return c.cursorGrowth(c.clock())
}
