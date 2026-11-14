package harness

import (
	"image"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const uncappedTries = 50

var fast = backend.Mode{Size: image.Pt(160, 100), RefreshMHz: 120000, Preferred: true}

func powerSaver(h *Harness, on bool) {
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		c.SetPowerSaver(on)
		return nil
	})
}

func drawContinuously(t *testing.T, pc *paced, w *testclient.Window, frames int) []*feedbackEvents {
	t.Helper()
	shown := make([]*feedbackEvents, frames)
	for i := range shown {
		done := false
		if _, err := w.Surface.Frame(frameDone{&done}); err != nil {
			t.Fatal(err)
		}
		shown[i] = pc.update(t, w.Surface, pc.c.Buffer(w.Size, i%2, quadrants))
		if err := pc.c.Display.Flush(); err != nil {
			t.Fatal(err)
		}
		pc.c.DispatchUntil("the frame callback", func() bool { return done })
	}
	pc.c.DispatchUntil("every frame answered", answered(shown...))
	return shown
}

func refreshGaps(t *testing.T, shown []*feedbackEvents) []uint64 {
	t.Helper()
	gaps := make([]uint64, 0, len(shown))
	for i, fb := range shown[2:] {
		prev := shown[i+1]
		if !slices.Equal(fb.log, []string{"presented"}) {
			t.Fatalf("frame %d was %v", i+2, fb.log)
		}
		gaps = append(gaps, fb.seq-prev.seq)
	}
	return gaps
}

func TestPowerSaverHalvesTheFrameRateOfA120HzOutput(t *testing.T) {
	h := Start(t, fast)
	pc := pace(t, h)
	w := pc.window(t)
	powerSaver(h, true)
	const frames = 24
	shown := drawContinuously(t, pc, w, frames)
	twice := 0
	for i, gap := range refreshGaps(t, shown) {
		if gap < 2 {
			t.Fatalf("under power saver frame %d followed the one before after %d refreshes of a 120 Hz output, want at least 2", i+3, gap)
		}
		if gap == 2 {
			twice++
		}
	}
	if twice*2 < frames-2 {
		t.Fatalf("%d of %d frames came two refreshes after the one before: a client drawing every frame should see 60 Hz", twice, frames-2)
	}
	if want := uint32(fast.Interval()); shown[frames-1].refresh != want {
		t.Fatalf("under power saver presentation feedback reports a refresh of %d ns, want the mode's %d", shown[frames-1].refresh, want)
	}
	powerSaver(h, false)
	shown = drawContinuously(t, pc, w, 6)
	if want := uint32(fast.Interval()); shown[len(shown)-1].refresh != want {
		t.Fatalf("after power saver presentation feedback reports a refresh of %d ns, want %d", shown[len(shown)-1].refresh, want)
	}
	for range uncappedTries {
		if slices.Min(refreshGaps(t, shown)) == 1 {
			return
		}
		shown = drawContinuously(t, pc, w, 6)
	}
	t.Fatalf("after power saver a client drawing every frame never got the next refresh: gaps %v", refreshGaps(t, shown))
}

func TestPowerSaverSlidesAWorkspaceInHalfTheFrames(t *testing.T) {
	d, on := 300*time.Millisecond, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: new(false)}, anim.Workspaces: {Enabled: &on, Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Animations = table }, fast)
	powerSaver(h, true)
	c := testclient.Connect(t, h.Socket())
	red := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", red.Mapped)
	h.Eventually("red drawn", showing(10, 10, [3]byte{0xff, 0, 0}))
	var before uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		before = b.Outputs()[0].Presents()
		return nil
	})
	h.Freeze()
	start := h.Now()
	h.dispatch("workspace 2")
	h.Eventually("the slide's first frame", func(b *headless.Backend, _ *compositor.Compositor) bool { return b.Outputs()[0].Presents() > before })
	h.Eventually("the slide to end", func(_ *headless.Backend, c *compositor.Compositor) bool { return !c.Animating(c.Outputs()[0]) })
	took := h.Now() - start
	var presents uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		presents = b.Outputs()[0].Presents() - before
		return nil
	})
	if refreshes := uint64(took / fast.Interval()); presents == 0 || presents > refreshes/2+2 {
		t.Fatalf("a workspace slide over %d refreshes of a 120 Hz output composited %d frames under power saver, want at most every other refresh", refreshes, presents)
	}
}
