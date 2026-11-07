package wm

import (
	"fmt"
	"image"
	"slices"
)

type ScratchpadName string

type ScratchSize struct {
	W, H int
}

func (z ScratchSize) area(usable image.Rectangle) image.Rectangle {
	if z == (ScratchSize{}) {
		return usable
	}
	return Centered(usable, image.Pt(usable.Dx()*z.W/100, usable.Dy()*z.H/100))
}

type ScratchpadSpec struct {
	Exec  string
	Size  ScratchSize
	Stays bool
}

type Scratchpad struct {
	Name  ScratchpadName
	Size  ScratchSize
	Stays bool
}

func (s State) Scratchpads() []Workspace {
	return slices.Clone(s.scratch)
}

func (s State) ScratchpadOf(id WorkspaceID) (ScratchpadName, bool) {
	w := s.workspace(id)
	if w == nil || w.Scratch.Name == "" {
		return "", false
	}
	return w.Scratch.Name, true
}

func (s *State) scratchpad(name ScratchpadName) *Workspace {
	for i := range s.scratch {
		if s.scratch[i].Scratch.Name == name {
			return &s.scratch[i]
		}
	}
	return nil
}

func (s *State) ensureScratchpad(name ScratchpadName, spec ScratchpadSpec) *Workspace {
	sp := Scratchpad{Name: name, Size: spec.Size, Stays: spec.Stays}
	if w := s.scratchpad(name); w != nil {
		w.Scratch = sp
		return w
	}
	id := WorkspaceID(-1)
	for s.workspace(id) != nil {
		id--
	}
	s.scratch = append(s.scratch, Workspace{ID: id, Output: s.focused, Scratch: sp})
	return &s.scratch[len(s.scratch)-1]
}

func (s *State) showScratch(o *Output, id WorkspaceID) {
	w := s.workspace(id)
	for i := range s.outputs {
		if other := &s.outputs[i]; other != o && other.Scratch == id {
			other.Scratch = 0
		}
	}
	if from := s.output(w.Output); from != nil && from != o {
		shift := o.Area.Min.Sub(from.Area.Min)
		for i := range w.Floating {
			w.Floating[i].Rect = w.Floating[i].Rect.Add(shift)
		}
	}
	if prev := o.Scratch; prev != 0 && prev != id {
		o.Scratch = 0
		s.dropIfIdle(prev)
	}
	w.Output, o.Scratch = o.Name, id
}

func (s *State) hideScratch(o *Output) {
	id := o.Scratch
	o.Scratch = 0
	s.dropIfIdle(id)
}

func (s State) ToggleScratchpad(name ScratchpadName, spec ScratchpadSpec) (State, bool, error) {
	o := s.output(s.focused)
	if o == nil {
		return State{}, false, fmt.Errorf("toggle scratchpad %s: %w", name, ErrUnknownOutput)
	}
	s = s.clone()
	o = s.output(s.focused)
	if w := s.scratchpad(name); w != nil && o.Scratch == w.ID {
		s.hideScratch(o)
		return s, false, nil
	}
	w := s.ensureScratchpad(name, spec)
	s.showScratch(o, w.ID)
	return s, s.workspace(o.Scratch).empty(), nil
}

func (s State) HideScratchpad() (State, error) {
	o := s.output(s.focused)
	if o == nil || o.Scratch == 0 {
		return s, nil
	}
	s = s.clone()
	s.hideScratch(s.output(s.focused))
	return s, nil
}

func (s State) MoveToScratchpad(name ScratchpadName, spec ScratchpadSpec, at Autotile) (State, error) {
	win, ok := s.Focused()
	if !ok {
		return s, nil
	}
	return s.MoveWindowToScratchpad(win, name, spec, at)
}

func (s State) MoveWindowToScratchpad(win WindowID, name ScratchpadName, spec ScratchpadSpec, at Autotile) (State, error) {
	if s.windowWorkspace(win) == nil {
		return State{}, fmt.Errorf("move window %d to scratchpad %s: %w", win, name, ErrUnknownWindow)
	}
	s = s.clone()
	id := s.ensureScratchpad(name, spec).ID
	return s.MoveWindowToWorkspace(win, id, at)
}

func (s State) placeScratchpad(all *Arranger, w *Workspace, sp Spacing) {
	from := len(all.Windows)
	o := s.output(w.Output)
	if o == nil {
		all.Windows = appendTreeWindows(all.Windows, w.Tree.root)
		for _, f := range w.Floating {
			all.Windows = append(all.Windows, Placement{ID: f.ID, Rect: f.Rect, Layer: LayerFloating})
		}
		all.Windows = appendMinimized(all.Windows, w.Minimized)
	} else {
		over := *o
		over.Area = w.Scratch.Size.area(o.Area)
		placeWorkspace(all, w, &over, o.Scratch == w.ID, sp)
	}
	all.inWorkspace(from, w.ID)
	for i := from; i < len(all.Windows); i++ {
		all.Windows[i].Scratch = true
	}
}

type (
	ScratchpadAction struct {
		Name ScratchpadName
		Hide bool
	}
	MoveToScratchpadAction struct{ Name ScratchpadName }
	ScratchpadExecAction   struct {
		Name    ScratchpadName
		Command string
	}
)

func (ScratchpadExecAction) isEffect() {}

func (a ScratchpadAction) String() string {
	if a.Hide {
		return "scratchpad hide"
	}
	return "scratchpad toggle " + string(a.Name)
}

func (a MoveToScratchpadAction) String() string { return "movetoscratchpad " + string(a.Name) }

func (a ScratchpadAction) apply(s State, settings Settings) (State, []Effect, error) {
	if a.Hide {
		next, err := s.HideScratchpad()
		return next, nil, err
	}
	spec := settings.Scratchpads[a.Name]
	next, empty, err := s.ToggleScratchpad(a.Name, spec)
	if err != nil || !empty || spec.Exec == "" {
		return next, nil, err
	}
	return emit(next, ScratchpadExecAction{Name: a.Name, Command: spec.Exec})
}

func (a MoveToScratchpadAction) apply(s State, settings Settings) (State, []Effect, error) {
	next, err := s.MoveToScratchpad(a.Name, settings.Scratchpads[a.Name], settings.Autotile)
	return next, nil, err
}

func parseScratchpad(spec ActionSpec, args []string, _ string) (Action, error) {
	switch {
	case len(args) == 1 && args[0] == "hide":
		return ScratchpadAction{Hide: true}, nil
	case len(args) == 2 && args[0] == "toggle":
		return ScratchpadAction{Name: ScratchpadName(args[1])}, nil
	}
	return nil, badArgs(spec, "takes toggle <name> or hide")
}

func parseMoveToScratchpad(spec ActionSpec, args []string, _ string) (Action, error) {
	if err := exactly(spec, args, 1); err != nil {
		return nil, err
	}
	return MoveToScratchpadAction{Name: ScratchpadName(args[0])}, nil
}

func (s *State) vacated(id WorkspaceID) {
	if w := s.workspace(id); w != nil && w.Scratch.Name != "" && w.empty() {
		if o := s.output(w.Output); o != nil && o.Scratch == id {
			o.Scratch = 0
		}
	}
	s.dropIfIdle(id)
}

func (s State) ScratchOn(output string) WorkspaceID {
	if o := s.output(output); o != nil {
		return o.Scratch
	}
	return 0
}
