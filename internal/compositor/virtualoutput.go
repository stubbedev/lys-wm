package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"strconv"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/virtual"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/output"
)

const virtualOutputPrefix = "VIRT-"

type virtualOutput struct {
	out        *virtual.Output
	scale      output.Scale
	configured bool
}

type VirtualOutput struct {
	Name       string
	Size       image.Point
	RefreshMHz int32
}

func (c *Compositor) CreateOutput(spec ipc.OutputSpec) (ipc.CreatedOutput, error) {
	var scale output.Scale
	if spec.Scale != 0 {
		s, err := output.ParseScale(spec.Scale)
		if err != nil {
			return ipc.CreatedOutput{}, fmt.Errorf("compositor: create an output: %w", err)
		}
		scale = s
	}
	name := spec.Name
	switch {
	case name == "":
		name = c.freeVirtualName()
	case c.outputNamed(name):
		return ipc.CreatedOutput{}, fmt.Errorf("compositor: create output %s: an output of that name exists", name)
	}
	if err := c.addVirtual(VirtualOutput{Name: name, Size: spec.Size, RefreshMHz: spec.RefreshMHz}, scale, false); err != nil {
		return ipc.CreatedOutput{}, err
	}
	return ipc.CreatedOutput{Name: name}, nil
}

func (c *Compositor) addVirtual(v VirtualOutput, scale output.Scale, configured bool) error {
	o, err := virtual.New(c.loop, c.renderer, c, v.Name, backend.Mode{Size: v.Size, RefreshMHz: v.RefreshMHz})
	if err != nil {
		return fmt.Errorf("compositor: create output %s: %w", v.Name, err)
	}
	c.virtualOutputs = append(c.virtualOutputs, virtualOutput{out: o, scale: scale, configured: configured})
	if err := c.OutputAdded(o); err != nil {
		return errors.Join(fmt.Errorf("compositor: create output %s: %w", v.Name, err), c.dropVirtual(v.Name))
	}
	return nil
}

func (c *Compositor) RemoveOutput(name string) error {
	i := slices.IndexFunc(c.virtualOutputs, func(v virtualOutput) bool { return v.out.Name() == name })
	if i < 0 {
		if c.outputNamed(name) {
			return fmt.Errorf("compositor: remove output %s: only virtual outputs can be removed", name)
		}
		return fmt.Errorf("compositor: remove output %s: no output of that name", name)
	}
	if c.virtualOutputs[i].configured {
		return fmt.Errorf("compositor: remove output %s: the config's virtual_outputs keeps it; remove it there", name)
	}
	return c.dropVirtual(name)
}

func (c *Compositor) dropVirtual(name string) error {
	i := slices.IndexFunc(c.virtualOutputs, func(v virtualOutput) bool { return v.out.Name() == name })
	o := c.virtualOutputs[i].out
	c.virtualOutputs = slices.Delete(c.virtualOutputs, i, i+1)
	return errors.Join(c.OutputRemoved(o), o.Close())
}

func (c *Compositor) syncVirtualOutputs() error {
	var errs []error
	for _, v := range slices.Clone(c.virtualOutputs) {
		if v.configured && !slices.Contains(c.virtualConfig, v.config()) {
			errs = append(errs, c.dropVirtual(v.out.Name()))
		}
	}
	for _, want := range c.virtualConfig {
		if slices.ContainsFunc(c.virtualOutputs, func(v virtualOutput) bool { return v.configured && v.out.Name() == want.Name }) {
			continue
		}
		if c.outputNamed(want.Name) {
			errs = append(errs, fmt.Errorf("compositor: virtual output %s from the config: an output of that name exists", want.Name))
			continue
		}
		errs = append(errs, c.addVirtual(want, 0, true))
	}
	return errors.Join(errs...)
}

func (v virtualOutput) config() VirtualOutput {
	mode := v.out.State().Mode
	return VirtualOutput{Name: v.out.Name(), Size: mode.Size, RefreshMHz: mode.RefreshMHz}
}

func (c *Compositor) outputNamed(name string) bool {
	if slices.ContainsFunc(c.outputs, func(o *output.Output) bool { return o.Backend().Name() == name }) {
		return true
	}
	return slices.ContainsFunc(c.disabled, func(b backend.Output) bool { return b.Name() == name })
}

func (c *Compositor) freeVirtualName() string {
	for n := 1; ; n++ {
		if name := virtualOutputPrefix + strconv.Itoa(n); !c.outputNamed(name) {
			return name
		}
	}
}

func (c *Compositor) virtualScale(b backend.Output) output.Scale {
	if i := slices.IndexFunc(c.virtualOutputs, func(v virtualOutput) bool { return v.out == b }); i >= 0 {
		return c.virtualOutputs[i].scale
	}
	return 0
}

func (c *Compositor) demanded(o *output.Output) bool {
	if _, ok := o.Backend().(*virtual.Output); !ok || len(c.captures[o]) > 0 {
		return true
	}
	return slices.ContainsFunc(c.captureSessions, func(s *captureSession) bool {
		to, _, ok := s.target()
		return ok && !s.stopped && to == o
	})
}

func (c *Compositor) drawing(o *output.Output) bool {
	return c.Powered(o) && c.demanded(o)
}

func (c *Compositor) renewVirtualOutputs() error {
	var errs []error
	for _, v := range c.virtualOutputs {
		errs = append(errs, v.out.Renew())
	}
	return errors.Join(errs...)
}

func (c *Compositor) closeVirtualOutputs() error {
	var errs []error
	for _, v := range c.virtualOutputs {
		errs = append(errs, v.out.Close())
	}
	c.virtualOutputs = nil
	return errors.Join(errs...)
}

func (c *Compositor) refuseVirtualHandover() error {
	i := slices.IndexFunc(c.virtualOutputs, func(v virtualOutput) bool { return !v.configured })
	if i < 0 {
		return nil
	}
	return fmt.Errorf("compositor: virtual output %s cannot move to another binary; remove it first", c.virtualOutputs[i].out.Name())
}
