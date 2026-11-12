package compositor

import (
	"fmt"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
)

func (c *Compositor) PowerChanged() {
	c.applyLidPolicy()
}

func (c *Compositor) applyLidPolicy() {
	lid := c.hooks.Lid
	if lid.Suspend == nil || !c.lidWantsSleep() {
		return
	}
	if c.lidBusy {
		c.lidAgain = true
		return
	}
	c.lidBusy = true
	var battery bool
	c.offLoop(func() (err error) {
		battery, err = lid.OnBattery()
		return err
	}, func(err error) error {
		switch {
		case err != nil:
			c.report(fmt.Errorf("compositor: the lid policy does not suspend, the power state is unknown: %w", err))
		case battery && c.lidWantsSleep():
			c.offLoop(lid.Suspend, c.lidSuspended)
			return nil
		}
		c.lidSettled()
		return nil
	})
}

func (c *Compositor) LidPending() bool {
	return c.lidBusy
}

func (c *Compositor) lidWantsSleep() bool {
	return c.lidClosed && !c.docked()
}

func (c *Compositor) clamshell() bool {
	return c.lidClosed && c.docked()
}

func (c *Compositor) lidSwitched(closed bool) error {
	if closed == c.lidClosed {
		return nil
	}
	c.lidClosed = closed
	err := c.applyOutputRules()
	c.applyLidPolicy()
	return err
}

func (c *Compositor) dockChanged(b backend.Output) error {
	if !c.lidClosed || internalPanel(b.Name()) {
		return nil
	}
	return c.applyOutputRules()
}

func (c *Compositor) lidSuspended(err error) error {
	if err != nil {
		c.report(fmt.Errorf("compositor: the lid policy could not suspend: %w", err))
	}
	c.lidSettled()
	return nil
}

func (c *Compositor) lidSettled() {
	c.lidBusy = false
	if c.lidAgain {
		c.lidAgain = false
		c.applyLidPolicy()
	}
}

func (c *Compositor) docked() bool {
	shows := func(b backend.Output) bool { return !internalPanel(b.Name()) && !c.configuredRule(b).Disabled }
	return slices.ContainsFunc(c.outputs, func(o *output.Output) bool { return shows(o.Backend()) }) ||
		slices.ContainsFunc(c.disabled, shows)
}
