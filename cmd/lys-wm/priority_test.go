package main

import (
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

func TestARealtimeTimeoutDemotesTheThreadToNormal(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		batch := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_BATCH, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
		if err := unix.SchedSetAttr(0, &batch, 0); err != nil {
			done <- err
			return
		}
		release := demoteOnRTTimeout(unix.Gettid())
		defer release()
		if err := unix.Kill(unix.Getpid(), unix.SIGXCPU); err != nil {
			done <- err
			return
		}
		for {
			attr, err := unix.SchedGetAttr(0, 0)
			if err != nil || attr.Policy == unix.SCHED_NORMAL {
				done <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
