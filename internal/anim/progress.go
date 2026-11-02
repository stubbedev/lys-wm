package anim

import (
	"math"
	"time"
)

const (
	CommitAt = 0.5

	progressUnits   = 1e4
	velocitySamples = 32
	velocityWindow  = 100 * time.Millisecond
	restedAfter     = 50 * time.Millisecond
	projectionTime  = 0.15
)

type progressSample struct {
	at    time.Duration
	value float64
}

type Progress struct {
	tween     Tween[float64]
	spring    follower
	springing bool
	tracking  bool
	samples   [velocitySamples]progressSample
	sampled   int
}

func NewProgress(at float64) Progress {
	return Progress{tween: NewTween(at, Lerp)}
}

func (p *Progress) state(now time.Duration) (x, v float64) {
	if !p.springing {
		return p.tween.At(now), 0
	}
	x, v = p.spring.at(now)
	return x / progressUnits, v / progressUnits
}

func (p *Progress) At(now time.Duration) float64 {
	x, _ := p.state(now)
	return x
}

func (p *Progress) Tracking() bool {
	return p.tracking
}

func (p *Progress) Done(now time.Duration) bool {
	switch {
	case p.tracking:
		return false
	case p.springing:
		return now-p.spring.start >= p.spring.settle
	}
	return p.tween.Done(now)
}

func (p *Progress) Target() float64 {
	if p.springing {
		return p.spring.target / progressUnits
	}
	return p.tween.Target()
}

func (p *Progress) jump(to float64) {
	p.springing = false
	p.tween.Jump(to)
}

func (p *Progress) Grab(now time.Duration) {
	p.jump(p.At(now))
	p.tracking = true
	p.sampled = 0
}

func (p *Progress) Track(to float64, at time.Duration) {
	p.jump(to)
	p.tracking = true
	p.samples[p.sampled%velocitySamples] = progressSample{at: at, value: to}
	p.sampled++
}

func (p *Progress) Shift(by float64) {
	p.jump(p.tween.Target() + by)
	for i := range min(p.sampled, velocitySamples) {
		p.samples[i].value += by
	}
}

func (p *Progress) Velocity(at time.Duration) float64 {
	if p.sampled == 0 {
		return 0
	}
	newest := p.samples[(p.sampled-1)%velocitySamples]
	if at-newest.at > restedAfter {
		return 0
	}
	oldest := newest
	for i := range min(p.sampled, velocitySamples) {
		s := p.samples[(p.sampled-1-i)%velocitySamples]
		if newest.at-s.at > velocityWindow {
			break
		}
		oldest = s
	}
	dt := (newest.at - oldest.at).Seconds()
	if dt <= 0 {
		return 0
	}
	return (newest.value - oldest.value) / dt
}

func (p *Progress) Projected(at time.Duration) float64 {
	return p.tween.Target() + float64(p.Velocity(at)*projectionTime)
}

func (p *Progress) Release(target float64, at, now time.Duration, c Curve) {
	x, v := p.tween.Target(), p.Velocity(at)
	p.tracking, p.sampled = false, 0
	if c.Duration() == 0 {
		p.jump(target)
		return
	}
	s, ok := c.(Spring)
	if !ok {
		s = Smooth
	}
	p.springTo(s, x, v, target, now)
}

func (p *Progress) Set(target float64, now time.Duration, c Curve) {
	if p.tracking {
		p.Release(target, now, now, c)
		return
	}
	x, v := p.state(now)
	if s, ok := c.(Spring); ok {
		p.springTo(s, x, v, target, now)
		return
	}
	p.springing = false
	p.tween.Jump(x)
	p.tween.Set(target, now, c)
}

func (p *Progress) springTo(s Spring, x, v, target float64, now time.Duration) {
	p.spring = newFollower(s, float64(x*progressUnits), float64(v*progressUnits), float64(target*progressUnits), now)
	p.springing = true
	p.tween.Jump(target)
}

func RubberBand(shown, delta, limit float64) float64 {
	gain := 1 - math.Abs(shown)/limit
	return min(max(shown+float64(delta*float64(gain*gain)), -limit), limit)
}
