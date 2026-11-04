package wm

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
)

type savedNode struct {
	Window WindowID    `json:"window,omitempty"`
	Group  *savedGroup `json:"group,omitempty"`
	Ratio  float64     `json:"ratio"`
}

type savedGroup struct {
	Layout    Layout      `json:"layout"`
	Children  []savedNode `json:"children"`
	Focus     int         `json:"focus"`
	Ephemeral uint8       `json:"ephemeral"`
}

type savedWorkspace struct {
	ID         WorkspaceID `json:"id"`
	Output     string      `json:"output"`
	Tree       *savedGroup `json:"tree,omitempty"`
	Floating   []Floating  `json:"floating,omitempty"`
	Minimized  []Minimized `json:"minimized,omitempty"`
	Fullscreen WindowID    `json:"fullscreen,omitempty"`
	FloatFocus bool        `json:"float_focus,omitempty"`
	Home       string      `json:"home,omitempty"`
	History    []WindowID  `json:"history,omitempty"`
}

type savedOutput struct {
	Name     string          `json:"name"`
	Full     image.Rectangle `json:"full"`
	Area     image.Rectangle `json:"area"`
	Active   WorkspaceID     `json:"active"`
	Previous WorkspaceID     `json:"previous"`
}

type savedLimit struct {
	ID     WindowID `json:"id"`
	Limits Limits   `json:"limits"`
}

type savedState struct {
	Outputs    []savedOutput          `json:"outputs"`
	Workspaces []savedWorkspace       `json:"workspaces"`
	Focused    string                 `json:"focused"`
	Remembered map[string]WorkspaceID `json:"remembered,omitempty"`
	Limits     []savedLimit           `json:"limits,omitempty"`
}

func saveGroup(g *Group) *savedGroup {
	if g == nil {
		return nil
	}
	out := &savedGroup{Layout: g.Layout, Focus: g.Focus, Ephemeral: uint8(g.ephemeral), Children: make([]savedNode, len(g.Children))}
	for i, ch := range g.Children {
		out.Children[i].Ratio = ch.Ratio
		switch n := ch.Node.(type) {
		case Window:
			out.Children[i].Window = n.ID
		case *Group:
			out.Children[i].Group = saveGroup(n)
		}
	}
	return out
}

func loadGroup(g *savedGroup) (*Group, error) {
	if g == nil {
		return nil, nil
	}
	out := &Group{Layout: g.Layout, Focus: g.Focus, ephemeral: ephemerality(g.Ephemeral), Children: make([]Child, len(g.Children))}
	for i, ch := range g.Children {
		out.Children[i].Ratio = ch.Ratio
		switch {
		case ch.Group != nil:
			sub, err := loadGroup(ch.Group)
			if err != nil {
				return nil, err
			}
			out.Children[i].Node = sub
		case ch.Window != 0:
			out.Children[i].Node = Window{ID: ch.Window}
		default:
			return nil, errors.New("wm: a saved tree has a child that is neither a window nor a group")
		}
	}
	return out, nil
}

func (s State) Save() ([]byte, error) {
	saved := savedState{Focused: s.focused, Remembered: s.remembered}
	for _, o := range s.outputs {
		saved.Outputs = append(saved.Outputs, savedOutput{Name: o.Name, Full: o.Full, Area: o.Area, Active: o.Active, Previous: o.previous})
	}
	for _, w := range s.workspaces {
		saved.Workspaces = append(saved.Workspaces, savedWorkspace{
			ID: w.ID, Output: w.Output, Tree: saveGroup(w.Tree.root), Floating: w.Floating, Minimized: w.Minimized,
			Fullscreen: w.Fullscreen, FloatFocus: w.floatFocus, Home: w.home, History: w.history,
		})
	}
	for id, l := range s.limits {
		saved.Limits = append(saved.Limits, savedLimit{ID: id, Limits: l})
	}
	return json.Marshal(saved)
}

func LoadState(data []byte) (State, error) {
	var saved savedState
	if err := json.Unmarshal(data, &saved); err != nil {
		return State{}, fmt.Errorf("wm: read a saved state: %w", err)
	}
	s := State{focused: saved.Focused, remembered: saved.Remembered, limits: make(map[WindowID]Limits, len(saved.Limits))}
	if s.remembered == nil {
		s.remembered = map[string]WorkspaceID{}
	}
	for _, o := range saved.Outputs {
		s.outputs = append(s.outputs, Output{Name: o.Name, Full: o.Full, Area: o.Area, Active: o.Active, previous: o.Previous})
	}
	for _, w := range saved.Workspaces {
		root, err := loadGroup(w.Tree)
		if err != nil {
			return State{}, err
		}
		s.workspaces = append(s.workspaces, Workspace{
			ID: w.ID, Output: w.Output, Tree: Tree{root: root}, Floating: w.Floating, Minimized: w.Minimized,
			Fullscreen: w.Fullscreen, floatFocus: w.FloatFocus, home: w.Home, history: w.History,
		})
	}
	for _, l := range saved.Limits {
		s.limits[l.ID] = l.Limits
	}
	return s, nil
}

func (s State) Windows() []WindowID {
	var out []WindowID
	for i := range s.workspaces {
		w := &s.workspaces[i]
		out = append(out, w.Tree.Windows()...)
		for _, f := range w.Floating {
			out = append(out, f.ID)
		}
		for _, m := range w.Minimized {
			out = append(out, m.ID)
		}
	}
	return out
}
