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
	external := func(name string) bool { return !internalPanel(name) }
	return slices.ContainsFunc(c.outputs, func(o *output.Output) bool { return external(o.Backend().Name()) }) ||
		slices.ContainsFunc(c.disabled, func(b backend.Output) bool { return external(b.Name()) })
}
