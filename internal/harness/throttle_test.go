package harness

import (
	"errors"
	"image"
	"regexp"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	hiddenFrame = 100 * time.Millisecond
	hiddenSpan  = 5
)

func throttled(cfg *compositor.Config) {
	cfg.HiddenFrame = hiddenFrame
	cfg.Rules = []wm.Rule{
		{Match: wm.Match{Title: regexp.MustCompile(`^behind$`)}, Mode: wm.RuleFloat, Size: wm.Px(80, 50)},
		{Match: wm.Match{Title: regexp.MustCompile(`^wall$`)}, Mode: wm.RuleFloat, Size: wm.Px(120, 80)},
	}
}

func hiddenFired(h *Harness) uint64 {
	var n uint64
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		n = c.HiddenFrames()
		return nil
	})
	return n
}

func awaitHiddenFrame(h *Harness, c *testclient.Client, what string) {
	h.t.Helper()
	from := hiddenFired(h)
	c.DispatchUntil("a hidden frame for "+what, func() bool { return hiddenFired(h) > from })
}

func atTheHiddenRate(t *testing.T, h *Harness, c *testclient.Client, what string, callbacks func() int) {
	t.Helper()
	fired, from := hiddenFired(h), callbacks()
	c.DispatchUntil(what+" to get its frame callbacks", func() bool { return callbacks()-from >= hiddenSpan })
	got, fired := callbacks()-from, hiddenFired(h)-fired
	if uint64(got) > fired+1 {
		t.Fatalf("%s got %d frame callbacks while the hidden frame timer fired %d times, want one per hidden frame", what, got, fired)
	}
}

func atTheRefreshRate(t *testing.T, h *Harness, c *testclient.Client, what string, callbacks func() int) {
	t.Helper()
	fired, from := hiddenFired(h), callbacks()
	c.DispatchUntil(what+" to get its frame callbacks", func() bool { return callbacks()-from >= hiddenSpan })
	if fired := hiddenFired(h) - fired; fired != 0 {
		t.Fatalf("%s got %d frame callbacks across %d hidden frames, want every one from a frame that shows it", what, callbacks()-from, fired)
	}
}

func TestASurfaceUnderAnOpaqueWindowGetsFramesAtTheHiddenRate(t *testing.T) {
	t.Parallel()
	h := StartWith(t, throttled, small)
	c := testclient.Connect(t, h.Socket())
	behind := c.Window("behind", fineChecker)
	c.DispatchUntil("the window behind to map", behind.Mapped)
	frames := func() int { return behind.Frames }
	atTheRefreshRate(t, h, c, "the shown window", frames)
	wall := c.Window("wall", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the wall to map", wall.Mapped)
	wall.Stop()
	awaitHiddenFrame(h, c, "the window under the wall")
	atTheHiddenRate(t, h, c, "the window under the wall", frames)
}

type frameTimes []uint32

func (f *frameTimes) Done(_ *cw.WlCallback, ms uint32) error {
	*f = append(*f, ms)
	return nil
}

func TestASurfaceNoOutputShowsGetsFramesAtTheHiddenRate(t *testing.T) {
	t.Parallel()
	h := StartWith(t, throttled, small)
	c := testclient.Connect(t, h.Socket())
	s := c.Surface()
	if err := errors.Join(s.Attach(c.Buffer(image.Pt(4, 4), 0, testclient.Fill(0, 0, 0xff)), 0, 0), s.DamageBuffer(0, 0, 4, 4)); err != nil {
		t.Fatal(err)
	}
	fired := hiddenFired(h)
	var times frameTimes
	for len(times) < hiddenSpan {
		asked := len(times)
		if _, err := s.Frame(&times); err != nil {
			t.Fatal(err)
		}
		if err := s.Commit(); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("a frame callback", func() bool { return len(times) > asked })
	}
	if fired := hiddenFired(h) - fired; fired < hiddenSpan {
		t.Fatalf("a surface without a role got %d frame callbacks while the hidden frame timer fired %d times, want one per hidden frame", len(times), fired)
	}
	for i := 1; i < len(times); i++ {
		if gap := time.Duration(times[i]-times[i-1]) * time.Millisecond; gap < hiddenFrame-time.Millisecond {
			t.Fatalf("a surface without a role got frame callbacks %v apart by the compositor's clock, want at least the hidden frame's %v: %v", gap, hiddenFrame, times)
		}
	}
}

func TestAFifoSurfaceNoOutputShowsAdvancesAtTheHiddenRate(t *testing.T) {
	t.Parallel()
	h := StartWith(t, throttled, small)
	pc := pace(t, h)
	s := pc.c.Surface()
	f, err := pc.fifos.GetFifo(s)
	if err != nil {
		t.Fatal(err)
	}
	fired := hiddenFired(h)
	updates := make([]*feedbackEvents, 2*hiddenSpan)
	for i := range updates {
		updates[i] = pc.update(t, s, nil, f.SetBarrier, f.WaitBarrier)
	}
	superseded := func() int {
		n := 0
		for _, fb := range updates {
			if len(fb.log) > 0 {
				n++
			}
		}
		return n
	}
	pc.c.DispatchUntil("hidden fifo updates to apply", func() bool { return superseded() >= hiddenSpan })
	if got, fired := superseded(), hiddenFired(h)-fired; uint64(got) > fired {
		t.Fatalf("a hidden fifo surface moved past %d updates while the hidden frame timer fired %d times, want one per hidden frame", got, fired)
	}
}
