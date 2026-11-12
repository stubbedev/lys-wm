package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/rtkit"
)

const (
	loopRTPriority = 1
	loopNice       = -10

	rtRegainFirstWait = time.Second
	rtRegainMostWait  = time.Minute
)

type thread string

const (
	compositorThread thread = "the compositor thread"
	inputThread      thread = "the input thread"
)

func (name thread) raise(systemBus string) (restore func() error) {
	how, restore, err := name.raisePriority(systemBus)
	if how != "" {
		fmt.Fprintf(os.Stderr, "lys-wm: %s runs at %s\n", name, how)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lys-wm: %v\n", err)
	}
	return restore
}

func (name thread) raisePriority(systemBus string) (how string, restore func() error, err error) {
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
	rr := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_RR, Priority: loopRTPriority, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
	rrErr := unix.SchedSetAttr(0, &rr, 0)
	if rrErr == nil {
		return "SCHED_RR", giveBack, nil
	}
	errs := []error{fmt.Errorf("SCHED_RR: %w", rrErr)}
	kit, kitErr := rtkit.Open(systemBus)
	if kitErr != nil {
		errs = append(errs, kitErr)
	} else {
		defer func() { err = errors.Join(err, kit.Close()) }()
		kitErr = kit.Realtime(tid, loopRTPriority)
		if kitErr == nil {
			stop := name.demoteOnRTTimeout(tid, regainThroughRtkit(systemBus, tid), rtRegainFirstWait)
			return "SCHED_RR through rtkit", func() error {
				stop()
				return giveBack()
			}, nil
		}
		errs = append(errs, kitErr)
	}
	nice := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_NORMAL, Nice: loopNice, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
	niceErr := unix.SchedSetAttr(0, &nice, 0)
	if niceErr == nil {
		return fmt.Sprintf("nice %d", loopNice), giveBack, nil
	}
	errs = append(errs, fmt.Errorf("nice %d: %w", loopNice, niceErr))
	if kit != nil {
		kitErr = kit.HighPriority(tid, loopNice)
		if kitErr == nil {
			return fmt.Sprintf("nice %d through rtkit", loopNice), giveBack, nil
		}
		errs = append(errs, kitErr)
	}
	return "", giveBack, fmt.Errorf("%s keeps its normal priority, as it needs an rtprio or nice limit (packaging/limits.d/90-lys-wm.conf), CAP_SYS_NICE or rtkit: %w", name, errors.Join(errs...))
}

func regainThroughRtkit(systemBus string, tid int) func() error {
	return func() error {
		kit, err := rtkit.Open(systemBus)
		if err != nil {
			return err
		}
		return errors.Join(kit.Realtime(tid, loopRTPriority), kit.Close())
	}
}

func (name thread) demoteOnRTTimeout(tid int, regain func() error, firstWait time.Duration) func() {
	xcpu := make(chan os.Signal, 1)
	done := make(chan struct{})
	stopped := make(chan struct{})
	signal.Notify(xcpu, unix.SIGXCPU)
	go func() {
		defer close(stopped)
		wait := firstWait
		for {
			select {
			case <-xcpu:
			case <-done:
				return
			}
			if err := setPolicyKeepingNice(tid, unix.SCHED_NORMAL); err != nil {
				fmt.Fprintf(os.Stderr, "lys-wm: a realtime thread ran past rtkit's RLIMIT_RTTIME and %s could not leave SCHED_RR: %v\n", name, err)
				return
			}
			fmt.Fprintf(os.Stderr, "lys-wm: a realtime thread ran past rtkit's RLIMIT_RTTIME without blocking, so %s runs at normal priority for %v\n", name, wait)
			pause := time.NewTimer(wait)
			select {
			case <-pause.C:
			case <-done:
				pause.Stop()
				return
			}
			select {
			case <-xcpu:
			default:
			}
			if err := regain(); err != nil {
				fmt.Fprintf(os.Stderr, "lys-wm: %s keeps normal priority, as rtkit refused SCHED_RR again: %v\n", name, err)
				return
			}
			fmt.Fprintf(os.Stderr, "lys-wm: %s runs at SCHED_RR through rtkit again\n", name)
			wait = min(2*wait, rtRegainMostWait)
		}
	}()
	return func() {
		signal.Stop(xcpu)
		close(done)
		<-stopped
	}
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
