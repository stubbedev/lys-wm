package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/godbus"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/rtkit/rtkittest"
	"github.com/stubbedev/lys-wm/internal/session"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

func TestTheCompositorThreadReadsBackThePriorityItTookAndGivesItBack(t *testing.T) {
	type outcome struct {
		how                   string
		err                   error
		before, raised, after unix.SchedAttr
		attr, restored        error
	}
	noBus := "unix:path=" + filepath.Join(t.TempDir(), "no-bus")
	o := onOwnThread(func() (o outcome) {
		tid := unix.Gettid()
		if o.before, o.attr = threadSchedule(tid); o.attr != nil {
			return o
		}
		var restore func() error
		o.how, restore, _, o.err = compositorThread.raisePriority(noBus, dropJob)
		raised, raisedErr := unix.SchedGetAttr(0, 0)
		o.restored = restore()
		after, afterErr := threadSchedule(tid)
		o.attr = errors.Join(raisedErr, afterErr)
		if o.attr == nil {
			o.raised, o.after = *raised, after
		}
		return o
	})
	if o.attr != nil {
		t.Fatal(o.attr)
	}
	if o.restored != nil {
		t.Fatal(o.restored)
	}
	if o.after != o.before {
		t.Fatalf("the thread was given back %+v, not the %+v it ran at before", o.after, o.before)
	}
	switch {
	case o.err != nil:
		t.Logf("refused here: %v", o.err)
		if o.raised.Policy != o.before.Policy {
			t.Fatalf("a refused raise moved the thread from policy %d to %d", o.before.Policy, o.raised.Policy)
		}
		if !strings.Contains(o.err.Error(), "90-lys-wm.conf") || !strings.Contains(o.err.Error(), "rtkit") {
			t.Fatalf("the refusal does not say how to grant the priority: %v", o.err)
		}
	case o.how == "SCHED_RR":
		if o.raised.Policy != unix.SCHED_RR || o.raised.Flags&unix.SCHED_FLAG_RESET_ON_FORK == 0 {
			t.Fatalf("policy %d flags %#x, want SCHED_RR that resets on fork", o.raised.Policy, o.raised.Flags)
		}
	default:
		if o.raised.Nice != loopNice || o.raised.Flags&unix.SCHED_FLAG_RESET_ON_FORK == 0 {
			t.Fatalf("nice %d flags %#x, want %d that resets on fork", o.raised.Nice, o.raised.Flags, loopNice)
		}
	}
}

func demotionUnavailable(start uint32, probe error) (reason string, err error) {
	switch {
	case probe == nil:
		return "", nil
	case !errors.Is(probe, unix.EPERM):
		return "", fmt.Errorf("probe a move from policy %d to SCHED_BATCH: %w", start, probe)
	case start == unix.SCHED_IDLE:
		return "this thread starts under SCHED_IDLE (a nix-daemon with daemonCPUSchedPolicy idle), which an unprivileged thread without an RLIMIT_NICE allowance cannot leave for SCHED_NORMAL", nil
	default:
		return fmt.Sprintf("this thread may not move from policy %d to SCHED_BATCH or SCHED_NORMAL: %v", start, probe), nil
	}
}

func TestTheDemotionTestSkipsOnlyWhereTheThreadCannotBeDemoted(t *testing.T) {
	cases := []struct {
		name  string
		start uint32
		probe error
		skip  bool
		fail  bool
	}{
		{"a demotable thread runs", unix.SCHED_NORMAL, nil, false, false},
		{"an idle thread that may leave runs", unix.SCHED_IDLE, nil, false, false},
		{"an idle thread refused skips", unix.SCHED_IDLE, unix.EPERM, true, false},
		{"a normal thread refused skips", unix.SCHED_NORMAL, unix.EPERM, true, false},
		{"another error fails", unix.SCHED_NORMAL, unix.EINVAL, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, err := demotionUnavailable(c.start, c.probe)
			if (err != nil) != c.fail {
				t.Fatalf("error %v, want failure %v", err, c.fail)
			}
			if (reason != "") != c.skip {
				t.Fatalf("skip reason %q, want skip %v", reason, c.skip)
			}
			if c.start == unix.SCHED_IDLE && c.skip && !strings.Contains(reason, "SCHED_IDLE") {
				t.Fatalf("the reason does not name SCHED_IDLE: %q", reason)
			}
		})
	}
}

func onOwnThread[T any](job func() T) T {
	out := make(chan T, 1)
	go func() {
		runtime.LockOSThread()
		out <- job()
	}()
	return <-out
}

func requireStandIn(t *testing.T) {
	t.Helper()
	type probe struct {
		reason string
		err    error
	}
	p := onOwnThread(func() probe {
		start, err := unix.SchedGetAttr(0, 0)
		if err != nil {
			return probe{err: fmt.Errorf("read this thread's policy: %w", err)}
		}
		reason, err := demotionUnavailable(start.Policy, setPolicyKeepingNice(unix.Gettid(), unix.SCHED_BATCH))
		return probe{reason, err}
	})
	if p.err != nil {
		t.Fatal(p.err)
	}
	if p.reason != "" {
		t.Skip(p.reason)
	}
}

func withoutRTPrio(t *testing.T) {
	t.Helper()
	var was unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTPRIO, &was); err != nil {
		t.Fatalf("read RLIMIT_RTPRIO: %v", err)
	}
	if err := unix.Setrlimit(unix.RLIMIT_RTPRIO, &unix.Rlimit{Cur: 0, Max: was.Max}); err != nil {
		t.Fatalf("drop the soft RLIMIT_RTPRIO: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Setrlimit(unix.RLIMIT_RTPRIO, &was); err != nil {
			t.Errorf("restore RLIMIT_RTPRIO %+v: %v", was, err)
		}
	})
}

type request struct {
	tid      int
	priority int64
	policy   uint32
}

func dropJob(string, func() error) {}

func rtkitStandingInWithBatch(t *testing.T) (bus string, asked <-chan request) {
	t.Helper()
	requests := make(chan request, 16)
	return rtkittest.Serve(t, func(req rtkittest.Request) *dbus.Error {
		if req.Method != rtkittest.Realtime || (req.Value != loopRTPriority && req.Value != guardRTPriority) {
			return dbus.MakeFailedError(fmt.Errorf("the fake rtkit grants SCHED_RR %d and %d only, not %+v", loopRTPriority, guardRTPriority, req))
		}
		attr, err := unix.SchedGetAttr(req.TID, 0)
		if err != nil {
			return dbus.MakeFailedError(fmt.Errorf("read the policy of thread %d: %w", req.TID, err))
		}
		if err := setPolicyKeepingNice(req.TID, unix.SCHED_BATCH); err != nil {
			return dbus.MakeFailedError(fmt.Errorf("stand SCHED_BATCH in for SCHED_RR: %w", err))
		}
		requests <- request{req.TID, req.Value, attr.Policy}
		return nil
	}), requests
}

type noDevices struct{}

func (noDevices) TakeDevice(path string) (int, session.Device, error) {
	return -1, session.Device{}, fmt.Errorf("no device for %s in this test: %w", path, unix.EPERM)
}

func (noDevices) ReleaseDevice(session.Device) error { return nil }

func openInputThread(t *testing.T, bus string) (in *session.Libinput, tid int) {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	path := session.LibinputPath(os.Getenv)
	in, err = session.OpenLibinput(l, path, noDevices{}, session.InputConfig{}, func(err error) { t.Errorf("reported: %v", err) }, func(hand func(string, func() error)) func() error {
		tid = unix.Gettid()
		restore, _ := inputThread.raise(bus, hand)
		return restore
	})
	testenv.Library(t, "libinput", path, err)
	return in, tid
}

func scheduleOf(t *testing.T, tid int) unix.SchedAttr {
	t.Helper()
	attr, err := threadSchedule(tid)
	if err != nil {
		t.Fatal(err)
	}
	return attr
}

func TestTheInputThreadRunsAtTheCompositorThreadsPriority(t *testing.T) {
	requireStandIn(t)
	withoutRTPrio(t)
	bus, _ := rtkitStandingInWithBatch(t)
	type taken struct {
		s   unix.SchedAttr
		err error
	}
	compositor := onOwnThread(func() taken {
		restore, _ := compositorThread.raise(bus, dropJob)
		s, err := threadSchedule(unix.Gettid())
		return taken{s, errors.Join(err, restore())}
	})
	if compositor.err != nil {
		t.Fatal(compositor.err)
	}
	in, tid := openInputThread(t, bus)
	defer func() {
		if err := in.Close(); err != nil {
			t.Error(err)
		}
	}()
	if got := scheduleOf(t, tid); got != compositor.s {
		t.Fatalf("the input thread runs at %+v, the compositor thread at %+v", got, compositor.s)
	}
}

func TestTheInputThreadIsGuardedByAThreadAboveIt(t *testing.T) {
	requireStandIn(t)
	withoutRTPrio(t)
	bus, asked := rtkitStandingInWithBatch(t)
	in, tid := openInputThread(t, bus)
	defer func() {
		if err := in.Close(); err != nil {
			t.Error(err)
		}
	}()
	var watched, guard request
	select {
	case watched = <-asked:
	default:
		if got := scheduleOf(t, tid); got.Policy != unix.SCHED_RR {
			t.Fatalf("the input thread runs at %+v without having asked rtkit, want SCHED_RR", got)
		}
		t.Skip("the input thread took SCHED_RR without rtkit")
	}
	select {
	case guard = <-asked:
	default:
		t.Fatal("rtkit raised the input thread, but nothing asked it to raise a guard")
	}
	if watched.tid != tid || watched.priority != loopRTPriority {
		t.Fatalf("rtkit was first asked for %+v, want the input thread %d at %d", watched, tid, loopRTPriority)
	}
	if guard.tid == tid || guard.priority != guardRTPriority {
		t.Fatalf("rtkit was then asked for %+v, want another thread at %d", guard, guardRTPriority)
	}
}

func TestARaiseThroughRtkitGivesTheThreadBackItsScheduling(t *testing.T) {
	requireStandIn(t)
	withoutRTPrio(t)
	bus, _ := rtkitStandingInWithBatch(t)
	type taken struct {
		before, raised, after unix.SchedAttr
		err                   error
	}
	got := onOwnThread(func() (got taken) {
		tid := unix.Gettid()
		var beforeErr, raisedErr, afterErr error
		if got.before, beforeErr = threadSchedule(tid); beforeErr != nil {
			return taken{err: beforeErr}
		}
		restore, _ := compositorThread.raise(bus, dropJob)
		got.raised, raisedErr = threadSchedule(tid)
		restoreErr := restore()
		got.after, afterErr = threadSchedule(tid)
		got.err = errors.Join(raisedErr, restoreErr, afterErr)
		return got
	})
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.raised.Policy != unix.SCHED_BATCH {
		t.Fatalf("the thread ran at %+v after the raise, want the fake rtkit's SCHED_BATCH", got.raised)
	}
	if got.after != got.before {
		t.Fatalf("the thread was given back %+v, not the %+v it ran at before", got.after, got.before)
	}
}

const finiteRTTimeChild = "LYS_WM_TEST_FINITE_RTTIME"

func inChildWithFiniteRTTime(t *testing.T) bool {
	t.Helper()
	if os.Getenv(finiteRTTimeChild) != "" {
		limit := unix.Rlimit{Cur: rtkittest.RTTimeUSecMax, Max: rtkittest.RTTimeUSecMax}
		if err := unix.Setrlimit(unix.RLIMIT_RTTIME, &limit); err != nil {
			t.Fatalf("lower RLIMIT_RTTIME to %+v as a parent under rtkit would hand it down: %v", limit, err)
		}
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), finiteRTTimeChild+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child under a finite RLIMIT_RTTIME: %v\n%s", err, out)
	}
	if _, reason, skipped := strings.Cut(string(out), "--- SKIP: "+t.Name()); skipped {
		t.Skip(strings.TrimSpace(reason))
	}
	if !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("the child under a finite RLIMIT_RTTIME did not run %s:\n%s", t.Name(), out)
	}
	return false
}

func withRTPrioOne(t *testing.T) {
	t.Helper()
	var was unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTPRIO, &was); err != nil {
		t.Fatalf("read RLIMIT_RTPRIO: %v", err)
	}
	if was.Max < loopRTPriority {
		t.Skipf("RLIMIT_RTPRIO allows %d, so no thread here takes SCHED_RR %d by itself", was.Max, loopRTPriority)
	}
	if err := unix.Setrlimit(unix.RLIMIT_RTPRIO, &unix.Rlimit{Cur: loopRTPriority, Max: was.Max}); err != nil {
		t.Fatalf("set the soft RLIMIT_RTPRIO to %d: %v", loopRTPriority, err)
	}
}

func realtimeThreads(priority uint32) ([]int, error) {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil, err
	}
	var found []int
	for _, task := range tasks {
		tid, err := strconv.Atoi(task.Name())
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", task.Name(), err)
		}
		attr, err := unix.SchedGetAttr(tid, 0)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read the policy of thread %d: %w", tid, err)
		}
		if attr.Policy == unix.SCHED_RR && attr.Priority == priority {
			found = append(found, tid)
		}
	}
	return found, nil
}

func TestADirectRealtimeGrantUnderAnInheritedFiniteRTTimeIsGuardedAndKeepsTheLimit(t *testing.T) {
	if !inChildWithFiniteRTTime(t) {
		return
	}
	requireStandIn(t)
	withRTPrioOne(t)
	bus, asked := rtkitStandingInWithBatch(t)
	type taken struct {
		tid    int
		how    string
		raised unix.SchedAttr
		guards []int
		err    error
	}
	got := onOwnThread(func() (got taken) {
		got.tid = unix.Gettid()
		var restore func() error
		got.how, restore, _, got.err = compositorThread.raisePriority(bus, dropJob)
		var raisedErr, guardsErr error
		got.raised, raisedErr = threadSchedule(got.tid)
		got.guards, guardsErr = realtimeThreads(guardRTPriority)
		got.err = errors.Join(got.err, raisedErr, guardsErr, restore())
		return got
	})
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.how != "SCHED_RR" || got.raised.Policy != unix.SCHED_RR {
		t.Fatalf("the thread took %q and ran at %+v, want SCHED_RR taken directly", got.how, got.raised)
	}
	select {
	case guard := <-asked:
		if guard.tid == got.tid || guard.priority != guardRTPriority {
			t.Fatalf("rtkit was asked for %+v, want a guard of thread %d at %d", guard, got.tid, guardRTPriority)
		}
	default:
		if len(got.guards) != 1 || got.guards[0] == got.tid {
			t.Fatalf("thread %d took SCHED_RR directly under a finite RLIMIT_RTTIME, but rtkit was not asked for a guard and the threads at SCHED_RR %d are %v", got.tid, guardRTPriority, got.guards)
		}
	}
	select {
	case again := <-asked:
		t.Fatalf("rtkit was asked again for %+v", again)
	default:
	}
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTTIME, &limit); err != nil {
		t.Fatal(err)
	}
	if want := (unix.Rlimit{Cur: rtkittest.RTTimeUSecMax, Max: rtkittest.RTTimeUSecMax}); limit != want {
		t.Fatalf("RLIMIT_RTTIME is %+v after the raise, want the inherited %+v", limit, want)
	}
}
