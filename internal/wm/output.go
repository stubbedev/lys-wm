package wm

import (
	"fmt"
	"image"
	"slices"
)

type OutputTarget interface {
	fmt.Stringer
	outputFrom(s State) (string, bool, error)
}

type OutputName string

func (n OutputName) String() string { return string(n) }

func (n OutputName) outputFrom(s State) (string, bool, error) {
	if s.output(string(n)) == nil {
		return "", false, fmt.Errorf("output %s: %w", n, ErrUnknownOutput)
	}
	return string(n), true, nil
}

func (d Direction) outputFrom(s State) (string, bool, error) {
	name, ok := s.outputToward(s.focused, d)
	return name, ok, nil
}

func parseOutputTarget(text string) OutputTarget {
	if d, ok := parseDirection(text); ok {
		return d
	}
	return OutputName(text)
}

func rootLayout(area image.Rectangle) Layout {
	if area.Dy() > area.Dx() {
		return SplitV
	}
	return SplitH
}

func splitAlong(d Direction) Layout {
	if d.vertical() {
		return SplitV
	}
	return SplitH
}

func (t Tree) InsertEdge(id WindowID, d Direction, area image.Rectangle) (Tree, error) {
	if t.Contains(id) {
		return Tree{}, fmt.Errorf("wm: insert window %d at the %s edge: already in the tree", id, d)
	}
	t = t.clone()
	t.insertEdge(Window{ID: id}, d, area)
	return t, nil
}

func (t *Tree) insertEdge(n Node, d Direction, area image.Rectangle) {
	if t.root == nil {
		t.attach(n, rootLayout(area))
		return
	}
	want := splitAlong(d)
	switch {
	case t.root.Layout == want:
	case len(t.root.Children) == 1 && t.root.Layout.split():
		t.root.Layout = want
	default:
		root := t.newGroup(want)
		root.Children = []Child{{Node: t.root, Ratio: 1}}
		t.root = root
	}
	at := 0
	if !d.forward() {
		at = len(t.root.Children)
	}
	t.root.insert(at, n)
	t.focusNode(n)
}

func rehomed(r, from, to image.Rectangle) image.Rectangle {
	return clampInto(r.Add(to.Min.Sub(from.Min)), to)
}

func (s *State) rehome(w *Workspace, output string) {
	from, to := s.output(w.Output), s.output(output)
	if from != nil && to != nil {
		w.Floating = slices.Clone(w.Floating)
		for i := range w.Floating {
			w.Floating[i].Rect = rehomed(w.Floating[i].Rect, from.Area, to.Area)
		}
		w.Minimized = slices.Clone(w.Minimized)
		for i := range w.Minimized {
			w.Minimized[i].Rect = rehomed(w.Minimized[i].Rect, from.Area, to.Area)
		}
	}
	w.Output, w.home = output, ""
}

func (s *State) replaceActive(output string, leaving WorkspaceID) {
	if o := s.output(output); o != nil && o.Active == leaving {
		s.fixActive(output)
	}
}

func (s State) MoveWorkspaceToOutput(id WorkspaceID, output string) (State, error) {
	switch {
	case s.output(output) == nil:
		return State{}, fmt.Errorf("move workspace %d to %s: %w", id, output, ErrUnknownOutput)
	case s.workspace(id) == nil || id < 1:
		return State{}, fmt.Errorf("move workspace %d to %s: %w", id, output, ErrInvalidWorkspace)
	}
	s = s.clone()
	w := s.workspace(id)
	from := w.Output
	if from != output {
		s.rehome(w, output)
		s.replaceActive(from, id)
	}
	s.activate(id)
	return s, nil
}

func (s State) SwapWorkspaces(a, b string) (State, error) {
	oa, ob := s.output(a), s.output(b)
	if oa == nil || ob == nil {
		return State{}, fmt.Errorf("swap the workspaces of %s and %s: %w", a, b, ErrUnknownOutput)
	}
	if a == b {
		return s, nil
	}
	s = s.clone()
	oa, ob = s.output(a), s.output(b)
	wa, wb := s.workspace(oa.Active), s.workspace(ob.Active)
	s.rehome(wa, b)
	s.rehome(wb, a)
	oa.Active, ob.Active = wb.ID, wa.ID
	s.gatherPinned()
	return s, nil
}

func (s State) MoveWindowToOutput(win WindowID, output string, at Autotile) (State, error) {
	o := s.output(output)
	if o == nil {
		return State{}, fmt.Errorf("move window %d to %s: %w", win, output, ErrUnknownOutput)
	}
	next, err := s.MoveWindowToWorkspace(win, o.Active, at)
	if err != nil {
		return State{}, err
	}
	return next.FocusWindow(win, Raise)
}

type (
	FocusOutputAction           struct{ Target OutputTarget }
	MoveToOutputAction          struct{ Target OutputTarget }
	MoveWorkspaceToOutputAction struct{ Target OutputTarget }
	SwapWorkspacesAction        struct{ A, B OutputTarget }
)

func (a FocusOutputAction) String() string  { return "focusoutput " + a.Target.String() }
func (a MoveToOutputAction) String() string { return "movetooutput " + a.Target.String() }
func (a MoveWorkspaceToOutputAction) String() string {
	return "moveworkspacetooutput " + a.Target.String()
}

func (a SwapWorkspacesAction) String() string {
	return "swapworkspaces " + a.A.String() + " " + a.B.String()
}

func (a FocusOutputAction) apply(s State, _ Settings) (State, []Effect, error) {
	name, found, err := a.Target.outputFrom(s)
	if err != nil || !found {
		return s, nil, err
	}
	next, err := s.FocusOutput(name)
	return next, nil, err
}

func (a MoveToOutputAction) apply(s State, settings Settings) (State, []Effect, error) {
	name, found, err := a.Target.outputFrom(s)
	if err != nil || !found {
		return s, nil, err
	}
	win, ok := s.Focused()
	if !ok {
		return s, nil, nil
	}
	next, err := s.MoveWindowToOutput(win, name, settings.Autotile)
	return next, nil, err
}

func (a MoveWorkspaceToOutputAction) apply(s State, _ Settings) (State, []Effect, error) {
	name, found, err := a.Target.outputFrom(s)
	if err != nil || !found {
		return s, nil, err
	}
	w := s.activeWorkspace()
	if w == nil || w.Scratch.Name != "" {
		return s, nil, nil
	}
	next, err := s.MoveWorkspaceToOutput(w.ID, name)
	return next, nil, err
}

func (a SwapWorkspacesAction) apply(s State, _ Settings) (State, []Effect, error) {
	first, foundA, err := a.A.outputFrom(s)
	if err != nil {
		return State{}, nil, err
	}
	second, foundB, err := a.B.outputFrom(s)
	if err != nil || !foundA || !foundB {
		return s, nil, err
	}
	next, err := s.SwapWorkspaces(first, second)
	return next, nil, err
}

func outputTargeted(build func(OutputTarget) Action) func(ActionSpec, []string, string) (Action, error) {
	return func(spec ActionSpec, args []string, _ string) (Action, error) {
		if err := exactly(spec, args, 1); err != nil {
			return nil, err
		}
		return build(parseOutputTarget(args[0])), nil
	}
}

func parseSwapWorkspaces(spec ActionSpec, args []string, _ string) (Action, error) {
	if err := exactly(spec, args, 2); err != nil {
		return nil, err
	}
	return SwapWorkspacesAction{A: parseOutputTarget(args[0]), B: parseOutputTarget(args[1])}, nil
}
