package backend

import (
	"slices"
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

func TestAFrameClockCycleDoesNotAllocate(t *testing.T) {
	var queued func() error
	clock := NewFrameClock(func(f func() error) { queued = f }, func() bool { return true }, func() error { return nil }, nil)
	cycle := func() {
		must(t, clock.Request())
		must(t, queued())
	}
	if n := testing.AllocsPerRun(100, cycle); n != 0 {
		t.Fatalf("a frame clock cycle allocated %v times", n)
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
	if got := b.Budget(interval); got != 3*time.Millisecond+renderMargin {
		t.Fatalf("the budget over renders up to 3 ms is %v, want the slowest plus the margin", got)
	}
	for range 32 {
		b.Sample(10 * time.Millisecond)
	}
	if got := b.Budget(interval); got != 10*time.Millisecond+renderMargin {
		t.Fatalf("renders of 10 ms, over half the 16 ms refresh, gave a budget of %v, want the slowest plus the margin so they still make it", got)
	}
	for range 32 {
		b.Sample(20 * time.Millisecond)
	}
	if got := b.Budget(interval); got != interval {
		t.Fatalf("renders slower than the refresh gave a budget of %v, want the whole refresh", got)
	}
}

const (
	paceInterval = 16 * time.Millisecond
	paceMargin   = renderMargin
)

func paced(b *RenderBudget, costs ...time.Duration) (late int, latency []time.Duration) {
	for _, cost := range costs {
		ahead := b.Budget(paceInterval)
		if cost > ahead {
			late++
			b.Missed(paceInterval)
		}
		b.Sample(cost)
		latency = append(latency, ahead)
	}
	return late, latency
}

func repeat(n int, d time.Duration) []time.Duration {
	return slices.Repeat([]time.Duration{d}, n)
}

func TestOneSlowFrameDoesNotCostAFullRefreshOfLatency(t *testing.T) {
	var b RenderBudget
	const steady, slow = 2 * time.Millisecond, 8 * time.Millisecond
	paced(&b, repeat(40, steady)...)
	late, latency := paced(&b, append([]time.Duration{slow}, repeat(40, steady)...)...)
	if late != 1 {
		t.Fatalf("%d frames late around one slow one", late)
	}
	if latency[0] != steady+paceMargin {
		t.Fatalf("the slow frame started %v ahead", latency[0])
	}
	for i, got := range latency[1:32] {
		if got != slow+paceMargin {
			t.Fatalf("%d frames after the slow one the budget is %v, want the slow frame's cost plus the margin", i+1, got)
		}
	}
	if got := latency[len(latency)-1]; got != steady+paceMargin {
		t.Fatalf("once the slow frame left the window the budget is %v", got)
	}
}

func TestSlowFramesMoreOftenThanTheWindowStayCoveredBelowARefresh(t *testing.T) {
	var b RenderBudget
	paced(&b, repeat(40, 2*time.Millisecond)...)
	var costs []time.Duration
	for range 20 {
		costs = append(costs, append(repeat(19, 2*time.Millisecond), 8*time.Millisecond)...)
	}
	late, latency := paced(&b, costs...)
	if late != 1 {
		t.Fatalf("%d frames late, want only the first slow one", late)
	}
	if got := slices.Max(latency); got != 8*time.Millisecond+paceMargin {
		t.Fatalf("the budget reached %v", got)
	}
}

func TestAStackSlowerThanHalfARefreshIsLateOnlyOnceFromCold(t *testing.T) {
	var b RenderBudget
	const cost = 10 * time.Millisecond
	late, latency := paced(&b, repeat(40, cost)...)
	if late != 1 {
		t.Fatalf("%d frames late from cold", late)
	}
	if got := latency[len(latency)-1]; got != cost+paceMargin {
		t.Fatalf("the budget settled on %v", got)
	}
}

func TestAMissNoSampleExplainsGrowsTheBudgetAMarginAndExpires(t *testing.T) {
	var b RenderBudget
	const cost = time.Millisecond
	paced(&b, repeat(32, cost)...)
	before := b.Budget(paceInterval)
	b.Missed(paceInterval)
	if got := b.Budget(paceInterval); got != before+paceMargin {
		t.Fatalf("after a miss the budget is %v, from %v", got, before)
	}
	for range 32 {
		b.Sample(cost)
	}
	if got := b.Budget(paceInterval); got != before {
		t.Fatalf("32 frames after a miss the budget is %v, want it measured again at %v", got, before)
	}
}

func TestRepeatedMissesNoSampleExplainsStopAtTheRefresh(t *testing.T) {
	var b RenderBudget
	for range 100 {
		b.Missed(paceInterval)
	}
	if got := b.Budget(paceInterval); got != paceInterval {
		t.Fatalf("the budget after many misses is %v, want the whole refresh and no more", got)
	}
}

func TestOneFrameNoBudgetCouldHoldDoesNotPinTheNextToAWholeRefresh(t *testing.T) {
	var b RenderBudget
	const steady = 2 * time.Millisecond
	paced(&b, repeat(40, steady)...)
	_, latency := paced(&b, append([]time.Duration{30 * time.Millisecond}, repeat(5, steady)...)...)
	for i, got := range latency[1:] {
		if want := steady + 2*paceMargin; got != want {
			t.Fatalf("%d frames after one of 30 ms the budget is %v, want the steady frames' and the miss's margin, %v", i+1, got, want)
		}
	}
	if late, _ := paced(&b, 30*time.Millisecond); late != 1 {
		t.Fatal("a second frame of 30 ms was not late")
	}
	if got := b.Budget(paceInterval); got != paceInterval {
		t.Fatalf("a second such frame in the window leaves a budget of %v, want the whole refresh", got)
	}
}

func TestAStartupFrameSlowerThanARefreshLeavesTheColdBudgetAtHalf(t *testing.T) {
	var b RenderBudget
	b.Sample(50 * time.Millisecond)
	b.Sample(time.Millisecond)
	if got := b.Budget(paceInterval); got != paceInterval/2 {
		t.Fatalf("after one slow startup frame the budget is %v", got)
	}
}
