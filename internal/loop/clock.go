package loop

import (
	"fmt"
	"math"
	"time"

	"golang.org/x/sys/unix"
)

const clockCalibrations = 16

var clockOrigin, clockOffset = calibrateClock()

func kernelMonotonic() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		panic(fmt.Sprintf("loop: clock_gettime(CLOCK_MONOTONIC) cannot fail with valid arguments: %v", err))
	}
	return time.Duration(ts.Nano())
}

func calibrateClock() (time.Time, time.Duration) {
	origin := time.Now()
	var offset time.Duration
	tightest := time.Duration(math.MaxInt64)
	for range clockCalibrations {
		before := time.Since(origin)
		kernel := kernelMonotonic()
		after := time.Since(origin)
		if width := after - before; width < tightest {
			tightest = width
			offset = kernel - (before + width/2)
		}
	}
	return origin, offset
}

func Monotonic() time.Duration {
	return clockOffset + time.Since(clockOrigin)
}
