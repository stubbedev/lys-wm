package harness

import (
	"errors"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func TestAHiddenWindowsCaptureIsPresentedAtItsOutputsRefresh(t *testing.T) {
	t.Parallel()
	fast := small
	fast.RefreshMHz = 85000
	h := StartWith(t, func(cfg *compositor.Config) { cfg.HiddenFrame = time.Hour }, fast)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("shared", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	tr := traceWindow(t, h, c, w)
	tr.drawn()
	r := newCaptureRig(t, h, c)
	h.dispatch("workspace 2")
	settle(t, h)
	session, view := r.windowSession()
	for i := range 4 {
		fb := &feedbackEvents{}
		if _, err := tr.p.Feedback(w.Surface, fb); err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(w.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		f, frame, _ := grab(t, c, session, view.size)
		c.DispatchUntil("a capture of the hidden window and its feedback", func() bool { return (f.ready || f.failed) && len(fb.log) > 0 })
		if f.failed || len(fb.log) != 1 || fb.log[0] != "presented" {
			t.Fatalf("capture %d of the hidden window: failed %v, feedback %v", i, f.failed, fb.log)
		}
		if err := errors.Join(frame.Destroy(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		tr.events = append(tr.events, fb)
	}
	for i, e := range tr.events {
		if time.Duration(e.refresh) != fast.Interval() || e.synced != 1 {
			t.Fatalf("presentation %d reported refresh %v on %d outputs, want the output's %v on it", i, time.Duration(e.refresh), e.synced, fast.Interval())
		}
		if prev := tr.events[max(i-1, 0)]; i > 0 && (e.at <= prev.at || e.seq < prev.seq) {
			t.Fatalf("presentation %d at %v seq %d does not follow %v seq %d", i, e.at, e.seq, prev.at, prev.seq)
		}
	}
}
