package backend

import (
	"testing"
	"time"
)

func TestNowReadsTheKernelsMonotonicClock(t *testing.T) {
	const tolerance = 100 * time.Microsecond
	closest := time.Hour
	for range 1000 {
		before := kernelMonotonic()
		now := Now()
		after := kernelMonotonic()
		if off := (now - before/2 - after/2).Abs(); off < closest {
			closest = off
		}
	}
	if closest > tolerance {
		t.Fatalf("Now is %v off CLOCK_MONOTONIC", closest)
	}
}

func TestNowDoesNotAllocate(t *testing.T) {
	if n := testing.AllocsPerRun(100, func() { Now() }); n != 0 {
		t.Fatalf("Now allocates %v times", n)
	}
}

func BenchmarkNow(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		Now()
	}
}
