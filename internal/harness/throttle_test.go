package harness

import (
	"errors"
	"image"
	"regexp"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	hiddenFrame  = 100 * time.Millisecond
	hiddenWindow = time.Second
)

func throttled(cfg *compositor.Config) {
	cfg.HiddenFrame = hiddenFrame
	cfg.Rules = []wm.Rule{
		{Match: wm.Match{Title: regexp.MustCompile(`^behind$`)}, Mode: wm.RuleFloat, Size: wm.Px(80, 50)},
		{Match: wm.Match{Title: regexp.MustCompile(`^wall$`)}, Mode: wm.RuleFloat, Size: wm.Px(120, 80)},
	}
}

func atTheHiddenRate(t *testing.T, what string, frames int) {
	t.Helper()
	most := int(hiddenWindow/hiddenFrame) + 3
	if frames < 3 || frames > most {
		t.Fatalf("%s advanced %d times in %v, want 3 to %d at one per %v", what, frames, hiddenWindow, most, hiddenFrame)
	}
}

func TestASurfaceUnderAnOpaqueWindowGetsFramesAtTheHiddenRate(t *testing.T) {
	t.Parallel()
	h := StartWith(t, throttled, small)
	c := testclient.Connect(t, h.Socket())
	behind := c.Window("behind", fineChecker)
	c.DispatchUntil("the window behind to map", behind.Mapped)
	pump(c, 200*time.Millisecond)
	if before := behind.Frames; before < 6 {
		t.Fatalf("the shown window got %d frame callbacks in 200ms, want the refresh rate", before)
	}
	wall := c.Window("wall", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the wall to map", wall.Mapped)
	wall.Stop()
	pump(c, 200*time.Millisecond)
	from := behind.Frames
	pump(c, hiddenWindow)
	atTheHiddenRate(t, "the window under the wall", behind.Frames-from)
}

type redraws struct {
	s      *cw.WlSurface
	buffer *cw.WlBuffer
	frames int
}

func (r *redraws) Done(*cw.WlCallback, uint32) error {
	r.frames++
	return r.commit()
}

func (r *redraws) commit() error {
	_, err := r.s.Frame(r)
	return errors.Join(err, r.s.Attach(r.buffer, 0, 0), r.s.DamageBuffer(0, 0, 4, 4), r.s.Commit())
}

func TestASurfaceNoOutputShowsGetsFramesAtTheHiddenRate(t *testing.T) {
	t.Parallel()
	h := StartWith(t, throttled, small)
	c := testclient.Connect(t, h.Socket())
	r := &redraws{s: c.Surface(), buffer: c.Buffer(image.Pt(4, 4), 0, testclient.Fill(0, 0, 0xff))}
	if err := r.commit(); err != nil {
		t.Fatal(err)
	}
	pump(c, hiddenWindow)
	atTheHiddenRate(t, "a surface without a role", r.frames)
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
	updates := make([]*feedbackEvents, 30)
	for i := range updates {
		updates[i] = pc.update(t, s, nil, f.SetBarrier, f.WaitBarrier)
	}
	pump(pc.c, hiddenWindow)
	applied := 0
	for _, fb := range updates {
		if len(fb.log) > 0 {
			applied++
		}
	}
	atTheHiddenRate(t, "a hidden fifo surface", applied)
}
