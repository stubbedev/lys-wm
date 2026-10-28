package wm

import (
	"fmt"
	"image"
)

type Limits struct {
	Min, Max image.Point
}

func (l Limits) Fit(size image.Point) image.Point {
	axis := func(v, lo, hi int) int {
		v = max(v, lo)
		if hi > 0 {
			v = min(v, max(hi, lo))
		}
		return v
	}
	return image.Pt(axis(size.X, l.Min.X, l.Max.X), axis(size.Y, l.Min.Y, l.Max.Y))
}

const minFloating = 32

func (l Limits) floored() Limits {
	l.Min = image.Pt(max(l.Min.X, minFloating), max(l.Min.Y, minFloating))
	return l
}

func (l Limits) fitRect(r image.Rectangle, right, bottom bool) image.Rectangle {
	size := l.Fit(r.Size())
	if right {
		r.Max.X = r.Min.X + size.X
	} else {
		r.Min.X = r.Max.X - size.X
	}
	if bottom {
		r.Max.Y = r.Min.Y + size.Y
	} else {
		r.Min.Y = r.Max.Y - size.Y
	}
	return r
}

func (s State) SetLimits(id WindowID, l Limits) (State, error) {
	if s.windowWorkspace(id) == nil {
		return State{}, fmt.Errorf("set the size limits of window %d: %w", id, ErrUnknownWindow)
	}
	s = s.clone()
	if s.limits == nil {
		s.limits = map[WindowID]Limits{}
	}
	s.limits[id] = l
	w := s.windowWorkspace(id)
	if i := w.floatingIndex(id); i >= 0 {
		w.Floating[i].Rect = l.fitRect(w.Floating[i].Rect, true, true)
	}
	return s, nil
}

func centered(area image.Rectangle, size image.Point) image.Rectangle {
	corner := area.Min.Add(area.Size().Sub(size).Div(2))
	return image.Rectangle{Min: corner, Max: corner.Add(size)}
}

func (s State) MapFloating(id WindowID, rect image.Rectangle) (State, error) {
	if id == 0 {
		return State{}, ErrReservedWindow
	}
	if s.windowWorkspace(id) != nil {
		return State{}, fmt.Errorf("wm: map floating window %d: already mapped", id)
	}
	s = s.clone()
	w := s.activeWorkspace()
	if w == nil {
		return State{}, fmt.Errorf("map floating window %d: %w", id, ErrUnknownOutput)
	}
	w.Floating = append(w.Floating, Floating{ID: id, Rect: rect})
	w.floatFocus = true
	w.record()
	return s, nil
}

func (s State) ToggleFloating(at Autotile) (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	s = s.clone()
	w := s.windowWorkspace(id)
	o := s.output(w.Output)
	if i := w.floatingIndex(id); i >= 0 {
		if err := w.remove(id); err != nil {
			return State{}, err
		}
		tree, err := w.Tree.Place(id, o.Area, at)
		if err != nil {
			return State{}, err
		}
		w.Tree, w.floatFocus = tree, false
		w.record()
		return s, nil
	}
	if err := w.remove(id); err != nil {
		return State{}, err
	}
	w.Floating = append(w.Floating, Floating{ID: id, Rect: centered(o.Area, s.limits[id].floored().Fit(o.Area.Size().Div(2)))})
	w.floatFocus = true
	w.record()
	return s, nil
}

func (s State) SetFloatingRect(id WindowID, rect image.Rectangle) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("place floating window %d: %w", id, ErrUnknownWindow)
	}
	i := w.floatingIndex(id)
	if i < 0 {
		return State{}, fmt.Errorf("wm: window %d is not floating", id)
	}
	w.Floating[i].Rect = s.limits[id].fitRect(rect, true, true)
	return s, nil
}

func (s State) ResizeFloating(id WindowID, from image.Rectangle, d image.Point, right, bottom bool) (State, error) {
	r := from
	if right {
		r.Max.X += d.X
	} else {
		r.Min.X += d.X
	}
	if bottom {
		r.Max.Y += d.Y
	} else {
		r.Min.Y += d.Y
	}
	return s.SetFloatingRect(id, s.limits[id].floored().fitRect(r, right, bottom))
}

func (s State) Floats(id WindowID) bool {
	w := s.windowWorkspace(id)
	return w != nil && w.floatingIndex(id) >= 0
}

func (s State) SetFullscreen(id WindowID, on bool) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("fullscreen window %d: %w", id, ErrUnknownWindow)
	}
	switch {
	case on:
		w.Fullscreen = id
		if err := w.focus(id); err != nil {
			return State{}, err
		}
	case w.Fullscreen == id:
		w.Fullscreen = 0
	}
	return s, nil
}

func (s State) ToggleFullscreen() (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	w := s.windowWorkspace(id)
	return s.SetFullscreen(id, w.Fullscreen != id)
}

func (s State) Fullscreen(id WindowID) bool {
	w := s.windowWorkspace(id)
	return w != nil && w.Fullscreen == id
}

func (s State) Reinsert(id WindowID, at image.Point, auto Autotile) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil || !w.Tree.Contains(id) {
		return State{}, fmt.Errorf("drop window %d: %w", id, ErrUnknownWindow)
	}
	o := s.output(w.Output)
	tree, err := w.Tree.Remove(id)
	if err != nil {
		return State{}, err
	}
	if tree, err = tree.InsertAt(id, o.Area, at, auto); err != nil {
		return State{}, err
	}
	w.Tree = tree
	w.record()
	return s, nil
}

func (s State) ResizeTiled(id WindowID, d Direction, delta int) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil || !w.Tree.Contains(id) {
		return State{}, fmt.Errorf("resize window %d: %w", id, ErrUnknownWindow)
	}
	tree, _, err := w.Tree.Resize(id, s.output(w.Output).Area, d, delta)
	if err != nil {
		return State{}, err
	}
	w.Tree = tree
	return s, nil
}

func (s State) ResizeFocused(dx, dy int) (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	s = s.clone()
	w := s.windowWorkspace(id)
	if i := w.floatingIndex(id); i >= 0 {
		r := w.Floating[i].Rect
		r.Max = r.Max.Add(image.Pt(dx, dy))
		w.Floating[i].Rect = s.limits[id].floored().fitRect(r, true, true)
		return s, nil
	}
	area := s.output(w.Output).Area
	for _, axis := range []struct {
		delta         int
		ahead, behind Direction
	}{{dx, Right, Left}, {dy, Down, Up}} {
		if axis.delta == 0 {
			continue
		}
		tree, done, err := w.Tree.Resize(id, area, axis.ahead, axis.delta)
		if err != nil {
			return State{}, err
		}
		if !done {
			if tree, _, err = w.Tree.Resize(id, area, axis.behind, axis.delta); err != nil {
				return State{}, err
			}
		}
		w.Tree = tree
	}
	return s, nil
}
