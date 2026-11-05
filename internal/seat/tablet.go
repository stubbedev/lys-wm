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

const tabletVersion = 1

type tabletResource struct {
	res  *tablet.ZwpTabletV2
	seat *tablet.ZwpTabletSeatV2
}

type toolResource struct {
	res  *tablet.ZwpTabletToolV2
	seat *tablet.ZwpTabletSeatV2
}

type tabletDevice struct {
	key       any
	name      string
	id        tabletinput.ID
	resources []tabletResource
}

type tabletTool struct {
	tool      *tabletinput.Tool
	resources []toolResource
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
	{tabletinput.CanTilt, tablet.ZwpTabletToolV2CapabilityTilt}, {tabletinput.CanPressure, tablet.ZwpTabletToolV2CapabilityPressure},
	{tabletinput.CanDistance, tablet.ZwpTabletToolV2CapabilityDistance}, {tabletinput.CanRotation, tablet.ZwpTabletToolV2CapabilityRotation},
	{tabletinput.CanSlider, tablet.ZwpTabletToolV2CapabilitySlider}, {tabletinput.CanWheel, tablet.ZwpTabletToolV2CapabilityWheel},
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
	return tabletSeat{}, errors.Join(errs...)
}

type tabletSeat struct{}

func (tabletSeat) Destroy(*tablet.ZwpTabletSeatV2) error { return nil }

type tabletRequests struct{}

func (tabletRequests) Destroy(*tablet.ZwpTabletV2) error { return nil }

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
	server.Track(res, &t.resources, toolResource{res, ts})
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
	return errors.Join(append(errs, res.SendDone())...)
}

func (s *Seat) AddTablet(key any, name string, id tabletinput.ID) error {
	d := &tabletDevice{key: key, name: name, id: id}
	s.tablet.tablets = append(s.tablet.tablets, d)
	var errs []error
	for _, ts := range s.tablet.seats {
		errs = append(errs, s.announceTablet(ts, d))
	}
	return errors.Join(errs...)
}

func (s *Seat) RemoveTablet(key any) error {
	i := slices.IndexFunc(s.tablet.tablets, func(d *tabletDevice) bool { return d.key == key })
	if i < 0 {
		return nil
	}
	d := s.tablet.tablets[i]
	s.tablet.tablets = slices.Delete(s.tablet.tablets, i, i+1)
	var errs []error
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

func (t *tabletTool) clientResources(c *server.Client) []toolResource {
	var out []toolResource
	for _, r := range t.resources {
		if r.res.Base().Peer() == c {
			out = append(out, r)
		}
	}
	return out
}

func (t *tabletTool) focused() []toolResource {
	if t.focus == nil {
		return nil
	}
	return t.clientResources(t.focus.Client())
}

func (s *Seat) TabletToolBound(tool *tabletinput.Tool, target *surface.Surface) bool {
	return target != nil && len(s.toolState(tool).clientResources(target.Client())) > 0
}

func (s *Seat) TabletToolFocus(tool *tabletinput.Tool) *surface.Surface {
	return s.toolState(tool).focus
}

func (s *Seat) TabletToolDown(tool *tabletinput.Tool) bool {
	return s.toolState(tool).down
}

func (s *Seat) eachTool(t *tabletTool, send func(*tablet.ZwpTabletToolV2) error) error {
	var errs []error
	for _, r := range t.focused() {
		errs = append(errs, send(r.res))
	}
	return errors.Join(errs...)
}

func (s *Seat) leaveTool(t *tabletTool) error {
	if t.focus == nil {
		return nil
	}
	err := s.eachTool(t, func(res *tablet.ZwpTabletToolV2) error {
		var errs []error
		if t.down {
			errs = append(errs, res.SendUp())
		}
		for _, b := range t.buttons {
			errs = append(errs, res.SendButton(s.display.NextSerial(), b, tablet.ZwpTabletToolV2ButtonStateReleased))
		}
		return errors.Join(append(errs, res.SendProximityOut(), res.SendFrame(wire.Millis(t.at)))...)
	})
	t.focus = nil
	return err
}

func (s *Seat) enterTool(t *tabletTool, d *tabletDevice, target *surface.Surface) error {
	t.focus, t.tablet = target, d
	t.serial = s.display.NextSerial()
	return s.eachTool(t, func(res *tablet.ZwpTabletToolV2) error {
		i := slices.IndexFunc(d.resources, func(r tabletResource) bool {
			return r.res.Base().Peer() == res.Base().Peer() && slices.ContainsFunc(t.resources, func(tr toolResource) bool { return tr.res == res && tr.seat == r.seat })
		})
		if i < 0 {
			return nil
		}
		errs := []error{res.SendProximityIn(t.serial, d.resources[i].res, target.Resource())}
		if t.down {
			errs = append(errs, res.SendDown(s.eventSerial(target.Client(), true)))
		}
		for _, b := range t.buttons {
			errs = append(errs, res.SendButton(s.display.NextSerial(), b, tablet.ZwpTabletToolV2ButtonStatePressed))
		}
		return errors.Join(errs...)
	})
}

func (s *Seat) TabletToolMotion(at time.Duration, key any, tool *tabletinput.Tool, target *surface.Surface, x, y float64) error {
	t := s.toolState(tool)
	t.at = at
	i := slices.IndexFunc(s.tablet.tablets, func(d *tabletDevice) bool { return d.key == key })
	if i < 0 || target == nil || len(t.clientResources(target.Client())) == 0 {
		return s.leaveTool(t)
	}
	var errs []error
	if d := s.tablet.tablets[i]; target != t.focus || d != t.tablet {
		errs = append(errs, s.leaveTool(t), s.enterTool(t, d, target))
	}
	return errors.Join(append(errs, s.eachTool(t, func(res *tablet.ZwpTabletToolV2) error {
		return res.SendMotion(wire.FixedFromFloat(x), wire.FixedFromFloat(y))
	}))...)
}

func unit(v, scale float64) uint32 {
	return uint32(math.Round(math.Min(math.Max(v, 0), 1) * scale))
}

func (s *Seat) TabletToolAxes(tool *tabletinput.Tool, a tabletinput.Axes) error {
	t := s.toolState(tool)
	return s.eachTool(t, func(res *tablet.ZwpTabletToolV2) error {
		var errs []error
		if a.Changed.Any(tabletinput.AxisPressure) {
			errs = append(errs, res.SendPressure(unit(a.Pressure, 65535)))
		}
		if a.Changed.Any(tabletinput.AxisDistance) {
			errs = append(errs, res.SendDistance(unit(a.Distance, 65535)))
		}
		if a.Changed.Any(tabletinput.AxisTiltX | tabletinput.AxisTiltY) {
			errs = append(errs, res.SendTilt(wire.FixedFromFloat(a.TiltX), wire.FixedFromFloat(a.TiltY)))
		}
		if a.Changed.Any(tabletinput.AxisRotation) {
			errs = append(errs, res.SendRotation(wire.FixedFromFloat(a.Rotation)))
		}
		if a.Changed.Any(tabletinput.AxisSlider) {
			errs = append(errs, res.SendSlider(int32(math.Round(math.Min(math.Max(a.Slider, -1), 1)*65535))))
		}
		if a.Changed.Any(tabletinput.AxisWheel) {
			errs = append(errs, res.SendWheel(wire.FixedFromFloat(a.Wheel), a.WheelClicks))
		}
		return errors.Join(errs...)
	})
}

func (s *Seat) TabletToolTip(tool *tabletinput.Tool, down bool) error {
	t := s.toolState(tool)
	if t.down == down {
		return nil
	}
	t.down = down
	return s.eachTool(t, func(res *tablet.ZwpTabletToolV2) error {
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
	return s.eachTool(t, func(res *tablet.ZwpTabletToolV2) error {
		return res.SendButton(s.eventSerial(t.focus.Client(), pressed), button, state)
	})
}

func (s *Seat) TabletToolFrame(at time.Duration, tool *tabletinput.Tool) error {
	t := s.toolState(tool)
	t.at = at
	return s.eachTool(t, func(res *tablet.ZwpTabletToolV2) error {
		return res.SendFrame(wire.Millis(at))
	})
}

func (s *Seat) TabletToolOut(tool *tabletinput.Tool) error {
	t := s.toolState(tool)
	err := s.leaveTool(t)
	t.down, t.buttons = false, nil
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
