package backend

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCommitsStallOnlyAfterFailingForTheWholeBound(t *testing.T) {
	busy := fmt.Errorf("commit: %w", unix.EBUSY)
	var c Commits
	start := time.Hour
	for _, at := range []time.Duration{0, StallAfter / 2, StallAfter - time.Nanosecond} {
		if stall := c.Failed("DP-1", start+at, busy); stall != nil || c.Stalled() {
			t.Fatalf("stalled %v after failing for %v: %v", c.Stalled(), at, stall)
		}
	}
	stall := c.Failed("DP-1", start+StallAfter, busy)
	if !c.Stalled() || !errors.Is(stall, unix.EBUSY) {
		t.Fatalf("after failing for %v: stalled %v, %v", StallAfter, c.Stalled(), stall)
	}
	if again := c.Failed("DP-1", start+2*StallAfter, busy); again != nil || !c.Stalled() {
		t.Fatalf("a stalled output reported its stall again: %v", again)
	}
	if recovery := c.Took("DP-1"); recovery == nil || c.Stalled() {
		t.Fatalf("a commit that took left it stalled %v, reported %v", c.Stalled(), recovery)
	}
	if recovery := c.Took("DP-1"); recovery != nil {
		t.Fatalf("a second commit that took reported %v", recovery)
	}
}

func TestCommitsStreakEndsWithACommitThatTakes(t *testing.T) {
	var c Commits
	if c.Failed("DP-1", 0, unix.EBUSY) != nil || c.Took("DP-1") != nil {
		t.Fatal("one refused commit stalled the output")
	}
	if stall := c.Failed("DP-1", 2*StallAfter, unix.EBUSY); stall != nil || c.Stalled() {
		t.Fatalf("a refused commit after one that took counted the earlier streak: %v", stall)
	}
}

func TestCommitsIgnoreALostDevice(t *testing.T) {
	var c Commits
	lost := fmt.Errorf("commit: %w", ErrDeviceLost)
	for _, at := range []time.Duration{0, 2 * StallAfter} {
		if stall := c.Failed("DP-1", at, lost); stall != nil || c.Stalled() {
			t.Fatalf("a lost device stalled the output: %v", stall)
		}
	}
}
