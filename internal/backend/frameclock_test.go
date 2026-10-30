package backend

import (
	"testing"
	"time"
)

type idleQueue []func() error

func (q *idleQueue) add(f func() error) { *q = append(*q, f) }

func (q *idleQueue) run(t *testing.T) {
	t.Helper()
	for len(*q) > 0 {
		f := (*q)[0]
		*q = (*q)[1:]
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAFrameClockFiresOnceForManyRequests(t *testing.T) {
	var q idleQueue
	frames := 0
	clock := NewFrameClock(q.add, func() bool { return true }, func() error { frames++; return nil }, nil)
	must(t, clock.Request())
	must(t, clock.Request())
	must(t, clock.Kick())
	if len(q) != 1 {
		t.Fatalf("%d idle callbacks queued for three requests, want 1", len(q))
	}
	q.run(t)
	if frames != 1 || clock.Requested() {
		t.Fatalf("%d frames, requested %t after the idle callback", frames, clock.Requested())
	}
	q.run(t)
	if frames != 1 {
		t.Fatalf("%d frames without a new request", frames)
	}
}

func TestAFrameClockWaitsUntilReady(t *testing.T) {
	var q idleQueue
	ready, frames := false, 0
	clock := NewFrameClock(q.add, func() bool { return ready }, func() error { frames++; return nil }, nil)
	must(t, clock.Request())
	if len(q) != 0 {
		t.Fatal("a request queued a frame while the output was not ready")
	}
	ready = true
	must(t, clock.Kick())
	ready = false
	q.run(t)
	if frames != 0 || !clock.Requested() {
		t.Fatalf("%d frames once readiness went away before the idle callback; requested %t", frames, clock.Requested())
	}
	ready = true
	must(t, clock.Kick())
	q.run(t)
	if frames != 1 {
		t.Fatalf("%d frames once ready again, want 1", frames)
	}
}

func TestACancelledFrameRequestNeverFires(t *testing.T) {
	var q idleQueue
	frames := 0
	clock := NewFrameClock(q.add, func() bool { return true }, func() error { frames++; return nil }, nil)
	must(t, clock.Request())
	clock.Cancel()
	q.run(t)
	if frames != 0 || clock.Requested() {
		t.Fatalf("%d frames after a cancel, requested %t", frames, clock.Requested())
	}
	must(t, clock.Request())
	q.run(t)
	if frames != 1 {
		t.Fatalf("%d frames after a request following a cancel, want 1", frames)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestAFrameClockWaitsForItsLatchDeadline(t *testing.T) {
	var q idleQueue
	frames := 0
	var wakes []time.Duration
	ahead := true
	clock := NewFrameClock(q.add, func() bool { return true }, func() error { frames++; return nil }, &Latch{
		Wait: func() time.Duration {
			if ahead {
				return 5 * time.Millisecond
			}
			return -time.Millisecond
		},
		Wake: func(after time.Duration) error {
			wakes = append(wakes, after)
			return nil
		},
	})
	must(t, clock.Request())
	must(t, clock.Request())
	if len(q) != 0 || len(wakes) != 1 || wakes[0] <= 0 || wakes[0] > 5*time.Millisecond {
		t.Fatalf("before the deadline: %d idle callbacks, wakes %v; want none and one wake within 5ms", len(q), wakes)
	}
	ahead = false
	must(t, clock.Woke())
	q.run(t)
	if frames != 1 || len(wakes) != 1 {
		t.Fatalf("after the wake: %d frames and wakes %v, want one frame and no new wake", frames, wakes)
	}
	must(t, clock.Request())
	q.run(t)
	if frames != 2 || len(wakes) != 1 {
		t.Fatalf("a request past its deadline: %d frames and wakes %v, want it drawn at once", frames, wakes)
	}
}

func TestTheLatchDeadlineIsHalfwayToTheNextVblank(t *testing.T) {
	const ms = time.Millisecond
	for _, tc := range []struct {
		last, now, want time.Duration
	}{
		{last: 100 * ms, now: 101 * ms, want: 108 * ms},
		{last: 100 * ms, now: 109 * ms, want: 108 * ms},
		{last: 100 * ms, now: 125 * ms, want: 124 * ms},
		{last: 0, now: 125 * ms, want: 125 * ms},
	} {
		if got := LatchDeadline(tc.last, tc.now, 16*ms); got != tc.want {
			t.Errorf("last %v, now %v: deadline %v, want %v", tc.last, tc.now, got, tc.want)
		}
	}
}

func TestRenderBudget(t *testing.T) {
	const interval = 16 * time.Millisecond
	var b RenderBudget
	if got := b.Budget(interval); got != interval/2 {
		t.Fatalf("an empty budget is %v, want half the refresh", got)
	}
	for i := range 32 {
		b.Sample(time.Duration(i%4) * time.Millisecond)
	}
	if got := b.Budget(interval); got != 3*time.Millisecond+1500*time.Microsecond {
		t.Fatalf("the budget over renders up to 3 ms is %v, want the slowest plus the margin", got)
	}
	for range 32 {
		b.Sample(20 * time.Millisecond)
	}
	if got := b.Budget(interval); got != interval/2 {
		t.Fatalf("renders slower than half the refresh gave a budget of %v, want it capped at half", got)
	}
	for range 32 {
		b.Sample(time.Millisecond)
	}
	b.Missed()
	if got := b.Budget(interval); got != interval/2 {
		t.Fatalf("right after a miss the budget is %v, want half the refresh", got)
	}
	for range 32 {
		b.Sample(time.Millisecond)
	}
	if got := b.Budget(interval); got != 2500*time.Microsecond {
		t.Fatalf("32 frames after a miss the budget is %v, want it measured again", got)
	}
}
