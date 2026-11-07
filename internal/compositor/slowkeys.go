package compositor

import (
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
)

type slowKeys struct {
	timer   *loop.Timer
	pending backend.Key
	device  string
	held    bool
}

func (c *Compositor) slowKey(device string, e backend.Key) error {
	s := &c.slow
	if !e.Pressed {
		if s.held && s.pending.Code == e.Code {
			s.held = false
			return s.timer.Disarm()
		}
		return c.acceptKey(device, e)
	}
	s.pending, s.device, s.held = e, device, true
	return s.timer.Arm(c.seatConfig.SlowKeys)
}

func (c *Compositor) slowKeyHeld() error {
	s := &c.slow
	if !s.held || c.seatConfig.SlowKeys == 0 {
		s.held = false
		return nil
	}
	s.held = false
	e := s.pending
	e.Time += c.seatConfig.SlowKeys
	return c.acceptKey(s.device, e)
}

func (c *Compositor) setSlowKeys(d time.Duration) error {
	c.seatConfig.SlowKeys = d
	if d > 0 || !c.slow.held {
		return nil
	}
	c.slow.held = false
	return c.slow.timer.Disarm()
}
