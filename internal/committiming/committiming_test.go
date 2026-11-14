package committiming

import (
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/loop"
)

func manualLoop(t *testing.T, start time.Duration) (*loop.Loop, *loop.Manual) {
	t.Helper()
	l, clock, err := loop.NewManual(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, clock
}

func TestAQueuedCommitTargetIsReused(t *testing.T) {
	l, _ := manualLoop(t, time.Second)
	clock, err := l.AddTimer(func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	tm := &timer{m: &Manager{loop: l}, clock: clock}
	cycle := func() {
		tm.targets = append(tm.targets, tm.newTarget(2*time.Second))
		if err := tm.arm(); err != nil {
			t.Error(err)
		}
		if err := tm.targets[0].applied(); err != nil {
			t.Error(err)
		}
	}
	cycle()
	if n := testing.AllocsPerRun(100, cycle); n != 0 {
		t.Fatalf("a commit target cycle allocated %v times", n)
	}
}

func TestATimerArmsOnlyWhenItsDeadlineMoves(t *testing.T) {
	l, manual := manualLoop(t, time.Second)
	clock, err := l.AddTimer(func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	tm := &timer{m: &Manager{loop: l}, clock: clock}
	tm.targets = append(tm.targets, tm.newTarget(3*time.Second))
	if err := tm.arm(); err != nil {
		t.Fatal(err)
	}
	if tm.armedFor != 3*time.Second {
		t.Fatalf("armed for %v", tm.armedFor)
	}
	if at, ok := manual.NextFrame(); !ok || at != 3*time.Second {
		t.Fatalf("the target armed a frame due at %v (%v), want a frame at the target, %v", at, ok, 3*time.Second)
	}
	tm.targets = append(tm.targets, tm.newTarget(2*time.Second))
	if err := tm.arm(); err != nil {
		t.Fatal(err)
	}
	if tm.armedFor != 2*time.Second {
		t.Fatalf("an earlier target left the timer armed for %v", tm.armedFor)
	}
	tm.targets = nil
	if err := tm.arm(); err != nil {
		t.Fatal(err)
	}
	if tm.armedFor != 0 {
		t.Fatalf("no targets left the timer armed for %v", tm.armedFor)
	}
}
