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
	if lid.Suspend == nil || !c.lidClosed || c.docked() {
		return
	}
	battery, err := lid.OnBattery()
	if err != nil {
		c.report(fmt.Errorf("compositor: the lid policy does not suspend, the power state is unknown: %w", err))
		return
	}
	if !battery {
		return
	}
	if err := lid.Suspend(); err != nil {
		c.report(fmt.Errorf("compositor: the lid policy could not suspend: %w", err))
	}
}

func (c *Compositor) docked() bool {
	external := func(name string) bool { return !internalPanel(name) }
	return slices.ContainsFunc(c.outputs, func(o *output.Output) bool { return external(o.Backend().Name()) }) ||
		slices.ContainsFunc(c.disabled, func(b backend.Output) bool { return external(b.Name()) })
}
