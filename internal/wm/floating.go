package wm

import (
	"fmt"
	"image"
	"slices"
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

type Edges uint8

const (
	EdgeTop Edges = 1 << iota
	EdgeBottom
	EdgeLeft
	EdgeRight
)

func (e Edges) Has(edge Edges) bool {
	return e&edge != 0
}

func (l Limits) fitRect(r image.Rectangle, moving Edges) image.Rectangle {
	size := l.Fit(r.Size())
	if moving.Has(EdgeLeft) {
		r.Min.X = r.Max.X - size.X
	} else {
		r.Max.X = r.Min.X + size.X
	}
	if moving.Has(EdgeTop) {
		r.Min.Y = r.Max.Y - size.Y
	} else {
		r.Max.Y = r.Min.Y + size.Y
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
		w.Floating[i].Rect = l.fitRect(w.Floating[i].Rect, 0)
	}
	return s, nil
}

func (s State) FloatingRect(id WindowID) (image.Rectangle, bool) {
	w := s.windowWorkspace(id)
	if w == nil {
		return image.Rectangle{}, false
	}
	i := w.floatingIndex(id)
	if i < 0 {
		return image.Rectangle{}, false
	}
	return w.Floating[i].Rect, true
}

func Centered(area image.Rectangle, size image.Point) image.Rectangle {
	corner := area.Min.Add(area.Size().Sub(size).Div(2))
	return image.Rectangle{Min: corner, Max: corner.Add(size)}
}

func (s State) MapFloating(id WindowID, rect image.Rectangle) (State, error) {
	return s.Open(id, Opening{Float: true, Rect: rect})
}

func (s State) ToggleFloating(at Autotile) (State, error) {
	if w, g := s.lifted(); g != nil {
		return s.floatLifted(w, g)
	}
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	if s.Floats(id) {
		return s.Tile(id, at)
	}
	area := s.output(s.windowWorkspace(id).Output).Area
	return s.Float(id, Centered(area, s.limits[id].floored().Fit(area.Size().Div(2))))
}

func (s State) Float(id WindowID, rect image.Rectangle) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil || !w.Tree.Contains(id) {
		return State{}, fmt.Errorf("float window %d: %w", id, ErrUnknownWindow)
	}
	focused, _ := w.focused()
	if err := w.remove(id); err != nil {
		return State{}, err
	}
	w.Floating = append(w.Floating, Floating{ID: id, Rect: s.limits[id].fitRect(rect, 0)})
	return s, w.focus(focused, Raise)
}

func (s State) Tile(id WindowID, at Autotile) (State, error) {
	s = s.clone()
	delete(s.pinned, id)
	w := s.windowWorkspace(id)
	if w == nil || w.floatingIndex(id) < 0 {
		return State{}, fmt.Errorf("tile window %d: %w", id, ErrUnknownWindow)
	}
	focused, _ := w.focused()
	if err := w.remove(id); err != nil {
		return State{}, err
	}
	tree, err := w.Tree.Place(id, s.output(w.Output).Area, at)
	if err != nil {
		return State{}, err
	}
	w.Tree = tree
	return s, w.focus(focused, Raise)
}

func (s State) SetFloatingRect(id WindowID, rect image.Rectangle) (State, error) {
	s = s.sharingWorkspaces()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("place floating window %d: %w", id, ErrUnknownWindow)
	}
	if i := w.minimizedIndex(id); i >= 0 && w.Minimized[i].Layer == LayerFloating {
		w.Minimized = slices.Clone(w.Minimized)
		w.Minimized[i].Rect = s.limits[id].fitRect(rect, 0)
		return s, nil
	}
	i := w.floatingIndex(id)
	if i < 0 {
		return State{}, fmt.Errorf("wm: window %d is not floating", id)
	}
	w.Floating = slices.Clone(w.Floating)
	w.Floating[i].Rect = s.limits[id].fitRect(rect, 0)
	return s, nil
}

func (s State) ResizeFloating(id WindowID, from image.Rectangle, d image.Point, moving Edges) (State, error) {
	r := from
	switch {
	case moving.Has(EdgeLeft):
		r.Min.X += d.X
	case moving.Has(EdgeRight):
		r.Max.X += d.X
	}
	switch {
	case moving.Has(EdgeTop):
		r.Min.Y += d.Y
	case moving.Has(EdgeBottom):
		r.Max.Y += d.Y
	}
	return s.SetFloatingRect(id, s.limits[id].floored().fitRect(r, moving))
}

func (s State) Floats(id WindowID) bool {
	w := s.windowWorkspace(id)
	if w == nil {
		return false
	}
	if i := w.minimizedIndex(id); i >= 0 {
		return w.Minimized[i].Layer == LayerFloating
	}
	return w.floatingIndex(id) >= 0
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
	s = s.sharingWorkspaces()
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
	s = s.sharingWorkspaces()
	w := s.windowWorkspace(id)
	if i := w.floatingIndex(id); i >= 0 {
		r := w.Floating[i].Rect
		r.Max = r.Max.Add(image.Pt(dx, dy))
		w.Floating = slices.Clone(w.Floating)
		w.Floating[i].Rect = s.limits[id].floored().fitRect(r, 0)
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

type SharedEdges struct {
	Left, Right, Top, Bottom bool
}

type side uint8

const (
	sideWhole side = iota
	sideLow
	sideHigh
)

func halfRect(area image.Rectangle, gaps Gaps, x, y side) image.Rectangle {
	r := gaps.Outer.shrink(area)
	whole := r
	midX, midY := r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2
	lead, trail := gaps.Inner/2, gaps.Inner-gaps.Inner/2
	switch x {
	case sideLow:
		r.Max.X = midX - lead
	case sideHigh:
		r.Min.X = midX + trail
	}
	switch y {
	case sideLow:
		r.Max.Y = midY - lead
	case sideHigh:
		r.Min.Y = midY + trail
	}
	if r.Empty() {
		return whole
	}
	return r
}

func SnapRect(area image.Rectangle, at image.Point, distance int, gaps Gaps, shared SharedEdges) (image.Rectangle, bool) {
	left, right := !shared.Left && at.X-area.Min.X < distance, !shared.Right && area.Max.X-at.X <= distance
	top, bottom := !shared.Top && at.Y-area.Min.Y < distance, !shared.Bottom && area.Max.Y-at.Y <= distance
	if !left && !right && !top && !bottom || !at.In(area) {
		return image.Rectangle{}, false
	}
	x, y := sideWhole, sideWhole
	switch {
	case left:
		x = sideLow
	case right:
		x = sideHigh
	}
	switch {
	case top && (left || right):
		y = sideLow
	case bottom:
		y = sideHigh
	}
	return halfRect(area, gaps, x, y), true
}

func (s State) SnapTarget(id WindowID, area image.Rectangle, at image.Point, distance int, gaps Gaps, shared SharedEdges) (image.Rectangle, bool) {
	r, ok := SnapRect(area, at, distance, gaps, shared)
	if !ok {
		return image.Rectangle{}, false
	}
	return s.limits[id].floored().fitRect(r, 0), true
}

func (s State) DropZones(id WindowID, reuse DropZones) (DropZones, error) {
	w := s.windowWorkspace(id)
	if w == nil || !w.Tree.Contains(id) {
		return DropZones{}, fmt.Errorf("drop window %d: %w", id, ErrUnknownWindow)
	}
	tree, err := w.Tree.Remove(id)
	if err != nil {
		return DropZones{}, err
	}
	return tree.dropZones(reuse, s.output(w.Output).Area), nil
}

func (s State) DropTarget(id WindowID, at image.Point, auto Autotile, sp Spacing) (image.Rectangle, error) {
	dropped, err := s.Reinsert(id, at, auto)
	if err != nil {
		return image.Rectangle{}, err
	}
	for _, p := range dropped.Arrange(sp).Windows {
		if p.ID == id {
			return p.Rect, nil
		}
	}
	return image.Rectangle{}, fmt.Errorf("drop window %d: %w", id, ErrUnknownWindow)
}

func KeptAspect(from image.Rectangle, d image.Point, moving Edges) (image.Point, Edges) {
	if from.Dx() <= 0 || from.Dy() <= 0 {
		return d, moving
	}
	signed := func(lo, hi Edges, delta int) int {
		switch {
		case moving.Has(lo):
			return -delta
		case moving.Has(hi):
			return delta
		}
		return 0
	}
	horizontal, vertical := moving.Has(EdgeLeft) || moving.Has(EdgeRight), moving.Has(EdgeTop) || moving.Has(EdgeBottom)
	grow := image.Pt(signed(EdgeLeft, EdgeRight, d.X), signed(EdgeTop, EdgeBottom, d.Y))
	if !vertical || horizontal && grow.X*from.Dy() >= grow.Y*from.Dx() {
		grow.Y = grow.X * from.Dy() / from.Dx()
	} else {
		grow.X = grow.Y * from.Dx() / from.Dy()
	}
	if !horizontal {
		moving |= EdgeRight
	}
	if !vertical {
		moving |= EdgeBottom
	}
	out := grow
	if moving.Has(EdgeLeft) {
		out.X = -grow.X
	}
	if moving.Has(EdgeTop) {
		out.Y = -grow.Y
	}
	return out, moving
}
