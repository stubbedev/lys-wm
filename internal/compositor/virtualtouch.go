package compositor

import (
	"errors"

	"github.com/stubbedev/lys-wm/internal/backend"
	slvt "github.com/stubbedev/lys-wm/internal/protocol/server/lysvirtualtouch"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const virtualTouchVersion = 1

// virtualTouchIDs is how many touch points a virtual touch device may
// number; each device's ids map into a block of their own above the
// ids a touchscreen uses.
const virtualTouchIDs = 1 << 16

// virtualTouchBase is the first id of the blocks virtual touch devices
// map their points into.
const virtualTouchBase = 1 << 30

func (c *Compositor) addVirtualTouch(d *server.Display) error {
	_, err := d.AddGlobal(slvt.LysVirtualTouchManagerV1Interface, virtualTouchVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := slvt.NewLysVirtualTouchManagerV1(cl, id, version, virtualTouches{c})
		return err
	})
	return err
}

type virtualTouches struct{ c *Compositor }

func (virtualTouches) Destroy(*slvt.LysVirtualTouchManagerV1) error {
	return nil
}

func (m virtualTouches) CreateVirtualTouch(_ *slvt.LysVirtualTouchManagerV1, _ *sw.WlSeat, res *slvt.LysVirtualTouchV1) (slvt.LysVirtualTouchV1Handler, error) {
	c := m.c
	base := int32(virtualTouchBase + c.virtualTouchBlocks%(virtualTouchBase/virtualTouchIDs)*virtualTouchIDs)
	c.virtualTouchBlocks++
	v := &virtualTouch{c: c, base: base, down: map[uint32]int32{}}
	if err := c.seat.AddDevice(sw.WlSeatCapabilityTouch); err != nil {
		return nil, err
	}
	server.OnDestroy(res, v.gone)
	return v, nil
}

type virtualTouch struct {
	c    *Compositor
	base int32
	down map[uint32]int32
}

func (v *virtualTouch) input(e backend.Event) error {
	return v.c.Input(virtualTouchDevice{}, e)
}

func (v *virtualTouch) position(obj *slvt.LysVirtualTouchV1, x, y, xExtent, yExtent uint32) (float64, float64, error) {
	if xExtent == 0 || yExtent == 0 || x > xExtent || y > yExtent {
		return 0, 0, obj.ProtocolError(slvt.LysVirtualTouchV1ErrorInvalidExtent, "position %d,%d in extent %dx%d", x, y, xExtent, yExtent)
	}
	return float64(x) / float64(xExtent), float64(y) / float64(yExtent), nil
}

func (v *virtualTouch) Down(obj *slvt.LysVirtualTouchV1, ms, id, x, y, xExtent, yExtent uint32) error {
	if _, taken := v.down[id]; taken || id >= virtualTouchIDs {
		return obj.ProtocolError(slvt.LysVirtualTouchV1ErrorInvalidID, "touch point %d is down already or not below %d", id, virtualTouchIDs)
	}
	fx, fy, err := v.position(obj, x, y, xExtent, yExtent)
	if err != nil {
		return err
	}
	point := v.base + int32(id)
	v.down[id] = point
	return v.input(backend.TouchDown{Time: millis(ms), ID: point, X: fx, Y: fy})
}

func (v *virtualTouch) Motion(obj *slvt.LysVirtualTouchV1, ms, id, x, y, xExtent, yExtent uint32) error {
	fx, fy, err := v.position(obj, x, y, xExtent, yExtent)
	if err != nil {
		return err
	}
	point, down := v.down[id]
	if !down {
		return nil
	}
	return v.input(backend.TouchMotion{Time: millis(ms), ID: point, X: fx, Y: fy})
}

func (v *virtualTouch) Up(_ *slvt.LysVirtualTouchV1, ms, id uint32) error {
	point, down := v.down[id]
	if !down {
		return nil
	}
	delete(v.down, id)
	return v.input(backend.TouchUp{Time: millis(ms), ID: point})
}

func (v *virtualTouch) Cancel(*slvt.LysVirtualTouchV1) error {
	return v.cancel()
}

func (v *virtualTouch) cancel() error {
	if len(v.down) == 0 {
		return nil
	}
	clear(v.down)
	return v.input(backend.TouchCancel{})
}

func (v *virtualTouch) Frame(*slvt.LysVirtualTouchV1) error {
	return v.input(backend.TouchFrame{})
}

func (*virtualTouch) Destroy(*slvt.LysVirtualTouchV1) error {
	return nil
}

func (v *virtualTouch) gone() error {
	return errors.Join(v.cancel(), v.c.seat.RemoveDevice(sw.WlSeatCapabilityTouch))
}

type virtualTouchDevice struct{}

func (virtualTouchDevice) Name() string                     { return "virtual-touch" }
func (virtualTouchDevice) Capabilities() backend.Capability { return backend.Touch }
