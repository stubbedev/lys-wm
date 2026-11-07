package compositor

import (
	"fmt"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type HDRPolicy uint8

const (
	HDROff HDRPolicy = iota
	HDROn
	HDRFullscreen
)

const DefaultHDRHold = time.Second

type hdrSwitch struct {
	timer   *loop.Timer
	want    bool
	on      bool
	refused *surface.Surface
	shown   *surface.Surface
}

func (c *Compositor) signalsHDR(b backend.Output, rule OutputRule) bool {
	switch rule.HDR {
	case HDROn:
		return true
	case HDRFullscreen:
		sw := c.hdrSwitches[b]
		return sw != nil && sw.on
	}
	return false
}

func (c *Compositor) showHDR(o *output.Output, s *surface.Surface, alone bool) {
	b := o.Backend()
	if c.outputRule(b).HDR != HDRFullscreen {
		return
	}
	sw := c.hdrSwitches[b]
	if sw == nil {
		timer, err := c.loop.AddTimer(func() error { return c.switchHDR(b) })
		if err != nil {
			c.report(fmt.Errorf("compositor: follow fullscreen HDR on %s: %w", b.Name(), err))
			return
		}
		sw = &hdrSwitch{timer: timer}
		c.hdrSwitches[b] = sw
	}
	want := alone && c.surfaceSpace(s).HDRContent()
	if want && sw.refused == s {
		want = false
	}
	sw.shown = s
	if want == sw.want {
		return
	}
	sw.want = want
	hold := time.Duration(0)
	if !want {
		hold = c.hdrHold
	}
	if want == sw.on {
		hold = 0
	}
	if err := sw.timer.Arm(hold); err != nil {
		c.report(fmt.Errorf("compositor: follow fullscreen HDR on %s: %w", b.Name(), err))
	}
}

func (c *Compositor) switchHDR(b backend.Output) error {
	sw := c.hdrSwitches[b]
	if sw == nil || sw.want == sw.on {
		return nil
	}
	sw.on = sw.want
	rule := c.outputRule(b)
	if rule.HDR != HDRFullscreen {
		return c.dropHDRSwitch(b)
	}
	c.stateChanged()
	if _, _, err := c.commitOutput(b, rule); err != nil {
		return err
	}
	if sw.on && !b.State().HDR.Active {
		sw.on, sw.want, sw.refused = false, false, sw.shown
	}
	return nil
}

func (c *Compositor) dropHDRSwitch(b backend.Output) error {
	sw := c.hdrSwitches[b]
	if sw == nil {
		return nil
	}
	delete(c.hdrSwitches, b)
	return sw.timer.Remove()
}
