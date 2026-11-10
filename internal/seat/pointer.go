package seat

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

const pointerFrameVersion = 5

type Axis struct {
	Orientation sw.WlPointerAxis
	Source      sw.WlPointerAxisSource
	Value       float64
	Value120    int32
}

type Cursor interface {
	cursor()
}

type DefaultCursor struct{}

type HiddenCursor struct{}

type SurfaceCursor struct {
	Surface *surface.Surface
	Hotspot image.Point
}

func IsCursorSurface(s *surface.Surface) bool {
	return s.Role() != nil && s.Role().RoleName() == surface.CursorRole
}

type ShapeCursor struct {
	Shape string
}

func (DefaultCursor) cursor() {}
func (HiddenCursor) cursor()  {}
func (SurfaceCursor) cursor() {}
func (ShapeCursor) cursor()   {}

type clientCursor struct {
	client *server.Client
	want   Cursor
}

type heldButton struct {
	code, serial uint32
}

type pointerState struct {
	focus       *surface.Surface
	enterSerial uint32
	local       fixedPoint
	buttons     []heldButton
	cursor      clientCursor
	framed      []*server.Client
}

type fixedPoint struct {
	x, y wire.Fixed
}

func fixedAt(x, y float64) fixedPoint {
	return fixedPoint{wire.FixedFromFloat(x), wire.FixedFromFloat(y)}
}

func (s *Seat) HasPointer() bool {
	return s.devices[sw.WlSeatCapabilityPointer] > 0
}

func (s *Seat) PointerFocus() *surface.Surface {
	return s.pointer.focus
}

func (s *Seat) PointerGrabbed() bool {
	return len(s.pointer.buttons) > 0
}

func (s *Seat) Cursor() Cursor {
	if s.pointer.focus == nil || s.pointer.cursor.client != s.pointer.focus.Client() {
		return DefaultCursor{}
	}
	return s.pointer.cursor.want
}

func (s *Seat) focusPointers() (*server.Client, []*sw.WlPointer) {
	c, r := s.resourcesAt(s.pointer.focus)
	if r == nil {
		return c, nil
	}
	return c, r.pointers
}

func (s *Seat) SetPointerFocus(target *surface.Surface, x, y float64) error {
	p := &s.pointer
	if target == p.focus {
		return nil
	}
	var errs error
	if c, pointers := s.focusPointers(); c != nil {
		for _, ptr := range pointers {
			errs = errors.Join(errs, ptr.SendLeave(s.display.NextSerial(), p.focus.Resource()))
		}
		if !slices.Contains(p.framed, c) {
			p.framed = append(p.framed, c)
		}
	}
	p.focus, p.buttons, p.local = target, p.buttons[:0], fixedAt(x, y)
	if c, pointers := s.focusPointers(); c != nil {
		p.enterSerial = s.display.NextSerial()
		for _, ptr := range pointers {
			errs = errors.Join(errs, p.enter(ptr))
		}
	}
	return errors.Join(errs, s.listener.PointerFocusChanged(target))
}

func (p *pointerState) enter(ptr *sw.WlPointer) error {
	return ptr.SendEnter(p.enterSerial, p.focus.Resource(), p.local.x, p.local.y)
}

func (s *Seat) PointerMotion(at time.Duration, x, y float64) error {
	s.pointer.local = fixedAt(x, y)
	_, pointers := s.focusPointers()
	var errs error
	for _, ptr := range pointers {
		errs = errors.Join(errs, ptr.SendMotion(wire.Millis(at), s.pointer.local.x, s.pointer.local.y))
	}
	return errs
}

func (s *Seat) RebasePointer(at time.Duration, x, y float64) error {
	if s.pointer.focus == nil || s.pointer.local == fixedAt(x, y) {
		return nil
	}
	return errors.Join(s.PointerMotion(at, x, y), s.PointerFrame())
}

func (s *Seat) PointerButton(at time.Duration, button uint32, pressed bool) error {
	p := &s.pointer
	i := slices.IndexFunc(p.buttons, func(b heldButton) bool { return b.code == button })
	switch {
	case pressed && i >= 0, !pressed && i < 0 && p.focus == nil:
		return nil
	case i >= 0:
		p.buttons = slices.Delete(p.buttons, i, i+1)
	}
	c, pointers := s.focusPointers()
	if c == nil {
		if pressed {
			p.buttons = append(p.buttons, heldButton{code: button})
		}
		return nil
	}
	serial := s.eventSerial(c, pressed)
	state := sw.WlPointerButtonStateReleased
	if pressed {
		p.buttons = append(p.buttons, heldButton{code: button, serial: serial})
		state = sw.WlPointerButtonStatePressed
	}
	var errs error
	for _, ptr := range pointers {
		errs = errors.Join(errs, ptr.SendButton(serial, wire.Millis(at), button, state))
	}
	return errs
}

func (s *Seat) PointerPress(c *server.Client, serial uint32) (uint32, bool) {
	p := &s.pointer
	if serial == 0 || p.focus == nil || p.focus.Client() != c {
		return 0, false
	}
	for _, b := range p.buttons {
		if b.serial == serial {
			return b.code, true
		}
	}
	return 0, false
}

func (s *Seat) PointerHeld(target *surface.Surface, button uint32) (uint32, bool) {
	p := &s.pointer
	if target == nil || p.focus != target {
		return 0, false
	}
	for _, b := range p.buttons {
		if button == 0 || b.code == button {
			return b.code, true
		}
	}
	return 0, false
}

func (s *Seat) PointerAxis(at time.Duration, a Axis) error {
	_, pointers := s.focusPointers()
	var errs error
	for _, ptr := range pointers {
		errs = errors.Join(errs, s.sendAxis(ptr, at, a))
	}
	return errs
}

func (s *Seat) sendAxis(ptr *sw.WlPointer, at time.Duration, a Axis) error {
	v := ptr.Base().Version()
	if v >= 5 {
		if err := ptr.SendAxisSource(a.Source); err != nil {
			return err
		}
	}
	if a.Value == 0 && a.Source == sw.WlPointerAxisSourceFinger {
		if v >= 5 {
			return ptr.SendAxisStop(wire.Millis(at), a.Orientation)
		}
		return nil
	}
	switch {
	case v >= 8 && a.Value120 != 0:
		if err := ptr.SendAxisValue120(a.Orientation, a.Value120); err != nil {
			return err
		}
	case v >= 5 && a.Value120 != 0 && a.Value120%120 == 0:
		if err := ptr.SendAxisDiscrete(a.Orientation, a.Value120/120); err != nil {
			return err
		}
	}
	if v >= 9 {
		if err := ptr.SendAxisRelativeDirection(a.Orientation, sw.WlPointerAxisRelativeDirectionIdentical); err != nil {
			return err
		}
	}
	return ptr.SendAxis(wire.Millis(at), a.Orientation, wire.FixedFromFloat(a.Value))
}

func (s *Seat) PointerFrame() error {
	p := &s.pointer
	if p.focus != nil && !slices.Contains(p.framed, p.focus.Client()) {
		p.framed = append(p.framed, p.focus.Client())
	}
	var errs error
	for _, c := range p.framed {
		r := s.lookup(c)
		if r == nil {
			continue
		}
		for _, ptr := range r.pointers {
			if ptr.Base().Version() >= pointerFrameVersion {
				errs = errors.Join(errs, ptr.SendFrame())
			}
		}
	}
	clear(p.framed)
	p.framed = p.framed[:0]
	return errs
}

func (r seatRequests) GetPointer(obj *sw.WlSeat, ptr *sw.WlPointer) (sw.WlPointerHandler, error) {
	s := r.s
	if !s.everCaps.Has(sw.WlSeatCapabilityPointer) {
		return nil, missing(obj, "pointer")
	}
	res := s.resources(r.c)
	track(s, res, &res.pointers, ptr)
	if p := &s.pointer; p.focus != nil && p.focus.Client() == r.c {
		if err := p.enter(ptr); err != nil {
			return nil, err
		}
		if ptr.Base().Version() >= pointerFrameVersion {
			if err := ptr.SendFrame(); err != nil {
				return nil, err
			}
		}
	}
	return pointerRequests{s, r.c}, nil
}

type cursorSurface struct {
	s *Seat
}

func (cursorSurface) RoleName() string {
	return surface.CursorRole
}

func (c cursorSurface) Committed(sf *surface.Surface) error {
	cur := &c.s.pointer.cursor
	if sc, ok := cur.want.(SurfaceCursor); ok && sc.Surface == sf {
		sc.Hotspot = sc.Hotspot.Sub(sf.Offset())
		cur.want = sc
	}
	return nil
}

type pointerRequests struct {
	s *Seat
	c *server.Client
}

func (s *Seat) PointerEntered(c *server.Client, serial uint32) bool {
	return s.pointer.focus != nil && s.pointer.focus.Client() == c && serial == s.pointer.enterSerial
}

func (r pointerRequests) SetCursor(obj *sw.WlPointer, serial uint32, wl *sw.WlSurface, hx, hy int32) error {
	if !r.s.PointerEntered(r.c, serial) {
		return nil
	}
	return r.s.setCursor(r.c, wl, hx, hy, func(err error) error {
		return obj.ProtocolError(sw.WlPointerErrorRole, "%v", err)
	})
}

func (s *Seat) setCursor(c *server.Client, wl *sw.WlSurface, hx, hy int32, roleError func(error) error) error {
	cur := clientCursor{client: c, want: HiddenCursor{}}
	if wl != nil {
		sf, err := surface.From(wl)
		if err != nil {
			return err
		}
		if !IsCursorSurface(sf) {
			if err := sf.SetRole(cursorSurface{s}); err != nil {
				return roleError(err)
			}
		}
		cur.want = SurfaceCursor{Surface: sf, Hotspot: image.Pt(int(hx), int(hy))}
	}
	s.pointer.cursor = cur
	return s.listener.CursorChanged()
}

func (r pointerRequests) Release(*sw.WlPointer) error {
	return nil
}
