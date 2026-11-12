package wm

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"
)

type WorkspaceID int

type Output struct {
	Name     string
	Full     image.Rectangle
	Area     image.Rectangle
	Active   WorkspaceID
	Scratch  WorkspaceID
	previous WorkspaceID
}

type Floating struct {
	ID   WindowID
	Rect image.Rectangle
}

type Workspace struct {
	ID         WorkspaceID
	Output     string
	Tree       Tree
	Floating   []Floating
	Minimized  []Minimized
	Cover      Cover
	Scratch    Scratchpad
	floatFocus WindowID
	home       string
	history    []WindowID
}

func (w *Workspace) floatingIndex(id WindowID) int {
	return slices.IndexFunc(w.Floating, func(f Floating) bool { return f.ID == id })
}

func (w *Workspace) minimizedIndex(id WindowID) int {
	return slices.IndexFunc(w.Minimized, func(m Minimized) bool { return m.ID == id })
}

func (w *Workspace) contains(id WindowID) bool {
	return w.floatingIndex(id) >= 0 || w.minimizedIndex(id) >= 0 || w.Tree.Contains(id)
}

func (w *Workspace) empty() bool {
	return w.Tree.Empty() && len(w.Floating) == 0 && len(w.Minimized) == 0
}

func (w *Workspace) focused() (WindowID, bool) {
	if w.floatFocus != 0 {
		return w.floatFocus, true
	}
	return w.Tree.Focused()
}

func (w *Workspace) focus(id WindowID, st Stacking) error {
	if i := w.floatingIndex(id); i >= 0 {
		if st == Raise {
			f := w.Floating[i]
			w.Floating = append(slices.Delete(w.Floating, i, i+1), f)
		}
		w.floatFocus = id
		w.record()
		return nil
	}
	tree, err := w.Tree.Focus(id)
	if err != nil {
		return err
	}
	w.Tree, w.floatFocus = tree, 0
	w.record()
	return nil
}

func (w *Workspace) raised(id WindowID) bool {
	i := w.floatingIndex(id)
	return i < 0 || i == len(w.Floating)-1
}

func (w *Workspace) refocusTop() {
	w.floatFocus = 0
	if w.Tree.Empty() && len(w.Floating) > 0 {
		w.floatFocus = w.Floating[len(w.Floating)-1].ID
	}
}

func (w *Workspace) remove(id WindowID) error {
	if i := w.minimizedIndex(id); i >= 0 {
		w.Minimized = slices.Delete(w.Minimized, i, i+1)
		return nil
	}
	focused, _ := w.focused()
	if i := w.floatingIndex(id); i >= 0 {
		w.Floating = slices.Delete(w.Floating, i, i+1)
	} else {
		tree, err := w.Tree.Remove(id)
		if err != nil {
			return err
		}
		w.Tree = tree
	}
	if w.Cover.ID == id {
		w.Cover = Cover{}
	}
	w.history = slices.DeleteFunc(w.history, func(x WindowID) bool { return x == id })
	if focused != id {
		return nil
	}
	w.refocusTop()
	if n := len(w.history); n > 0 {
		return w.focus(w.history[n-1], Raise)
	}
	return nil
}

func (w *Workspace) record() {
	id, ok := w.focused()
	if !ok {
		return
	}
	if n := len(w.history); n > 0 && w.history[n-1] == id {
		return
	}
	w.history = append(slices.DeleteFunc(w.history, func(x WindowID) bool { return x == id }), id)
}

type State struct {
	outputs       []Output
	workspaces    []Workspace
	scratch       []Workspace
	focused       string
	remembered    map[string]WorkspaceID
	limits        map[WindowID]Limits
	restore       map[WindowID]restorePoint
	fake          map[WindowID]struct{}
	pinned        map[WindowID]struct{}
	swallows      []Swallow
	layoutDefault LayoutKind
	wsRules       []WorkspaceRule
}

var (
	ErrUnknownOutput    = errors.New("wm: unknown output")
	ErrInvalidWorkspace = errors.New("wm: invalid workspace")
)

func (s State) clone() State {
	c := State{
		outputs:       slices.Clone(s.outputs),
		workspaces:    make([]Workspace, len(s.workspaces)),
		focused:       s.focused,
		remembered:    make(map[string]WorkspaceID, len(s.remembered)),
		limits:        maps.Clone(s.limits),
		restore:       maps.Clone(s.restore),
		fake:          maps.Clone(s.fake),
		pinned:        maps.Clone(s.pinned),
		swallows:      slices.Clone(s.swallows),
		wsRules:       s.wsRules,
		layoutDefault: s.layoutDefault,
	}
	maps.Copy(c.remembered, s.remembered)
	for i, w := range s.workspaces {
		c.workspaces[i] = w.deepCopy()
	}
	if len(s.scratch) > 0 {
		c.scratch = make([]Workspace, len(s.scratch))
		for i, w := range s.scratch {
			c.scratch[i] = w.deepCopy()
		}
	}
	return c
}

func (w Workspace) deepCopy() Workspace {
	w.history = slices.Clone(w.history)
	w.Floating = slices.Clone(w.Floating)
	w.Minimized = slices.Clone(w.Minimized)
	return w
}

func (s State) sharingWorkspaces() State {
	s.workspaces = slices.Clone(s.workspaces)
	s.scratch = slices.Clone(s.scratch)
	return s
}

func (s State) Outputs() []Output {
	return slices.Clone(s.outputs)
}

func (s State) Workspaces() []Workspace {
	return slices.Clone(s.workspaces)
}

func (s State) FocusedOutput() string {
	return s.focused
}

func (s State) ActiveOn(output string) (WorkspaceID, bool) {
	o := s.output(output)
	if o == nil {
		return 0, false
	}
	return o.Active, true
}

func (s State) Shown(w Workspace) bool {
	o := s.output(w.Output)
	if o == nil {
		return false
	}
	if w.Scratch.Name != "" {
		return o.Scratch == w.ID
	}
	return o.Active == w.ID
}

func (s *State) output(name string) *Output {
	for i := range s.outputs {
		if s.outputs[i].Name == name {
			return &s.outputs[i]
		}
	}
	return nil
}

func (s *State) workspace(id WorkspaceID) *Workspace {
	all := s.workspaces
	if id < 0 {
		all = s.scratch
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i]
		}
	}
	return nil
}

func (s State) Workspace(id WorkspaceID) (Workspace, bool) {
	w := s.workspace(id)
	if w == nil {
		return Workspace{}, false
	}
	return *w, true
}

func (s *State) windowWorkspace(id WindowID) *Workspace {
	if by, hidden := s.hiddenBy(id); hidden {
		return s.windowWorkspace(by)
	}
	return s.workspaceHolding(id)
}

func (s *State) create(id WorkspaceID, output string) *Workspace {
	if bound := s.boundOutput(id); bound != "" {
		output = bound
	}
	s.workspaces = append(s.workspaces, Workspace{ID: id, Output: output, Tree: Tree{scroll: s.kindFor(id) == ScrollLayout}})
	slices.SortFunc(s.workspaces, func(a, b Workspace) int { return int(a.ID) - int(b.ID) })
	return s.workspace(id)
}

func (s *State) firstUnused() WorkspaceID {
	id := WorkspaceID(1)
	for s.workspace(id) != nil {
		id++
	}
	return id
}

func (s *State) dropIfIdle(id WorkspaceID) {
	w := s.workspace(id)
	if w == nil || !w.empty() || s.Shown(*w) || s.persistent(id) {
		return
	}
	if id < 0 {
		s.scratch = slices.DeleteFunc(s.scratch, func(x Workspace) bool { return x.ID == id })
		return
	}
	s.workspaces = slices.DeleteFunc(s.workspaces, func(x Workspace) bool { return x.ID == id })
}

func (s *State) activate(id WorkspaceID) {
	w := s.workspace(id)
	o := s.output(w.Output)
	if o == nil {
		return
	}
	s.focused = o.Name
	if w.Scratch.Name != "" {
		s.showScratch(o, id)
		return
	}
	prev := o.Active
	o.Active = id
	if sp := s.workspace(o.Scratch); sp != nil && (prev == id || !sp.Scratch.Stays) {
		s.hideScratch(o)
	}
	if prev != id {
		o.previous = prev
		s.gatherPinned()
		s.dropIfIdle(prev)
	}
}

func (s State) AddOutput(name string, full, area image.Rectangle) (State, error) {
	if s.output(name) != nil {
		return State{}, fmt.Errorf("wm: output %s added twice", name)
	}
	s = s.clone()
	var followed WorkspaceID
	if f := s.output(s.focused); f != nil {
		followed = f.Active
	}
	s.outputs = append(s.outputs, Output{Name: name, Full: full, Area: area})
	for i := range s.workspaces {
		if w := &s.workspaces[i]; w.home == name || w.Output == "" {
			s.rehome(w, name)
		}
	}
	s.bindTo(name)
	for i := range s.scratch {
		if s.scratch[i].Output == "" {
			s.rehome(&s.scratch[i], name)
		}
	}
	s.fixActive(name, followed, s.remembered[name], s.remembered[""])
	o := s.output(name)
	s.showDefault(o)
	delete(s.remembered, name)
	delete(s.remembered, "")
	for _, other := range s.outputs {
		if other.Name != name {
			if w := s.workspace(other.Active); w == nil || w.Output != other.Name {
				s.fixActive(other.Name)
			}
		}
	}
	if s.focused == "" || o.Active == followed {
		s.focused = name
	}
	s.gatherPinned()
	return s, nil
}

func (s *State) firstOn(output string) *Workspace {
	for i := range s.workspaces {
		if s.workspaces[i].Output == output {
			return &s.workspaces[i]
		}
	}
	return nil
}

func (s *State) fixActive(output string, prefer ...WorkspaceID) {
	o := s.output(output)
	for _, id := range append(prefer, o.previous) {
		if w := s.workspace(id); w != nil && w.Output == output {
			o.Active = id
			return
		}
	}
	if w := s.firstOn(output); w != nil {
		o.Active = w.ID
		return
	}
	o.Active = s.create(s.firstUnused(), output).ID
}

func (s State) RemoveOutput(name string) (State, error) {
	if s.output(name) == nil {
		return State{}, fmt.Errorf("remove output %s: %w", name, ErrUnknownOutput)
	}
	s = s.clone()
	gone := *s.output(name)
	wasFocused := s.focused == name
	if wasFocused {
		s.focused = ""
		if i := slices.IndexFunc(s.outputs, func(o Output) bool { return o.Name != name }); i >= 0 {
			s.focused = s.outputs[i].Name
		}
	}
	for i := range s.workspaces {
		if w := &s.workspaces[i]; w.Output == name {
			home := cmp.Or(w.home, name)
			s.rehome(w, s.focused)
			w.home = home
		}
	}
	for i := range s.scratch {
		if s.scratch[i].Output == name {
			s.rehome(&s.scratch[i], s.focused)
		}
	}
	s.outputs = slices.DeleteFunc(s.outputs, func(o Output) bool { return o.Name == name })
	s.remembered[name] = gone.Active
	if s.focused == "" {
		s.remembered[""] = gone.Active
	} else {
		s.inherit(gone, wasFocused)
	}
	s.scratch = slices.DeleteFunc(s.scratch, func(w Workspace) bool { return w.empty() && !s.Shown(w) })
	s.gatherPinned()
	if s.focused != "" {
		s.dropIdleOn(s.focused)
	}
	return s, nil
}

func (s *State) inherit(gone Output, wasFocused bool) {
	o := s.output(s.focused)
	if !s.showsWindows(gone) || (!wasFocused && s.showsWindows(*o)) {
		return
	}
	if w := s.workspace(gone.Active); w != nil && !w.empty() {
		s.activate(gone.Active)
	}
	if w := s.workspace(gone.Scratch); w != nil && !w.empty() && o.Scratch == 0 {
		s.showScratch(o, gone.Scratch)
	}
}

func (s *State) showsWindows(o Output) bool {
	return slices.ContainsFunc([]WorkspaceID{o.Active, o.Scratch}, func(id WorkspaceID) bool {
		w := s.workspace(id)
		return w != nil && !w.empty()
	})
}

func (s *State) dropIdleOn(output string) {
	for _, w := range slices.Clone(s.workspaces) {
		if w.Output == output {
			s.dropIfIdle(w.ID)
		}
	}
}

func (s State) SetGeometry(name string, full, area image.Rectangle) (State, error) {
	if s.output(name) == nil {
		return State{}, fmt.Errorf("set the geometry of output %s: %w", name, ErrUnknownOutput)
	}
	s = s.clone()
	o := s.output(name)
	o.Full, o.Area = full, area
	return s, nil
}

func (s State) FocusOutput(name string) (State, error) {
	if s.output(name) == nil {
		return State{}, fmt.Errorf("focus output %s: %w", name, ErrUnknownOutput)
	}
	s = s.clone()
	s.focused = name
	return s, nil
}

func (s State) activeWorkspace() *Workspace {
	o := s.output(s.focused)
	if o == nil {
		return nil
	}
	if w := s.workspace(o.Scratch); w != nil && !w.empty() {
		return w
	}
	return s.workspace(o.Active)
}

func (s State) Focused() (WindowID, bool) {
	w := s.activeWorkspace()
	if w == nil {
		return 0, false
	}
	return w.focused()
}

func (s State) Settled(id WindowID, st Stacking) bool {
	w := s.activeWorkspace()
	if w == nil {
		return false
	}
	focused, ok := w.focused()
	return ok && focused == id && (st == KeepStacking || w.raised(id))
}

var ErrReservedWindow = errors.New("wm: window id 0 is reserved")

func (s State) MapWindow(id WindowID, at Autotile) (State, error) {
	return s.Open(id, Opening{Autotile: at})
}

func (s State) UnmapWindow(id WindowID) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("unmap window %d: %w", id, ErrUnknownWindow)
	}
	if err := s.unmapFrom(w, id); err != nil {
		return State{}, err
	}
	s.dropSwallows(id)
	delete(s.limits, id)
	delete(s.restore, id)
	delete(s.fake, id)
	delete(s.pinned, id)
	s.vacated(w.ID)
	return s, nil
}

func (s State) FocusWindow(id WindowID, st Stacking) (State, error) {
	if by, hidden := s.hiddenBy(id); hidden {
		id = by
	}
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("focus window %d: %w", id, ErrUnknownWindow)
	}
	if err := w.focus(id, st); err != nil {
		return State{}, err
	}
	s.activate(w.ID)
	return s, nil
}

func (s State) WorkspaceOf(id WindowID) (WorkspaceID, bool) {
	w := s.windowWorkspace(id)
	if w == nil {
		return 0, false
	}
	return w.ID, true
}

func (s State) SwitchWorkspace(id WorkspaceID) (State, error) {
	if id < 1 {
		return State{}, fmt.Errorf("switch to workspace %d: %w", id, ErrInvalidWorkspace)
	}
	if s.output(s.focused) == nil {
		return State{}, fmt.Errorf("switch to workspace %d: %w", id, ErrUnknownOutput)
	}
	s = s.clone()
	if s.workspace(id) == nil {
		s.create(id, s.focused)
	}
	s.activate(id)
	return s, nil
}

func (s State) relative(offset int, onOutput bool) (WorkspaceID, error) {
	o := s.output(s.focused)
	if o == nil {
		return 0, fmt.Errorf("step %+d workspaces: %w", offset, ErrUnknownOutput)
	}
	ids := make([]WorkspaceID, 0, len(s.workspaces))
	for _, w := range s.workspaces {
		if !onOutput || w.Output == o.Name {
			ids = append(ids, w.ID)
		}
	}
	at, n := slices.Index(ids, o.Active), len(ids)
	return ids[((at+offset)%n+n)%n], nil
}

func (s State) previous() (WorkspaceID, error) {
	o := s.output(s.focused)
	if o == nil {
		return 0, fmt.Errorf("go back to the previous workspace: %w", ErrUnknownOutput)
	}
	if o.previous == 0 {
		return o.Active, nil
	}
	return o.previous, nil
}

func (s State) AssignWorkspace(id WorkspaceID, output string) (State, error) {
	switch {
	case s.output(output) == nil:
		return State{}, fmt.Errorf("assign workspace %d to %s: %w", id, output, ErrUnknownOutput)
	case s.workspace(id) == nil:
		return State{}, fmt.Errorf("assign workspace %d to %s: %w", id, output, ErrInvalidWorkspace)
	case s.workspace(id).Output == output:
		return s, nil
	}
	s = s.clone()
	w := s.workspace(id)
	from := w.Output
	s.rehome(w, output)
	s.replaceActive(from, id)
	s.dropIfIdle(id)
	return s, nil
}

func (s State) RelativeWorkspace(offset int) (State, error) {
	id, err := s.relative(offset, false)
	if err != nil {
		return State{}, err
	}
	return s.SwitchWorkspace(id)
}

func (s State) MoveToWorkspace(id WorkspaceID, at Autotile) (State, error) {
	if id < 1 {
		return State{}, fmt.Errorf("move to workspace %d: %w", id, ErrInvalidWorkspace)
	}
	win, ok := s.Focused()
	if !ok {
		return s, nil
	}
	return s.MoveWindowToWorkspace(win, id, at)
}

func (s State) MoveWindowToWorkspace(win WindowID, id WorkspaceID, at Autotile) (State, error) {
	w := s.windowWorkspace(win)
	if w == nil {
		return State{}, fmt.Errorf("move window %d to workspace %d: %w", win, id, ErrUnknownWindow)
	}
	from := w.ID
	next, err := s.moveWindow(win, id, at)
	if err != nil || from == id {
		return next, err
	}
	next.vacated(from)
	if next.Pinned(win) {
		delete(next.pinned, win)
	}
	return next, nil
}

func (s State) moveWindow(win WindowID, id WorkspaceID, at Autotile) (State, error) {
	if id == 0 || id < 0 && s.workspace(id) == nil {
		return State{}, fmt.Errorf("move window %d to workspace %d: %w", win, id, ErrInvalidWorkspace)
	}
	if s.windowWorkspace(win) == nil {
		return State{}, fmt.Errorf("move window %d to workspace %d: %w", win, id, ErrUnknownWindow)
	}
	s = s.clone()
	from := s.windowWorkspace(win)
	if from.ID == id {
		return s, nil
	}
	fromOutput := from.Output
	floating := -1
	var rect image.Rectangle
	if i := from.floatingIndex(win); i >= 0 {
		floating, rect = i, from.Floating[i].Rect
	}
	if err := from.remove(win); err != nil {
		return State{}, err
	}
	to := s.workspace(id)
	if to == nil {
		to = s.create(id, s.focused)
	}
	var area image.Rectangle
	if o := s.output(to.Output); o != nil {
		area = o.Area
	}
	if floating >= 0 {
		if src := s.output(fromOutput); src != nil && fromOutput != to.Output {
			rect = rehomed(rect, src.Area, area)
		}
		to.Floating = append(to.Floating, Floating{ID: win, Rect: rect})
		to.floatFocus = win
		to.record()
		return s, nil
	}
	tree, err := to.Tree.Place(win, area, at)
	if err != nil {
		return State{}, err
	}
	to.Tree, to.floatFocus = tree, 0
	to.record()
	return s, nil
}

type PlacementLayer uint8

const (
	LayerTiled PlacementLayer = iota
	LayerFloating
	LayerFullscreen
	LayerMaximized
)

func (s State) Arrange(sp Spacing) Arrangement {
	var x Arranger
	s.ArrangeInto(&x, sp)
	return x.Arrangement
}

func (s State) ArrangeInto(all *Arranger, sp Spacing) {
	all.reset()
	_, all.lifted = s.lifted()
	for i := range s.workspaces {
		w := &s.workspaces[i]
		from := len(all.Windows)
		o := s.output(w.Output)
		if o == nil {
			all.Windows = appendTreeWindows(all.Windows, w.Tree.root)
			for _, f := range w.Floating {
				all.Windows = append(all.Windows, Placement{ID: f.ID, Rect: f.Rect, Layer: LayerFloating})
			}
			all.Windows = appendMinimized(all.Windows, w.Minimized)
			all.inWorkspace(from, w.ID)
			continue
		}
		look, wsp := s.spacingFor(w, sp)
		placeWorkspace(all, w, o, o.Active == w.ID, wsp)
		all.inWorkspace(from, w.ID)
		if look != (WorkspaceLook{}) {
			for j := from; j < len(all.Windows); j++ {
				all.Windows[j].Look = look
			}
		}
	}
	for i := range s.scratch {
		s.placeScratchpad(all, &s.scratch[i], sp)
	}
	s.placeSwallowed(all)
	if len(s.fake) == 0 && len(s.pinned) == 0 {
		return
	}
	for i := range all.Windows {
		id := all.Windows[i].ID
		_, all.Windows[i].Fake = s.fake[id]
		_, all.Windows[i].Pinned = s.pinned[id]
	}
}

func appendTreeWindows(out []Placement, n Node) []Placement {
	switch n := n.(type) {
	case Window:
		out = append(out, Placement{ID: n.ID})
	case *Group:
		if n == nil {
			return out
		}
		for _, c := range n.Children {
			out = appendTreeWindows(out, c.Node)
		}
	}
	return out
}

func (c Cover) rect(o *Output, sp Spacing) image.Rectangle {
	if c.Mode == ModeFullscreen {
		return o.Full
	}
	return sp.Gaps.Outer.shrink(o.Area)
}

func placeWorkspace(all *Arranger, w *Workspace, o *Output, shown bool, sp Spacing) {
	covered := w.Cover.ID != 0
	from, bars, rings := len(all.Windows), len(all.TabBars), len(all.Rings)
	w.Tree.arrangeInto(all, o.Area, sp)
	kept := all.Windows[:from]
	for _, p := range all.Windows[from:] {
		if p.ID == w.Cover.ID {
			continue
		}
		p.Laid = p.Visible && !covered
		p.Visible = p.Laid && shown
		p.Offscreen = p.Offscreen && !covered && shown
		if w.Tree.scroll {
			p.Clip = o.Full
		}
		kept = append(kept, p)
	}
	all.Windows = kept
	if covered {
		all.TabBars, all.Rings = all.TabBars[:bars], all.Rings[:rings]
	}
	for i := bars; i < len(all.TabBars); i++ {
		all.TabBars[i].Workspace, all.TabBars[i].Shown = w.ID, shown
		if w.Tree.scroll {
			all.TabBars[i].Clip = o.Full
		}
	}
	for i := rings; i < len(all.Rings); i++ {
		all.Rings[i].Workspace, all.Rings[i].Shown = w.ID, shown
	}
	for _, f := range w.Floating {
		if f.ID != w.Cover.ID {
			all.Windows = append(all.Windows, Placement{ID: f.ID, Rect: f.Rect, Visible: shown && !covered, Laid: !covered, Layer: LayerFloating})
		}
	}
	if covered {
		all.Windows = append(all.Windows, Placement{ID: w.Cover.ID, Rect: w.Cover.rect(o, sp), Visible: shown, Laid: true, Layer: w.Cover.Mode.layer()})
	}
	all.Windows = appendMinimized(all.Windows, w.Minimized)
}

func (s State) LookOf(id WindowID) WorkspaceLook {
	if len(s.wsRules) == 0 {
		return WorkspaceLook{}
	}
	for i := range s.workspaces {
		w := &s.workspaces[i]
		if !w.contains(id) {
			continue
		}
		if s.output(w.Output) == nil {
			return WorkspaceLook{}
		}
		return s.lookFor(w)
	}
	return WorkspaceLook{}
}

func (s *State) spacingFor(w *Workspace, sp Spacing) (WorkspaceLook, Spacing) {
	if len(s.wsRules) == 0 {
		return WorkspaceLook{}, sp
	}
	look := s.lookFor(w)
	if look.Gaps != nil {
		sp.Gaps = *look.Gaps
	}
	return look, sp
}
