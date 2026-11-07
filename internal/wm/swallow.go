package wm

import (
	"fmt"
	"image"
	"slices"
)

type Swallow struct {
	Terminal WindowID `json:"terminal"`
	By       WindowID `json:"by"`
	Hidden   bool     `json:"hidden"`
}

func (s State) swallowIndex(id WindowID) int {
	return slices.IndexFunc(s.swallows, func(p Swallow) bool { return p.Terminal == id || p.By == id })
}

func (s State) hiddenBy(id WindowID) (WindowID, bool) {
	for _, p := range s.swallows {
		if p.Hidden && p.Terminal == id {
			return p.By, true
		}
	}
	return 0, false
}

func (s State) SwallowedBy(id WindowID) (WindowID, bool) {
	return s.hiddenBy(id)
}

func (s State) Swallowable(id WindowID) bool {
	w := s.windowWorkspace(id)
	_, hidden := s.hiddenBy(id)
	return w != nil && !hidden && s.swallowIndex(id) < 0 && w.Tree.Contains(id) && w.Cover.ID != id
}

func (s *State) dropSwallows(id WindowID) {
	s.swallows = slices.DeleteFunc(s.swallows, func(p Swallow) bool { return p.Terminal == id || p.By == id })
}

func (t Tree) Replace(old, next WindowID) (Tree, error) {
	if t.Contains(next) {
		return Tree{}, fmt.Errorf("wm: put window %d in the place of %d: already in the tree", next, old)
	}
	t = t.clone()
	groups, idx, ok := t.path(old)
	if !ok {
		return Tree{}, fmt.Errorf("put window %d in the place of %d: %w", next, old, ErrUnknownWindow)
	}
	groups[len(groups)-1].Children[idx[len(idx)-1]].Node = Window{ID: next}
	return t, nil
}

func (w *Workspace) replace(old, next WindowID) error {
	tree, err := w.Tree.Replace(old, next)
	if err != nil {
		return err
	}
	w.Tree = tree
	w.history = slices.DeleteFunc(w.history, func(x WindowID) bool { return x == next })
	if i := slices.Index(w.history, old); i >= 0 {
		w.history[i] = next
	}
	if w.Cover.ID == old {
		w.Cover = Cover{}
	}
	return nil
}

func (s *State) unmapFrom(w *Workspace, id WindowID) error {
	i := s.swallowIndex(id)
	if i < 0 || !s.swallows[i].Hidden {
		return w.remove(id)
	}
	p := s.swallows[i]
	switch {
	case p.Terminal == id:
		return nil
	case w.Tree.Contains(id):
		return w.replace(id, p.Terminal)
	}
	if err := w.remove(id); err != nil {
		return err
	}
	tree, err := w.Tree.Insert(p.Terminal, rootLayout(s.areaOf(w)))
	if err != nil {
		return err
	}
	w.Tree, w.floatFocus = tree, 0
	w.record()
	return nil
}

func (s *State) areaOf(w *Workspace) image.Rectangle {
	if o := s.output(w.Output); o != nil {
		return o.Area
	}
	return image.Rectangle{}
}

func (s *State) swallowAt(w *Workspace, child, terminal WindowID) error {
	if err := w.replace(terminal, child); err != nil {
		return err
	}
	w.history = slices.DeleteFunc(w.history, func(x WindowID) bool { return x == terminal })
	s.swallows = append(s.swallows, Swallow{Terminal: terminal, By: child, Hidden: true})
	return nil
}

func (s State) SwitchSwallow(sw Switch) (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	i := s.swallowIndex(id)
	if i < 0 {
		return s, nil
	}
	p := s.swallows[i]
	hide := sw.From(p.Hidden)
	if hide == p.Hidden {
		return s, nil
	}
	s = s.clone()
	s.swallows[i].Hidden = hide
	if hide {
		from := s.workspaceHolding(p.Terminal)
		if from == nil || !from.Tree.Contains(p.Terminal) {
			return State{}, fmt.Errorf("swallow window %d: %w", p.Terminal, ErrUnknownWindow)
		}
		if err := from.remove(p.Terminal); err != nil {
			return State{}, err
		}
		s.vacated(from.ID)
		return s, s.windowWorkspace(p.By).focus(p.By, Raise)
	}
	w := s.windowWorkspace(p.By)
	if !w.Tree.Contains(p.By) {
		tree, err := w.Tree.Insert(p.Terminal, rootLayout(s.areaOf(w)))
		if err != nil {
			return State{}, err
		}
		w.Tree = tree
		return s, w.focus(id, Raise)
	}
	focused, err := w.Tree.Focus(p.By)
	if err != nil {
		return State{}, err
	}
	tree, err := focused.Insert(p.Terminal, rootLayout(s.areaOf(w)))
	if err != nil {
		return State{}, err
	}
	w.Tree = tree
	return s, w.focus(id, Raise)
}

func (s *State) workspaceHolding(id WindowID) *Workspace {
	for i := range s.workspaces {
		if s.workspaces[i].contains(id) {
			return &s.workspaces[i]
		}
	}
	for i := range s.scratch {
		if s.scratch[i].contains(id) {
			return &s.scratch[i]
		}
	}
	return nil
}

func (s State) placeSwallowed(all *Arranger) {
	for _, p := range s.swallows {
		if !p.Hidden {
			continue
		}
		for _, by := range all.Windows {
			if by.ID == p.By {
				all.Windows = append(all.Windows, Placement{ID: p.Terminal, Rect: by.Rect, Workspace: by.Workspace})
				break
			}
		}
	}
}

type SwallowAction struct{ Switch Switch }

func (a SwallowAction) String() string { return switched("swallow", a.Switch) }

func (a SwallowAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.SwitchSwallow(a.Switch)
	return next, nil, err
}

func parseSwallow(spec ActionSpec, args []string, _ string) (Action, error) {
	sw, err := optionalSwitch(spec, args)
	return SwallowAction{Switch: sw}, err
}

func (s State) openSwallowing(id WindowID, o Opening) (State, error) {
	if !s.Swallowable(o.Swallow) {
		return State{}, fmt.Errorf("wm: open window %d in the place of %d: that window cannot be swallowed", id, o.Swallow)
	}
	s = s.clone()
	w := s.windowWorkspace(o.Swallow)
	if err := s.swallowAt(w, id, o.Swallow); err != nil {
		return State{}, err
	}
	if err := w.focus(id, Raise); err != nil {
		return State{}, err
	}
	if o.Mode != ModeNormal {
		return s.SetMode(id, o.Mode, true)
	}
	return s, nil
}
