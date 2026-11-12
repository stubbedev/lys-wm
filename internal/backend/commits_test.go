package backend

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var errBusy = fmt.Errorf("commit: %w", unix.EBUSY)

func stalledAt(t *testing.T, start time.Duration) *Commits {
	t.Helper()
	var c Commits
	if c.Failed("DP-1", start, errBusy) != nil || c.Failed("DP-1", start+SustainedFor, errBusy) == nil {
		t.Fatalf("failing for %v did not stall the output", SustainedFor)
	}
	return &c
}

func TestCommitsStallOnlyAfterFailingForTheWholeBound(t *testing.T) {
	var c Commits
	start := time.Hour
	for _, at := range []time.Duration{0, SustainedFor / 2, SustainedFor - time.Nanosecond} {
		if stall := c.Failed("DP-1", start+at, errBusy); stall != nil || c.Stalled() {
			t.Fatalf("stalled %v after failing for %v: %v", c.Stalled(), at, stall)
		}
	}
	stall := c.Failed("DP-1", start+SustainedFor, errBusy)
	if !c.Stalled() || !errors.Is(stall, unix.EBUSY) {
		t.Fatalf("after failing for %v: stalled %v, %v", SustainedFor, c.Stalled(), stall)
	}
	if again := c.Failed("DP-1", start+2*SustainedFor, errBusy); again != nil || !c.Stalled() {
		t.Fatalf("a stalled output reported its stall again: %v", again)
	}
}

func TestCommitsRecoverOnlyAfterTakingForTheWholeBound(t *testing.T) {
	start := time.Hour
	c := stalledAt(t, start)
	took := start + 2*SustainedFor
	for _, at := range []time.Duration{0, SustainedFor / 2, SustainedFor - time.Nanosecond} {
		if recovery := c.Took("DP-1", took+at); recovery != nil || !c.Stalled() {
			t.Fatalf("recovered %v after taking commits for %v: %v", !c.Stalled(), at, recovery)
		}
	}
	recovery := c.Took("DP-1", took+SustainedFor)
	if c.Stalled() || recovery == nil || !strings.Contains(recovery.Error(), "DP-1 has taken every commit for 1s") {
		t.Fatalf("after taking commits for %v: stalled %v, %v", SustainedFor, c.Stalled(), recovery)
	}
	if again := c.Took("DP-1", took+2*SustainedFor); again != nil {
		t.Fatalf("a recovered output reported its recovery again: %v", again)
	}
}

func TestCommitsThatFailForTheBoundAndTakeOnceStayStalled(t *testing.T) {
	start := time.Hour
	c := stalledAt(t, start)
	at := start + SustainedFor
	for range 10 {
		at += time.Millisecond
		if recovery := c.Took("DP-1", at); recovery != nil || !c.Stalled() {
			t.Fatalf("one commit that took between failing runs recovered the output: %v", recovery)
		}
		for _, failing := range []time.Duration{time.Millisecond, SustainedFor} {
			if stall := c.Failed("DP-1", at+failing, errBusy); stall != nil || !c.Stalled() {
				t.Fatalf("a failing run after one commit that took: stalled %v, reported %v", c.Stalled(), stall)
			}
		}
		at += SustainedFor
	}
}

func TestCommitsStreakEndsWithACommitThatTakes(t *testing.T) {
	var c Commits
	if c.Failed("DP-1", 0, unix.EBUSY) != nil || c.Took("DP-1", 0) != nil {
		t.Fatal("one refused commit stalled the output")
	}
	if stall := c.Failed("DP-1", 2*SustainedFor, unix.EBUSY); stall != nil || c.Stalled() {
		t.Fatalf("a refused commit after one that took counted the earlier streak: %v", stall)
	}
}

func TestCommitsIgnoreALostDevice(t *testing.T) {
	var c Commits
	lost := fmt.Errorf("commit: %w", ErrDeviceLost)
	for _, at := range []time.Duration{0, 2 * SustainedFor} {
		if stall := c.Failed("DP-1", at, lost); stall != nil || c.Stalled() {
			t.Fatalf("a lost device stalled the output: %v", stall)
		}
	}
}
