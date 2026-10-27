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
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.PowerChanged() })
	expect("unplugging AC with the lid closed", 2)
	lid(false)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.PowerChanged() })
	expect("a power change with the lid open", 2)
}

func TestWithoutALidPolicyTheLidNeverSuspends(t *testing.T) {
	h := StartWith(t, func(*compositor.Config) {})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddNamedOutput("eDP-1", small)
		return err
	})
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		return errors.Join(b.Switches().Toggle(backend.SwitchLid, true), c.PowerChanged())
	})
}
