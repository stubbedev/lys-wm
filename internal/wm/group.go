package wm

import (
	"fmt"
	"image"
	"slices"
	"strconv"
)

func (t Tree) focusPath() ([]*Group, []int) {
	id, ok := t.Focused()
	if !ok {
		return nil, nil
	}
	groups, idx, _ := t.path(id)
	n := max(len(groups)-t.lift, 0)
	return groups[:n], idx[:n]
}

func (t Tree) nodeAt(groups []*Group, idx []int) Node {
	if len(groups) == 0 {
		return t.root
	}
	return groups[len(groups)-1].Children[idx[len(idx)-1]].Node
}

func (t Tree) LiftedGroup() (*Group, bool) {
	if t.root == nil || t.lift == 0 {
		return nil, false
	}
	g, ok := t.nodeAt(t.focusPath()).(*Group)
	return g, ok
}

func (t Tree) innermostGroup() *Group {
	groups, idx := t.focusPath()
	if g, ok := t.nodeAt(groups, idx).(*Group); ok {
		return g
	}
	return groups[len(groups)-1]
}

func (t Tree) nearestTabs() *Group {
	id, ok := t.Focused()
	if !ok {
		return nil
	}
	groups, _, _ := t.path(id)
	for _, g := range slices.Backward(groups) {
		if g.Layout == Tabbed {
			return g
		}
	}
	return nil
}

func depth(n Node) int {
	d := 0
	for {
		g, ok := n.(*Group)
		if !ok {
			return d
		}
		d++
		n = g.Children[g.Focus].Node
	}
}

func (t *Tree) focusNode(n Node) {
	switch n := n.(type) {
	case Window:
		groups, idx, _ := t.path(n.ID)
		for i, g := range groups {
			g.Focus = idx[i]
		}
	case *Group:
		groups, idx := t.groupPath(n)
		for i := range len(groups) - 1 {
			groups[i].Focus = idx[i]
		}
	}
	t.lift = depth(n)
}

func appendWindows(out []WindowID, n Node) []WindowID {
	switch n := n.(type) {
	case Window:
		out = append(out, n.ID)
	case *Group:
		for _, c := range n.Children {
			out = appendWindows(out, c.Node)
		}
	}
	return out
}

func (t *Tree) detachFocused() Node {
	groups, idx := t.focusPath()
	node := t.nodeAt(groups, idx)
	t.lift = 0
	if len(groups) == 0 {
		t.root = nil
		return node
	}
	groups[len(groups)-1].remove(idx[len(idx)-1])
	t.collapse(groups, idx, CollapseInvalid)
	return node
}

func (t *Tree) attach(n Node, rootLayout Layout) {
	if g, isGroup := n.(*Group); isGroup && t.root == nil && !t.scroll {
		t.root = g
	} else {
		t.insertNode(n, rootLayout)
	}
	t.focusNode(n)
}

type GroupChange uint8

const (
	ChangeToH GroupChange = iota + 1
	ChangeToV
	ChangeToTab
	ChangeToggleTab
	ChangeOpposite
)

var groupChanges = []string{ChangeToH: "h", ChangeToV: "v", ChangeToTab: "tab", ChangeToggleTab: "toggletab", ChangeOpposite: "opposite"}

func (c GroupChange) String() string { return enumName(groupChanges, c, "GroupChange") }

func (g *Group) setLayout(l Layout) bool {
	if g.Layout == l || g.Layout == Scrolled {
		return false
	}
	if l == Tabbed {
		g.untabbed = g.Layout
	}
	g.Layout = l
	return true
}

func (t Tree) ChangeGroup(c GroupChange) (Tree, bool) {
	if t.root == nil {
		return t, false
	}
	t = t.clone()
	g := t.innermostGroup()
	switch c {
	case ChangeToH:
		return t, g.setLayout(SplitH)
	case ChangeToV:
		return t, g.setLayout(SplitV)
	case ChangeToTab:
		return t, g.setLayout(Tabbed)
	case ChangeToggleTab:
		if g.Layout != Tabbed {
			return t, g.setLayout(Tabbed)
		}
		if g.untabbed == 0 {
			return t, g.setLayout(SplitH)
		}
		return t, g.setLayout(g.untabbed)
	case ChangeOpposite:
		switch g.Layout {
		case SplitH:
			return t, g.setLayout(SplitV)
		case SplitV:
			return t, g.setLayout(SplitH)
		}
	}
	return t, false
}

type TabTarget interface {
	fmt.Stringer
	pick(at, n int, wrap bool) (int, bool)
}

type TabStep int

type TabIndex int

func (s TabStep) String() string {
	if s < 0 {
		return "prev"
	}
	return "next"
}

func (s TabStep) pick(at, n int, wrap bool) (int, bool) {
	next := at + int(s)
	switch {
	case next >= 0 && next < n:
		return next, true
	case !wrap:
		return at, false
	}
	return (next%n + n) % n, true
}

func (i TabIndex) String() string { return strconv.Itoa(int(i)) }

func (i TabIndex) pick(_, n int, _ bool) (int, bool) {
	return int(i) - 1, int(i) <= n
}

func (t Tree) FocusTab(target TabTarget, wrap bool) (Tree, bool) {
	t = t.clone()
	g := t.nearestTabs()
	if g == nil {
		return t, false
	}
	k, ok := target.pick(g.Focus, len(g.Children), wrap)
	if !ok || k == g.Focus {
		return t, false
	}
	g.Focus = k
	t.lift = 0
	return t, true
}

func (t Tree) FocusParent() (Tree, bool) {
	id, ok := t.Focused()
	if !ok {
		return t, false
	}
	groups, _, _ := t.path(id)
	if t.lift >= len(groups) {
		return t, false
	}
	t.lift++
	return t, true
}

func (t Tree) FocusChild() (Tree, bool) {
	if t.lift == 0 {
		return t, false
	}
	t.lift--
	return t, true
}

func enteredSlot(g *Group, j int, d Direction) (*Group, int) {
	for {
		sub, ok := g.Children[j].Node.(*Group)
		if !ok || sub.Locked {
			return g, j
		}
		g = sub
		switch {
		case sub.Layout == Tabbed:
			j = sub.Focus
		case along(sub.Layout, d) && d.forward():
			j = 0
		case along(sub.Layout, d):
			j = len(sub.Children) - 1
		default:
			j = sub.Focus
		}
	}
}

func (t Tree) Swap(d Direction) (Tree, bool) {
	original := t
	t = t.clone()
	groups, idx := t.focusPath()
	if len(groups) == 0 {
		return original, false
	}
	level := breakout(groups, idx, d)
	if level < 0 || leavesLocked(groups, level) {
		return original, false
	}
	g, j := enteredSlot(groups[level], step(idx[level], d), d)
	parent, i := groups[len(groups)-1], idx[len(idx)-1]
	node := parent.Children[i].Node
	parent.Children[i].Node, g.Children[j].Node = g.Children[j].Node, node
	t.focusNode(node)
	return t, true
}

func (t Tree) MoveOut() (Tree, bool) {
	original := t
	t = t.clone()
	groups, idx := t.focusPath()
	leaf := len(groups) - 1
	if leaf < 1 || groups[leaf].Locked {
		return original, false
	}
	parent := groups[leaf]
	node := parent.Children[idx[leaf]].Node
	groups[leaf-1].insert(idx[leaf-1]+1, node)
	parent.remove(idx[leaf])
	t.collapseAt(parent, CollapseInvalid)
	t.focusNode(node)
	return t, true
}

func equalize(g *Group, all bool) bool {
	changed := false
	for i := range g.Children {
		c := &g.Children[i]
		if c.Ratio != 1 && g.Layout != Scrolled {
			c.Ratio, changed = 1, true
		}
		if sub, ok := c.Node.(*Group); ok && all && equalize(sub, true) {
			changed = true
		}
	}
	return changed
}

func (t Tree) Equalize(all bool) (Tree, bool) {
	if t.root == nil {
		return t, false
	}
	t = t.clone()
	if all {
		return t, equalize(t.root, true)
	}
	return t, equalize(t.innermostGroup(), false)
}

func (t Tree) LockTabs(sw Switch) (Tree, bool) {
	t = t.clone()
	g := t.nearestTabs()
	if g == nil {
		return t, false
	}
	locked := sw.From(g.Locked)
	if locked == g.Locked {
		return t, false
	}
	g.Locked = locked
	return t, true
}

func (t Tree) WrapFocused(layout Layout, ephemeral bool) (Tree, bool) {
	if t.root == nil {
		return t, false
	}
	t = t.clone()
	groups, idx := t.focusPath()
	if len(groups) == 0 && t.scroll {
		return t, false
	}
	t.wrapAt(groups, idx, layout, ephemeral)
	return t, true
}

func (w *Workspace) treeFocused() bool {
	id, ok := w.focused()
	return ok && w.floatingIndex(id) < 0
}

func (s State) lifted() (*Workspace, *Group) {
	w := s.activeWorkspace()
	if w == nil || !w.treeFocused() {
		return nil, nil
	}
	g, ok := w.Tree.LiftedGroup()
	if !ok {
		return nil, nil
	}
	return w, g
}

func (w *Workspace) detachFocused() (Node, error) {
	tree := w.Tree.clone()
	node := tree.detachFocused()
	w.Tree = tree
	gone := appendWindows(nil, node)
	if slices.Contains(gone, w.Cover.ID) {
		w.Cover = Cover{}
	}
	w.history = slices.DeleteFunc(w.history, func(x WindowID) bool { return slices.Contains(gone, x) })
	w.refocusTop()
	if n := len(w.history); n > 0 {
		return node, w.focus(w.history[n-1], Raise)
	}
	return node, nil
}

func (s State) withFocusedTree(op func(Tree) (Tree, bool)) State {
	w := s.activeWorkspace()
	if w == nil || !w.treeFocused() {
		return s
	}
	tree, changed := op(w.Tree)
	if !changed {
		return s
	}
	s = s.clone()
	active := s.activeWorkspace()
	active.Tree = tree
	active.record()
	return s
}

func (s State) moveFocusedNode(id WorkspaceID) (State, error) {
	if id < 1 {
		return State{}, fmt.Errorf("move the focused group to workspace %d: %w", id, ErrInvalidWorkspace)
	}
	s = s.clone()
	from := s.activeWorkspace()
	if from.ID == id {
		return s, nil
	}
	fromID := from.ID
	node, err := from.detachFocused()
	if err != nil {
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
	to.Tree = to.Tree.clone()
	to.Tree.attach(node, rootLayout(area))
	to.floatFocus = 0
	to.record()
	s.vacated(fromID)
	return s, nil
}

func (s State) moveFocusedToOutputEdge(output string, d Direction) (State, error) {
	s = s.clone()
	from := s.activeWorkspace()
	fromID := from.ID
	node, err := from.detachFocused()
	if err != nil {
		return State{}, err
	}
	o := s.output(output)
	to := s.workspace(o.Active)
	to.Tree = to.Tree.clone()
	to.Tree.insertEdge(node, d, o.Area)
	to.floatFocus = 0
	to.record()
	s.focused = output
	s.vacated(fromID)
	return s, nil
}

func (s State) floatLifted(w *Workspace, g *Group) (State, error) {
	area := s.output(w.Output).Area
	for i, id := range appendWindows(nil, g) {
		rect := Centered(area, s.limits[id].floored().Fit(area.Size().Div(2))).Add(image.Pt(i*cascade, i*cascade))
		next, err := s.Float(id, rect)
		if err != nil {
			return State{}, err
		}
		s = next
	}
	return s, nil
}

const cascade = 32

type (
	ChangeGroupAction struct{ Change GroupChange }
	FocusTabAction    struct {
		Target TabTarget
		Wrap   bool
	}
	FocusParentAction struct{}
	FocusChildAction  struct{}
	SwapAction        struct{ Dir Direction }
	MoveOutAction     struct{}
	EqualizeAction    struct{ All bool }
	LockTabAction     struct{ Switch Switch }
)

func (a ChangeGroupAction) String() string { return "changegroup " + a.Change.String() }

func (a FocusTabAction) String() string {
	if a.Wrap {
		return "focustab " + a.Target.String() + " wrap"
	}
	return "focustab " + a.Target.String()
}

func (FocusParentAction) String() string { return "focusparent" }
func (FocusChildAction) String() string  { return "focuschild" }
func (a SwapAction) String() string      { return "swap " + a.Dir.String() }
func (MoveOutAction) String() string     { return "moveout" }

func (a EqualizeAction) String() string {
	if a.All {
		return "equalize all"
	}
	return "equalize"
}

func (a LockTabAction) String() string { return switched("locktab", a.Switch) }

func (a ChangeGroupAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(func(t Tree) (Tree, bool) { return t.ChangeGroup(a.Change) }), nil, nil
}

func (a FocusTabAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(func(t Tree) (Tree, bool) { return t.FocusTab(a.Target, a.Wrap) }), nil, nil
}

func (FocusParentAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(Tree.FocusParent), nil, nil
}

func (FocusChildAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(Tree.FocusChild), nil, nil
}

func (a SwapAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(func(t Tree) (Tree, bool) { return t.Swap(a.Dir) }), nil, nil
}

func (MoveOutAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(Tree.MoveOut), nil, nil
}

func (a EqualizeAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(func(t Tree) (Tree, bool) { return t.Equalize(a.All) }), nil, nil
}

func (a LockTabAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(func(t Tree) (Tree, bool) { return t.LockTabs(a.Switch) }), nil, nil
}

func parseChangeGroup(spec ActionSpec, args []string, _ string) (Action, error) {
	if err := exactly(spec, args, 1); err != nil {
		return nil, err
	}
	c, ok := parseEnum[GroupChange](groupChanges, args[0])
	if !ok {
		return nil, badArgs(spec, "unknown layout change %q", args[0])
	}
	return ChangeGroupAction{Change: c}, nil
}

func parseFocusTab(spec ActionSpec, args []string, _ string) (Action, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, badArgs(spec, "takes a tab and an optional wrap, not %d arguments", len(args))
	}
	var target TabTarget
	switch args[0] {
	case "prev":
		target = TabStep(-1)
	case "next":
		target = TabStep(1)
	default:
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return nil, badArgs(spec, "%q is not prev, next or a tab number from 1", args[0])
		}
		target = TabIndex(n)
	}
	if len(args) == 1 {
		return FocusTabAction{Target: target}, nil
	}
	if _, step := target.(TabStep); !step || args[1] != "wrap" {
		return nil, badArgs(spec, "%q after %q is not wrap after prev or next", args[1], args[0])
	}
	return FocusTabAction{Target: target, Wrap: true}, nil
}

func parseEqualize(spec ActionSpec, args []string, _ string) (Action, error) {
	switch {
	case len(args) == 0:
		return EqualizeAction{}, nil
	case len(args) == 1 && args[0] == "all":
		return EqualizeAction{All: true}, nil
	}
	return nil, badArgs(spec, "takes all or nothing")
}

func parseLockTab(spec ActionSpec, args []string, _ string) (Action, error) {
	sw, err := optionalSwitch(spec, args)
	return LockTabAction{Switch: sw}, err
}
