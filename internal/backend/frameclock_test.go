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

func TestAFrameClockSaysWhetherItsFrameIsQueuedOrWaiting(t *testing.T) {
	var q idleQueue
	ahead := true
	clock := NewFrameClock(q.add, func() bool { return true }, func() error { return nil }, &Latch{
		Wait: func() time.Duration {
			if ahead {
				return 5 * time.Millisecond
			}
			return 0
		},
		Wake: func(time.Duration) error { return nil },
	})
	if clock.Queued() || clock.Waiting() {
		t.Fatal("a clock with no request is queued or waiting")
	}
	must(t, clock.Request())
	if clock.Queued() || !clock.Waiting() {
		t.Fatalf("a request before its deadline: queued %t, waiting %t", clock.Queued(), clock.Waiting())
	}
	clock.Cancel()
	if clock.Waiting() {
		t.Fatal("a cancelled request still waits")
	}
	ahead = false
	must(t, clock.Woke())
	must(t, clock.Request())
	if !clock.Queued() || clock.Waiting() {
		t.Fatalf("a request past its deadline: queued %t, waiting %t", clock.Queued(), clock.Waiting())
	}
	q.run(t)
	if clock.Queued() || clock.Waiting() {
		t.Fatal("a drawn frame is still queued or waiting")
	}
}

func TestTheLatchDeadlineLeavesTheBudgetBeforeTheNextVblank(t *testing.T) {
	const ms = time.Millisecond
	for _, tc := range []struct {
		last, now, want time.Duration
	}{
		{last: 100 * ms, now: 101 * ms, want: 108 * ms},
		{last: 100 * ms, now: 109 * ms, want: 108 * ms},
		{last: 100 * ms, now: 125 * ms, want: 124 * ms},
		{last: 0, now: 125 * ms, want: 125 * ms},
	} {
		var b RenderBudget
		if got := b.Deadline(uncapped(tc.last, 16*ms), tc.now); got != tc.want {
			t.Errorf("last %v, now %v: deadline %v, want %v", tc.last, tc.now, got, tc.want)
		}
	}
}

func uncapped(vblank, refresh time.Duration) Pacing {
	return Pacing{Vblank: vblank, Refresh: refresh, Paced: refresh}
}

func TestACappedFrameNeverLatchesAtTheRefreshBeforeItsTurn(t *testing.T) {
	const (
		ms      = time.Millisecond
		refresh = 8 * ms
		shown   = 100 * ms
	)
	var slow RenderBudget
	for range renderSamples {
		slow.Sample(12 * ms)
	}
	for _, tc := range []struct {
		name        string
		budget      *RenderBudget
		paced, want time.Duration
	}{
		{"a budget longer than a refresh", &slow, 2 * refresh, 108 * ms},
		{"a cold budget", &RenderBudget{}, 2 * refresh, 108 * ms},
		{"no cap", &RenderBudget{}, refresh, 104 * ms},
	} {
		p := Pacing{Vblank: shown, Shown: shown, Refresh: refresh, Paced: tc.paced}
		got := tc.budget.Latch(p, shown+ms)
		if got != tc.want {
			t.Errorf("%s: latched at %v, want %v", tc.name, got, tc.want)
		}
		if at, _ := NextVblank(shown, got, refresh); at < shown+tc.paced {
			t.Errorf("%s: a frame started at %v lands at %v, before its turn at %v", tc.name, got, at, shown+tc.paced)
		}
	}
}

func TestACappedFrameAimsAtItsTurnFromTheFrameShownNotTheLatestVblank(t *testing.T) {
	const (
		ms      = time.Millisecond
		refresh = 8 * ms
		paced   = 2 * refresh
		shown   = 100 * ms
	)
	for _, tc := range []struct {
		name              string
		vblank, now, want time.Duration
		variable          bool
	}{
		{"before the refresh between", shown, shown + ms, shown + paced, false},
		{"after the refresh between", shown + refresh, shown + refresh + ms, shown + paced, false},
		{"with the vblanks drifted off the flip", shown + refresh + 30*time.Microsecond, shown + refresh + ms, shown + paced + 30*time.Microsecond, false},
		{"past its turn", shown + 3*refresh, shown + 3*refresh + ms, shown + 4*refresh, false},
		{"under VRR", shown, shown + ms, shown + paced + 3*ms, true},
	} {
		p := Pacing{Vblank: tc.vblank, Shown: shown, Refresh: refresh, Paced: paced, Variable: tc.variable}
		if tc.variable {
			p.Paced += 3 * ms
		}
		if got := p.Aim(tc.now); got != tc.want {
			t.Errorf("%s: aimed at %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestACappedFrameAfterTheRefreshBetweenStartsAtOnce(t *testing.T) {
	const (
		ms      = time.Millisecond
		refresh = 8 * ms
		shown   = 100 * ms
	)
	var b RenderBudget
	p := Pacing{Vblank: shown + refresh, Shown: shown, Refresh: refresh, Paced: 2 * refresh}
	now := shown + refresh + ms
	if got := b.Deadline(p, now); got > now {
		t.Fatalf("a capped frame asked for one refresh after the last flip, with the CRTC past the refresh between, waits until %v, want to start at once at %v", got, now)
	}
}

func TestAFrameWithNoTimeLeftWaitsForTheNextDeadline(t *testing.T) {
	const ms = time.Millisecond
	var b RenderBudget
	if got := b.Deadline(uncapped(100*ms, 16*ms), 114*ms); got != 124*ms {
		t.Fatalf("2 ms before the vblank the deadline is %v, want the next refresh's, 124 ms", got)
	}
	if got := b.Deadline(uncapped(100*ms, 16*ms), 124*ms); got != 124*ms {
		t.Fatalf("at the next refresh's deadline it is %v", got)
	}
}

func TestAFrameWithAFastRendersWorthOfTimeLeftStillStarts(t *testing.T) {
	const ms = time.Millisecond
	var b RenderBudget
	if got := b.Deadline(uncapped(100*ms, 16*ms), 112*ms); got != 108*ms {
		t.Fatalf("4 ms before the vblank the deadline is %v, want the one already past", got)
	}
}

func TestADeferredFrameIsNotDeferredAgain(t *testing.T) {
	const ms = time.Millisecond
	var b RenderBudget
	if got := b.Deadline(uncapped(100*ms, 16*ms), 114*ms); got != 124*ms {
		t.Fatalf("the first deadline is %v", got)
	}
	if got := b.Deadline(uncapped(100*ms, 16*ms), 131*ms); got > 131*ms {
		t.Fatalf("woken 1 ms before the next vblank, a deferred frame waits until %v", got)
	}
	if got := b.Deadline(uncapped(100*ms, 16*ms), 147*ms); got != 156*ms {
		t.Fatalf("the frame after it is deferred again only as its own: %v", got)
	}
}

func TestWhatIsTooLateToStartFollowsTheTypicalRender(t *testing.T) {
	const ms = time.Millisecond
	var b RenderBudget
	for i := range renderSamples {
		cost := 7 * ms
		if i == 0 {
			cost = 12 * ms
		}
		b.Sample(cost)
	}
	if got := b.Deadline(uncapped(100*ms, 16*ms), 110*ms); got != 117*ms {
		t.Fatalf("6 ms before the vblank with 7 ms renders the deadline is %v, want the next refresh's", got)
	}
	var fast RenderBudget
	for range renderSamples {
		fast.Sample(ms)
	}
	if got := fast.Deadline(uncapped(100*ms, 16*ms), 112*ms); got > 112*ms {
		t.Fatalf("4 ms before the vblank with 1 ms renders the frame waits until %v", got)
	}
}

func TestALatchNeverDefersAndLeavesAFramesDeferralAlone(t *testing.T) {
	const ms = time.Millisecond
	var b RenderBudget
	if got := b.Latch(uncapped(100*ms, 16*ms), 115*ms); got != 108*ms {
		t.Fatalf("1 ms before the vblank the latch is %v, want the one already past", got)
	}
	if got := b.Deadline(uncapped(100*ms, 16*ms), 115*ms); got != 124*ms {
		t.Fatalf("after a latch a frame's deadline is %v, want the next refresh's", got)
	}
	b.Latch(uncapped(100*ms, 16*ms), 115*ms)
	if got := b.Deadline(uncapped(100*ms, 16*ms), 131*ms); got > 131*ms {
		t.Fatalf("a latch between them made the deferred frame wait again, until %v", got)
	}
}

func TestTheLatchDeadlineDoesNotAllocate(t *testing.T) {
	const ms = time.Millisecond
	var b RenderBudget
	for range renderSamples {
		b.Sample(ms)
	}
	now := 100 * ms
	if n := testing.AllocsPerRun(100, func() {
		now += ms
		b.Deadline(uncapped(100*ms, 16*ms), now)
	}); n != 0 {
		t.Fatalf("a latch deadline allocated %v times", n)
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

func TestAHintedFrameLatchesEarlierInProportionToItsWork(t *testing.T) {
	const interval = 16 * time.Millisecond
	var b RenderBudget
	for range renderSamples {
		b.Painted(1 << 18)
		b.Sample(time.Millisecond)
	}
	last, now := time.Second, time.Second+time.Millisecond
	calm := b.Latch(uncapped(last, interval), now)
	var woke []time.Duration
	var f *FrameClock
	clock := now
	f = NewFrameClock(func(func() error) {}, func() bool { return true }, func() error { return nil }, &Latch{
		Wait: func() time.Duration { return b.Deadline(uncapped(last, interval), clock) - clock },
		Wake: func(after time.Duration) error { woke = append(woke, after); return nil },
	})
	if err := f.Request(); err != nil {
		t.Fatal(err)
	}
	f.Cancel()
	if err := f.Woke(); err != nil {
		t.Fatal(err)
	}
	b.Expect(2 << 20)
	hinted := b.Latch(uncapped(last, interval), now)
	if hinted >= calm {
		t.Fatalf("a frame expected to paint 8 times the last one latches at %v, the calm one at %v", hinted, calm)
	}
	if err := f.Request(); err != nil {
		t.Fatal(err)
	}
	if len(woke) != 2 || woke[1] >= woke[0] {
		t.Fatalf("the clock armed its wakes after %v, want the hinted one sooner", woke)
	}
	b.Started()
	if got := b.Latch(uncapped(last, interval), now); got != calm {
		t.Fatalf("after its frame started the hint still moves the latch: %v, want %v", got, calm)
	}
}

func TestThePlaneLatchLeadsTheVblankByLessThanAFrameCouldStillMake(t *testing.T) {
	interval := 16 * time.Millisecond
	if PlaneLead >= renderMargin {
		t.Fatalf("plane lead %v, want it under the render margin %v", PlaneLead, renderMargin)
	}
	if got := PlaneLatch(100*time.Millisecond, 105*time.Millisecond, interval); got != 116*time.Millisecond-PlaneLead {
		t.Fatalf("the plane latch is %v, want the next vblank less the plane lead", got)
	}
	if got := PlaneLatch(0, 105*time.Millisecond, interval); got != 105*time.Millisecond {
		t.Fatalf("with no flip yet the plane latch is %v, want now", got)
	}
}

func TestRendersOverrunOnlyWhileTheTypicalFrameTakesARefresh(t *testing.T) {
	interval := 16 * time.Millisecond
	var b RenderBudget
	if b.Overruns(interval) {
		t.Fatal("a budget with no samples overruns")
	}
	for range renderSamples / 2 {
		b.Sample(20 * time.Millisecond)
	}
	if !b.Overruns(interval) {
		t.Fatal("frames of 20 ms on a 16 ms refresh do not overrun")
	}
	for range renderSamples {
		b.Sample(5 * time.Millisecond)
	}
	if b.Overruns(interval) {
		t.Fatal("frames of 5 ms on a 16 ms refresh overrun")
	}
}
