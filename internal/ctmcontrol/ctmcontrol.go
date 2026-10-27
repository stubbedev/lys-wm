// Package ctmcontrol implements hyprland_ctm_control_manager_v1: the first
// client to bind it sets the colour transform matrix of outputs, as
// hyprsunset does, and every output it changed returns to the identity when
// it lets go. Managers bound while another one owns the outputs are blocked.
package ctmcontrol

import (
	"errors"
	"fmt"
	"math"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	shc "github.com/stubbedev/lys-wm/internal/protocol/server/hyprlandctmcontrol"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 2

var Interface = shc.HyprlandCtmControlManagerV1Interface

type Outputs interface {
	Owner(res *sw.WlOutput) *output.Output
}

type Manager struct {
	outputs Outputs
	report  func(error)
	owner   *control
}

func New(d *server.Display, outputs Outputs, report func(error)) (*Manager, error) {
	m := &Manager{outputs: outputs, report: report}
	if _, err := d.AddGlobal(Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Removed(o *output.Output) {
	if m.owner == nil {
		return
	}
	delete(m.owner.pending, o)
	delete(m.owner.applied, o)
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	k := &control{m: m}
	res, err := shc.NewHyprlandCtmControlManagerV1(c, id, v, k)
	if err != nil {
		return err
	}
	if m.owner != nil {
		if v < 2 {
			return nil
		}
		return res.SendBlocked()
	}
	k.pending, k.applied = map[*output.Output]backend.Matrix{}, map[*output.Output]bool{}
	m.owner = k
	server.OnDestroy(res, k.release)
	return nil
}

type control struct {
	m       *Manager
	pending map[*output.Output]backend.Matrix
	applied map[*output.Output]bool
}

func (k *control) owns() bool { return k.m.owner == k }

func (k *control) SetCtmForOutput(obj *shc.HyprlandCtmControlManagerV1, wl *sw.WlOutput, mat0, mat1, mat2, mat3, mat4, mat5, mat6, mat7, mat8 wire.Fixed) error {
	var m backend.Matrix
	for i, f := range [9]wire.Fixed{mat0, mat1, mat2, mat3, mat4, mat5, mat6, mat7, mat8} {
		v := f.Float()
		if v < 0 || math.IsNaN(v) {
			return obj.ProtocolError(shc.HyprlandCtmControlManagerV1ErrorInvalidMatrix, "matrix entry %d is %v, below 0", i, v)
		}
		m[i] = v
	}
	if !k.owns() {
		return nil
	}
	if o := k.m.outputs.Owner(wl); o != nil {
		k.pending[o] = m
	}
	return nil
}

func (k *control) Commit(*shc.HyprlandCtmControlManagerV1) error {
	if !k.owns() {
		return nil
	}
	for o := range k.applied {
		if _, ok := k.pending[o]; !ok {
			k.apply(o, backend.Identity)
		}
	}
	for o, m := range k.pending {
		k.apply(o, m)
	}
	clear(k.pending)
	return nil
}

func (k *control) apply(o *output.Output, m backend.Matrix) {
	if m == backend.Identity {
		delete(k.applied, o)
	} else {
		k.applied[o] = true
	}
	ct, ok := o.Backend().(backend.ColorTransform)
	if !ok || !ct.HasColorTransform() {
		k.m.report(fmt.Errorf("ctmcontrol: %s has no colour transform", o.Backend().Name()))
		return
	}
	if err := ct.SetColorTransform(m); err != nil {
		k.m.report(fmt.Errorf("ctmcontrol: %w", err))
	}
}

func (*control) Destroy(*shc.HyprlandCtmControlManagerV1) error { return nil }

func (k *control) release() error {
	if !k.owns() {
		return nil
	}
	k.m.owner = nil
	var errs []error
	for o := range k.applied {
		ct, ok := o.Backend().(backend.ColorTransform)
		if !ok || !ct.HasColorTransform() {
			continue
		}
		errs = append(errs, ct.SetColorTransform(backend.Identity))
	}
	return errors.Join(errs...)
}
