package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestTheCompositorThreadReadsBackThePriorityItTook(t *testing.T) {
	type outcome struct {
		how    string
		err    error
		before uint32
		policy uint32
		flags  uint64
		nice   int32
		attr   error
	}
	noBus := "unix:path=" + filepath.Join(t.TempDir(), "no-bus")
	done := make(chan outcome, 1)
	go func() {
		runtime.LockOSThread()
		var o outcome
		before, err := unix.SchedGetAttr(0, 0)
		if err != nil {
			o.attr = err
			done <- o
			return
		}
		o.before = before.Policy
		var release func()
		o.how, release, o.err = raiseThreadPriority(noBus)
		release()
		attr, err := unix.SchedGetAttr(0, 0)
		o.attr = err
		if err == nil {
			o.policy, o.flags, o.nice = attr.Policy, attr.Flags, attr.Nice
		}
		done <- o
	}()
	o := <-done
	if o.attr != nil {
		t.Fatal(o.attr)
	}
	switch {
	case o.err != nil:
		t.Logf("refused here: %v", o.err)
		if o.policy != o.before {
			t.Fatalf("a refused raise moved the thread from policy %d to %d", o.before, o.policy)
		}
		if !strings.Contains(o.err.Error(), "90-lys-wm.conf") || !strings.Contains(o.err.Error(), "rtkit") {
			t.Fatalf("the refusal does not say how to grant the priority: %v", o.err)
		}
	case o.how == "SCHED_RR":
		if o.policy != unix.SCHED_RR || o.flags&unix.SCHED_FLAG_RESET_ON_FORK == 0 {
			t.Fatalf("policy %d flags %#x, want SCHED_RR that resets on fork", o.policy, o.flags)
		}
	default:
		if o.nice != loopNice || o.flags&unix.SCHED_FLAG_RESET_ON_FORK == 0 {
			t.Fatalf("nice %d flags %#x, want %d that resets on fork", o.nice, o.flags, loopNice)
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

func TestARealtimeTimeoutDemotesTheThreadThenAsksRtkitAgain(t *testing.T) {
	type probe struct {
		reason string
		err    error
	}
	probed := make(chan probe, 1)
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		start, err := unix.SchedGetAttr(0, 0)
		if err != nil {
			probed <- probe{err: err}
			return
		}
		batch := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_BATCH, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
		reason, err := demotionUnavailable(start.Policy, unix.SchedSetAttr(0, &batch, 0))
		probed <- probe{reason, err}
		if reason != "" || err != nil {
			return
		}
		tid := unix.Gettid()
		asked := make(chan uint32, 2)
		release := demoteOnRTTimeout(tid, func() error {
			attr, err := unix.SchedGetAttr(tid, 0)
			if err != nil {
				return err
			}
			asked <- attr.Policy
			return nil
		}, time.Millisecond)
		defer release()
		for round := range 2 {
			if err := unix.SchedSetAttr(0, &batch, 0); err != nil {
				done <- err
				return
			}
			if err := unix.Kill(unix.Getpid(), unix.SIGXCPU); err != nil {
				done <- err
				return
			}
			select {
			case policy := <-asked:
				if policy != unix.SCHED_NORMAL {
					done <- fmt.Errorf("round %d: rtkit was asked again while the thread ran at policy %d, want SCHED_NORMAL first", round, policy)
					return
				}
			case <-time.After(10 * time.Second):
				done <- fmt.Errorf("round %d: rtkit was not asked again within 10s of SIGXCPU", round)
				return
			}
		}
		done <- nil
	}()
	p := <-probed
	if p.err != nil {
		t.Fatal(p.err)
	}
	if p.reason != "" {
		t.Skip(p.reason)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
