package compositor

import (
	"math"
	"runtime"
	"runtime/debug"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type spun struct {
	cpu      time.Duration
	switches int64
	err      error
}

//go:nosplit
func measuredSpin(ns int64, before, after *unix.Rusage) (start, now int64, errno syscall.Errno) {
	if _, _, errno = syscall.RawSyscall(unix.SYS_GETRUSAGE, unix.RUSAGE_THREAD, uintptr(unsafe.Pointer(before)), 0); errno != 0 {
		return 0, 0, errno
	}
	if start, now, errno = spinThreadCPU(ns); errno != 0 {
		return 0, 0, errno
	}
	_, _, errno = syscall.RawSyscall(unix.SYS_GETRUSAGE, unix.RUSAGE_THREAD, uintptr(unsafe.Pointer(after)), 0)
	return start, now, errno
}

func spinOnOwnThread(d time.Duration) spun {
	out := make(chan spun, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var before, after unix.Rusage
		if _, _, errno := measuredSpin(d.Nanoseconds(), &before, &after); errno != 0 {
			out <- spun{err: errno}
			return
		}
		out <- spun{
			cpu:      time.Duration(after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano()),
			switches: after.Nvcsw - before.Nvcsw,
		}
	}()
	return <-out
}

func atLeastTwoProcs(t *testing.T) {
	t.Helper()
	was := runtime.GOMAXPROCS(max(spinLeastProcs, runtime.GOMAXPROCS(0)))
	t.Cleanup(func() { runtime.GOMAXPROCS(was) })
}

func TestADebugSpinRunsItsCPUTimeOnTheThreadWithoutBlocking(t *testing.T) {
	atLeastTwoProcs(t)
	const d = 40 * time.Millisecond
	stop := make(chan struct{})
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	}()
	got := spinOnOwnThread(d)
	close(stop)
	<-collected
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

func TestADebugSpinTurnsPreemptibleOnceItsThreadLeavesItsSchedulingPolicy(t *testing.T) {
	atLeastTwoProcs(t)
	const most = time.Minute
	tids := make(chan int)
	type result struct {
		ran   time.Duration
		errno syscall.Errno
	}
	out := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		tids <- unix.Gettid()
		start, now, errno := spinThreadCPU(most.Nanoseconds())
		out <- result{time.Duration(now - start), errno}
	}()
	tid := <-tids
	if err := unix.SchedSetAttr(tid, &unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_BATCH}, 0); err != nil {
		t.Fatalf("move the spinning thread %d to SCHED_BATCH: %v", tid, err)
	}
	got := <-out
	if got.errno != 0 {
		t.Fatal(got.errno)
	}
	if got.ran >= most {
		t.Fatalf("the spin ran its whole %v unpreemptibly after its thread moved to SCHED_BATCH", most)
	}
}

func TestADebugSpinCollectsNoGarbageAndGivesTheGCItsSettingsBack(t *testing.T) {
	percent := debug.SetGCPercent(150)
	limit := debug.SetMemoryLimit(3 << 30)
	t.Cleanup(func() {
		debug.SetGCPercent(percent)
		debug.SetMemoryLimit(limit)
	})
	var during [2]int64
	if err := withoutGC(func() error {
		during = [2]int64{int64(debug.SetGCPercent(-1)), debug.SetMemoryLimit(-1)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if during != [2]int64{-1, math.MaxInt64} {
		t.Fatalf("the spin ran with GC percent %d and memory limit %d, want the GC off", during[0], during[1])
	}
	if after := [2]int64{int64(debug.SetGCPercent(150)), debug.SetMemoryLimit(-1)}; after != [2]int64{150, 3 << 30} {
		t.Fatalf("the GC came back with percent %d and memory limit %d, want 150 and %d", after[0], after[1], int64(3<<30))
	}
}

func TestADebugSpinRefusesToHoldTheOnlyProcessor(t *testing.T) {
	was := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(was) })
	if err := spinWithoutBlocking(time.Hour); err == nil {
		t.Fatal("a spin started with GOMAXPROCS 1, where it would starve every other goroutine")
	}
}
