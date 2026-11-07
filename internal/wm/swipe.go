package wm

import (
	"math"

	"github.com/stubbedev/lys-wm/internal/anim"
)

const swipeRubberBand = 0.2

type WorkspaceSwipe struct {
	From, To WorkspaceID
	At       float64
	Shift    float64
}

func (s State) Neighbour(output string, id WorkspaceID, forward bool) (WorkspaceID, bool) {
	var found WorkspaceID
	for _, w := range s.workspaces {
		if w.Output != output {
			continue
		}
		if forward && w.ID > id {
			return w.ID, true
		}
		if !forward && w.ID < id {
			found = w.ID
		}
	}
	return found, found != 0
}

func (s State) EdgeWorkspace(output string, id WorkspaceID, forward bool) (WorkspaceID, bool) {
	if next, ok := s.Neighbour(output, id, forward); ok || !forward {
		return next, ok
	}
	next := id + 1
	for s.workspace(next) != nil {
		next++
	}
	return next, true
}

func (s State) SwipeWorkspace(output string, from WorkspaceID, at, delta float64) WorkspaceSwipe {
	sw := WorkspaceSwipe{From: from}
	target := at + delta
	for {
		to, ok := s.Neighbour(output, sw.From, target >= 0)
		if !ok {
			if (at >= 0) != (target >= 0) || sw.Shift != 0 {
				at, delta = 0, target
			}
			sw.At = anim.RubberBand(at, delta, swipeRubberBand)
			return sw
		}
		sw.To = to
		if math.Abs(target) < 1 {
			sw.At = target
			return sw
		}
		step := math.Copysign(1, target)
		sw.From, sw.To = to, 0
		target -= step
		sw.Shift -= step
	}
}

func (sw WorkspaceSwipe) Rest(projected float64) (float64, WorkspaceID) {
	if sw.To == 0 {
		return 0, sw.From
	}
	sign := 1.0
	if sw.To < sw.From {
		sign = -1
	}
	if float64(projected*sign) >= anim.CommitAt {
		return sign, sw.To
	}
	return 0, sw.From
}
