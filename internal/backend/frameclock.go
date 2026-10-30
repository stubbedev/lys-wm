package backend

import "time"

type Latch struct {
	Wait func() time.Duration
	Wake func(after time.Duration) error
}

type FrameClock struct {
	idle      func(func() error)
	ready     func() bool
	frame     func() error
	latch     *Latch
	requested bool
	queued    bool
	waiting   bool
}

func NewFrameClock(idle func(func() error), ready func() bool, frame func() error, latch *Latch) *FrameClock {
	return &FrameClock{idle: idle, ready: ready, frame: frame, latch: latch}
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
	f.idle(f.fire)
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

func LatchDeadline(last, now, interval time.Duration) time.Duration {
	return LatchDeadlineWithin(last, now, interval, interval/2)
}

func LatchDeadlineWithin(last, now, interval, budget time.Duration) time.Duration {
	if last == 0 || last > now {
		return now
	}
	next, _ := NextVblank(last, now, interval)
	return next - budget
}

const (
	renderSamples = 32
	renderMargin  = 1500 * time.Microsecond
)

type RenderBudget struct {
	samples [renderSamples]time.Duration
	n, next int
	held    int
}

func (b *RenderBudget) Sample(took time.Duration) {
	b.samples[b.next] = took
	b.next = (b.next + 1) % renderSamples
	b.n = min(b.n+1, renderSamples)
	b.held = max(b.held-1, 0)
}

func (b *RenderBudget) Missed() {
	b.held = renderSamples
}

func (b *RenderBudget) Budget(interval time.Duration) time.Duration {
	ceiling := interval / 2
	if b.held > 0 || b.n < renderSamples {
		return ceiling
	}
	var slowest time.Duration
	for _, s := range b.samples {
		slowest = max(slowest, s)
	}
	return min(slowest+renderMargin, ceiling)
}
