package loop

import (
	"errors"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newManual(t *testing.T, start time.Duration) (*Loop, *Manual) {
	t.Helper()
	l, clock, err := NewManual(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return l, clock
}

func armed(t *testing.T, l *Loop, d time.Duration, handler func() error) *Timer {
	t.Helper()
	timer, err := l.AddTimer(handler)
	if err != nil {
		t.Fatal(err)
	}
	if err := timer.Arm(d); err != nil {
		t.Fatal(err)
	}
	return timer
}

func TestAManualLoopFiresATimerWhenItsClockIsSetToIt(t *testing.T) {
	l, clock := newManual(t, time.Hour)
	var fired []time.Duration
	for _, d := range []time.Duration{3 * time.Hour, time.Hour, 2 * time.Hour} {
		armed(t, l, d, func() error {
			fired = append(fired, clock.Now())
			return nil
		})
	}
	dry := 0
	clock.WhenDry(func() {
		dry++
		next, ok := clock.NextTimer()
		if !ok {
			l.Stop()
			return
		}
		clock.Set(next)
	})
	run(t, l)
	if want := []time.Duration{2 * time.Hour, 3 * time.Hour, 4 * time.Hour}; !slices.Equal(fired, want) || dry != len(want)+1 {
		t.Fatalf("timers one, two and three hours out fired at %v after the loop ran dry %d times, want %v, each after the loop ran dry", fired, dry, want)
	}
}

func TestAManualLoopRunsWhatIsReadyBeforeItIsDry(t *testing.T) {
	l, clock := newManual(t, time.Second)
	r, w := pipe(t)
	var log []string
	if _, err := l.AddFD(r, Readable, func(Events) error {
		var buf [8]byte
		_, err := unix.Read(r, buf[:])
		log = append(log, "source")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Write(w, []byte("x")); err != nil {
		t.Fatal(err)
	}
	l.Idle(func() error {
		log = append(log, "idle")
		return nil
	})
	armed(t, l, 0, func() error {
		log = append(log, "a timer armed for no time")
		return nil
	})
	armed(t, l, time.Minute, func() error {
		log = append(log, "a timer the clock was set past")
		return nil
	})
	armed(t, l, time.Hour, func() error { return errors.New("a timer fired ahead of the clock") })
	clock.Set(clock.Now() + 2*time.Minute)
	clock.WhenDry(func() {
		log = append(log, "dry")
		l.Stop()
	})
	run(t, l)
	if want := []string{"idle", "source", "a timer armed for no time", "a timer the clock was set past", "dry"}; !slices.Equal(log, want) {
		t.Fatalf("the loop ran %q, want %q", log, want)
	}
}

func TestADryManualLoopWaitsForAPostAndNotForATimer(t *testing.T) {
	l, clock := newManual(t, time.Second)
	armed(t, l, time.Nanosecond, func() error { return errors.New("a timer fired on a clock nobody moved") })
	dry := 0
	clock.WhenDry(func() {
		if dry++; dry > 1 {
			return
		}
		go func() {
			_ = l.Post(func() error {
				l.Stop()
				return nil
			})
		}()
	})
	run(t, l)
	if dry != 1 || clock.Now() != time.Second {
		t.Fatalf("the loop ran dry %d times and its clock reads %v, want once, at the second it started on", dry, clock.Now())
	}
}
