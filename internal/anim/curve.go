// Package anim is the animation engine: easing curves, springs, tweens that
// sample them at a monotonic time, and the per-event table the config builds.
// It imports only the standard library.
package anim

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type Curve interface {
	At(elapsed time.Duration) float64
	Duration() time.Duration
}

type instant struct{}

func (instant) At(time.Duration) float64 { return 1 }

func (instant) Duration() time.Duration { return 0 }

var Instant Curve = instant{}

type Shape interface {
	Timed(d time.Duration, explicit bool) (Curve, error)
}

type Bezier struct {
	x1, y1, x2, y2 float64
}

func NewBezier(x1, y1, x2, y2 float64) (Bezier, error) {
	for _, v := range [4]float64{x1, y1, x2, y2} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return Bezier{}, fmt.Errorf("anim: bézier control point %v is not finite", v)
		}
	}
	if x1 < 0 || x1 > 1 || x2 < 0 || x2 > 1 {
		return Bezier{}, fmt.Errorf("anim: bézier x coordinates %v and %v must lie in [0, 1]", x1, x2)
	}
	return Bezier{x1, y1, x2, y2}, nil
}

func cubic(a, b, t float64) float64 {
	u := 1 - t
	return 3*u*u*t*a + 3*u*t*t*b + t*t*t
}

func cubicSlope(a, b, t float64) float64 {
	u := 1 - t
	return 3*u*u*a + 6*u*t*(b-a) + 3*t*t*(1-b)
}

const solveEpsilon = 1e-9

func (b Bezier) param(x float64) float64 {
	t := x
	for range 8 {
		d := cubic(b.x1, b.x2, t) - x
		if math.Abs(d) < solveEpsilon {
			return t
		}
		s := cubicSlope(b.x1, b.x2, t)
		if math.Abs(s) < 1e-6 {
			break
		}
		t -= d / s
		if t < 0 || t > 1 {
			break
		}
	}
	lo, hi := 0.0, 1.0
	t = x
	for range 64 {
		v := cubic(b.x1, b.x2, t)
		if math.Abs(v-x) < solveEpsilon {
			return t
		}
		if v < x {
			lo = t
		} else {
			hi = t
		}
		t = (lo + hi) / 2
	}
	return t
}

func (b Bezier) Ease(x float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	return cubic(b.y1, b.y2, b.param(x))
}

func (b Bezier) Timed(d time.Duration, _ bool) (Curve, error) {
	if d <= 0 {
		return nil, fmt.Errorf("anim: a bézier curve needs a positive duration, not %v", d)
	}
	return eased{b, d}, nil
}

type eased struct {
	b Bezier
	d time.Duration
}

func (e eased) At(elapsed time.Duration) float64 {
	return e.b.Ease(float64(elapsed) / float64(e.d))
}

func (e eased) Duration() time.Duration { return e.d }

const (
	SettleTolerance = 1e-4
	maxSettle       = 30 * time.Second
)

type Spring struct {
	omega, zeta float64
	settle      time.Duration
}

const criticalBand = 1e-6

func NewSpring(stiffness, damping, mass float64) (Spring, error) {
	for _, v := range [3]float64{stiffness, damping, mass} {
		if !(v > 0) || math.IsInf(v, 0) {
			return Spring{}, fmt.Errorf("anim: spring stiffness %v, damping %v and mass %v must all be positive and finite", stiffness, damping, mass)
		}
	}
	s := Spring{omega: math.Sqrt(stiffness / mass), zeta: damping / (2 * math.Sqrt(stiffness*mass))}
	if math.Abs(s.zeta-1) < criticalBand {
		s.zeta = 1
	}
	s.settle = s.settleTime()
	if s.settle >= maxSettle {
		return Spring{}, fmt.Errorf("anim: a spring of stiffness %v, damping %v and mass %v takes longer than %v to settle", stiffness, damping, mass, maxSettle)
	}
	return s, nil
}

func (s Spring) Timed(_ time.Duration, explicit bool) (Curve, error) {
	if explicit {
		return nil, errors.New("anim: a spring settles in its own time and takes no duration")
	}
	return s, nil
}

func (s Spring) offset(t float64) float64 {
	w, z := s.omega, s.zeta
	switch {
	case z < 1:
		wd := w * math.Sqrt(1-z*z)
		return -math.Exp(-z*w*t) * (math.Cos(wd*t) + z*w/wd*math.Sin(wd*t))
	case z == 1:
		return -(1 + w*t) * math.Exp(-w*t)
	}
	r := w * math.Sqrt(z*z-1)
	r1, r2 := -z*w+r, -z*w-r
	return (r2*math.Exp(r1*t) - r1*math.Exp(r2*t)) / (r1 - r2)
}

func (s Spring) envelope(t float64) float64 {
	w, z := s.omega, s.zeta
	switch {
	case z < 1:
		wd := w * math.Sqrt(1-z*z)
		return math.Exp(-z*w*t) * math.Hypot(1, z*w/wd)
	case z == 1:
		return (1 + w*t) * math.Exp(-w*t)
	}
	r := w * math.Sqrt(z*z-1)
	r1, r2 := -z*w+r, -z*w-r
	return (math.Abs(r2) + math.Abs(r1)) / (r1 - r2) * math.Exp(r1*t)
}

func (s Spring) settleTime() time.Duration {
	lo, hi := time.Duration(0), maxSettle
	for hi-lo > time.Millisecond {
		mid := (lo + hi) / 2
		if s.envelope(mid.Seconds()) < SettleTolerance {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

func (s Spring) At(elapsed time.Duration) float64 {
	if elapsed >= s.settle {
		return 1
	}
	if elapsed <= 0 {
		return 0
	}
	return 1 + s.offset(elapsed.Seconds())
}

func (s Spring) Duration() time.Duration { return s.settle }
