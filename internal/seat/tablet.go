package seat

import (
	"errors"
	"math"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/protocol/server/tablet"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const tabletVersion = 2

type tabletResource struct {
	res  *tablet.ZwpTabletV2
	seat *tablet.ZwpTabletSeatV2
}

type toolResource struct {
	res  *tablet.ZwpTabletToolV2
	seat *tablet.ZwpTabletSeatV2
	near bool
}

type tabletDevice struct {
	key       any
	group     uintptr
	name      string
	id        tabletinput.ID
	resources []tabletResource
}

type tabletTool struct {
	tool      *tabletinput.Tool
	resources []*toolResource
	focus     *surface.Surface
	tablet    *tabletDevice
	serial    uint32
	down      bool
	buttons   []uint32
	at        time.Duration
}

type tabletState struct {
	seats   []*tablet.ZwpTabletSeatV2
	tablets []*tabletDevice
	tools   []*tabletTool
	pads    []*padDevice
}

var toolTypes = map[tabletinput.ToolType]tablet.ZwpTabletToolV2Type{
	tabletinput.Pen: tablet.ZwpTabletToolV2TypePen, tabletinput.Eraser: tablet.ZwpTabletToolV2TypeEraser,
	tabletinput.Brush: tablet.ZwpTabletToolV2TypeBrush, tabletinput.Pencil: tablet.ZwpTabletToolV2TypePencil,
	tabletinput.Airbrush: tablet.ZwpTabletToolV2TypeAirbrush, tabletinput.Mouse: tablet.ZwpTabletToolV2TypeMouse,
	tabletinput.Lens: tablet.ZwpTabletToolV2TypeLens, tabletinput.Totem: tablet.ZwpTabletToolV2TypePen,
}

var toolCapabilities = []struct {
	has  tabletinput.Capability
	send tablet.ZwpTabletToolV2Capability
}{
	{tabletinput.CanTilt, tablet.ZwpTabletToolV2CapabilityTilt},
	{tabletinput.CanPressure, tablet.ZwpTabletToolV2CapabilityPressure},
	{tabletinput.CanDistance, tablet.ZwpTabletToolV2CapabilityDistance},
	{tabletinput.CanRotation, tablet.ZwpTabletToolV2CapabilityRotation},
	{tabletinput.CanSlider, tablet.ZwpTabletToolV2CapabilitySlider},
	{tabletinput.CanWheel, tablet.ZwpTabletToolV2CapabilityWheel},
}

func (s *Seat) bindTablets(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := tablet.NewZwpTabletManagerV2(c, id, version, tabletManager{s})
	return err
}

type tabletManager struct {
	s *Seat
}

func (tabletManager) Destroy(*tablet.ZwpTabletManagerV2) error { return nil }

func (m tabletManager) GetTabletSeat(_ *tablet.ZwpTabletManagerV2, ts *tablet.ZwpTabletSeatV2, _ *sw.WlSeat) (tablet.ZwpTabletSeatV2Handler, error) {
	s := m.s
	server.Track(ts, &s.tablet.seats, ts)
	var errs []error
	for _, d := range s.tablet.tablets {
		errs = append(errs, s.announceTablet(ts, d))
	}
	for _, t := range s.tablet.tools {
		errs = append(errs, s.announceTool(ts, t))
	}
	for _, p := range s.tablet.pads {
		errs = append(errs, s.announcePad(ts, p))
	}
	return tabletSeat{}, errors.Join(errs...)
}

type tabletSeat struct{}

func (tabletSeat) Destroy(*tablet.ZwpTabletSeatV2) error { return nil }

type tabletRequests struct{}

func (tabletRequests) Destroy(*tablet.ZwpTabletV2) error { return nil }

func (d *tabletDevice) resourceOn(ts *tablet.ZwpTabletSeatV2) *tablet.ZwpTabletV2 {
	for _, r := range d.resources {
		if r.seat == ts {
			return r.res
		}
	}
	return nil
}

func (s *Seat) announceTablet(ts *tablet.ZwpTabletSeatV2, d *tabletDevice) error {
	res, err := ts.SendTabletAdded(tabletRequests{})
	if err != nil {
		return err
	}
	server.Track(res, &d.resources, tabletResource{res, ts})
	errs := []error{res.SendName(d.name)}
	if d.id != (tabletinput.ID{}) {
		errs = append(errs, res.SendID(d.id.Vendor, d.id.Product))
	}
	return errors.Join(append(errs, res.SendDone())...)
}

func (s *Seat) announceTool(ts *tablet.ZwpTabletSeatV2, t *tabletTool) error {
	res, err := ts.SendToolAdded(toolRequests{s, t})
	if err != nil {
		return err
	}
	r := &toolResource{res: res, seat: ts}
	server.Track(res, &t.resources, r)
	server.OnDestroy(res, func() error {
		if !t.near() {
			t.focus = nil
		}
		return nil
	})
	errs := []error{res.SendType(toolTypes[t.tool.Type])}
	if t.tool.Serial != 0 {
		errs = append(errs, res.SendHardwareSerial(wire.Halves(t.tool.Serial)))
	}
	if t.tool.HardwareID != 0 {
		errs = append(errs, res.SendHardwareIDWacom(wire.Halves(t.tool.HardwareID)))
	}
	for _, c := range toolCapabilities {
		if t.tool.Caps.Has(c.has) {
			errs = append(errs, res.SendCapability(c.send))
		}
	}
	errs = append(errs, res.SendDone())
	if t.focus != nil && res.Base().Peer() == t.focus.Client() {
		errs = append(errs, s.enterToolResource(t, r))
		if r.near {
			errs = append(errs, res.SendFrame(wire.Millis(t.at)))
		}
	}
	return errors.Join(errs...)
}

func (s *Seat) AddTablet(key any, group uintptr, name string, id tabletinput.ID) error {
	d := &tabletDevice{key: key, group: group, name: name, id: id}
	s.tablet.tablets = append(s.tablet.tablets, d)
	var errs []error
	for _, ts := range s.tablet.seats {
		errs = append(errs, s.announceTablet(ts, d))
	}
	return errors.Join(append(errs, s.refocusPads(group))...)
}

func (s *Seat) RemoveTablet(key any) error {
	i := slices.IndexFunc(s.tablet.tablets, func(d *tabletDevice) bool { return d.key == key })
	if i < 0 {
		return nil
	}
	d := s.tablet.tablets[i]
	s.tablet.tablets = slices.Delete(s.tablet.tablets, i, i+1)
	errs := []error{s.removePadsOf(d.group)}
	for _, t := range s.tablet.tools {
		if t.tablet == d {
			errs = append(errs, s.TabletToolOut(t.tool))
			t.tablet = nil
		}
	}
	for _, r := range d.resources {
		errs = append(errs, r.res.SendRemoved())
	}
	d.resources = nil
	if len(s.tablet.tablets) == 0 {
		for _, t := range s.tablet.tools {
			for _, r := range t.resources {
				errs = append(errs, r.res.SendRemoved())
			}
		}
		s.tablet.tools = nil
	}
	return errors.Join(errs...)
}

func (s *Seat) toolState(tool *tabletinput.Tool) *tabletTool {
	for _, t := range s.tablet.tools {
		if t.tool == tool {
			return t
		}
	}
	return &tabletTool{tool: tool}
}

func (s *Seat) TabletTool(tool *tabletinput.Tool) error {
	if slices.ContainsFunc(s.tablet.tools, func(t *tabletTool) bool { return t.tool == tool }) {
		return nil
	}
	t := &tabletTool{tool: tool}
	s.tablet.tools = append(s.tablet.tools, t)
	var errs []error
	for _, ts := range s.tablet.seats {
		errs = append(errs, s.announceTool(ts, t))
	}
	return errors.Join(errs...)
}

func (t *tabletTool) near() bool {
	return slices.ContainsFunc(t.resources, func(r *toolResource) bool { return r.near })
}

func (t *tabletTool) reaches(d *tabletDevice, c *server.Client) bool {
	return slices.ContainsFunc(t.resources, func(r *toolResource) bool {
		return r.res.Base().Peer() == c && d.resourceOn(r.seat) != nil
	})
}

func (s *Seat) TabletToolFocus(tool *tabletinput.Tool) *surface.Surface {
	return s.toolState(tool).focus
}

func (s *Seat) TabletToolDown(tool *tabletinput.Tool) bool {
	return s.toolState(tool).down
}

func (t *tabletTool) each(send func(*tablet.ZwpTabletToolV2) error) error {
	var errs error
	for _, r := range t.resources {
		if r.near {
			errs = errors.Join(errs, send(r.res))
		}
	}
	return errs
}

func (s *Seat) leaveTool(t *tabletTool) error {
	var errs error
	for _, r := range t.resources {
		if !r.near {
			continue
		}
		r.near = false
		if t.down {
			errs = errors.Join(errs, r.res.SendUp())
		}
		for _, b := range t.buttons {
			errs = errors.Join(errs, r.res.SendButton(s.display.NextSerial(), b, tablet.ZwpTabletToolV2ButtonStateReleased))
		}
		errs = errors.Join(errs, r.res.SendProximityOut(), r.res.SendFrame(wire.Millis(t.at)))
	}
	t.focus = nil
	return errs
}

func (s *Seat) enterTool(t *tabletTool, d *tabletDevice, target *surface.Surface) error {
	c := target.Client()
	if !t.reaches(d, c) {
		return nil
	}
	t.focus, t.tablet, t.serial = target, d, s.display.NextSerial()
	var errs error
	for _, r := range t.resources {
		if r.res.Base().Peer() == c {
			errs = errors.Join(errs, s.enterToolResource(t, r))
		}
	}
	return errs
}

func (s *Seat) enterToolResource(t *tabletTool, r *toolResource) error {
	on := t.tablet.resourceOn(r.seat)
	if on == nil {
		return nil
	}
	r.near = true
	errs := r.res.SendProximityIn(t.serial, on, t.focus.Resource())
	if t.down {
		errs = errors.Join(errs, r.res.SendDown(s.eventSerial(t.focus.Client(), true)))
	}
	for _, b := range t.buttons {
		errs = errors.Join(errs, r.res.SendButton(s.display.NextSerial(), b, tablet.ZwpTabletToolV2ButtonStatePressed))
	}
	return errs
}

func (s *Seat) TabletToolMotion(at time.Duration, key any, tool *tabletinput.Tool, target *surface.Surface, x, y float64) error {
	t := s.toolState(tool)
	t.at = at
	i := slices.IndexFunc(s.tablet.tablets, func(d *tabletDevice) bool { return d.key == key })
	if i < 0 || target == nil {
		return s.leaveTool(t)
	}
	var errs error
	if d := s.tablet.tablets[i]; target != t.focus || d != t.tablet {
		errs = errors.Join(s.leaveTool(t), s.enterTool(t, d, target))
	}
	return errors.Join(errs, t.each(func(res *tablet.ZwpTabletToolV2) error {
		return res.SendMotion(wire.FixedFromFloat(x), wire.FixedFromFloat(y))
	}))
}

func unit(v, scale float64) uint32 {
	return uint32(math.Round(math.Min(math.Max(v, 0), 1) * scale))
}

func (s *Seat) TabletToolAxes(tool *tabletinput.Tool, a tabletinput.Axes) error {
	t := s.toolState(tool)
	return t.each(func(res *tablet.ZwpTabletToolV2) error {
		var errs error
		if a.Changed.Any(tabletinput.AxisPressure) {
			errs = errors.Join(errs, res.SendPressure(unit(a.Pressure, 65535)))
		}
		if a.Changed.Any(tabletinput.AxisDistance) {
			errs = errors.Join(errs, res.SendDistance(unit(a.Distance, 65535)))
		}
		if a.Changed.Any(tabletinput.AxisTiltX | tabletinput.AxisTiltY) {
			errs = errors.Join(errs, res.SendTilt(wire.FixedFromFloat(a.TiltX), wire.FixedFromFloat(a.TiltY)))
		}
		if a.Changed.Any(tabletinput.AxisRotation) {
			errs = errors.Join(errs, res.SendRotation(wire.FixedFromFloat(a.Rotation)))
		}
		if a.Changed.Any(tabletinput.AxisSlider) {
			errs = errors.Join(errs, res.SendSlider(int32(math.Round(math.Min(math.Max(a.Slider, -1), 1)*65535))))
		}
		if a.Changed.Any(tabletinput.AxisWheel) {
			errs = errors.Join(errs, res.SendWheel(wire.FixedFromFloat(a.Wheel), a.WheelClicks))
		}
		return errs
	})
}

func (s *Seat) TabletToolTip(tool *tabletinput.Tool, down bool) error {
	t := s.toolState(tool)
	if t.down == down {
		return nil
	}
	t.down = down
	return t.each(func(res *tablet.ZwpTabletToolV2) error {
		if down {
			return res.SendDown(s.eventSerial(t.focus.Client(), true))
		}
		return res.SendUp()
	})
}

func (s *Seat) TabletToolButton(tool *tabletinput.Tool, button uint32, pressed bool) error {
	t := s.toolState(tool)
	i := slices.Index(t.buttons, button)
	if pressed == (i >= 0) {
		return nil
	}
	state := tablet.ZwpTabletToolV2ButtonStateReleased
	if pressed {
		t.buttons = append(t.buttons, button)
		state = tablet.ZwpTabletToolV2ButtonStatePressed
	} else {
		t.buttons = slices.Delete(t.buttons, i, i+1)
	}
	return t.each(func(res *tablet.ZwpTabletToolV2) error {
		return res.SendButton(s.eventSerial(t.focus.Client(), pressed), button, state)
	})
}

func (s *Seat) TabletToolFrame(at time.Duration, tool *tabletinput.Tool) error {
	t := s.toolState(tool)
	t.at = at
	return t.each(func(res *tablet.ZwpTabletToolV2) error {
		return res.SendFrame(wire.Millis(at))
	})
}

func (s *Seat) TabletToolOut(tool *tabletinput.Tool) error {
	t := s.toolState(tool)
	err := s.leaveTool(t)
	t.down, t.buttons = false, t.buttons[:0]
	return err
}

func (s *Seat) tabletSurfaceDestroyed(target *surface.Surface) error {
	var errs []error
	for _, t := range s.tablet.tools {
		if t.focus == target {
			errs = append(errs, s.leaveTool(t))
		}
	}
	return errors.Join(errs...)
}

func (t *tabletTool) entered(c *server.Client, serial uint32) bool {
	return t.focus != nil && t.focus.Client() == c && t.serial == serial
}

type toolRequests struct {
	s *Seat
	t *tabletTool
}

func (r toolRequests) SetCursor(obj *tablet.ZwpTabletToolV2, serial uint32, wl *sw.WlSurface, hx, hy int32) error {
	c, ok := obj.Base().Peer().(*server.Client)
	if !ok || !r.t.entered(c, serial) {
		return nil
	}
	return r.s.setCursor(c, wl, hx, hy, func(err error) error {
		return obj.ProtocolError(tablet.ZwpTabletToolV2ErrorRole, "%v", err)
	})
}

func (toolRequests) Destroy(*tablet.ZwpTabletToolV2) error { return nil }
