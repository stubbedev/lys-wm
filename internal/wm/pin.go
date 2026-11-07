package wm

import (
	"fmt"
	"maps"
	"slices"
)

func (s State) Pinned(id WindowID) bool {
	_, ok := s.pinned[id]
	return ok
}

func (s State) SetPinned(id WindowID, on bool) (State, error) {
	w := s.windowWorkspace(id)
	switch {
	case w == nil:
		return State{}, fmt.Errorf("pin window %d: %w", id, ErrUnknownWindow)
	case s.Pinned(id) == on, on && (w.Scratch.Name != "" || w.minimizedIndex(id) >= 0):
		return s, nil
	case !on:
		s = s.clone()
		delete(s.pinned, id)
		return s, nil
	}
	if w.Tree.Contains(id) {
		area := s.output(w.Output).Area
		next, err := s.Float(id, Centered(area, s.limits[id].floored().Fit(area.Size().Div(2))))
		if err != nil {
			return State{}, err
		}
		s = next
	}
	s = s.clone()
	if s.pinned == nil {
		s.pinned = map[WindowID]struct{}{}
	}
	s.pinned[id] = struct{}{}
	return s, nil
}

func (s State) SwitchPin(sw Switch) (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	return s.SetPinned(id, sw.From(s.Pinned(id)))
}

func (s *State) gatherPinned() {
	if len(s.pinned) == 0 {
		return
	}
	for _, id := range slices.Sorted(maps.Keys(s.pinned)) {
		from := s.windowWorkspace(id)
		o := s.output(from.Output)
		if o == nil || from.ID == o.Active || from.Scratch.Name != "" {
			continue
		}
		to := s.workspace(o.Active)
		i := from.floatingIndex(id)
		if i < 0 || to == nil {
			continue
		}
		f := from.Floating[i]
		focused, _ := from.focused()
		if err := from.remove(id); err != nil {
			continue
		}
		at := len(to.Floating)
		if j := to.floatingIndex(to.floatFocus); focused != id && j >= 0 {
			at = j
		}
		to.Floating = slices.Insert(to.Floating, at, f)
		if focused == id {
			to.floatFocus = id
			to.record()
		}
		s.dropIfIdle(from.ID)
	}
}

type PinAction struct{ Switch Switch }

func (a PinAction) String() string { return switched("pin", a.Switch) }

func (a PinAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.SwitchPin(a.Switch)
	return next, nil, err
}
