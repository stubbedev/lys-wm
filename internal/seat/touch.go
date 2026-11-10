package seat

import (
	"errors"
	"slices"
	"time"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type touchPoint struct {
	id      int32
	surface *surface.Surface
	serial  uint32
	at      uint32
	local   fixedPoint
}

func (p *touchPoint) down(t *sw.WlTouch) error {
	return t.SendDown(p.serial, p.at, p.surface.Resource(), p.id, p.local.x, p.local.y)
}

type touchState struct {
	points    []touchPoint
	framed    []*server.Client
	cancelled []*server.Client
}

func (t *touchState) point(id int32) *touchPoint {
	for i := range t.points {
		if t.points[i].id == id {
			return &t.points[i]
		}
	}
	return nil
}

func (s *Seat) TouchSurface(id int32) *surface.Surface {
	if p := s.touch.point(id); p != nil {
		return p.surface
	}
	return nil
}

func (s *Seat) TouchBySerial(target *surface.Surface, serial uint32) (int32, bool) {
	for _, p := range s.touch.points {
		if p.surface == target && p.serial == serial {
			return p.id, true
		}
	}
	return 0, false
}

func (s *Seat) TouchPress(c *server.Client, serial uint32) (int32, bool) {
	for _, p := range s.touch.points {
		if serial != 0 && p.serial == serial && p.surface.Client() == c {
			return p.id, true
		}
	}
	return 0, false
}

func (s *Seat) DropTouch(id int32) {
	s.touch.points = slices.DeleteFunc(s.touch.points, func(p touchPoint) bool { return p.id == id })
}

func (s *Seat) touchesOf(target *surface.Surface) (*server.Client, []*sw.WlTouch) {
	c := target.Client()
	if !slices.Contains(s.touch.framed, c) {
		s.touch.framed = append(s.touch.framed, c)
	}
	if r := s.lookup(c); r != nil {
		return c, r.touches
	}
	return c, nil
}

func (s *Seat) TouchDown(at time.Duration, id int32, target *surface.Surface, x, y float64) error {
	if s.touch.point(id) != nil {
		return nil
	}
	c, touches := s.touchesOf(target)
	p := touchPoint{id: id, surface: target, serial: s.eventSerial(c, true), at: wire.Millis(at), local: fixedAt(x, y)}
	s.touch.points = append(s.touch.points, p)
	var errs error
	for _, t := range touches {
		errs = errors.Join(errs, p.down(t))
	}
	return errs
}

func (s *Seat) TouchMotion(at time.Duration, id int32, x, y float64) error {
	p := s.touch.point(id)
	if p == nil {
		return nil
	}
	p.at, p.local = wire.Millis(at), fixedAt(x, y)
	_, touches := s.touchesOf(p.surface)
	var errs error
	for _, t := range touches {
		errs = errors.Join(errs, t.SendMotion(p.at, id, p.local.x, p.local.y))
	}
	return errs
}

func (s *Seat) TouchUp(at time.Duration, id int32) error {
	target := s.TouchSurface(id)
	if target == nil {
		return nil
	}
	s.DropTouch(id)
	_, touches := s.touchesOf(target)
	var errs error
	for _, t := range touches {
		errs = errors.Join(errs, t.SendUp(s.display.NextSerial(), wire.Millis(at), id))
	}
	return errs
}

func (s *Seat) touchClients(send func(*sw.WlTouch) error) error {
	var errs error
	for _, c := range s.touch.framed {
		if r := s.lookup(c); r != nil {
			for _, t := range r.touches {
				errs = errors.Join(errs, send(t))
			}
		}
	}
	s.touch.framed = s.touch.framed[:0]
	return errs
}

func (s *Seat) TouchFrame() error {
	return s.touchClients((*sw.WlTouch).SendFrame)
}

func (s *Seat) TouchCancel() error {
	for _, p := range s.touch.points {
		if !slices.Contains(s.touch.framed, p.surface.Client()) {
			s.touch.framed = append(s.touch.framed, p.surface.Client())
		}
	}
	s.touch.points = nil
	return s.touchClients((*sw.WlTouch).SendCancel)
}

// CancelTouches sends wl_touch.cancel to the clients holding any of ids, which
// ends every touch point those clients hold.
func (s *Seat) CancelTouches(ids []int32) error {
	t := &s.touch
	t.cancelled = t.cancelled[:0]
	for _, p := range t.points {
		if slices.Contains(ids, p.id) && !slices.Contains(t.cancelled, p.surface.Client()) {
			t.cancelled = append(t.cancelled, p.surface.Client())
		}
	}
	t.points = slices.DeleteFunc(t.points, func(p touchPoint) bool { return slices.Contains(t.cancelled, p.surface.Client()) })
	t.framed = slices.DeleteFunc(t.framed, func(c *server.Client) bool { return slices.Contains(t.cancelled, c) })
	var errs error
	for _, c := range t.cancelled {
		if r := s.lookup(c); r != nil {
			for _, wt := range r.touches {
				errs = errors.Join(errs, wt.SendCancel())
			}
		}
	}
	return errs
}

func (r seatRequests) GetTouch(obj *sw.WlSeat, t *sw.WlTouch) (sw.WlTouchHandler, error) {
	s := r.s
	if !s.everCaps.Has(sw.WlSeatCapabilityTouch) {
		return nil, missing(obj, "touch device")
	}
	res := s.resources(r.c)
	track(s, res, &res.touches, t)
	held := false
	for i := range s.touch.points {
		p := &s.touch.points[i]
		if p.surface.Client() != r.c {
			continue
		}
		if err := p.down(t); err != nil {
			return nil, err
		}
		held = true
	}
	if held {
		if err := t.SendFrame(); err != nil {
			return nil, err
		}
	}
	return touchRequests{}, nil
}

type touchRequests struct{}

func (touchRequests) Release(*sw.WlTouch) error {
	return nil
}
