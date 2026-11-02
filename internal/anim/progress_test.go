package anim

import (
	"math"
	"testing"
	"time"
)

const ms = time.Millisecond

func tracked(p *Progress, step time.Duration, from, per float64, n int) time.Duration {
	at := time.Second
	for i := range n {
		p.Track(from+float64(float64(i)*per), at)
		at += step
	}
	return at - step
}

func TestProgressFollowsTheFingersExactly(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	p.Track(0.37, time.Second)
	if got := p.At(time.Hour); got != 0.37 || !p.Tracking() || p.Done(time.Hour) {
		t.Fatalf("a tracked progress is at %v, tracking %v, done %v, however late the frame", got, p.Tracking(), p.Done(time.Hour))
	}
}

func TestProgressMeasuresTheFingerVelocity(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	last := tracked(&p, 8*ms, 0, 0.02, 20)
	if v := p.Velocity(last + 4*ms); math.Abs(v-2.5) > 1e-6 {
		t.Fatalf("0.02 every 8ms is 2.5 a second, measured %v", v)
	}
	if v := p.Velocity(last + 51*ms); v != 0 {
		t.Fatalf("fingers that rested before lifting have velocity %v", v)
	}
}

func TestProgressVelocityOnlyLooksAtTheLastMoments(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	at := time.Second
	for i := range 20 {
		p.Track(float64(i), at)
		at += 8 * ms
	}
	x := 19.0
	for range 15 {
		x += 0.01
		p.Track(x, at)
		at += 8 * ms
	}
	if v := p.Velocity(at); math.Abs(v-1.25) > 1e-6 {
		t.Fatalf("a fast start then a slow finish measures %v, want the slow 1.25", v)
	}
}

func TestProgressVelocityOutlivesItsSampleBuffer(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	last := tracked(&p, ms, 0, 0.001, 3*velocitySamples)
	if v := p.Velocity(last); math.Abs(v-1) > 1e-6 {
		t.Fatalf("a full buffer measures %v, want 1", v)
	}
}

func TestProgressReleaseCarriesTheFingerVelocity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		curve Curve
	}{{"its spring", Wobbly}, {"a bézier, which has no velocity to carry", eased{EaseOut, time.Second}}} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProgress(0)
			p.Grab(0)
			last := tracked(&p, 8*ms, 0, 0.02, 20)
			x, v := p.At(0), p.Velocity(last)
			p.Release(1, last, 10*time.Second, tc.curve)
			if p.Tracking() || p.Target() != 1 {
				t.Fatalf("tracking %v, target %v after the release", p.Tracking(), p.Target())
			}
			if got := p.At(10 * time.Second); math.Abs(got-x) > 1e-9 {
				t.Fatalf("the release jumped from %v to %v", x, got)
			}
			_, carried := p.state(10*time.Second + time.Microsecond)
			if math.Abs(carried-v) > 0.1 {
				t.Fatalf("the spring moves at %v from fingers moving at %v", carried, v)
			}
			if p.Done(11*time.Second) && p.At(10*time.Second+time.Millisecond) == 1 {
				t.Fatal("settled straight away")
			}
			end := 10*time.Second + 30*time.Second
			if !p.Done(end) || p.At(end) != 1 {
				t.Fatalf("at the end it is at %v, done %v", p.At(end), p.Done(end))
			}
		})
	}
}

func TestProgressReleaseWithoutAnimationsJumps(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	last := tracked(&p, 8*ms, 0, 0.02, 20)
	p.Release(1, last, last, Instant)
	if p.At(last) != 1 || !p.Done(last) {
		t.Fatalf("at %v, done %v", p.At(last), p.Done(last))
	}
}

func TestProgressGrabHoldsASettlingSpringWhereItIs(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	last := tracked(&p, 8*ms, 0, 0.02, 20)
	p.Release(1, last, 0, Smooth)
	mid := p.At(100 * ms)
	if mid <= 0.38 || mid >= 1 {
		t.Fatalf("the settle should be under way, at %v", mid)
	}
	p.Grab(100 * ms)
	if got := p.At(time.Hour); got != mid || !p.Tracking() || p.Velocity(time.Hour) != 0 {
		t.Fatalf("held at %v, want %v, tracking %v, velocity %v", got, mid, p.Tracking(), p.Velocity(time.Hour))
	}
}

func TestProgressSetRetargetsWithItsVelocity(t *testing.T) {
	p := NewProgress(0)
	p.Set(1, 0, Smooth)
	x, v := p.state(100 * ms)
	p.Set(0, 100*ms, Smooth)
	gx, gv := p.state(100 * ms)
	if gx != x && math.Abs(gx-x) > 1e-9 || math.Abs(gv-v) > 1e-9 {
		t.Fatalf("(%v, %v) became (%v, %v) on the retarget", x, v, gx, gv)
	}
	timed := NewProgress(0)
	c := eased{Linear, time.Second}
	timed.Set(1, 0, c)
	if got := timed.At(500 * ms); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("a linear second is at %v halfway", got)
	}
	timed.Set(0, 500*ms, c)
	if got := timed.At(500 * ms); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("a timed retarget starts from %v", got)
	}
	if got := timed.At(time.Second); math.Abs(got-0.25) > 1e-9 {
		t.Fatalf("a timed retarget is at %v halfway back", got)
	}
}

func TestProgressSetWhileTrackingEndsTheTracking(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	last := tracked(&p, 8*ms, 0, 0.02, 20)
	p.Set(1, last, eased{Linear, time.Second})
	if p.Tracking() {
		t.Fatal("still tracking")
	}
	if _, v := p.state(last + time.Microsecond); math.Abs(v-2.5) > 0.1 {
		t.Fatalf("an interrupted track lost its velocity, moving at %v", v)
	}
}

func TestProgressShiftKeepsTheVelocity(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	last := tracked(&p, 8*ms, 0.5, 0.02, 20)
	x := p.At(0)
	p.Shift(-1)
	if got := p.At(0); math.Abs(got-(x-1)) > 1e-9 {
		t.Fatalf("shifted to %v, want %v", got, x-1)
	}
	if v := p.Velocity(last); math.Abs(v-2.5) > 1e-6 {
		t.Fatalf("the shift changed the velocity to %v", v)
	}
}

func TestProgressProjectsWhereTheVelocityIsGoing(t *testing.T) {
	p := NewProgress(0)
	p.Grab(0)
	last := tracked(&p, 8*ms, 0, 0.02, 20)
	want := 0.38 + float64(2.5*projectionTime)
	if got := p.Projected(last); math.Abs(got-want) > 1e-6 {
		t.Fatalf("projected %v, want %v", got, want)
	}
}

func TestRubberBandStartsOneToOneAndHasALimit(t *testing.T) {
	const limit = 0.25
	if got := RubberBand(0, 0.001, limit); got != 0.001 {
		t.Fatalf("the first bit of travel moves %v, want 0.001", got)
	}
	x := 0.0
	for range 1000 {
		x = RubberBand(x, 0.01, limit)
	}
	if x >= limit || x < 0.2 {
		t.Fatalf("ten whole swipes reach %v, want under the limit %v", x, limit)
	}
	if got := RubberBand(0.1, 0.01, limit); got-0.1 >= 0.01 {
		t.Fatalf("no resistance at 0.1: moved %v", got-0.1)
	}
	if got := RubberBand(-0.1, -0.01, limit); got != -RubberBand(0.1, 0.01, limit) {
		t.Fatalf("not symmetric: %v", got)
	}
	if got := RubberBand(0.24, 100, limit); got != limit {
		t.Fatalf("a huge step goes past the limit to %v", got)
	}
}

func BenchmarkProgressTrack(b *testing.B) {
	b.ReportAllocs()
	p := NewProgress(0)
	p.Grab(0)
	at := time.Second
	for i := range b.N {
		at += 8 * ms
		p.Track(float64(i%100)/100, at)
		if p.Velocity(at) > 1e9 {
			b.Fatal("impossible velocity")
		}
	}
}
