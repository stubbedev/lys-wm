package binds

import (
	"math"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type GestureDirection uint8

const (
	NoDirection GestureDirection = iota
	SwipeLeft
	SwipeRight
	SwipeUp
	SwipeDown
	SwipeHorizontal
	SwipeVertical
	PinchIn
	PinchOut
	PinchBoth
	SwipeAny
)

type Drive uint8

const (
	NoDrive Drive = iota
	DriveWorkspaces
	DriveZoom
	DriveOverview
	DriveView
	DriveMove
	DriveResize
)

type Edge uint8

const (
	NoEdge Edge = iota
	EdgeTop
	EdgeBottom
	EdgeLeft
	EdgeRight
)

func (e Edge) Inward() GestureDirection {
	switch e {
	case EdgeTop:
		return SwipeDown
	case EdgeBottom:
		return SwipeUp
	case EdgeLeft:
		return SwipeRight
	case EdgeRight:
		return SwipeLeft
	}
	return NoDirection
}

type GestureBind struct {
	Kind      backend.Gesture
	Fingers   uint32
	Direction GestureDirection
	Drive     Drive
	Action    wm.Action
	Modifiers ModifierSet
	Mods      uint32
	Edge      Edge
}

func ResolveGestures(all []GestureBind, r Resolver) ([]GestureBind, error) {
	out := slices.Clone(all)
	for i := range out {
		mask, err := out[i].Modifiers.Mask(r)
		if err != nil {
			return nil, err
		}
		out[i].Mods = mask
	}
	return out, nil
}

func HeldFor(dst, all []GestureBind, held uint32) []GestureBind {
	for _, b := range all {
		if b.Mods != 0 && b.Mods == held {
			dst = append(dst, b)
		}
	}
	for _, b := range all {
		if b.Mods == 0 {
			dst = append(dst, b)
		}
	}
	return dst
}

func (d GestureDirection) covers(actual GestureDirection) bool {
	switch d {
	case SwipeHorizontal:
		return actual == SwipeLeft || actual == SwipeRight || actual == SwipeHorizontal
	case SwipeVertical:
		return actual == SwipeUp || actual == SwipeDown || actual == SwipeVertical
	case PinchBoth:
		return actual == PinchIn || actual == PinchOut || actual == PinchBoth
	case SwipeAny:
		return actual >= SwipeLeft && actual <= SwipeVertical || actual == SwipeAny
	}
	return d == actual
}

func (b GestureBind) Overlaps(o GestureBind) bool {
	return b.Kind == o.Kind && b.Fingers == o.Fingers && b.Modifiers == o.Modifiers && b.Edge == o.Edge && (b.Direction.covers(o.Direction) || o.Direction.covers(b.Direction))
}

func (d GestureDirection) Horizontal() bool {
	return d == SwipeLeft || d == SwipeRight || d == SwipeHorizontal
}

func (b GestureBind) along(dx, dy float64) float64 {
	switch b.Direction {
	case SwipeLeft:
		return -dx
	case SwipeUp:
		return -dy
	case SwipeVertical, SwipeDown:
		return dy
	}
	return dx
}

const (
	swipeLock      = 12
	actionTravel   = 100
	pinchThreshold = 0.15
)

type recognition uint8

const (
	idle recognition = iota
	undecided
	claimed
	passed
)

type Step struct {
	Forward      bool
	CancelClient bool
	Claimed      bool
	Passed       bool
	Bind         GestureBind
	Fire         bool
	Travel       float64
}

type Recognizer struct {
	binds     []GestureBind
	kind      backend.Gesture
	fingers   uint32
	state     recognition
	swallowed bool
	dx, dy    float64
	travel    float64
	bind      GestureBind
	fired     bool
}

func (r *Recognizer) Begin(binds []GestureBind, kind backend.Gesture, fingers uint32, allowed bool) Step {
	*r = Recognizer{binds: binds, kind: kind, fingers: fingers, state: passed}
	if !allowed {
		return Step{Forward: true}
	}
	for _, b := range binds {
		if b.Kind != kind || b.Fingers != fingers {
			continue
		}
		if kind == backend.Hold || b.Drive == DriveZoom {
			r.state, r.bind = claimed, b
			return Step{Claimed: true, Bind: b}
		}
		r.state = undecided
		break
	}
	return Step{Forward: true}
}

func (r *Recognizer) Drives(d Drive) bool {
	if r.state != undecided {
		return false
	}
	for _, b := range r.binds {
		if b.Kind == r.kind && b.Fingers == r.fingers && b.Drive == d {
			return true
		}
	}
	return false
}

func (r *Recognizer) Swallow() {
	r.swallowed = true
}

func (r *Recognizer) forward() Step {
	return Step{Forward: !r.swallowed}
}

func (r *Recognizer) claim(direction GestureDirection) (GestureBind, bool) {
	for _, b := range r.binds {
		if b.Kind == r.kind && b.Fingers == r.fingers && b.Direction.covers(direction) {
			return b, true
		}
	}
	return GestureBind{}, false
}

func swipeDirection(dx, dy float64) GestureDirection {
	switch {
	case dx*dx >= dy*dy && dx < 0:
		return SwipeLeft
	case dx*dx >= dy*dy:
		return SwipeRight
	case dy < 0:
		return SwipeUp
	}
	return SwipeDown
}

func (r *Recognizer) Swipe(dx, dy float64) Step {
	switch r.state {
	case idle:
		return Step{Forward: true}
	case passed:
		return r.forward()
	case claimed:
		return r.travelled(dx, dy)
	}
	r.dx, r.dy = r.dx+dx, r.dy+dy
	if float64(r.dx*r.dx)+float64(r.dy*r.dy) < swipeLock*swipeLock {
		return r.forward()
	}
	b, ok := r.claim(swipeDirection(r.dx, r.dy))
	if !ok {
		r.state = passed
		s := r.forward()
		s.Passed = true
		return s
	}
	r.state, r.bind = claimed, b
	s := r.travelled(r.dx, r.dy)
	s.Claimed, s.CancelClient = true, !r.swallowed
	return s
}

func (r *Recognizer) travelled(dx, dy float64) Step {
	s := Step{Bind: r.bind, Travel: r.bind.along(dx, dy)}
	if r.bind.Action == nil || r.fired {
		return s
	}
	r.travel += s.Travel
	if math.Abs(r.travel) >= actionTravel {
		r.fired, s.Fire = true, true
	}
	return s
}

func (r *Recognizer) Pinch(scale float64) Step {
	switch r.state {
	case idle:
		return Step{Forward: true}
	case claimed:
		return Step{Bind: r.bind}
	case passed:
		return r.forward()
	}
	var direction GestureDirection
	switch {
	case scale <= 1-pinchThreshold:
		direction = PinchIn
	case scale >= 1+pinchThreshold:
		direction = PinchOut
	default:
		return r.forward()
	}
	b, ok := r.claim(direction)
	if !ok {
		r.state = passed
		s := r.forward()
		s.Passed = true
		return s
	}
	r.state, r.bind, r.fired = claimed, b, true
	return Step{Claimed: true, CancelClient: !r.swallowed, Bind: b, Fire: b.Action != nil}
}

func (r *Recognizer) End(cancelled bool) Step {
	s := r.forward()
	if r.state == claimed {
		s = Step{Bind: r.bind, Fire: r.kind == backend.Hold && !cancelled}
	}
	if r.state == idle {
		s = Step{Forward: true}
	}
	*r = Recognizer{}
	return s
}
