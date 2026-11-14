package compositor

import (
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type spun struct {
	cpu      time.Duration
	switches int64
	err      error
}

func spinOnOwnThread(d time.Duration) spun {
	out := make(chan spun, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var before, after unix.Rusage
		if err := unix.Getrusage(unix.RUSAGE_THREAD, &before); err != nil {
			out <- spun{err: err}
			return
		}
		err := spinWithoutBlocking(d)
		if usage := unix.Getrusage(unix.RUSAGE_THREAD, &after); err == nil {
			err = usage
		}
		out <- spun{
			cpu:      time.Duration(after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano()),
			switches: after.Nvcsw - before.Nvcsw,
			err:      err,
		}
	}()
	return <-out
}

func TestADebugSpinRunsItsCPUTimeOnTheThreadWithoutBlocking(t *testing.T) {
	const d = 40 * time.Millisecond
	got := spinOnOwnThread(d)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.cpu < d {
		t.Fatalf("the spin used %v of the thread's CPU, want at least %v", got.cpu, d)
	}
	if got.switches != 0 {
		t.Fatalf("the spin blocked %d times, want a thread that never gives up the CPU by itself", got.switches)
	}
}
