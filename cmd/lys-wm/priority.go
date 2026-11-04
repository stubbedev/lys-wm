package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	loopRTPriority = 1
	loopNice       = -10
)

func raiseThreadPriority() (string, error) {
	rr := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_RR, Priority: loopRTPriority, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
	rrErr := unix.SchedSetAttr(0, &rr, 0)
	if rrErr == nil {
		return "SCHED_RR", nil
	}
	nice := unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_NORMAL, Nice: loopNice, Flags: unix.SCHED_FLAG_RESET_ON_FORK}
	niceErr := unix.SchedSetAttr(0, &nice, 0)
	if niceErr == nil {
		return fmt.Sprintf("nice %d", loopNice), nil
	}
	return "", fmt.Errorf("the compositor thread keeps its normal priority, as SCHED_RR and nice both need a limit or CAP_SYS_NICE: %w", errors.Join(rrErr, niceErr))
}
