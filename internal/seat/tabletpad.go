package seat

import (
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/protocol/server/tablet"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const padDialVersion = 2

type padGroupResource struct {
	res    *tablet.ZwpTabletPadGroupV2
	rings  []*tablet.ZwpTabletPadRingV2
	strips []*tablet.ZwpTabletPadStripV2
	dials  []*tablet.ZwpTabletPadDialV2
}

type padResource struct {
	res     *tablet.ZwpTabletPadV2
	seat    *tablet.ZwpTabletSeatV2
	groups  []padGroupResource
	entered bool
}

type padDevice struct {
	key       any
	group     uintptr
	layout    tabletinput.PadLayout
	resources []*padResource
	focus     *surface.Surface
	at        time.Duration
}

func (s *Seat) announcePad(ts *tablet.ZwpTabletSeatV2, p *padDevice) error {
	res, err := ts.SendPadAdded(padRequests{})
	if err != nil {
		return err
	}
	r := &padResource{res: res, seat: ts}
	server.Track(res, &p.resources, r)
	var errs []error
	for _, g := range p.layout.Groups {
		gr, err := res.SendGroup(padGroupRequests{})
		if err != nil {
			return err
		}
		buttons := make([]byte, 0, 4*len(g.Buttons))
		for _, b := range g.Buttons {
			buttons = binary.LittleEndian.AppendUint32(buttons, b)
		}
		errs = append(errs, gr.SendButtons(buttons))
		group := padGroupResource{res: gr}
		for range g.Rings {
			ring, err := gr.SendRing(padRingRequests{})
			if err != nil {
				return err
			}
			group.rings = append(group.rings, ring)
		}
		for range g.Strips {
			strip, err := gr.SendStrip(padStripRequests{})
			if err != nil {
				return err
			}
			group.strips = append(group.strips, strip)
		}
		if gr.Base().Version() >= padDialVersion {
			for range g.Dials {
				dial, err := gr.SendDial(padDialRequests{})
				if err != nil {
					return err
				}
				group.dials = append(group.dials, dial)
			}
		}
		errs = append(errs, gr.SendModes(max(g.Modes, 1)), gr.SendDone())
		r.groups = append(r.groups, group)
	}
	if p.layout.Buttons > 0 {
		errs = append(errs, res.SendButtons(p.layout.Buttons))
	}
	errs = append(errs, res.SendDone())
	if p.focus != nil && res.Base().Peer() == p.focus.Client() {
		errs = append(errs, s.enterPadResource(p, r))
	}
	return errors.Join(errs...)
}

func (s *Seat) AddPad(key any, group uintptr, layout tabletinput.PadLayout) error {
	p := &padDevice{key: key, group: group, layout: layout}
	s.tablet.pads = append(s.tablet.pads, p)
	var errs []error
	for _, ts := range s.tablet.seats {
		errs = append(errs, s.announcePad(ts, p))
	}
	return errors.Join(append(errs, s.focusPad(p, s.keyboard.focus))...)
}

func (s *Seat) RemovePad(key any) error {
	i := slices.IndexFunc(s.tablet.pads, func(p *padDevice) bool { return p.key == key })
	if i < 0 {
		return nil
	}
	return s.dropPad(i)
}

func (s *Seat) dropPad(i int) error {
	p := s.tablet.pads[i]
	s.tablet.pads = slices.Delete(s.tablet.pads, i, i+1)
	var errs []error
	for _, r := range p.resources {
		errs = append(errs, r.res.SendRemoved())
	}
	p.resources = nil
	return errors.Join(errs...)
}

func (s *Seat) refocusPads(group uintptr) error {
	var errs []error
	for _, p := range s.tablet.pads {
		if p.group != group {
			continue
		}
		for _, r := range s.focusedPad(p) {
			if !r.entered {
				errs = append(errs, s.enterPadResource(p, r))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Seat) removePadsOf(group uintptr) error {
	var errs []error
	for i, p := range slices.Backward(s.tablet.pads) {
		if p.group == group {
			errs = append(errs, s.dropPad(i))
		}
	}
	return errors.Join(errs...)
}

func (s *Seat) padTablet(p *padDevice) *tabletDevice {
	for _, d := range s.tablet.tablets {
		if d.group == p.group {
			return d
		}
	}
	return nil
}

func (s *Seat) focusedPad(p *padDevice) []*padResource {
	if p.focus == nil {
		return nil
	}
	var out []*padResource
	for _, r := range p.resources {
		if r.res.Base().Peer() == p.focus.Client() {
			out = append(out, r)
		}
	}
	return out
}

func (s *Seat) enterPadResource(p *padDevice, r *padResource) error {
	d := s.padTablet(p)
	if d == nil {
		return nil
	}
	i := slices.IndexFunc(d.resources, func(t tabletResource) bool { return t.seat == r.seat })
	if i < 0 {
		return nil
	}
	r.entered = true
	serial := s.display.NextSerial()
	errs := []error{r.res.SendEnter(serial, d.resources[i].res, p.focus.Resource())}
	for gi, g := range p.layout.Groups {
		errs = append(errs, r.groups[gi].res.SendModeSwitch(wire.Millis(p.at), serial, g.Mode))
	}
	return errors.Join(errs...)
}

func (s *Seat) focusPad(p *padDevice, target *surface.Surface) error {
	if p.focus == target {
		return nil
	}
	var errs []error
	if p.focus != nil {
		serial := s.display.NextSerial()
		for _, r := range s.focusedPad(p) {
			if r.entered {
				r.entered = false
				errs = append(errs, r.res.SendLeave(serial, p.focus.Resource()))
			}
		}
	}
	p.focus = target
	for _, r := range s.focusedPad(p) {
		errs = append(errs, s.enterPadResource(p, r))
	}
	return errors.Join(errs...)
}

func (s *Seat) focusPads(target *surface.Surface) error {
	var errs []error
	for _, p := range s.tablet.pads {
		errs = append(errs, s.focusPad(p, target))
	}
	return errors.Join(errs...)
}

func (s *Seat) padsSurfaceDestroyed(target *surface.Surface) {
	for _, p := range s.tablet.pads {
		if p.focus == target {
			for _, r := range p.resources {
				r.entered = false
			}
			p.focus = nil
		}
	}
}

func (s *Seat) pad(key any) *padDevice {
	for _, p := range s.tablet.pads {
		if p.key == key {
			return p
		}
	}
	return nil
}

func (s *Seat) padMember(key any, group uint32, members func(tabletinput.PadGroup) []uint32, n uint32) int {
	p := s.pad(key)
	if p == nil || int(group) >= len(p.layout.Groups) {
		return -1
	}
	return slices.Index(members(p.layout.Groups[group]), n)
}

func (s *Seat) padEvent(key any, at time.Duration, group, mode uint32, send func(*padResource) error) error {
	p := s.pad(key)
	if p == nil || int(group) >= len(p.layout.Groups) {
		return nil
	}
	p.at = at
	g := &p.layout.Groups[group]
	switched := g.Mode != mode
	g.Mode = mode
	var errs []error
	var serial uint32
	if switched {
		serial = s.display.NextSerial()
	}
	for _, r := range s.focusedPad(p) {
		if !r.entered {
			continue
		}
		if switched {
			errs = append(errs, r.groups[group].res.SendModeSwitch(wire.Millis(at), serial, mode))
		}
		errs = append(errs, send(r))
	}
	return errors.Join(errs...)
}

func (s *Seat) PadButton(key any, e *tabletinput.PadButton) error {
	state := tablet.ZwpTabletPadV2ButtonStateReleased
	if e.Pressed {
		state = tablet.ZwpTabletPadV2ButtonStatePressed
	}
	return s.padEvent(key, e.Time, e.Group, e.Mode, func(r *padResource) error {
		return r.res.SendButton(wire.Millis(e.Time), e.Button, state)
	})
}

func (s *Seat) PadRing(key any, e *tabletinput.PadRing) error {
	i := s.padMember(key, e.Group, func(g tabletinput.PadGroup) []uint32 { return g.Rings }, e.Ring)
	if i < 0 {
		return nil
	}
	return s.padEvent(key, e.Time, e.Group, e.Mode, func(r *padResource) error {
		ring := r.groups[e.Group].rings[i]
		var errs []error
		if e.Finger {
			errs = append(errs, ring.SendSource(tablet.ZwpTabletPadRingV2SourceFinger))
		}
		if e.Stopped {
			errs = append(errs, ring.SendStop())
		} else {
			errs = append(errs, ring.SendAngle(wire.FixedFromFloat(e.Degrees)))
		}
		return errors.Join(append(errs, ring.SendFrame(wire.Millis(e.Time)))...)
	})
}

func (s *Seat) PadStrip(key any, e *tabletinput.PadStrip) error {
	i := s.padMember(key, e.Group, func(g tabletinput.PadGroup) []uint32 { return g.Strips }, e.Strip)
	if i < 0 {
		return nil
	}
	return s.padEvent(key, e.Time, e.Group, e.Mode, func(r *padResource) error {
		strip := r.groups[e.Group].strips[i]
		var errs []error
		if e.Finger {
			errs = append(errs, strip.SendSource(tablet.ZwpTabletPadStripV2SourceFinger))
		}
		if e.Stopped {
			errs = append(errs, strip.SendStop())
		} else {
			errs = append(errs, strip.SendPosition(uint32(math.Round(math.Min(math.Max(e.Position, 0), 1)*65535))))
		}
		return errors.Join(append(errs, strip.SendFrame(wire.Millis(e.Time)))...)
	})
}

func (s *Seat) PadDial(key any, e *tabletinput.PadDial) error {
	i := s.padMember(key, e.Group, func(g tabletinput.PadGroup) []uint32 { return g.Dials }, e.Dial)
	if i < 0 {
		return nil
	}
	return s.padEvent(key, e.Time, e.Group, e.Mode, func(r *padResource) error {
		dials := r.groups[e.Group].dials
		if i >= len(dials) {
			return nil
		}
		return errors.Join(dials[i].SendDelta(e.Delta), dials[i].SendFrame(wire.Millis(e.Time)))
	})
}

type padRequests struct{}

func (padRequests) SetFeedback(*tablet.ZwpTabletPadV2, uint32, string, uint32) error { return nil }
func (padRequests) Destroy(*tablet.ZwpTabletPadV2) error                             { return nil }

type padGroupRequests struct{}

func (padGroupRequests) Destroy(*tablet.ZwpTabletPadGroupV2) error { return nil }

type padRingRequests struct{}

func (padRingRequests) SetFeedback(*tablet.ZwpTabletPadRingV2, string, uint32) error { return nil }
func (padRingRequests) Destroy(*tablet.ZwpTabletPadRingV2) error                     { return nil }

type padStripRequests struct{}

func (padStripRequests) SetFeedback(*tablet.ZwpTabletPadStripV2, string, uint32) error { return nil }

func (padStripRequests) Destroy(*tablet.ZwpTabletPadStripV2) error { return nil }

type padDialRequests struct{}

func (padDialRequests) SetFeedback(*tablet.ZwpTabletPadDialV2, string, uint32) error { return nil }
func (padDialRequests) Destroy(*tablet.ZwpTabletPadDialV2) error                     { return nil }
