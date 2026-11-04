package main

import (
	"runtime"
	"testing"

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
		o.how, o.err = raiseThreadPriority()
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
