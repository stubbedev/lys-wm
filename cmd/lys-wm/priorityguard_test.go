package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/testenv"
)

const scriptedBudget = 20 * time.Millisecond

type scriptedThread struct {
	t        *testing.T
	switches uint64
	cpu      time.Duration
	unseen   error
	refuse   bool
	pauses   int
	events   []string
	handed   []func() error
}

func (s *scriptedThread) guard() *rtGuard {
	return &rtGuard{name: "the scripted thread", budget: scriptedBudget, wait: rtRegainFirstWait, ops: guardOps{
		switches: func() (uint64, error) { return s.switches, s.unseen },
		cpu:      func() (time.Duration, error) { return s.cpu, nil },
		arm: func(on bool) error {
			s.events = append(s.events, fmt.Sprintf("arm %v", on))
			return nil
		},
		demote: func() error {
			s.events = append(s.events, "demote")
			return nil
		},
		doing: func() string { return "running outside the kernel" },
		raise: func() error {
			s.events = append(s.events, "raise")
			if s.refuse {
				return errors.New("refused")
			}
			return nil
		},
		settle: func() (uint64, error) {
			s.switches++
			return s.switches, nil
		},
		hand: func(_ string, job func() error) {
			s.events = append(s.events, "hand")
			s.handed = append(s.handed, job)
		},
		pause: func(d time.Duration) bool {
			s.events = append(s.events, fmt.Sprintf("pause %v", d))
			s.pauses--
			return s.pauses >= 0
		},
		say:   func(line string) { s.events = append(s.events, "say "+line) },
		stack: func() string { return "goroutine 1 [running]" },
	}}
}

func (s *scriptedThread) run(d time.Duration, blocks bool) {
	s.cpu += d
	if blocks {
		s.switches++
	}
}

func (s *scriptedThread) regainHanded() {
	s.t.Helper()
	if len(s.handed) != 1 {
		s.t.Fatalf("%d jobs were handed to the thread, want one regain", len(s.handed))
	}
	job := s.handed[0]
	s.handed = s.handed[:0]
	if err := job(); err != nil {
		s.t.Fatalf("a regain returned %v to the thread's loop, which would end it", err)
	}
}

func (s *scriptedThread) without(prefix string) []string {
	return slices.DeleteFunc(slices.Clone(s.events), func(e string) bool { return strings.HasPrefix(e, prefix) })
}

func started(t *testing.T, s *scriptedThread) *rtGuard {
	t.Helper()
	g := s.guard()
	if err := g.start(); err != nil {
		t.Fatal(err)
	}
	s.events = s.events[:0]
	return g
}

func TestTheGuardLeavesAThreadThatBlocksWithinEveryBudgetAtSCHED_RR(t *testing.T) {
	s := &scriptedThread{t: t}
	g := started(t, s)
	for range 100 {
		s.run(3*scriptedBudget, true)
		if !g.step() {
			t.Fatal("the guard stopped watching a thread that blocks")
		}
	}
	if len(s.events) != 0 {
		t.Fatalf("the guard did %q to a thread that blocks", s.events)
	}
}

func TestTheGuardIgnoresAWakeBeforeHalfABudgetRan(t *testing.T) {
	s := &scriptedThread{t: t}
	g := started(t, s)
	s.run(scriptedBudget/2-time.Microsecond, false)
	if !g.step() || len(s.events) != 0 {
		t.Fatalf("an early wake did %q", s.events)
	}
	s.run(time.Microsecond, false)
	g.step()
	if !slices.Contains(s.events, "demote") {
		t.Fatalf("half a budget without blocking did %q, want a demotion", s.events)
	}
}

func TestTheGuardDemotesAThreadThatRanABudgetWithoutBlockingAndTheThreadRegainsItself(t *testing.T) {
	s := &scriptedThread{t: t, pauses: 100}
	g := started(t, s)
	s.run(scriptedBudget, true)
	g.step()
	s.run(scriptedBudget, false)
	g.step()
	if want := []string{"demote", "arm false", "pause 1s", "hand"}; !slices.Equal(s.without("say "), want) {
		t.Fatalf("events %q, want %q", s.events, want)
	}
	said := s.events[2]
	for _, part := range []string{"the scripted thread ran 20ms", "running outside the kernel", "goroutine 1 [running]", "for 1s"} {
		if !strings.Contains(said, part) {
			t.Fatalf("the demotion said %q, which lacks %q", said, part)
		}
	}
	s.run(5*scriptedBudget, false)
	if !g.step() || len(s.events) != 5 {
		t.Fatalf("the guard acted on a thread it had already demoted: %q", s.events)
	}
	s.events = s.events[:0]
	before := s.switches
	s.regainHanded()
	if want := []string{"raise", "arm true", "say the scripted thread runs at SCHED_RR again"}; !slices.Equal(s.events, want) {
		t.Fatalf("the regain did %q, want %q", s.events, want)
	}
	if s.switches == before {
		t.Fatal("the thread regained SCHED_RR without blocking once after it, so RLIMIT_RTTIME kept the time it ran demoted")
	}
}

func TestTheGuardBacksOffUpToAMinute(t *testing.T) {
	s := &scriptedThread{t: t, pauses: 100}
	g := started(t, s)
	var pauses []string
	for range 9 {
		s.events = s.events[:0]
		s.run(scriptedBudget, false)
		g.step()
		pauses = append(pauses, slices.DeleteFunc(slices.Clone(s.events), func(e string) bool { return !strings.HasPrefix(e, "pause ") })...)
		s.regainHanded()
	}
	want := []string{"pause 1s", "pause 2s", "pause 4s", "pause 8s", "pause 16s", "pause 32s", "pause 1m0s", "pause 1m0s", "pause 1m0s"}
	if !slices.Equal(pauses, want) {
		t.Fatalf("pauses %q, want %q", pauses, want)
	}
}

func TestARefusedRegainLeavesTheThreadAtNormalPriorityForGood(t *testing.T) {
	s := &scriptedThread{t: t, pauses: 100, refuse: true}
	g := started(t, s)
	s.run(scriptedBudget, false)
	g.step()
	s.events = s.events[:0]
	s.regainHanded()
	if len(s.events) != 2 || s.events[0] != "raise" || !strings.Contains(s.events[1], "keeps normal priority") {
		t.Fatalf("a refused regain did %q", s.events)
	}
	s.events = s.events[:0]
	s.run(10*scriptedBudget, false)
	if !g.step() || len(s.events) != 0 || len(s.handed) != 0 {
		t.Fatalf("the guard went on with %q after the refusal", s.events)
	}
}

func TestAStoppedGuardEndsAndNoLateRegainRaisesTheThread(t *testing.T) {
	s := &scriptedThread{t: t}
	g := started(t, s)
	s.run(scriptedBudget, false)
	if g.step() {
		t.Fatal("the guard went on after its pause was cut short")
	}
	g.stop()
	s.events = s.events[:0]
	s.handed = append(s.handed, g.regain)
	s.regainHanded()
	if len(s.events) != 0 {
		t.Fatalf("a regain after the stop did %q", s.events)
	}
	if g.step() {
		t.Fatal("a stopped guard went on")
	}
}

func TestAGuardThatCannotSeeTheThreadDemotesItForGood(t *testing.T) {
	s := &scriptedThread{t: t, pauses: 100}
	g := started(t, s)
	s.unseen = errors.New("status gone")
	s.run(scriptedBudget, true)
	if g.step() {
		t.Fatal("a blind guard went on")
	}
	if want := []string{"demote"}; !slices.Equal(s.without("say "), want) || len(s.handed) != 0 {
		t.Fatalf("a blind guard did %q, want only a demotion", s.events)
	}
}

func TestTheVoluntarySwitchCountIsNotTheInvoluntaryOne(t *testing.T) {
	status := []byte("Name:\tlys-wm\nState:\tR (running)\nnonvoluntary_ctxt_switches:\t7\nvoluntary_ctxt_switches:\t42\n")
	if n, err := voluntarySwitches(status); err != nil || n != 42 {
		t.Fatalf("got %d, %v, want 42", n, err)
	}
	if _, err := voluntarySwitches([]byte("nonvoluntary_ctxt_switches:\t7\n")); err == nil {
		t.Fatal("a status without voluntary_ctxt_switches parsed")
	}
}

func TestSettlingBlocksTheThreadAtLeastOnce(t *testing.T) {
	type got struct {
		before, settled, after int64
		err                    error
	}
	g := onOwnThread(func() (g got) {
		var r unix.Rusage
		if g.err = unix.Getrusage(unix.RUSAGE_THREAD, &r); g.err != nil {
			return g
		}
		g.before = r.Nvcsw
		n, err := settle()
		if g.err = err; err != nil {
			return g
		}
		g.settled = int64(n)
		g.err = unix.Getrusage(unix.RUSAGE_THREAD, &r)
		g.after = r.Nvcsw
		return g
	})
	if g.err != nil {
		t.Fatal(g.err)
	}
	if g.settled <= g.before || g.settled > g.after {
		t.Fatalf("voluntary switches %d before, %d settled, %d after: settle did not block between them", g.before, g.settled, g.after)
	}
}

func processCPUClock(pid int) int32 {
	return int32(^pid)<<3 | 2
}

type watchedSwitches struct {
	changed, same int
	err           error
}

func waitForCPU(clock int32, status string, spent time.Duration) error {
	return onOwnThread(func() error {
		w, err := newCPUWatch(clock, status, spent)
		if err != nil {
			return err
		}
		if err := w.arm(true); err != nil {
			return errors.Join(err, w.Close())
		}
		return errors.Join(w.wait(), w.arm(false), w.Close())
	})
}

func countSwitchChanges(clock int32, status string, checks int) watchedSwitches {
	return onOwnThread(func() (got watchedSwitches) {
		w, err := newCPUWatch(clock, status, time.Millisecond)
		if err != nil {
			return watchedSwitches{err: err}
		}
		defer func() { got.err = errors.Join(got.err, w.Close()) }()
		last, err := w.switches()
		if err != nil {
			return watchedSwitches{err: err}
		}
		if got.err = w.arm(true); got.err != nil {
			return got
		}
		for range checks {
			if got.err = w.wait(); got.err != nil {
				return got
			}
			now, err := w.switches()
			if err != nil {
				got.err = err
				return got
			}
			if now == last {
				got.same++
			} else {
				got.changed++
			}
			last = now
		}
		got.err = w.arm(false)
		return got
	})
}

func TestACPUWatchSeesAProcessThatNeverBlocks(t *testing.T) {
	spin := exec.Command("/bin/sh", "-c", "while :; do :; done")
	if err := spin.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = spin.Process.Kill()
		_ = spin.Wait()
	})
	pid := spin.Process.Pid
	clock, status := processCPUClock(pid), fmt.Sprintf("/proc/%d/status", pid)
	if err := waitForCPU(clock, status, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got := countSwitchChanges(clock, status, 3)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.changed != 0 {
		t.Fatalf("a shell spinning without a system call blocked within %d of 3 CPU budgets", got.changed)
	}
}

func TestACPUWatchSeesAThreadThatBlocksWithinEveryBudget(t *testing.T) {
	tids := make(chan int, 1)
	quit := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		tids <- unix.Gettid()
		for {
			select {
			case <-quit:
				ended <- nil
				return
			default:
			}
			if _, err := settle(); err != nil {
				ended <- err
				return
			}
		}
	}()
	tid := <-tids
	got := countSwitchChanges(threadCPUClock(tid), fmt.Sprintf("/proc/self/task/%d/status", tid), 5)
	close(quit)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.same != 0 {
		t.Fatalf("a thread that blocks every iteration ran %d of 5 CPU budgets without a voluntary switch", got.same)
	}
}

func TestTheGuardReportsTheWatchedGoroutinesStack(t *testing.T) {
	header := goroutineHeader()
	got := string(goroutineStack(header))
	if !strings.HasPrefix(got, string(header[1:])) || !strings.Contains(got, "TestTheGuardReportsTheWatchedGoroutinesStack") {
		t.Fatalf("the stack of %q is %q", header, got)
	}
}

func TestAThreadRaisedThroughRtkitThatNeverBlocksIsDemotedAndRegainsRealtimeItself(t *testing.T) {
	requireStandIn(t)
	withoutRTPrio(t)
	bus, asked := rtkitStandingInWithBatch(t)
	jobs := make(chan func() error, 4)
	hand := func(_ string, job func() error) { jobs <- job }
	type spinner struct {
		tid  int
		skip string
		err  error
	}
	up := make(chan spinner, 1)
	quit := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		how, restore, err := compositorThread.raisePriority(bus, hand)
		if err != nil || how != "SCHED_RR through rtkit" {
			up <- spinner{skip: how, err: errors.Join(err, restore())}
			return
		}
		up <- spinner{tid: unix.Gettid()}
		zero, err := os.Open("/dev/zero")
		if err != nil {
			ended <- errors.Join(err, restore())
			return
		}
		buf := make([]byte, 16<<20)
		for {
			select {
			case job := <-jobs:
				err = job()
			case <-quit:
				ended <- errors.Join(restore(), zero.Close())
				return
			default:
				_, err = zero.Read(buf)
			}
			if err != nil {
				ended <- errors.Join(err, restore(), zero.Close())
				return
			}
		}
	}()
	s := <-up
	if s.err != nil {
		t.Fatal(s.err)
	}
	if s.skip != "" {
		t.Skipf("the thread took %s, not SCHED_RR through rtkit", s.skip)
	}
	first := <-asked
	guard := <-asked
	if first.tid != s.tid || guard.tid == s.tid || guard.priority != guardRTPriority {
		t.Fatalf("rtkit was asked for %+v then %+v, want the thread %d then a guard at %d", first, guard, s.tid, guardRTPriority)
	}
	select {
	case again := <-asked:
		if again.tid != s.tid || again.priority != loopRTPriority || again.policy != unix.SCHED_NORMAL {
			t.Errorf("rtkit was asked again for %+v, want thread %d at %d from SCHED_NORMAL", again, s.tid, loopRTPriority)
		}
	case <-time.After(testenv.HangLimit):
		t.Errorf("a thread reading /dev/zero without blocking was not demoted and raised again within %v", testenv.HangLimit)
	}
	close(quit)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
}
