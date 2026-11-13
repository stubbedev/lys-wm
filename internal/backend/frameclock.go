package backend

import (
	"slices"
	"time"
)

type Latch struct {
	Wait func() time.Duration
	Wake func(after time.Duration) error
}

type FrameClock struct {
	idle      func(func() error)
	ready     func() bool
	frame     func() error
	onIdle    func() error
	latch     *Latch
	requested bool
	queued    bool
	waiting   bool
}

func NewFrameClock(idle func(func() error), ready func() bool, frame func() error, latch *Latch) *FrameClock {
	f := &FrameClock{idle: idle, ready: ready, frame: frame, latch: latch}
	f.onIdle = f.fire
	return f
}

func (f *FrameClock) Request() error {
	f.requested = true
	return f.Kick()
}

func (f *FrameClock) Cancel() {
	f.requested = false
}

func (f *FrameClock) Requested() bool {
	return f.requested
}

func (f *FrameClock) Queued() bool {
	return f.requested && f.queued
}

func (f *FrameClock) Waiting() bool {
	return f.requested && f.waiting
}

func (f *FrameClock) Kick() error {
	if !f.requested || f.queued || f.waiting || !f.ready() {
		return nil
	}
	if f.latch != nil {
		if wait := f.latch.Wait(); wait > 0 {
			f.waiting = true
			return f.latch.Wake(wait)
		}
	}
	f.queued = true
	f.idle(f.onIdle)
	return nil
}

func (f *FrameClock) Woke() error {
	f.waiting = false
	return f.Kick()
}

func (f *FrameClock) fire() error {
	f.queued = false
	if !f.requested || !f.ready() {
		return nil
	}
	f.requested = false
	return f.frame()
}

const (
	renderSamples = 32
	renderMargin  = 3 * time.Millisecond
	PlaneLead     = 2 * time.Millisecond
)

func PlaneLatch(last, now, interval time.Duration) time.Duration {
	if last == 0 || last > now {
		return now
	}
	next, _ := NextVblank(last, now, interval)
	return next - PlaneLead
}

type RenderBudget struct {
	samples  [renderSamples]time.Duration
	n, next  int
	deferred bool
	painted  int
	perPixel float64
	expected int
}

const leastPixelsForARatio = 1 << 16

type WorkHinter interface {
	ExpectFrameWork(pixels int)
}

func (b *RenderBudget) Painted(pixels int) {
	b.painted = pixels
}

func (b *RenderBudget) Expect(pixels int) {
	b.expected = max(b.expected, pixels)
}

func (b *RenderBudget) Started() {
	b.expected = 0
}

func (b *RenderBudget) expectedBudget() time.Duration {
	return time.Duration(b.perPixel*float64(b.expected)) + renderMargin
}

func (b *RenderBudget) Latch(p Pacing, now time.Duration) time.Duration {
	if !p.Anchored(now) {
		return now
	}
	return b.latch(p, p.Aim(now))
}

func (b *RenderBudget) latch(p Pacing, at time.Duration) time.Duration {
	return at - min(b.Budget(p.Paced), p.Refresh)
}

func (b *RenderBudget) Deadline(p Pacing, now time.Duration) time.Duration {
	if !p.Anchored(now) {
		return now
	}
	at := p.Aim(now)
	deadline := b.latch(p, at)
	if now < deadline {
		return deadline
	}
	if !b.deferred && at-now < b.leastToStart() {
		b.deferred = true
		return deadline + p.Refresh
	}
	b.deferred = false
	return deadline
}

func (b *RenderBudget) leastToStart() time.Duration {
	sorted := b.samples
	seen := sorted[:b.n]
	slices.Sort(seen)
	if b.n == 0 {
		return renderMargin
	}
	return max(renderMargin, seen[b.n/2])
}

func (b *RenderBudget) Sample(took time.Duration) {
	if b.painted >= leastPixelsForARatio {
		b.perPixel = float64(took) / float64(b.painted)
	}
	b.painted = 0
	b.samples[b.next] = took
	b.next = (b.next + 1) % renderSamples
	b.n = min(b.n+1, renderSamples)
}

func (b *RenderBudget) Missed(interval time.Duration) {
	b.Sample(b.Budget(interval))
}

func (b *RenderBudget) Overruns(interval time.Duration) bool {
	return b.n > 0 && b.leastToStart() >= interval
}

func (b *RenderBudget) Budget(interval time.Duration) time.Duration {
	var slowest, next time.Duration
	for _, s := range b.samples {
		if s > slowest {
			slowest, next = s, slowest
		} else if s > next {
			next = s
		}
	}
	if slowest+renderMargin >= interval {
		slowest = next
	}
	budget := slowest + renderMargin
	if b.n < renderSamples {
		budget = max(budget, interval/2)
	}
	if b.expected > 0 && b.perPixel > 0 {
		budget = max(budget, b.expectedBudget())
	}
	return min(budget, interval)
}
