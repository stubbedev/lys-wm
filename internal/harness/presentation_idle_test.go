package harness

import (
	"errors"
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cpt "github.com/stubbedev/lys-wm/internal/protocol/client/presentationtime"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type presentationTrace struct {
	t      *testing.T
	h      *Harness
	c      *testclient.Client
	w      *testclient.Window
	p      *cpt.WpPresentation
	frame  int
	events []*feedbackEvents
}

func tracePresentations(t *testing.T) *presentationTrace {
	t.Helper()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	p, err := cpt.NewWpPresentation(c.Display, 2, &presentationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(p)
	w := c.Window("timed", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	return &presentationTrace{t: t, h: h, c: c, w: w, p: p}
}

func (tr *presentationTrace) commit(paint bool) *feedbackEvents {
	tr.t.Helper()
	fb := &feedbackEvents{}
	s := tr.w.Surface
	_, err := tr.p.Feedback(s, fb)
	if paint {
		tr.frame++
		shade := byte(0x20 * (tr.frame%7 + 1))
		err = errors.Join(err, s.Attach(tr.c.Buffer(tr.w.Size, tr.frame, testclient.Fill(shade, 0, 0xff-shade)), 0, 0), s.DamageBuffer(0, 0, int32(tr.w.Size.X), int32(tr.w.Size.Y)))
	}
	if err := errors.Join(err, s.Commit(), tr.c.Display.Flush()); err != nil {
		tr.t.Fatal(err)
	}
	tr.c.DispatchUntil("the feedback answered", func() bool { return len(fb.log) > 0 })
	if len(fb.log) != 1 || fb.log[0] != "presented" {
		tr.t.Fatalf("a commit's feedback was %v, want presented", fb.log)
	}
	tr.events = append(tr.events, fb)
	return fb
}

func (tr *presentationTrace) drawn() {
	tr.t.Helper()
	if fb := tr.commit(true); fb.flags&cpt.WpPresentationFeedbackKindVsync == 0 {
		tr.t.Fatalf("a painted commit was presented with flags %#x, want a vsynced presentation", fb.flags)
	}
}

func (tr *presentationTrace) idle() {
	tr.t.Helper()
	if fb := tr.commit(false); fb.flags != 0 {
		tr.t.Fatalf("a commit that changed nothing was presented with flags %#x, want an idle presentation", fb.flags)
	}
}

func (tr *presentationTrace) check() {
	tr.t.Helper()
	period := small.Interval()
	idle := 0
	for i, e := range tr.events {
		if time.Duration(e.refresh) != period {
			tr.t.Fatalf("presentation %d (flags %#x) reported refresh %d ns on a %v output: a client pacing by it changes its frame interval", i, e.flags, e.refresh, period)
		}
		if e.flags == 0 {
			idle++
		}
		if i == 0 {
			continue
		}
		prev := tr.events[i-1]
		if e.at <= prev.at || e.seq <= prev.seq {
			tr.t.Fatalf("presentation %d at %v seq %d does not follow presentation %d at %v seq %d", i, e.at, e.seq, i-1, prev.at, prev.seq)
		}
		if ticks := uint64((e.at - prev.at + period/2) / period); e.seq-prev.seq != ticks {
			tr.t.Fatalf("presentation %d is %v after presentation %d, %d refreshes, but its seq moved %d", i, e.at-prev.at, i-1, ticks, e.seq-prev.seq)
		}
	}
	if idle == 0 {
		tr.t.Fatal("no presentation was idle, so the test proves nothing about idle presentations")
	}
}

func TestAnIdlePresentationReportsTheOutputsRefreshAndItsVblank(t *testing.T) {
	t.Parallel()
	tr := tracePresentations(t)
	tr.drawn()
	tr.idle()
	tr.idle()
	tr.drawn()
	tr.idle()
	tr.drawn()
	tr.check()
}

func TestPresentationsStayInOrderAcrossPowerAndLock(t *testing.T) {
	t.Parallel()
	tr := tracePresentations(t)
	tr.drawn()
	tr.idle()
	tr.h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		return c.SetPower(c.Outputs()[0], false)
	})
	tr.h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		return c.SetPower(c.Outputs()[0], true)
	})
	tr.drawn()
	tr.idle()
	l := lockSession(t, tr.h)
	l.seen("locked")
	l.cover(image.Pt(160, 100), 0xff, 0, 0)
	tr.h.Eventually("the lock surface shown", showing(5, 5, [3]byte{0, 0, 0xff}))
	if err := errors.Join(l.lock.UnlockAndDestroy(), l.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	tr.drawn()
	tr.idle()
	tr.drawn()
	tr.check()
}
