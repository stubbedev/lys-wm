package harness

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/loop"
)

func TestAStartWithTheLidClosedAppliesTheLidPolicy(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		closed  bool
		battery bool
		outputs []string
		want    int
	}{
		{"closed, undocked, on battery", true, true, []string{"eDP-1"}, 1},
		{"closed, undocked, on AC", true, false, []string{"eDP-1"}, 0},
		{"closed, docked, on battery", true, true, []string{"eDP-1", "DP-1"}, 0},
		{"open, undocked, on battery", false, true, []string{"eDP-1"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			suspends := 0
			h := start(t, setup{
				open: func(l *loop.Loop) (*headless.Backend, error) {
					b := headless.New(l)
					for _, name := range c.outputs {
						if _, err := b.AddNamedOutput(name, small); err != nil {
							return nil, err
						}
					}
					return b, nil
				},
				configure: func(cfg *compositor.Config) {
					cfg.LidClosedAtStart = c.closed
					cfg.Hooks.Lid = compositor.LidPolicy{
						Suspend: func() error {
							suspends++
							return nil
						},
						OnBattery: func() (bool, error) { return c.battery, nil },
					}
				},
			})
			lidSettled(h)
			h.Do(func(*headless.Backend, *compositor.Compositor) error {
				if suspends != c.want {
					t.Errorf("%d suspends after the start, want %d", suspends, c.want)
				}
				return nil
			})
		})
	}
}
