package anim

import (
	"image"
	"math"
	"time"
)

const FollowTolerance = 0.25

type follower struct {
	s      Spring
	start  time.Duration
	u0, v0 float64
	target float64
	settle time.Duration
}

func newFollower(s Spring, at, velocity, target float64, now time.Duration) follower {
	f := follower{s: s, start: now, u0: at - target, v0: velocity, target: target}
	f.settle = f.settleTime()
	return f
}

func (f follower) roots() (r1, r2 float64) {
	w, z := f.s.omega, f.s.zeta
	r := w * math.Sqrt(z*z-1)
	return -z*w + r, -z*w - r
}

func (f follower) offset(t float64) (u, v float64) {
	w, z := f.s.omega, f.s.zeta
	switch {
	case z < 1:
		wd := w * math.Sqrt(1-z*z)
		a, b := f.u0, (f.v0+z*w*f.u0)/wd
		e := math.Exp(-z * w * t)
		c, s := math.Cos(wd*t), math.Sin(wd*t)
		u = e * (a*c + b*s)
		v = e * ((b*wd-z*w*a)*c - (a*wd+z*w*b)*s)
		return u, v
	case z == 1:
		a, b := f.u0, f.v0+w*f.u0
		e := math.Exp(-w * t)
		return (a + b*t) * e, (b - w*(a+b*t)) * e
	}
	r1, r2 := f.roots()
	c1 := (f.v0 - r2*f.u0) / (r1 - r2)
	c2 := f.u0 - c1
	e1, e2 := math.Exp(r1*t), math.Exp(r2*t)
	return c1*e1 + c2*e2, r1*c1*e1 + r2*c2*e2
}

func (f follower) envelope(t float64) float64 {
	w, z := f.s.omega, f.s.zeta
	switch {
	case z < 1:
		wd := w * math.Sqrt(1-z*z)
		return math.Exp(-z*w*t) * (math.Abs(f.u0) + math.Abs((f.v0+z*w*f.u0)/wd))
	case z == 1:
		return (math.Abs(f.u0) + math.Abs(f.v0+w*f.u0)*t) * math.Exp(-w*t)
	}
	r1, r2 := f.roots()
	c1 := (f.v0 - r2*f.u0) / (r1 - r2)
	return (math.Abs(c1) + math.Abs(f.u0-c1)) * math.Exp(r1*t)
}

func (f follower) settleTime() time.Duration {
	if f.envelope(0) < FollowTolerance {
		return 0
	}
	lo, hi := time.Duration(0), maxSettle
	for hi-lo > time.Millisecond {
		mid := (lo + hi) / 2
		if f.envelope(mid.Seconds()) < FollowTolerance {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

func (f follower) at(now time.Duration) (x, v float64) {
	t := now - f.start
	if t >= f.settle {
		return f.target, 0
	}
	if t <= 0 {
		return f.target + f.u0, f.v0
	}
	u, v := f.offset(t.Seconds())
	return f.target + u, v
}

type RectMotion struct {
	tween   Tween[image.Rectangle]
	springs *[4]follower
}

func NewRectMotion(at image.Rectangle) RectMotion {
	return RectMotion{tween: NewTween(at, LerpRect)}
}

func corners(r image.Rectangle) [4]float64 {
	return [4]float64{float64(r.Min.X), float64(r.Min.Y), float64(r.Max.X), float64(r.Max.Y)}
}

func (m RectMotion) state(now time.Duration) (pos, vel [4]float64) {
	if m.springs == nil {
		return corners(m.tween.At(now)), vel
	}
	for i, f := range m.springs {
		pos[i], vel[i] = f.at(now)
	}
	return pos, vel
}

func (m RectMotion) At(now time.Duration) image.Rectangle {
	if m.springs == nil {
		return m.tween.At(now)
	}
	p, _ := m.state(now)
	x0, y0 := int(math.Round(p[0])), int(math.Round(p[1]))
	return image.Rect(x0, y0, max(x0, int(math.Round(p[2]))), max(y0, int(math.Round(p[3]))))
}

func (m RectMotion) Velocity(now time.Duration) [4]float64 {
	_, v := m.state(now)
	return v
}

func (m RectMotion) Done(now time.Duration) bool {
	if m.springs == nil {
		return m.tween.Done(now)
	}
	for _, f := range m.springs {
		if now-f.start < f.settle {
			return false
		}
	}
	return true
}

func (m RectMotion) Target() image.Rectangle {
	return m.tween.Target()
}

func (m RectMotion) Source() image.Rectangle {
	return m.tween.Source()
}

func (m *RectMotion) Set(to image.Rectangle, now time.Duration, c Curve) {
	s, ok := c.(Spring)
	if !ok {
		from := m.At(now)
		m.springs = nil
		m.tween.Jump(from)
		m.tween.Set(to, now, c)
		return
	}
	pos, vel := m.state(now)
	goal := corners(to)
	var springs [4]follower
	for i := range springs {
		springs[i] = newFollower(s, pos[i], vel[i], goal[i], now)
	}
	m.tween.Jump(m.At(now))
	m.tween.to = to
	m.springs = &springs
}

func (m *RectMotion) Jump(to image.Rectangle) {
	m.springs = nil
	m.tween.Jump(to)
}
