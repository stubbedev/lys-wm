package harness

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func failCommitsOn(t *testing.T, h *Harness, name string, err error) {
	t.Helper()
	i := outputIndex(t, h, name)
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		o := b.Outputs()[i]
		o.FailPresents(err)
		return c.OutputDamaged(o)
	})
}

func expectDroppedFrames(t *testing.T, got *reports, name string) {
	t.Helper()
	for _, err := range got.take() {
		dropped := errors.Is(err, unix.EBUSY) && strings.Contains(err.Error(), "frame on "+name)
		if !dropped && !strings.Contains(err.Error(), name+" draws again") {
			t.Errorf("reported %v, want only the dropped frames of %s and their end", err, name)
		}
	}
}

func TestWithTheLidClosedAnExternalOutputThatStopsTakingCommitsBringsThePanelBack(t *testing.T) {
	t.Parallel()
	got := &reports{}
	h := startedWithOutputs(t, func(c *compositor.Config) {
		lidClosed(c)
		c.Report = got.report
	}, "eDP-1", "DP-1")
	expectLit(t, h, "docked with the lid closed", map[string]bool{"eDP-1": false, "DP-1": true})
	panel := outputIndex(t, h, "eDP-1")
	var presents uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		presents = b.Outputs()[panel].Presents()
		return nil
	})
	failCommitsOn(t, h, "DP-1", fmt.Errorf("commit: %w", unix.EBUSY))
	h.Eventually("the panel lit and drawing again", func(b *headless.Backend, _ *compositor.Compositor) bool {
		o := b.Outputs()[panel]
		return o.State().Enabled() && o.Presents() > presents
	})
	if n := len(testclient.Connect(t, h.Socket()).Outputs); n != 2 {
		t.Errorf("%d outputs advertised with the external one stalled, want the panel too", n)
	}
	failCommitsOn(t, h, "DP-1", nil)
	h.Eventually("the panel off again once the external output takes commits", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return !b.Outputs()[panel].State().Enabled()
	})
	expectDroppedFrames(t, got, "DP-1")
}

func TestTheLidSuspendsALaptopWhoseExternalOutputStoppedTakingCommits(t *testing.T) {
	t.Parallel()
	got := &reports{}
	suspends := 0
	h := startedWithOutputs(t, func(c *compositor.Config) {
		c.Report = got.report
		c.Hooks.Lid = compositor.LidPolicy{
			Suspend: func() error {
				suspends++
				return nil
			},
			OnBattery: func() (bool, error) { return true, nil },
		}
	}, "eDP-1", "DP-1")
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return b.Switches().Toggle(backend.SwitchLid, true)
	})
	lidSettled(h)
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if suspends != 0 {
			t.Errorf("docked with the lid closed: %d suspends, want none", suspends)
		}
		return nil
	})
	failCommitsOn(t, h, "DP-1", fmt.Errorf("commit: %w", unix.EBUSY))
	h.Eventually("the suspend once the external output stalls", func(*headless.Backend, *compositor.Compositor) bool { return suspends > 0 })
	lidSettled(h)
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if suspends != 1 {
			t.Errorf("%d suspends, want one", suspends)
		}
		return nil
	})
	expectDroppedFrames(t, got, "DP-1")
}
