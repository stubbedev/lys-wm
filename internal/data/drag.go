package data

import (
	"errors"
	"image"
	"slices"
	"time"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const iconRole = "wl_data_device-icon"

type Drag struct {
	m          *Manager
	client     *server.Client
	source     DragSource
	origin     *surface.Surface
	serial     uint32
	icon       *surface.Surface
	iconOffset image.Point
	focus      *surface.Surface
	devices    []*sw.WlDataDevice
	offers     []*dataOffer
	offer      *dataOffer
	dropped    bool
	ended      bool
	done       bool
	external   bool
	accepted   bool
}

type DragSource interface {
	Source
	Actions() Action
	Target(mime *string) error
	Action(a Action) error
	DropPerformed() error
	Finished() error
}

type DropTarget interface {
	Takes(s *surface.Surface) bool
	DragEnter(d *Drag, s *surface.Surface, x, y float64) error
	DragMotion(x, y float64) error
	DragLeave() error
	DragDrop() error
}

func (m *Manager) SetDropTarget(t DropTarget) {
	m.dropTarget = t
}

type dragIcon struct{ d *Drag }

func (dragIcon) RoleName() string {
	return iconRole
}

func (i dragIcon) Committed(s *surface.Surface) error {
	i.d.iconOffset = i.d.iconOffset.Add(s.Offset())
	return nil
}

func (m *Manager) startDrag(dev *sw.WlDataDevice, s *dataSource, origin, icon *surface.Surface, serial uint32) error {
	if old := m.drag; old != nil && !old.dropped {
		if err := old.Cancel(); err != nil {
			return err
		}
	}
	d := &Drag{m: m, client: origin.Client(), origin: origin, serial: serial, icon: icon}
	if s != nil {
		d.source = s
	}
	if icon != nil {
		if err := icon.SetRole(dragIcon{d}); err != nil {
			return dev.ProtocolError(sw.WlDataDeviceErrorRole, "%v", err)
		}
		server.OnDestroy(icon.Resource(), func() error {
			if d.icon == icon {
				d.icon = nil
			}
			return nil
		})
	}
	m.drag = d
	return m.listener.DragStarted(d)
}

func (m *Manager) StartForeignDrag(src DragSource) (*Drag, error) {
	if old := m.drag; old != nil && !old.dropped {
		if err := old.Cancel(); err != nil {
			return nil, err
		}
	}
	d := &Drag{m: m, client: src.Client(), source: src}
	m.drag = d
	return d, nil
}

func (d *Drag) Icon() (*surface.Surface, image.Point) {
	if d.ended {
		return nil, image.Point{}
	}
	return d.icon, d.iconOffset
}

func (d *Drag) Origin() *surface.Surface {
	return d.origin
}

func (d *Drag) Serial() uint32 {
	return d.serial
}

func (d *Drag) Source() Source {
	if d.source == nil {
		return nil
	}
	return d.source
}

func (d *Drag) Actions() Action {
	return d.sourceActions()
}

func (d *Drag) Status(accepted bool, action Action) error {
	if !d.external || d.ended || d.source == nil {
		return nil
	}
	var errs []error
	if accepted != d.accepted {
		d.accepted = accepted
		var mime *string
		if mimes := d.source.Mimes(); accepted && len(mimes) > 0 {
			mime = &mimes[0]
		}
		errs = append(errs, d.target(mime))
	}
	if !accepted {
		action = sw.WlDataDeviceManagerDndActionNone
	}
	errs = append(errs, d.source.Action(action&d.sourceActions()))
	return errors.Join(errs...)
}

func (d *Drag) Finished(ok bool) error {
	if !d.dropped || d.done {
		return nil
	}
	if ok {
		return d.finish()
	}
	var err error
	if d.source != nil {
		err = d.source.Cancel()
	}
	d.release()
	return err
}

func (d *Drag) takes(target *surface.Surface) bool {
	t := d.m.dropTarget
	return t != nil && target != nil && d.source != nil && t.Takes(target)
}

func (d *Drag) liveDevices() []*sw.WlDataDevice {
	if d.focus == nil {
		return nil
	}
	devs := d.m.lookup(d.focus.Client())
	if devs == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(d.devices), func(x *sw.WlDataDevice) bool { return !slices.Contains(devs.data, x) })
}

func (d *Drag) leave() error {
	if d.external {
		err := d.m.dropTarget.DragLeave()
		if d.accepted {
			d.accepted = false
			err = errors.Join(err, d.target(nil))
		}
		d.focus, d.external = nil, false
		return err
	}
	var errs []error
	for _, dev := range d.liveDevices() {
		errs = append(errs, dev.SendLeave())
	}
	for _, o := range d.offers {
		if o != d.offer {
			o.dead = true
		}
	}
	d.focus, d.devices, d.offers = nil, nil, nil
	return errors.Join(errs...)
}

func (d *Drag) enter(target *surface.Surface, x, y float64) error {
	d.focus = target
	if d.source == nil && target.Client() != d.client {
		return nil
	}
	devs := d.m.lookup(target.Client())
	if devs == nil {
		return nil
	}
	serial := d.m.display.NextSerial()
	var errs []error
	for _, dev := range devs.data {
		var res *sw.WlDataOffer
		if d.source != nil {
			o, err := d.offerTo(dev)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			res = o.resource
		}
		d.devices = append(d.devices, dev)
		errs = append(errs, dev.SendEnter(serial, target.Resource(), wire.FixedFromFloat(x), wire.FixedFromFloat(y), res))
	}
	return errors.Join(errs...)
}

func (d *Drag) offerTo(dev *sw.WlDataDevice) (*dataOffer, error) {
	o := &dataOffer{source: d.source, m: d.m, client: d.focus.Client(), drag: d}
	res, err := dev.SendDataOffer(o)
	if err != nil {
		return nil, err
	}
	o.resource = res
	for _, mime := range d.source.Mimes() {
		if err := res.SendOffer(mime); err != nil {
			return nil, err
		}
	}
	if res.Base().Version() >= 3 {
		if err := res.SendSourceActions(d.sourceActions()); err != nil {
			return nil, err
		}
	}
	d.offers = append(d.offers, o)
	return o, nil
}

func (d *Drag) sourceActions() Action {
	if d.source == nil {
		return sw.WlDataDeviceManagerDndActionCopy
	}
	return d.source.Actions()
}

func (d *Drag) Motion(target *surface.Surface, x, y float64, at time.Duration) error {
	if d.ended {
		return nil
	}
	if target != d.focus {
		var errs []error
		if d.focus != nil {
			errs = append(errs, d.leave())
		}
		switch {
		case d.takes(target):
			d.focus, d.external = target, true
			errs = append(errs, d.m.dropTarget.DragEnter(d, target, x, y))
		case target != nil:
			errs = append(errs, d.enter(target, x, y))
		}
		return errors.Join(errs...)
	}
	if d.external {
		return d.m.dropTarget.DragMotion(x, y)
	}
	var errs []error
	for _, dev := range d.liveDevices() {
		errs = append(errs, dev.SendMotion(wire.Millis(at), wire.FixedFromFloat(x), wire.FixedFromFloat(y)))
	}
	return errors.Join(errs...)
}

func (d *Drag) target(mime *string) error {
	if d.source == nil {
		return nil
	}
	return d.source.Target(mime)
}

func choose(available, preferred Action) Action {
	if preferred&available != 0 {
		return preferred
	}
	for _, a := range []Action{sw.WlDataDeviceManagerDndActionCopy, sw.WlDataDeviceManagerDndActionMove, sw.WlDataDeviceManagerDndActionAsk} {
		if available&a != 0 {
			return a
		}
	}
	return sw.WlDataDeviceManagerDndActionNone
}

func (d *Drag) negotiate() error {
	var errs []error
	for _, o := range d.offers {
		if o.dead {
			continue
		}
		chosen := choose(d.sourceActions()&o.actions, o.preferred)
		if chosen == o.chosen {
			continue
		}
		o.chosen = chosen
		errs = append(errs, o.resource.SendAction(chosen))
		if d.source != nil {
			errs = append(errs, d.source.Action(chosen))
		}
	}
	return errors.Join(errs...)
}

func (d *Drag) acceptedOffer() *dataOffer {
	for _, o := range d.offers {
		if !o.dead && o.accepted != nil && (o.resource.Base().Version() < 3 || o.chosen != sw.WlDataDeviceManagerDndActionNone) {
			return o
		}
	}
	return nil
}

func (d *Drag) Drop() error {
	if d.ended {
		return nil
	}
	if d.external {
		return d.dropExternal()
	}
	if d.source == nil {
		var errs []error
		for _, dev := range d.liveDevices() {
			errs = append(errs, dev.SendDrop())
		}
		d.dropped = true
		errs = append(errs, d.leave(), d.end())
		d.release()
		return errors.Join(errs...)
	}
	o := d.acceptedOffer()
	if o == nil {
		return d.Cancel()
	}
	d.dropped, d.offer = true, o
	var errs []error
	for _, dev := range d.liveDevices() {
		errs = append(errs, dev.SendDrop())
	}
	errs = append(errs, d.source.DropPerformed())
	return errors.Join(append(errs, d.leave(), d.end())...)
}

func (d *Drag) dropExternal() error {
	if !d.accepted {
		return d.Cancel()
	}
	d.dropped = true
	var errs []error
	errs = append(errs, d.source.DropPerformed())
	d.focus, d.external = nil, false
	return errors.Join(append(errs, d.m.dropTarget.DragDrop(), d.end())...)
}

func (d *Drag) end() error {
	if d.ended {
		return nil
	}
	d.ended = true
	if d.icon != nil {
		d.icon.ClearRole(dragIcon{d})
	}
	return d.m.listener.DragEnded(d)
}

func (d *Drag) release() {
	d.done = true
	if d.m.drag == d {
		d.m.drag = nil
	}
}

func (d *Drag) Cancel() error {
	if d.done {
		return nil
	}
	var errs []error
	if !d.ended {
		errs = append(errs, d.leave())
	}
	if d.offer != nil {
		d.offer.dead = true
	}
	if d.source != nil {
		errs = append(errs, d.source.Cancel())
	}
	errs = append(errs, d.end())
	d.release()
	return errors.Join(errs...)
}

func (d *Drag) finish() error {
	if d.done {
		return nil
	}
	var errs []error
	if d.source != nil {
		errs = append(errs, d.source.Finished())
	}
	d.release()
	return errors.Join(errs...)
}
