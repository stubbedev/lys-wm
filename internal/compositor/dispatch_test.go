package compositor

import (
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/spawn"
)

type spun struct {
	cpu      time.Duration
	switches int64
	faults   int64
	err      error
}

//go:nosplit
func measuredSpin(ns int64, before, after *unix.Rusage) (start, now int64, errno syscall.Errno) {
	was, errno := blockSignals()
	if errno != 0 {
		return 0, 0, errno
	}
	start, now, errno = measuredUnsignalled(ns, before, after)
	if restored := setSignalMask(was); errno == 0 {
		errno = restored
	}
	return start, now, errno
}

//go:nosplit
func measuredUnsignalled(ns int64, before, after *unix.Rusage) (start, now int64, errno syscall.Errno) {
	if _, _, errno = syscall.RawSyscall(unix.SYS_GETRUSAGE, unix.RUSAGE_THREAD, uintptr(unsafe.Pointer(after)), 0); errno != 0 {
		return 0, 0, errno
	}
	if _, _, errno = spinThreadCPU(0); errno != 0 {
		return 0, 0, errno
	}
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
			faults:   after.Minflt + after.Majflt - before.Minflt - before.Majflt,
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
	const windows = 20
	var got spun
	faulted := make([]int64, 0, windows)
	for range windows {
		if got = spinOnOwnThread(d); got.err != nil || got.faults == 0 {
			break
		}
		faulted = append(faulted, got.faults)
	}
	close(stop)
	<-collected
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.faults != 0 {
		t.Fatalf("all %d spins took page faults (%v), each a kernel entry that may sleep, so none shows whether the spin itself blocks", windows, faulted)
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
	toBatch(t, tid)
	got := <-out
	if got.errno != 0 {
		t.Fatal(got.errno)
	}
	if got.ran >= most {
		t.Fatalf("the spin ran its whole %v unpreemptibly after its thread moved to SCHED_BATCH", most)
	}
}

func toBatch(t *testing.T, tid int) {
	t.Helper()
	prio, err := unix.Getpriority(unix.PRIO_PROCESS, tid)
	if err != nil {
		t.Fatalf("read the nice value of the spinning thread %d: %v", tid, err)
	}
	batch := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_BATCH, Nice: int32(20 - prio)}
	if err := unix.SchedSetAttr(tid, &batch, 0); err != nil {
		t.Fatalf("move the spinning thread %d to SCHED_BATCH at its nice %d: %v", tid, batch.Nice, err)
	}
}

func blockedSignals(t *testing.T, tid int) uint64 {
	t.Helper()
	status, err := os.ReadFile("/proc/self/task/" + strconv.Itoa(tid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		if hex, ok := strings.CutPrefix(line, "SigBlk:"); ok {
			mask, err := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
			if err != nil {
				t.Fatalf("SigBlk %q: %v", hex, err)
			}
			return mask
		}
	}
	t.Fatalf("no SigBlk in the status of thread %d", tid)
	return 0
}

func TestADebugSpinTakesNoSignalsAndGivesItsThreadItsSignalMaskBack(t *testing.T) {
	atLeastTwoProcs(t)
	const most = time.Minute
	unblockable := uint64(1)<<(unix.SIGKILL-1) | uint64(1)<<(unix.SIGSTOP-1)
	tids := make(chan int)
	type result struct {
		before, after unix.Sigset_t
		errno         syscall.Errno
		err           error
	}
	out := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		var r result
		if r.err = unix.PthreadSigmask(unix.SIG_BLOCK, nil, &r.before); r.err != nil {
			close(tids)
			out <- r
			return
		}
		tids <- unix.Gettid()
		_, _, r.errno = spinThreadCPU(most.Nanoseconds())
		r.err = unix.PthreadSigmask(unix.SIG_BLOCK, nil, &r.after)
		out <- r
	}()
	tid, ok := <-tids
	if !ok {
		t.Fatal((<-out).err)
	}
	for deadline := time.Now().Add(30 * time.Second); blockedSignals(t, tid) != ^unblockable; {
		if time.Now().After(deadline) {
			t.Fatalf("the spinning thread %d blocks signals %#x, want every one it can", tid, blockedSignals(t, tid))
		}
		runtime.Gosched()
	}
	toBatch(t, tid)
	got := <-out
	if got.err != nil || got.errno != 0 {
		t.Fatal(got.err, got.errno)
	}
	if got.after != got.before {
		t.Fatalf("the spin left its thread blocking %#x, not the %#x it blocked before", got.after.Val[0], got.before.Val[0])
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

func execRig(t *testing.T, spawner *spawn.Spawner, stdout *os.File) (*Compositor, *[]error) {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	reports := &[]error{}
	c := &Compositor{loop: l, execOutput: ExecOutput{Stdout: stdout, Stderr: os.Stderr}, lookPath: exec.LookPath, report: func(err error) {
		*reports = append(*reports, err)
		l.Stop()
	}}
	c.hooks.Spawner = spawner
	return c, reports
}

func runUntilStopped(t *testing.T, c *Compositor) {
	t.Helper()
	hang, err := c.loop.AddTimer(func() error { return errors.New("the exec neither started nor failed") })
	if err != nil {
		t.Fatal(err)
	}
	defer hang.Remove()
	if err := hang.Arm(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.loop.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestAnExecStartsFromTheSpawner(t *testing.T) {
	spawner, err := spawn.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := spawner.Close(); err != nil {
			t.Error(err)
		}
	}()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c, reports := execRig(t, spawner, w)
	pid := 0
	c.spawnWith(`echo "$PPID"`, nil, func(started int) {
		pid = started
		c.loop.Stop()
	})
	runUntilStopped(t, c)
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	c.WaitForExecs()
	if len(*reports) != 0 || pid == 0 {
		t.Fatalf("the exec reported %v and started pid %d", *reports, pid)
	}
	parent, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("the exec printed %q: %v", out, err)
	}
	if parent == os.Getpid() {
		t.Fatalf("the exec is a child of the compositor's process %d, not of its spawner", parent)
	}
}

func TestAnExecWithoutASpawnerFailsInsteadOfForking(t *testing.T) {
	c, reports := execRig(t, nil, os.Stdout)
	c.spawnWith("true", nil, func(pid int) { t.Errorf("an exec without a spawner started pid %d", pid) })
	runUntilStopped(t, c)
	c.WaitForExecs()
	if len(*reports) != 1 || !strings.Contains((*reports)[0].Error(), "spawner") {
		t.Fatalf("an exec without a spawner reported %v, want one error naming the missing spawner", *reports)
	}
}
