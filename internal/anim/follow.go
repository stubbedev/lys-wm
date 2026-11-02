package anim

import (
	"image"
	"math"
	"time"

	"github.com/stubbedev/lys-lib/motion"
	"github.com/stubbedev/lys-lib/third_party/gomath"
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

func (f follower) envelope(t float64) float64 {
	w, z := f.s.omega, f.s.zeta
	switch {
	case z < 1:
		wd := w * math.Sqrt(1-float64(z*z))
		return gomath.Exp(-z*w*t) * (math.Abs(f.u0) + math.Abs((f.v0+float64(z*w*f.u0))/wd))
	case z == 1:
		return (math.Abs(f.u0) + float64(math.Abs(f.v0+float64(w*f.u0))*t)) * gomath.Exp(-w*t)
	}
	r1, r2 := motion.OverdampedRoots(w, z)
	c1 := (f.v0 - float64(r2*f.u0)) / (r1 - r2)
	return (math.Abs(c1) + math.Abs(f.u0-c1)) * gomath.Exp(r1*t)
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
	u, v := motion.SpringStep(f.s.omega, f.s.zeta, f.u0, f.v0, t.Seconds())
	return f.target + u, v
}

type Motion struct {
	tween  Tween[float64]
	spring *follower
}

func NewMotion(at float64) Motion {
	return Motion{tween: NewTween(at, Lerp)}
}

func (m Motion) state(now time.Duration) (x, v float64) {
	if m.spring == nil {
		return m.tween.At(now), 0
	}
	return m.spring.at(now)
}

func (m Motion) At(now time.Duration) float64 {
	x, _ := m.state(now)
	return x
}

func (m Motion) Done(now time.Duration) bool {
	if m.spring == nil {
		return m.tween.Done(now)
	}
	return now-m.spring.start >= m.spring.settle
}

func (m *Motion) Set(to float64, now time.Duration, c Curve) {
	at, v := m.state(now)
	m.tween.Jump(at)
	m.spring = nil
	if s, ok := c.(Spring); ok {
		f := newFollower(s, at, v, to, now)
		m.spring = &f
		return
	}
	m.tween.Set(to, now, c)
}

func (m *Motion) Jump(to float64) {
	m.spring = nil
	m.tween.Jump(to)
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
