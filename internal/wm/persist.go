package wm

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"
)

type savedNode struct {
	Window WindowID    `json:"window,omitempty"`
	Group  *savedGroup `json:"group,omitempty"`
	Ratio  float64     `json:"ratio"`
}

type savedGroup struct {
	ID        GroupID     `json:"id,omitempty"`
	Layout    Layout      `json:"layout"`
	Children  []savedNode `json:"children"`
	Focus     int         `json:"focus"`
	Ephemeral uint8       `json:"ephemeral"`
	Locked    bool        `json:"locked,omitempty"`
	Untabbed  Layout      `json:"untabbed,omitempty"`
}

type savedWorkspace struct {
	ID         WorkspaceID `json:"id"`
	Output     string      `json:"output"`
	Tree       *savedGroup `json:"tree,omitempty"`
	Lift       int         `json:"lift,omitempty"`
	Floating   []Floating  `json:"floating,omitempty"`
	Minimized  []Minimized `json:"minimized,omitempty"`
	Fullscreen WindowID    `json:"fullscreen,omitempty"`
	Maximized  WindowID    `json:"maximized,omitempty"`
	FloatFocus bool        `json:"float_focus,omitempty"`
	Home       string      `json:"home,omitempty"`
	History    []WindowID  `json:"history,omitempty"`
	Scratch    *Scratchpad `json:"scratchpad,omitempty"`
}

type savedOutput struct {
	Name     string          `json:"name"`
	Full     image.Rectangle `json:"full"`
	Area     image.Rectangle `json:"area"`
	Active   WorkspaceID     `json:"active"`
	Previous WorkspaceID     `json:"previous"`
	Scratch  WorkspaceID     `json:"scratch,omitempty"`
}

type savedLimit struct {
	ID     WindowID `json:"id"`
	Limits Limits   `json:"limits"`
}

type savedState struct {
	Outputs    []savedOutput          `json:"outputs"`
	Workspaces []savedWorkspace       `json:"workspaces"`
	Scratch    []savedWorkspace       `json:"scratchpads,omitempty"`
	Focused    string                 `json:"focused"`
	Remembered map[string]WorkspaceID `json:"remembered,omitempty"`
	Limits     []savedLimit           `json:"limits,omitempty"`
	Fake       []WindowID             `json:"fake_fullscreen,omitempty"`
	Pinned     []WindowID             `json:"pinned,omitempty"`
}

func saveGroup(g *Group) *savedGroup {
	if g == nil {
		return nil
	}
	out := &savedGroup{ID: g.id, Layout: g.Layout, Focus: g.Focus, Ephemeral: uint8(g.ephemeral), Locked: g.Locked, Untabbed: g.untabbed, Children: make([]savedNode, len(g.Children))}
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

func loadTree(g *savedGroup, lift int) (Tree, error) {
	t := Tree{lift: lift}
	root, err := t.loadGroup(g)
	if err != nil {
		return Tree{}, err
	}
	t.root = root
	t.mintMissing(root)
	return t, nil
}

func (t *Tree) mintMissing(g *Group) {
	if g == nil {
		return
	}
	if g.id == 0 {
		t.minted++
		g.id = t.minted
	}
	for _, ch := range g.Children {
		if sub, ok := ch.Node.(*Group); ok {
			t.mintMissing(sub)
		}
	}
}

func (t *Tree) loadGroup(g *savedGroup) (*Group, error) {
	if g == nil {
		return nil, nil
	}
	t.minted = max(t.minted, g.ID)
	out := &Group{id: g.ID, Layout: g.Layout, Focus: g.Focus, ephemeral: ephemerality(g.Ephemeral), Locked: g.Locked, untabbed: g.Untabbed, Children: make([]Child, len(g.Children))}
	for i, ch := range g.Children {
		out.Children[i].Ratio = ch.Ratio
		switch {
		case ch.Group != nil:
			sub, err := t.loadGroup(ch.Group)
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
		saved.Outputs = append(saved.Outputs, savedOutput{Name: o.Name, Full: o.Full, Area: o.Area, Active: o.Active, Previous: o.previous, Scratch: o.Scratch})
	}
	for _, w := range s.workspaces {
		saved.Workspaces = append(saved.Workspaces, saveWorkspace(w))
	}
	for _, w := range s.scratch {
		saved.Scratch = append(saved.Scratch, saveWorkspace(w))
	}
	for id := range s.fake {
		saved.Fake = append(saved.Fake, id)
	}
	slices.Sort(saved.Fake)
	saved.Pinned = slices.Sorted(maps.Keys(s.pinned))
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
		s.outputs = append(s.outputs, Output{Name: o.Name, Full: o.Full, Area: o.Area, Active: o.Active, previous: o.Previous, Scratch: o.Scratch})
	}
	var err error
	if s.workspaces, err = loadWorkspaces(saved.Workspaces); err != nil {
		return State{}, err
	}
	if s.scratch, err = loadWorkspaces(saved.Scratch); err != nil {
		return State{}, err
	}
	for _, l := range saved.Limits {
		s.limits[l.ID] = l.Limits
	}
	for _, id := range saved.Pinned {
		if s.pinned == nil {
			s.pinned = map[WindowID]struct{}{}
		}
		s.pinned[id] = struct{}{}
	}
	for _, id := range saved.Fake {
		if s.fake == nil {
			s.fake = map[WindowID]struct{}{}
		}
		s.fake[id] = struct{}{}
	}
	return s, nil
}

func (s State) Windows() []WindowID {
	var out []WindowID
	for _, w := range slices.Concat(s.workspaces, s.scratch) {
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

func savedCover(w savedWorkspace) Cover {
	switch {
	case w.Fullscreen != 0:
		return Cover{ID: w.Fullscreen, Mode: ModeFullscreen}
	case w.Maximized != 0:
		return Cover{ID: w.Maximized, Mode: ModeMaximized}
	}
	return Cover{}
}

func saveWorkspace(w Workspace) savedWorkspace {
	sw := savedWorkspace{
		ID: w.ID, Output: w.Output, Tree: saveGroup(w.Tree.root), Lift: w.Tree.lift, Floating: w.Floating, Minimized: w.Minimized,
		FloatFocus: w.floatFocus, Home: w.home, History: w.history,
	}
	if w.Scratch.Name != "" {
		sp := w.Scratch
		sw.Scratch = &sp
	}
	switch w.Cover.Mode {
	case ModeFullscreen:
		sw.Fullscreen = w.Cover.ID
	case ModeMaximized:
		sw.Maximized = w.Cover.ID
	}
	return sw
}

func loadWorkspaces(saved []savedWorkspace) ([]Workspace, error) {
	out := make([]Workspace, 0, len(saved))
	for _, w := range saved {
		tree, err := loadTree(w.Tree, w.Lift)
		if err != nil {
			return nil, err
		}
		ws := Workspace{
			ID: w.ID, Output: w.Output, Tree: tree, Floating: w.Floating, Minimized: w.Minimized,
			Cover: savedCover(w), floatFocus: w.FloatFocus, home: w.Home, history: w.History,
		}
		if w.Scratch != nil {
			ws.Scratch = *w.Scratch
		}
		out = append(out, ws)
	}
	return out, nil
}
