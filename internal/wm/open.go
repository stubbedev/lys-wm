package wm

import (
	"fmt"
	"image"
	"slices"
)

type Opening struct {
	Float      bool
	Rect       image.Rectangle
	Workspace  WorkspaceID
	Output     string
	Scratchpad ScratchpadName
	Scratch    ScratchpadSpec
	Silent     bool
	NoFocus    bool
	Group      *Layout
	Pin        bool
	Swallow    WindowID
	Mode       WindowMode
	Cover      CoverPolicy
	Autotile   Autotile
}

func (s *State) openingTarget(id WindowID, o Opening) (*Workspace, error) {
	switch {
	case o.Scratchpad != "":
		return s.ensureScratchpad(o.Scratchpad, o.Scratch), nil
	case o.Workspace != 0:
		if w := s.workspace(o.Workspace); w != nil {
			return w, nil
		}
		output := o.Output
		if s.output(output) == nil {
			output = s.focused
		}
		return s.create(o.Workspace, output), nil
	case o.Output != "":
		out := s.output(o.Output)
		if out == nil {
			return nil, fmt.Errorf("open window %d on %s: %w", id, o.Output, ErrUnknownOutput)
		}
		return s.workspace(out.Active), nil
	}
	if w := s.activeWorkspace(); w != nil {
		return w, nil
	}
	if len(s.outputs) > 0 {
		return nil, fmt.Errorf("wm: open window %d: no active workspace", id)
	}
	if w := s.firstOn(""); w != nil {
		return w, nil
	}
	return s.create(s.firstUnused(), ""), nil
}

func (s State) Open(id WindowID, o Opening) (State, error) {
	if id == 0 {
		return State{}, ErrReservedWindow
	}
	if s.windowWorkspace(id) != nil {
		return State{}, fmt.Errorf("wm: open window %d: already mapped", id)
	}
	if o.Swallow != 0 {
		return s.openSwallowing(id, o)
	}
	s = s.clone()
	w, err := s.openingTarget(id, o)
	if err != nil {
		return State{}, err
	}
	before, hadFocus := w.focused()
	if o.Float {
		w.Floating = append(w.Floating, Floating{ID: id, Rect: o.Rect})
		w.floatFocus = true
	} else {
		var area image.Rectangle
		if out := s.output(w.Output); out != nil {
			area = out.Area
		}
		tree, err := grouped(w.Tree, o.Group)
		if err != nil {
			return State{}, err
		}
		at := o.Autotile
		if o.Group != nil {
			at.Enabled = false
		}
		if w.Tree, err = tree.Place(id, area, at); err != nil {
			return State{}, err
		}
		w.floatFocus = false
	}
	w.record()
	if o.NoFocus && hadFocus {
		w.history = slices.DeleteFunc(w.history, func(x WindowID) bool { return x == id })
		w.history = slices.Insert(w.history, max(len(w.history)-1, 0), id)
		if err := w.focus(before); err != nil {
			return State{}, err
		}
	}
	follows := (o.Workspace != 0 || o.Output != "") && !o.Silent && !o.NoFocus && w.Scratch.Name == ""
	if follows && s.output(w.Output) != nil {
		s.activate(w.ID)
	}
	if !o.NoFocus {
		if s, err = s.Opened(id, o.Cover); err != nil {
			return State{}, err
		}
	}
	if o.Mode != ModeNormal {
		if s, err = s.SetMode(id, o.Mode, true); err != nil {
			return State{}, err
		}
	}
	if o.Pin {
		return s.SetPinned(id, true)
	}
	return s, nil
}

func grouped(t Tree, layout *Layout) (Tree, error) {
	if layout == nil {
		return t, nil
	}
	focused, ok := t.Focused()
	if !ok {
		return t, nil
	}
	groups, _, _ := t.path(focused)
	if groups[len(groups)-1].Layout == *layout {
		return t, nil
	}
	return t.Wrap(focused, *layout, false)
}
