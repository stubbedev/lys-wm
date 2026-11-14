package harness

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
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
	if n := len(testclient.Connect(t, h.Socket()).Outputs); n != 1 {
		t.Errorf("%d outputs advertised with the external one stalled, want the panel alone, as after DP-1 is removed", n)
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
	var suspends atomic.Int32
	h := startedWithOutputs(t, func(c *compositor.Config) {
		c.Report = got.report
		c.Hooks.Lid = compositor.LidPolicy{
			Suspend: func() error {
				suspends.Add(1)
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
		if suspends.Load() != 0 {
			t.Errorf("docked with the lid closed: %d suspends, want none", suspends.Load())
		}
		return nil
	})
	failCommitsOn(t, h, "DP-1", fmt.Errorf("commit: %w", unix.EBUSY))
	h.Eventually("the suspend once the external output stalls", func(*headless.Backend, *compositor.Compositor) bool { return suspends.Load() > 0 })
	lidSettled(h)
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if suspends.Load() != 1 {
			t.Errorf("%d suspends, want one", suspends.Load())
		}
		return nil
	})
	expectDroppedFrames(t, got, "DP-1")
}

func TestAWindowOnAStalledOutputMovesToThePanelAndBackOnceTheOutputDisplaysForTheBound(t *testing.T) {
	t.Parallel()
	got := &reports{}
	h := startedWithOutputs(t, func(c *compositor.Config) {
		lidClosed(c)
		c.Report = got.report
	}, "eDP-1", "DP-1")
	expectLit(t, h, "docked with the lid closed", map[string]bool{"eDP-1": false, "DP-1": true})
	c := testclient.Connect(t, h.Socket())
	w := c.Window("form", fillGreen)
	c.DispatchUntil("the form to map", w.Mapped)
	request(t, c, func() error { return w.Toplevel.SetFullscreen(nil) })
	shownOn := func(when, output string) {
		t.Helper()
		h.settled(c, "the form fullscreen and focused on "+output+" "+when, func(x ipc.Window) bool {
			return x.Fullscreen && x.Focused && x.Output == output
		}, "form")
		h.Eventually("the form drawn on "+output+" "+when, showingOn(outputIndex(t, h, output), 5, 5, greenColor))
		c.DispatchUntil("the form activated at the size of "+output+" "+when, func() bool {
			return w.Size == small.Size && slices.Contains(w.States, cxdg.XdgToplevelStateActivated)
		})
	}
	shownOn("once mapped", "DP-1")

	failCommitsOn(t, h, "DP-1", fmt.Errorf("commit: %w", unix.EBUSY))
	shownOn("after DP-1 stalled", "eDP-1")
	expectLit(t, h, "with DP-1 stalled", map[string]bool{"eDP-1": true})

	external := outputIndex(t, h, "DP-1")
	for cycle := range 2 {
		var presents uint64
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			o := b.Outputs()[external]
			presents = o.Presents()
			o.TakePresents(1)
			return nil
		})
		h.Eventually("one probe frame taken on DP-1", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return b.Outputs()[external].Presents() > presents
		})
		for range 10 {
			h.Advance(backend.SustainedFor / 8)
			var flapped string
			h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
				switch x, _ := windowNamed(comp, "form"); {
				case !b.Outputs()[external].Stalled():
					flapped = "DP-1 counts as displaying again"
				case x.Output != "eDP-1":
					flapped = "the form is on " + x.Output
				}
				return nil
			})
			if flapped != "" {
				t.Fatalf("cycle %d: after one probe frame that took between failing runs, %s", cycle, flapped)
			}
		}
	}

	failCommitsOn(t, h, "DP-1", nil)
	shownOn("after DP-1 displayed again", "DP-1")
	expectLit(t, h, "with DP-1 displaying again", map[string]bool{"eDP-1": false, "DP-1": true})
	expectDroppedFrames(t, got, "DP-1")
}
