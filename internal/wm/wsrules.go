package wm

import (
	"fmt"
	"slices"
)

type WorkspaceSelector uint8

const (
	SelectID WorkspaceSelector = iota
	SelectAny
	SelectSingleTiled
	SelectFullscreen
)

type WorkspaceLook struct {
	Gaps        *Gaps
	BorderWidth *int
	Rounding    *int
	Shadow      *bool
}

func (l WorkspaceLook) then(r WorkspaceLook) WorkspaceLook {
	l.Gaps = pick(l.Gaps, r.Gaps)
	l.BorderWidth = pick(l.BorderWidth, r.BorderWidth)
	l.Rounding = pick(l.Rounding, r.Rounding)
	l.Shadow = pick(l.Shadow, r.Shadow)
	return l
}

type WorkspaceRule struct {
	ID             WorkspaceID
	Select         WorkspaceSelector
	Name           string
	Output         string
	Default        bool
	Persistent     bool
	OnCreatedEmpty string
	Layout         LayoutKind
	Look           WorkspaceLook
}

func (s State) WithWorkspaceRules(rules []WorkspaceRule) State {
	s = s.clone()
	s.wsRules = slices.Clone(rules)
	for _, r := range s.wsRules {
		if r.Select != SelectID {
			continue
		}
		if w := s.workspace(r.ID); w != nil {
			if out := s.boundOutput(r.ID); out != "" && w.Output != out {
				from := w.Output
				s.rehome(w, out)
				s.replaceActive(from, r.ID)
			}
			continue
		}
		if r.Persistent {
			output := s.boundOutput(r.ID)
			if output == "" {
				output = s.focused
			}
			s.create(r.ID, output)
		}
	}
	for i := range s.outputs {
		s.showDefault(&s.outputs[i])
	}
	return s
}

func (s *State) idRule(id WorkspaceID) *WorkspaceRule {
	for i := range s.wsRules {
		if r := &s.wsRules[i]; r.Select == SelectID && r.ID == id {
			return r
		}
	}
	return nil
}

func (s *State) boundOutput(id WorkspaceID) string {
	if r := s.idRule(id); r != nil && r.Output != "" && s.output(r.Output) != nil {
		return r.Output
	}
	return ""
}

func (s *State) persistent(id WorkspaceID) bool {
	r := s.idRule(id)
	return r != nil && r.Persistent
}

func (s *State) showDefault(o *Output) {
	for _, r := range s.wsRules {
		if r.Select != SelectID || !r.Default || r.Output != o.Name {
			continue
		}
		w := s.workspace(r.ID)
		if w == nil {
			w = s.create(r.ID, o.Name)
		}
		if w.Output == o.Name && o.Active != r.ID {
			if cur := s.workspace(o.Active); cur == nil || cur.empty() {
				prev := o.Active
				o.Active = r.ID
				s.dropIfIdle(prev)
			}
		}
		return
	}
}

func (s *State) bindTo(output string) {
	for _, r := range s.wsRules {
		if r.Select != SelectID || r.Output != output {
			continue
		}
		switch w := s.workspace(r.ID); {
		case w != nil:
			s.rehome(w, output)
		case r.Persistent:
			s.create(r.ID, output)
		}
	}
}

func (s State) WorkspaceName(id WorkspaceID) string {
	if r := s.idRule(id); r != nil {
		return r.Name
	}
	return ""
}

func (s State) OnCreatedEmpty(id WorkspaceID) string {
	if r := s.idRule(id); r != nil {
		return r.OnCreatedEmpty
	}
	return ""
}

type WorkspaceName string

func (n WorkspaceName) String() string { return string(n) }

func (n WorkspaceName) resolve(s State) (WorkspaceID, error) {
	for _, r := range s.wsRules {
		if r.Select == SelectID && r.Name == string(n) {
			return r.ID, nil
		}
	}
	return 0, fmt.Errorf("workspace %q: %w", string(n), ErrInvalidWorkspace)
}

func countWindows(n Node) int {
	switch n := n.(type) {
	case Window:
		return 1
	case *Group:
		if n == nil {
			return 0
		}
		total := 0
		for _, c := range n.Children {
			total += countWindows(c.Node)
		}
		return total
	}
	return 0
}

func (s *State) lookFor(w *Workspace) WorkspaceLook {
	var look WorkspaceLook
	for _, r := range s.wsRules {
		var applies bool
		switch r.Select {
		case SelectID:
			applies = r.ID == w.ID
		case SelectAny:
			applies = true
		case SelectSingleTiled:
			applies = w.Cover.ID == 0 && len(w.Floating) == 0 && countWindows(w.Tree.root) == 1
		case SelectFullscreen:
			applies = w.Cover.Mode == ModeFullscreen
		}
		if applies {
			look = look.then(r.Look)
		}
	}
	return look
}

func (s State) WorkspaceEmpty(id WorkspaceID) bool {
	w := s.workspace(id)
	return w == nil || w.empty()
}
