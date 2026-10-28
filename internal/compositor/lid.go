package compositor

import (
	"fmt"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
)

func (c *Compositor) PowerChanged() error {
	return c.applyLidPolicy()
}

func (c *Compositor) applyLidPolicy() error {
	lid := c.hooks.Lid
	if lid.Suspend == nil || !c.lidClosed || c.docked() {
		return nil
	}
	battery, err := lid.OnBattery()
	if err != nil {
		return fmt.Errorf("compositor: read the power supply for the lid policy: %w", err)
	}
	if !battery {
		return nil
	}
	if err := lid.Suspend(); err != nil {
		c.report(fmt.Errorf("compositor: the lid policy could not suspend: %w", err))
	}
	return nil
}

func (c *Compositor) docked() bool {
	external := func(name string) bool { return !internalPanel(name) }
	return slices.ContainsFunc(c.outputs, func(o *output.Output) bool { return external(o.Backend().Name()) }) ||
		slices.ContainsFunc(c.disabled, func(b backend.Output) bool { return external(b.Name()) })
}
