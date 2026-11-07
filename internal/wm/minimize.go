package wm

import (
	"fmt"
	"image"
	"slices"
)

type Minimized struct {
	ID    WindowID
	Rect  image.Rectangle
	Layer PlacementLayer
}

func appendMinimized(out []Placement, ms []Minimized) []Placement {
	for _, m := range ms {
		out = append(out, Placement{ID: m.ID, Rect: m.Rect, Layer: m.Layer, Minimized: true})
	}
	return out
}

func (s State) Minimized(id WindowID) bool {
	w := s.windowWorkspace(id)
	return w != nil && w.minimizedIndex(id) >= 0
}

func (s State) LastMinimized() (WindowID, bool) {
	w := s.activeWorkspace()
	if w == nil || len(w.Minimized) == 0 {
		return 0, false
	}
	return w.Minimized[len(w.Minimized)-1].ID, true
}

func (s State) Minimize(id WindowID, tile image.Rectangle) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("minimize window %d: %w", id, ErrUnknownWindow)
	}
	if w.minimizedIndex(id) >= 0 {
		return s, nil
	}
	m := Minimized{ID: id, Rect: tile}
	if i := w.floatingIndex(id); i >= 0 {
		m.Rect, m.Layer = w.Floating[i].Rect, LayerFloating
	}
	if w.Cover.ID == id {
		m.Layer = w.Cover.Mode.layer()
	}
	if err := w.remove(id); err != nil {
		return State{}, err
	}
	w.Minimized = append(w.Minimized, m)
	return s, nil
}

func (s State) Restore(id WindowID, at Autotile) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("restore window %d: %w", id, ErrUnknownWindow)
	}
	i := w.minimizedIndex(id)
	if i < 0 {
		return s, nil
	}
	m := w.Minimized[i]
	w.Minimized = slices.Delete(w.Minimized, i, i+1)
	if m.Layer == LayerFloating {
		w.Floating = append(w.Floating, Floating{ID: id, Rect: m.Rect})
	} else {
		var area image.Rectangle
		if o := s.output(w.Output); o != nil {
			area = o.Area
		}
		tree, err := w.Tree.Place(id, area, at)
		if err != nil {
			return State{}, err
		}
		w.Tree = tree
	}
	if mode := modeOf(m.Layer); mode != ModeNormal {
		w.Cover = Cover{ID: id, Mode: mode}
	}
	if err := w.focus(id, Raise); err != nil {
		return State{}, err
	}
	if s.output(w.Output) != nil {
		s.activate(w.ID)
	}
	return s, nil
}
