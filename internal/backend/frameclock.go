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
)

type RenderBudget struct {
	samples  [renderSamples]time.Duration
	n, next  int
	deferred bool
}

func (b *RenderBudget) Latch(last, now, interval time.Duration) time.Duration {
	if last == 0 || last > now {
		return now
	}
	next, _ := NextVblank(last, now, interval)
	return next - b.Budget(interval)
}

func (b *RenderBudget) Deadline(last, now, interval time.Duration) time.Duration {
	deadline := b.Latch(last, now, interval)
	if now < deadline || last == 0 || last > now {
		return deadline
	}
	next, _ := NextVblank(last, now, interval)
	if !b.deferred && next-now < b.leastToStart() {
		b.deferred = true
		return deadline + interval
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
	b.samples[b.next] = took
	b.next = (b.next + 1) % renderSamples
	b.n = min(b.n+1, renderSamples)
}

func (b *RenderBudget) Missed(interval time.Duration) {
	b.Sample(b.Budget(interval))
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
	return min(budget, interval)
}
