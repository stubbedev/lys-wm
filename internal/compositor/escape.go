package compositor

import (
	"slices"

	sle "github.com/stubbedev/lys-wm/internal/protocol/server/lysescape"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const escapeVersion = 1

type escapeArm struct {
	res    *sle.LysEscapeArmV1
	client *server.Client
}

func (*escapeArm) Destroy(*sle.LysEscapeArmV1) error { return nil }

func (c *Compositor) addEscape(d *server.Display) error {
	_, err := d.AddGlobal(sle.LysEscapeManagerV1Interface, escapeVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := sle.NewLysEscapeManagerV1(cl, id, version, escapeManager{c, cl})
		return err
	})
	return err
}

type escapeManager struct {
	c  *Compositor
	cl *server.Client
}

func (escapeManager) Destroy(*sle.LysEscapeManagerV1) error { return nil }

func (m escapeManager) Arm(_ *sle.LysEscapeManagerV1, res *sle.LysEscapeArmV1) (sle.LysEscapeArmV1Handler, error) {
	a := &escapeArm{res: res, client: m.cl}
	m.c.escapeArms = append(m.c.escapeArms, a)
	server.OnDestroy(res, func() error {
		m.c.dropEscapeArm(a)
		return nil
	})
	return a, nil
}

func (c *Compositor) dropEscapeArm(a *escapeArm) {
	c.escapeArms = slices.DeleteFunc(c.escapeArms, func(o *escapeArm) bool { return o == a })
}

func (c *Compositor) escapeTaker() *escapeArm {
	if len(c.escapeArms) == 0 || c.keyboardClaimed() {
		return nil
	}
	a := c.escapeArms[len(c.escapeArms)-1]
	if f := c.seat.Focus(); f != nil && f.Client() == a.client {
		return nil
	}
	return a
}

func (c *Compositor) keyboardClaimed() bool {
	return c.shell.Locked() || len(c.shell.Grab()) > 0 || c.exclusiveLayer() != nil ||
		c.seat.KeyboardGrabbed() || c.drag != nil || c.inhibiting != nil
}

func (c *Compositor) fireEscape(a *escapeArm) error {
	c.dropEscapeArm(a)
	return a.res.SendPressed()
}

func (c *Compositor) takeEscape(code uint32, pressed bool) (bool, error) {
	if !pressed {
		return false, nil
	}
	if slices.Contains(c.held, code) {
		return true, nil
	}
	a := c.escapeTaker()
	if a == nil || !c.seat.BareEscape(code) {
		return false, nil
	}
	c.held = append(c.held, code)
	return true, c.fireEscape(a)
}

func (v *virtualKeyboard) takeEscape(code uint32, pressed bool) (bool, error) {
	i := slices.Index(v.taken, code)
	if !pressed {
		if i < 0 {
			return false, nil
		}
		v.taken = slices.Delete(v.taken, i, i+1)
		return true, nil
	}
	if i >= 0 {
		return true, nil
	}
	if v.c.overviewHolds() && v.c.seat.Focus() == nil && v.c.seat.VirtualBareEscape(v.kb, code) {
		v.taken = append(v.taken, code)
		return true, v.c.closeOverview()
	}
	a := v.c.escapeTaker()
	if a == nil || !v.c.seat.VirtualBareEscape(v.kb, code) {
		return false, nil
	}
	v.taken = append(v.taken, code)
	return true, v.c.fireEscape(a)
}
