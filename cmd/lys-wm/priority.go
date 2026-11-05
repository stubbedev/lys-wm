package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/rtkit"
)

const (
	loopRTPriority = 1
	loopNice       = -10
)

func keep() {}

func raiseThreadPriority(systemBus string) (how string, release func(), err error) {
	tid := unix.Gettid()
	rr := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_RR, Priority: loopRTPriority, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
	rrErr := unix.SchedSetAttr(0, &rr, 0)
	if rrErr == nil {
		return "SCHED_RR", keep, nil
	}
	errs := []error{fmt.Errorf("SCHED_RR: %w", rrErr)}
	kit, kitErr := rtkit.Open(systemBus)
	if kitErr != nil {
		errs = append(errs, kitErr)
	} else {
		defer func() { err = errors.Join(err, kit.Close()) }()
		kitErr = kit.Realtime(tid, loopRTPriority)
		if kitErr == nil {
			return "SCHED_RR through rtkit", demoteOnRTTimeout(tid), nil
		}
		errs = append(errs, kitErr)
	}
	nice := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_NORMAL, Nice: loopNice, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
	niceErr := unix.SchedSetAttr(0, &nice, 0)
	if niceErr == nil {
		return fmt.Sprintf("nice %d", loopNice), keep, nil
	}
	errs = append(errs, fmt.Errorf("nice %d: %w", loopNice, niceErr))
	if kit != nil {
		kitErr = kit.HighPriority(tid, loopNice)
		if kitErr == nil {
			return fmt.Sprintf("nice %d through rtkit", loopNice), keep, nil
		}
		errs = append(errs, kitErr)
	}
	return "", keep, fmt.Errorf("the compositor thread keeps its normal priority, as it needs an rtprio or nice limit (packaging/limits.d/90-lys-wm.conf), CAP_SYS_NICE or rtkit: %w", errors.Join(errs...))
}

func demoteOnRTTimeout(tid int) func() {
	xcpu := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(xcpu, unix.SIGXCPU)
	go func() {
		select {
		case <-xcpu:
			normal := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_NORMAL, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
			if err := unix.SchedSetAttr(tid, &normal, 0); err != nil {
				fmt.Fprintf(os.Stderr, "lys-wm: the compositor thread ran past rtkit's RLIMIT_RTTIME and could not leave SCHED_RR: %v\n", err)
				return
			}
			fmt.Fprintln(os.Stderr, "lys-wm: the compositor thread ran past rtkit's RLIMIT_RTTIME without blocking, so it runs at normal priority from now on")
		case <-done:
		}
	}()
	return func() {
		signal.Stop(xcpu)
		close(done)
	}
}
