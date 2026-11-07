package wm

import (
	"fmt"
	"image"
	"maps"
)

const DefaultFloatStep = 40

type restorePoint struct {
	rect  image.Rectangle
	tiled bool
}

type Placing uint8

const (
	PlaceLeft Placing = iota + 1
	PlaceRight
	PlaceTop
	PlaceBottom
	PlaceTopLeft
	PlaceTopRight
	PlaceBottomLeft
	PlaceBottomRight
	PlaceFill
	PlaceCenter
	PlaceRestore
)

var placings = []string{
	PlaceLeft: "left", PlaceRight: "right", PlaceTop: "top", PlaceBottom: "bottom",
	PlaceTopLeft: "topleft", PlaceTopRight: "topright", PlaceBottomLeft: "bottomleft", PlaceBottomRight: "bottomright",
	PlaceFill: "fill", PlaceCenter: "center", PlaceRestore: "restore",
}

var placingSides = [...][2]side{
	PlaceLeft: {sideLow, sideWhole}, PlaceRight: {sideHigh, sideWhole}, PlaceTop: {sideWhole, sideLow}, PlaceBottom: {sideWhole, sideHigh},
	PlaceTopLeft: {sideLow, sideLow}, PlaceTopRight: {sideHigh, sideLow}, PlaceBottomLeft: {sideLow, sideHigh}, PlaceBottomRight: {sideHigh, sideHigh},
	PlaceFill: {sideWhole, sideWhole},
}

func (p Placing) String() string { return enumName(placings, p, "Placing") }

func (s *State) remember(id WindowID, p restorePoint) {
	s.restore = maps.Clone(s.restore)
	if s.restore == nil {
		s.restore = map[WindowID]restorePoint{}
	}
	s.restore[id] = p
}

func (s *State) forget(id WindowID) {
	if _, ok := s.restore[id]; ok {
		s.restore = maps.Clone(s.restore)
		delete(s.restore, id)
	}
}

func (s State) Place(id WindowID, p Placing, settings Settings) (State, error) {
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("place window %d %s: %w", id, p, ErrUnknownWindow)
	}
	if p == PlaceRestore {
		return s.restorePlace(id, settings.Autotile)
	}
	o := s.output(w.Output)
	if o == nil {
		return State{}, fmt.Errorf("place window %d %s on %q: %w", id, p, w.Output, ErrUnknownOutput)
	}
	current, floats := s.FloatingRect(id)
	tiled := w.Tree.Contains(id)
	if !floats && !tiled {
		return s, nil
	}
	fit := s.limits[id].floored()
	var rect image.Rectangle
	if p == PlaceCenter {
		size := current.Size()
		if tiled {
			size = fit.Fit(o.Area.Size().Div(2))
		}
		rect = Centered(o.Area, size)
	} else {
		sides := placingSides[p]
		rect = fit.fitRect(halfRect(o.Area, settings.Gaps, sides[0], sides[1]), 0)
	}
	var next State
	var err error
	if tiled {
		next, err = s.Float(id, rect)
	} else {
		next, err = s.SetFloatingRect(id, rect)
	}
	if err != nil {
		return State{}, err
	}
	next.remember(id, restorePoint{rect: current, tiled: tiled})
	return next, nil
}

func (s State) restorePlace(id WindowID, at Autotile) (State, error) {
	p, ok := s.restore[id]
	if !ok {
		return s, nil
	}
	var next State
	var err error
	switch {
	case p.tiled && s.Floats(id):
		next, err = s.Tile(id, at)
	case p.tiled:
		next = s
	case s.Floats(id):
		next, err = s.SetFloatingRect(id, p.rect)
	default:
		next, err = s.Float(id, p.rect)
	}
	if err != nil {
		return State{}, err
	}
	next.forget(id)
	return next, nil
}

func (s State) SnapFloating(id WindowID, rect, from image.Rectangle) (State, error) {
	next, err := s.SetFloatingRect(id, rect)
	if err != nil {
		return State{}, err
	}
	next.remember(id, restorePoint{rect: from})
	return next, nil
}

func clampInto(r, area image.Rectangle) image.Rectangle {
	r = r.Add(image.Pt(min(0, area.Max.X-r.Max.X), min(0, area.Max.Y-r.Max.Y)))
	return r.Add(image.Pt(max(0, area.Min.X-r.Min.X), max(0, area.Min.Y-r.Min.Y)))
}

func (d Direction) delta(n int) image.Point {
	switch d {
	case Left:
		return image.Pt(-n, 0)
	case Right:
		return image.Pt(n, 0)
	case Up:
		return image.Pt(0, -n)
	}
	return image.Pt(0, n)
}

func atEdge(r, area image.Rectangle, d Direction) bool {
	switch d {
	case Left:
		return r.Min.X <= area.Min.X
	case Right:
		return r.Max.X >= area.Max.X
	case Up:
		return r.Min.Y <= area.Min.Y
	}
	return r.Max.Y >= area.Max.Y
}

func entering(r, area image.Rectangle, d Direction) image.Rectangle {
	switch d {
	case Left:
		return r.Add(image.Pt(area.Max.X-r.Max.X, 0))
	case Right:
		return r.Add(image.Pt(area.Min.X-r.Min.X, 0))
	case Up:
		return r.Add(image.Pt(0, area.Max.Y-r.Max.Y))
	}
	return r.Add(image.Pt(0, area.Min.Y-r.Min.Y))
}

func (s State) stepFloating(w *Workspace, id WindowID, d Direction, step int) (State, error) {
	o := s.output(w.Output)
	if o == nil {
		return s, nil
	}
	r, _ := s.FloatingRect(id)
	if !atEdge(r, o.Area, d) {
		return s.SetFloatingRect(id, clampInto(r.Add(d.delta(step)), o.Area))
	}
	name, ok := s.outputToward(w.Output, d)
	if !ok {
		return s, nil
	}
	next, err := s.MoveWindowToOutput(id, name, Autotile{})
	if err != nil {
		return State{}, err
	}
	landed, _ := next.FloatingRect(id)
	return next.SetFloatingRect(id, entering(landed, next.output(name).Area, d))
}

func (s State) FloatingNeighbours(id WindowID, buf []image.Rectangle) []image.Rectangle {
	w := s.windowWorkspace(id)
	if w == nil || w.Cover.ID != 0 {
		return buf
	}
	for _, f := range w.Floating {
		if f.ID != id {
			buf = append(buf, f.Rect)
		}
	}
	return buf
}

type snapAxis struct {
	shift int
}

func (a *snapAxis) offer(edge, target, distance int) {
	if d := target - edge; abs(d) <= distance && abs(d) < abs(a.shift) {
		a.shift = d
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func SnapToWindows(r image.Rectangle, others []image.Rectangle, distance, gap int) image.Rectangle {
	x, y := snapAxis{shift: distance + 1}, snapAxis{shift: distance + 1}
	for _, o := range others {
		if r.Min.Y < o.Max.Y+distance && o.Min.Y < r.Max.Y+distance {
			x.offer(r.Min.X, o.Max.X+gap, distance)
			x.offer(r.Max.X, o.Min.X-gap, distance)
			x.offer(r.Min.X, o.Min.X, distance)
			x.offer(r.Max.X, o.Max.X, distance)
		}
		if r.Min.X < o.Max.X+distance && o.Min.X < r.Max.X+distance {
			y.offer(r.Min.Y, o.Max.Y+gap, distance)
			y.offer(r.Max.Y, o.Min.Y-gap, distance)
			y.offer(r.Min.Y, o.Min.Y, distance)
			y.offer(r.Max.Y, o.Max.Y, distance)
		}
	}
	var shift image.Point
	if x.shift <= distance {
		shift.X = x.shift
	}
	if y.shift <= distance {
		shift.Y = y.shift
	}
	return r.Add(shift)
}

func (s State) FocusLayer(sw Switch) (State, error) {
	w := s.activeWorkspace()
	if w == nil {
		return s, nil
	}
	floating := sw.From(!w.treeFocused())
	if floating && len(w.Floating) > 0 {
		return s.FocusWindow(w.Floating[len(w.Floating)-1].ID, Raise)
	}
	if id, ok := w.Tree.Focused(); ok && !floating {
		return s.FocusWindow(id, Raise)
	}
	return s, nil
}

type (
	PlaceAction         struct{ Target Placing }
	FocusFloatingAction struct{ Switch Switch }
)

func (a PlaceAction) String() string         { return "place " + a.Target.String() }
func (a FocusFloatingAction) String() string { return switched("focusfloating", a.Switch) }

func (a PlaceAction) apply(s State, settings Settings) (State, []Effect, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil, nil
	}
	next, err := s.Place(id, a.Target, settings)
	return next, nil, err
}

func (a FocusFloatingAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.FocusLayer(a.Switch)
	return next, nil, err
}

func parsePlace(spec ActionSpec, args []string, _ string) (Action, error) {
	if err := exactly(spec, args, 1); err != nil {
		return nil, err
	}
	p, ok := parseEnum[Placing](placings, args[0])
	if !ok {
		return nil, badArgs(spec, "unknown place %q", args[0])
	}
	return PlaceAction{Target: p}, nil
}

func parseFocusFloating(spec ActionSpec, args []string, _ string) (Action, error) {
	sw, err := optionalSwitch(spec, args)
	return FocusFloatingAction{Switch: sw}, err
}
