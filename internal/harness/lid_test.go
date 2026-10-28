package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
)

func TestTheLidSuspendsOnlyAnUndockedLaptopOnBattery(t *testing.T) {
	suspends, battery := 0, true
	h := StartWith(t, func(c *compositor.Config) {
		c.Hooks.Lid = compositor.LidPolicy{
			Suspend: func() error {
				suspends++
				return nil
			},
			OnBattery: func() (bool, error) { return battery, nil },
		}
	})
	var external *headless.Output
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if _, err := b.AddNamedOutput("eDP-1", small); err != nil {
			return err
		}
		var err error
		external, err = b.AddNamedOutput("DP-1", small)
		return err
	})
	lid := func(closed bool) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return b.Switches().Toggle(backend.SwitchLid, closed)
		})
	}
	expect := func(when string, want int) {
		t.Helper()
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			if suspends != want {
				t.Errorf("%s: %d suspends, want %d", when, suspends, want)
			}
			return nil
		})
	}

	lid(true)
	expect("closing the lid with an external output", 0)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return b.RemoveOutput(external) })
	expect("unplugging the external output with the lid closed, on battery", 1)

	lid(false)
	battery = false
	lid(true)
	expect("closing the lid undocked on AC", 1)
	battery = true
	powerChanged(h)
	expect("unplugging AC with the lid closed", 2)
	lid(false)
	powerChanged(h)
	expect("a power change with the lid open", 2)
}

func TestWithoutALidPolicyTheLidNeverSuspends(t *testing.T) {
	h := StartWith(t, func(*compositor.Config) {})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddNamedOutput("eDP-1", small)
		return err
	})
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		err := b.Switches().Toggle(backend.SwitchLid, true)
		c.PowerChanged()
		return err
	})
}

func TestARefusedSuspendIsReportedAndTheSessionGoesOn(t *testing.T) {
	var r reports
	refusal := errors.New("session: logind refused to suspend: org.freedesktop.login1.OperationInProgress: There's already a shutdown or sleep operation in progress")
	suspends := 0
	h := StartWith(t, func(c *compositor.Config) {
		c.Report = r.report
		c.Hooks.Lid = compositor.LidPolicy{
			Suspend: func() error {
				suspends++
				return refusal
			},
			OnBattery: func() (bool, error) { return true, nil },
		}
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if _, err := b.AddNamedOutput("eDP-1", small); err != nil {
			return err
		}
		return b.Switches().Toggle(backend.SwitchLid, true)
	})
	powerChanged(h)
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if suspends != 2 {
			t.Errorf("%d suspends, want one for the lid closing and one for the power change", suspends)
		}
		got := r.take()
		if len(got) != 2 || !errors.Is(got[0], refusal) || !errors.Is(got[1], refusal) {
			t.Errorf("reports %v, want the refusal twice", got)
		}
		return nil
	})
}

func powerChanged(h *Harness) {
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		c.PowerChanged()
		return nil
	})
}

func TestAPowerReadErrorIsReportedAndTheLidDoesNotSuspend(t *testing.T) {
	var r reports
	failure := errors.New("session: read the power supplies under /sys: read class/power_supply/AC/online: no such device")
	suspends := 0
	h := StartWith(t, func(c *compositor.Config) {
		c.Report = r.report
		c.Hooks.Lid = compositor.LidPolicy{
			Suspend: func() error {
				suspends++
				return nil
			},
			OnBattery: func() (bool, error) { return false, failure },
		}
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if _, err := b.AddNamedOutput("eDP-1", small); err != nil {
			return err
		}
		return b.Switches().Toggle(backend.SwitchLid, true)
	})
	powerChanged(h)
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if suspends != 0 {
			t.Errorf("%d suspends with the power state unknown, want none", suspends)
		}
		got := r.take()
		if len(got) != 2 || !errors.Is(got[0], failure) || !errors.Is(got[1], failure) {
			t.Errorf("reports %v, want the power read failure for the lid closing and for the power change", got)
		}
		return nil
	})
}
