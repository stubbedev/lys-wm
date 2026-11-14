package loop

import (
	"testing"
	"time"
)

func TestMonotonicReadsTheKernelsMonotonicClock(t *testing.T) {
	const tolerance = 100 * time.Microsecond
	closest := time.Hour
	for range 1000 {
		before := kernelMonotonic()
		now := Monotonic()
		after := kernelMonotonic()
		if off := (now - before/2 - after/2).Abs(); off < closest {
			closest = off
		}
	}
	if closest > tolerance {
		t.Fatalf("Monotonic is %v off CLOCK_MONOTONIC", closest)
	}
}

func TestMonotonicDoesNotAllocate(t *testing.T) {
	if n := testing.AllocsPerRun(100, func() { Monotonic() }); n != 0 {
		t.Fatalf("Monotonic allocates %v times", n)
	}
}

func TestARealLoopRunsOnTheKernelsMonotonicClock(t *testing.T) {
	l, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	before := kernelMonotonic()
	now := l.Now()
	after := kernelMonotonic()
	if now < before-time.Millisecond || now > after+time.Millisecond {
		t.Fatalf("a real loop reads %v between CLOCK_MONOTONIC readings %v and %v", now, before, after)
	}
}

func BenchmarkMonotonic(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		Monotonic()
	}
}
