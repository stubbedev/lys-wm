package harness

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	ccm "github.com/stubbedev/lys-wm/internal/protocol/client/colormanagement"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const testHDRHold = 300 * time.Millisecond

func hdrState(h *Harness) (on bool, commits uint64) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		on, commits = o.State().HDR.Active, o.HDRCommits()
		return nil
	})
	return on, commits
}

func TestHDRFullscreenSwitchesTheSignalForAnHDRFullscreenSurface(t *testing.T) {
	onEachDMABUFRenderer(t, testHDRFullscreenSwitchesTheSignal)
}

func testHDRFullscreenSwitchesTheSignal(t *testing.T, start gpuStarter) {
	s := fullscreenScanout(t, start, func(cfg *compositor.Config) {
		cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: compositor.HDRFullscreen}}
		cfg.HDRHold = testHDRHold
	})
	h, c := s.h, s.c
	m := bindColorManager(t, c)
	log := &outputLog{}
	wl, err := cw.NewWlOutput(c.Display, 4, log)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(wl)
	if _, err := m.GetOutput(log, wl); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	changes := func() int {
		c.Sync()
		n := 0
		for _, e := range log.events {
			if e == "image_description_changed" {
				n++
			}
		}
		return n
	}
	buffers := [2]*testclient.DMABUF{c.DMABUF(small.Size, testclient.Fill(0, 0, 0xff)), c.DMABUF(small.Size, testclient.Fill(0, 0xff, 0))}
	s.show(t, "an untagged buffer", buffers[0])
	settle(t, h)
	if on, commits := hdrState(h); on || commits != 0 {
		t.Fatalf("an untagged fullscreen surface turned HDR %v after %d signal commits", on, commits)
	}
	cs, err := m.GetSurface(s.w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.SetImageDescription(hdr10Description(t, c, m, 1000), ccm.WpColorManagerV1RenderIntentPerceptual); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(s.w.Surface.Attach(buffers[1].WL, 0, 0), s.w.Surface.DamageBuffer(0, 0, 160, 100), s.w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the HDR signal for a PQ fullscreen surface", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].State().HDR.Active
	})
	if _, commits := hdrState(h); commits != 1 {
		t.Fatalf("turning HDR on took %d signal commits, want 1", commits)
	}
	before := composited(h)
	for i := range 100 {
		s.show(t, "a PQ buffer", buffers[(i+1)%2])
	}
	if n := composited(h) - before; n != 0 {
		t.Fatalf("100 commits of a fitting PQ buffer composited %d frames, want 0", n)
	}
	for range 10 {
		h.dispatch("fullscreen")
		c.Sync()
	}
	time.Sleep(testHDRHold * 2)
	settle(t, h)
	if on, commits := hdrState(h); !on || commits != 1 {
		t.Fatalf("10 fullscreen toggles within the hold left HDR %v after %d signal commits, want on after 1", on, commits)
	}
	h.dispatch("fullscreen")
	other := c.Window("beside", quadrants)
	c.DispatchUntil("a second window to map", other.Mapped)
	h.Eventually("the SDR signal once the hold passes", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return !b.Outputs()[0].State().HDR.Active
	})
	if _, commits := hdrState(h); commits != 2 {
		t.Fatalf("leaving HDR took %d signal commits in all, want 2", commits)
	}
	if n := changes(); n != 2 {
		t.Fatalf("two signal switches sent %d image_description_changed, want 2: %v", n, slices.Clone(log.events))
	}
}
