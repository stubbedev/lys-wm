package surface

import (
	"time"

	"golang.org/x/sys/unix"

	spt "github.com/stubbedev/lys-wm/internal/protocol/server/presentationtime"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const presentationVersion = 2

type PresentationKind = spt.WpPresentationFeedbackKind

func bindPresentation(c *server.Client, id wire.ObjectID, version uint32) error {
	p, err := spt.NewWpPresentation(c, id, version, presentationRequests{})
	if err != nil {
		return err
	}
	return p.SendClockID(unix.CLOCK_MONOTONIC)
}

type presentationRequests struct{}

func (presentationRequests) Destroy(*spt.WpPresentation) error {
	return nil
}

func (presentationRequests) Feedback(_ *spt.WpPresentation, wl *sw.WlSurface, fb *spt.WpPresentationFeedback) error {
	s, err := From(wl)
	if err != nil {
		return err
	}
	s.pending.feedbacks = append(s.pending.feedbacks, fb)
	return nil
}

func discard(feedbacks []*spt.WpPresentationFeedback) error {
	for _, fb := range feedbacks {
		if err := fb.SendDiscarded(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Surface) Latch() {
	s.latchedFrames = append(s.latchedFrames, s.current.frames...)
	clear(s.current.frames)
	s.current.frames = s.current.frames[:0]
	s.latchedFeedbacks = append(s.latchedFeedbacks, s.current.feedbacks...)
	clear(s.current.feedbacks)
	s.current.feedbacks = s.current.feedbacks[:0]
}

func feedbackRefresh(refresh time.Duration, variable bool, version uint32) time.Duration {
	if variable && version < 2 {
		return 0
	}
	return refresh
}

func (s *Surface) Presented(outputs []*sw.WlOutput, at, refresh time.Duration, variable bool, seq uint64, kind PresentationKind) error {
	feedbacks := s.latchedFeedbacks
	s.latchedFeedbacks = feedbacks[:0]
	defer clear(feedbacks)
	ts := wire.TimespecOf(at)
	seqHi, seqLo := wire.Halves(seq)
	for _, fb := range feedbacks {
		for _, o := range outputs {
			if err := fb.SendSyncOutput(o); err != nil {
				return err
			}
		}
		if err := fb.SendPresented(ts.SecHi, ts.SecLo, ts.Nsec, uint32(feedbackRefresh(refresh, variable, fb.Base().Version()).Nanoseconds()), seqHi, seqLo, kind); err != nil { //nolint:gosec // a refresh period fits 32 bits of nanoseconds
			return err
		}
	}
	return nil
}
