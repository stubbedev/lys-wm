package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/rtkit"
)

const (
	loopRTPriority  = 1
	guardRTPriority = loopRTPriority + 1
	loopNice        = -10

	rtChecksPerLimit = 10

	rtRegainFirstWait = time.Second
	rtRegainMostWait  = time.Minute
)

type thread string

const (
	compositorThread thread = "the compositor thread"
	inputThread      thread = "the input thread"
)

type handFunc func(what string, job func() error)

type realtime func(tid int, priority uint32) error

func schedRR(tid int, priority uint32) error {
	rr := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_RR, Priority: priority, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
	if err := unix.SchedSetAttr(tid, &rr, 0); err != nil {
		return fmt.Errorf("SCHED_RR %d for thread %d: %w", priority, tid, err)
	}
	return nil
}

func throughRtkit(systemBus string) realtime {
	return func(tid int, priority uint32) error {
		kit, err := rtkit.Open(systemBus)
		if err != nil {
			return err
		}
		return errors.Join(kit.Realtime(tid, priority), kit.Close())
	}
}

func (name thread) raise(systemBus string, hand handFunc) (restore func() error) {
	how, restore, err := name.raisePriority(systemBus, hand)
	if how != "" {
		fmt.Fprintf(os.Stderr, "lys-wm: %s runs at %s\n", name, how)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lys-wm: %v\n", err)
	}
	return restore
}

func (name thread) raisePriority(systemBus string, hand handFunc) (how string, restore func() error, err error) {
	tid := unix.Gettid()
	was, err := threadSchedule(tid)
	if err != nil {
		return "", func() error { return nil }, fmt.Errorf("%s keeps its priority: %w", name, err)
	}
	giveBack := func() error {
		if err := setSchedule(tid, was); err != nil {
			return fmt.Errorf("give %s back its scheduling: %w", name, err)
		}
		return nil
	}
	guardRaise := func(guard int) error {
		direct := schedRR(guard, guardRTPriority)
		if direct == nil {
			return nil
		}
		if kitErr := throughRtkit(systemBus)(guard, guardRTPriority); kitErr != nil {
			return errors.Join(direct, kitErr)
		}
		return nil
	}
	var errs []error
	for _, rt := range []struct {
		how   string
		raise realtime
	}{
		{"SCHED_RR", schedRR},
		{"SCHED_RR through rtkit", throughRtkit(systemBus)},
	} {
		stop, err := name.realtime(tid, was, rt.raise, guardRaise, hand)
		if err == nil {
			return rt.how, func() error {
				stop()
				return giveBack()
			}, nil
		}
		errs = append(errs, err)
	}
	nice := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_NORMAL, Nice: loopNice, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
	niceErr := unix.SchedSetAttr(0, &nice, 0)
	if niceErr == nil {
		return fmt.Sprintf("nice %d", loopNice), giveBack, nil
	}
	errs = append(errs, fmt.Errorf("nice %d: %w", loopNice, niceErr))
	kit, kitErr := rtkit.Open(systemBus)
	if kitErr == nil {
		kitErr = errors.Join(kit.HighPriority(tid, loopNice), kit.Close())
		if kitErr == nil {
			return fmt.Sprintf("nice %d through rtkit", loopNice), giveBack, nil
		}
	}
	errs = append(errs, kitErr)
	return "", giveBack, fmt.Errorf("%s keeps its normal priority, as it needs an rtprio or nice limit (packaging/limits.d/90-lys-wm.conf), CAP_SYS_NICE or rtkit: %w", name, errors.Join(errs...))
}

func (name thread) realtime(tid int, was unix.SchedAttr, raise realtime, guardRaise func(guard int) error, hand handFunc) (stop func(), err error) {
	if err := raise(tid, loopRTPriority); err != nil {
		return nil, err
	}
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTTIME, &limit); err != nil {
		return nil, errors.Join(fmt.Errorf("read RLIMIT_RTTIME: %w", err), setSchedule(tid, was))
	}
	if limit.Max == unix.RLIM_INFINITY {
		return func() {}, nil
	}
	stop, err = startRTGuard(name, tid, rtBudget(limit.Max), func() error { return raise(tid, loopRTPriority) }, guardRaise, hand)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s may not run at SCHED_RR unguarded under a hard RLIMIT_RTTIME of %d µs: %w", name, limit.Max, err), setSchedule(tid, was))
	}
	return stop, nil
}

func rtBudget(hardMicros uint64) time.Duration {
	return time.Duration(hardMicros/rtChecksPerLimit) * time.Microsecond //nolint:gosec // a finite RLIMIT_RTTIME in µs over ten fits a Duration
}

func threadSchedule(tid int) (unix.SchedAttr, error) {
	attr, err := unix.SchedGetAttr(tid, 0)
	if err != nil {
		return unix.SchedAttr{}, fmt.Errorf("read the scheduling of thread %d: %w", tid, err)
	}
	prio, err := unix.Getpriority(unix.PRIO_PROCESS, tid)
	if err != nil {
		return unix.SchedAttr{}, fmt.Errorf("read the nice value of thread %d: %w", tid, err)
	}
	return unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: attr.Policy, Priority: attr.Priority, Nice: int32(20 - prio)}, nil //nolint:gosec // getpriority returns 20 minus a nice value in [-20, 19]
}

func setSchedule(tid int, attr unix.SchedAttr) error {
	attr.Flags = unix.SCHED_FLAG_RESET_ON_FORK
	if err := unix.SchedSetAttr(tid, &attr, 0); err != nil {
		return fmt.Errorf("move thread %d to policy %d, priority %d, nice %d: %w", tid, attr.Policy, attr.Priority, attr.Nice, err)
	}
	return nil
}

func setPolicyKeepingNice(tid int, policy uint32) error {
	attr, err := threadSchedule(tid)
	if err != nil {
		return err
	}
	attr.Policy, attr.Priority = policy, 0
	return setSchedule(tid, attr)
}
