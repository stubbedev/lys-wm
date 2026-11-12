package harness

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/wmipc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func failingPresents(t *testing.T, err error) (*Harness, *reports) {
	t.Helper()
	got := &reports{}
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Report = got.report }, big)
	h.Eventually("the first frame", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > 0
	})
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		b.Outputs()[0].FailPresents(err)
		return c.OutputDamaged(b.Outputs()[0])
	})
	return h, got
}

func TestAFailedFrameIsDroppedAndRetried(t *testing.T) {
	t.Parallel()
	h, got := failingPresents(t, fmt.Errorf("commit: %w", unix.EBUSY))
	var failed uint64
	h.Eventually("frames failing", func(b *headless.Backend, _ *compositor.Compositor) bool {
		failed = b.Outputs()[0].Frames()
		return len(got.errs) > 0 && failed > 3
	})
	errs := got.take()
	if len(errs) != 1 || !errors.Is(errs[0], unix.EBUSY) || !strings.Contains(errs[0].Error(), "dropping frames until one draws") {
		t.Fatalf("reported %v, want one report of the dropped frames", errs)
	}
	var presents uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		presents = b.Outputs()[0].Presents()
		b.Outputs()[0].FailPresents(nil)
		return nil
	})
	h.Eventually("a frame presented again", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > presents
	})
	if errs := got.take(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "HEADLESS-1 draws again after") {
		t.Fatalf("reported %v, want one report of the recovery", errs)
	}
}

func TestALostDeviceOrABrokenInvariantEndsTheSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"device lost", fmt.Errorf("%w: host gone", backend.ErrDeviceLost)},
		{"invariant", backend.Broken("a frame in progress twice")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, got := failingPresents(t, tc.err)
			if err := h.Ended(); !errors.Is(err, tc.err) {
				t.Fatalf("the loop ended with %v, want %v", err, tc.err)
			}
			if errs := got.take(); len(errs) != 0 {
				t.Fatalf("reported %v; a fatal error ends the session without a report", errs)
			}
		})
	}
}

func mirroring(more ...compositor.OutputRule) func(*compositor.Config) {
	return func(cfg *compositor.Config) {
		cfg.Outputs.Named = append([]compositor.OutputRule{{Match: wm.OutputMatch{Name: "HEADLESS-2"}, Mirror: &wm.OutputMatch{Name: "HEADLESS-1"}}}, more...)
	}
}

func TestAMirrorWhoseCommitsFailDropsFramesAndRetries(t *testing.T) {
	t.Parallel()
	got := &reports{}
	h := StartWith(t, func(cfg *compositor.Config) {
		mirroring()(cfg)
		cfg.Report = got.report
	}, small, small)
	h.Eventually("the mirror's first frame", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[1].Presents() > 0
	})
	got.take()
	var frames uint64
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		m := b.Outputs()[1]
		frames = m.Frames()
		m.FailPresents(fmt.Errorf("commit: %w", unix.EBUSY))
		return c.OutputDamaged(b.Outputs()[0])
	})
	h.Eventually("the mirror retrying its failed frames", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[1].Frames() > frames+3
	})
	errs := got.take()
	if len(errs) != 1 || !errors.Is(errs[0], unix.EBUSY) || !strings.Contains(errs[0].Error(), "frame on HEADLESS-2") || !strings.Contains(errs[0].Error(), "dropping frames until one draws") {
		t.Fatalf("reported %v, want one report of the mirror's dropped frames", errs)
	}
	var presents uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		m := b.Outputs()[1]
		presents = m.Presents()
		m.FailPresents(nil)
		return nil
	})
	h.Eventually("the mirror presenting again", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[1].Presents() > presents
	})
	if errs := got.take(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "HEADLESS-2 draws again after") {
		t.Fatalf("reported %v, want one report of the mirror's recovery", errs)
	}
}

func TestDamagingEveryOutputAsAResumeDoesRedrawsTheMirrorAndLeavesTheDisabledOutputDark(t *testing.T) {
	t.Parallel()
	h := StartWith(t, mirroring(compositor.OutputRule{Match: wm.OutputMatch{Name: "HEADLESS-3"}, Disabled: true}), small, small, small)
	h.Eventually("the mirror's first frame", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[1].Presents() > 0 && !b.Outputs()[1].FrameOwed()
	})
	var presents uint64
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		presents = b.Outputs()[1].Presents()
		var errs []error
		for _, o := range b.Outputs() {
			errs = append(errs, c.OutputDamaged(o))
		}
		return errors.Join(errs...)
	})
	h.Eventually("the mirror redrawn", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[1].Presents() > presents
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if d := b.Outputs()[2]; d.State().Enabled() || d.Frames() != 0 {
			t.Errorf("the disabled output is enabled %v with %d frames, want it off and never drawn", d.State().Enabled(), d.Frames())
		}
		return nil
	})
}

func TestAPanelThatRefusesToTurnOffAsTheLidClosesKeepsTheSession(t *testing.T) {
	t.Parallel()
	got := &reports{}
	h := startedWithOutputs(t, func(c *compositor.Config) { c.Report = got.report }, "eDP-1", "DP-1")
	expectLit(t, h, "with the lid open", map[string]bool{"eDP-1": true, "DP-1": true})
	panel := outputIndex(t, h, "eDP-1")
	got.take()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[panel].FailCommits(unix.EBUSY)
		return b.Switches().Toggle(backend.SwitchLid, true)
	})
	expectLit(t, h, "with the panel refusing the commit", map[string]bool{"eDP-1": true, "DP-1": true})
	errs := got.take()
	if len(errs) != 1 || !errors.Is(errs[0], unix.EBUSY) || !strings.Contains(errs[0].Error(), "eDP-1") {
		t.Fatalf("reported %v, want one report of the panel's refused commit", errs)
	}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[panel].FailCommits(nil)
		if err := b.Switches().Toggle(backend.SwitchLid, false); err != nil {
			return err
		}
		return b.Switches().Toggle(backend.SwitchLid, true)
	})
	expectLit(t, h, "once the panel takes commits and the lid closes again", map[string]bool{"eDP-1": false, "DP-1": true})
}

func TestAMirrorDroppedWhileItRefusesCommitsStaysAnOutput(t *testing.T) {
	t.Parallel()
	got := &reports{}
	var next compositor.Config
	h := StartWith(t, func(cfg *compositor.Config) {
		mirroring()(cfg)
		cfg.Report = got.report
		next = *cfg
		next.Outputs.Named = []compositor.OutputRule{{Match: wm.OutputMatch{Name: "HEADLESS-2"}, Disabled: true}}
	}, small, small)
	h.Eventually("the mirror's first frame", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[1].Presents() > 0
	})
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		m := b.Outputs()[1]
		m.FailCommits(unix.EBUSY)
		return errors.Join(c.Reconfigure(next), c.OutputDamaged(m))
	})
	if errs := got.take(); len(errs) != 1 || !errors.Is(errs[0], unix.EBUSY) || !strings.Contains(errs[0].Error(), "HEADLESS-2") {
		t.Fatalf("reported %v, want one report of the refused commit", errs)
	}
	outputs := query[[]ipc.Output](h, wmipc.KindOutputs)
	if i := slices.IndexFunc(outputs, func(o ipc.Output) bool { return o.Name == "HEADLESS-2" }); i < 0 || outputs[i].MirrorOf != "" {
		t.Fatalf("outputs %+v, want HEADLESS-2 still lit and no longer a mirror", outputs)
	}
}
