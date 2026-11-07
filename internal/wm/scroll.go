package wm

import (
	"cmp"
	"fmt"
	"image"
	"math"
	"slices"
	"strconv"
	"strings"
)

type LayoutKind uint8

const (
	TreeLayout LayoutKind = iota + 1
	ScrollLayout
)

var layoutKinds = []string{TreeLayout: "tree", ScrollLayout: "scroll"}

func (k LayoutKind) String() string { return enumName(layoutKinds, k, "LayoutKind") }

func ParseLayoutKind(s string) (LayoutKind, bool) {
	return parseEnum[LayoutKind](layoutKinds, s)
}

type CenterMode uint8

const (
	CenterNever CenterMode = iota + 1
	CenterAlways
	CenterOnOverflow
)

var centerModes = []string{CenterNever: "never", CenterAlways: "always", CenterOnOverflow: "on-overflow"}

func (m CenterMode) String() string { return enumName(centerModes, m, "CenterMode") }

func ParseCenterMode(s string) (CenterMode, bool) {
	return parseEnum[CenterMode](centerModes, s)
}

type Scrolling struct {
	Presets []ColumnWidth
	Center  CenterMode
}

func DefaultScrolling() Scrolling {
	return Scrolling{Presets: []ColumnWidth{Share(1.0 / 3), Share(0.5), Share(2.0 / 3)}, Center: CenterNever}
}

func (s State) WithLayoutDefault(k LayoutKind) State {
	s.layoutDefault = k
	return s
}

func (s *State) kindFor(id WorkspaceID) LayoutKind {
	kind := s.layoutDefault
	for _, r := range s.wsRules {
		if r.Layout != 0 && r.Select == SelectID && r.ID == id {
			kind = r.Layout
		}
	}
	return kind
}

func (t Tree) Kind() LayoutKind {
	if t.scroll {
		return ScrollLayout
	}
	return TreeLayout
}

func (t Tree) View() int {
	if t.root == nil {
		return 0
	}
	return t.root.view
}

type columnSpan struct {
	lo, hi, total int
}

func spanOf(g *Group, span, inner, target int) columnSpan {
	var c columnSpan
	x := 0
	for i, ch := range g.Children {
		w := ch.width().at(span, inner)
		if i == target {
			c.lo, c.hi = x, x+w
		}
		x += w + inner
	}
	c.total = max(x-inner, 0)
	return c
}

func (t Tree) focusedColumn() (int, bool) {
	if !t.scroll || t.root == nil || len(t.root.Children) == 0 {
		return 0, false
	}
	return t.root.Focus, true
}

func followedView(view, width int, c columnSpan, mode CenterMode) int {
	centered := c.lo + (c.hi-c.lo)/2 - width/2
	visible := c.lo >= view && c.hi <= view+width
	switch {
	case mode == CenterAlways:
		return centered
	case visible:
		return view
	case mode == CenterOnOverflow:
		view = centered
	case c.lo < view || c.hi-c.lo > width:
		view = c.lo
	default:
		view = c.hi - width
	}
	return max(min(view, c.total-width), 0)
}

func (s State) FollowFocus(sp Spacing, mode CenterMode) State {
	shared := false
	for i := range s.workspaces {
		w := &s.workspaces[i]
		col, ok := w.Tree.focusedColumn()
		o := s.output(w.Output)
		if !ok || o == nil {
			continue
		}
		_, wsp := s.spacingFor(w, sp)
		r := wsp.Gaps.Outer.shrink(o.Area)
		view := followedView(w.Tree.root.view, r.Dx(), spanOf(w.Tree.root, r.Dx(), wsp.Gaps.Inner, col), mode)
		if view == w.Tree.root.view {
			continue
		}
		if !shared {
			s, shared = s.sharingWorkspaces(), true
			w = &s.workspaces[i]
		}
		w.Tree = w.Tree.withView(view)
	}
	return s
}

func (t Tree) withView(view int) Tree {
	g := *t.root
	g.view = view
	t.root = &g
	return t
}

func (s State) withScrollTree(settings Settings, op func(t Tree, area image.Rectangle, inner int) (Tree, bool)) State {
	w := s.activeWorkspace()
	if w == nil || !w.Tree.scroll || !w.treeFocused() {
		return s
	}
	o := s.output(w.Output)
	if o == nil {
		return s
	}
	_, sp := s.spacingFor(w, Spacing{Gaps: settings.Gaps})
	tree, changed := op(w.Tree, sp.Gaps.Outer.shrink(o.Area), sp.Gaps.Inner)
	if !changed {
		return s
	}
	s = s.clone()
	active := s.activeWorkspace()
	active.Tree = tree
	active.record()
	return s
}

type ColumnWidth struct {
	share  float64
	pixels int
}

func Share(f float64) ColumnWidth { return ColumnWidth{share: f} }

func Pixels(n int) ColumnWidth { return ColumnWidth{pixels: n} }

func (w ColumnWidth) String() string {
	if w.pixels != 0 {
		return strconv.Itoa(w.pixels) + "px"
	}
	return strconv.FormatFloat(w.share*100, 'f', -1, 64) + "%"
}

func (w ColumnWidth) at(area, inner int) int {
	if w.pixels != 0 {
		return w.pixels
	}
	return max(int(math.Round(w.share*float64(area+inner))), minTile) - inner
}

func (w ColumnWidth) shareOf(area, inner int) float64 {
	if w.pixels == 0 {
		return w.share
	}
	return float64(w.pixels+inner) / float64(area+inner)
}

func (w ColumnWidth) clamped(area, inner int) ColumnWidth {
	if w.pixels == 0 {
		return Share(min(max(w.share, minColumnWidth), 1))
	}
	return Pixels(min(max(w.pixels, Share(minColumnWidth).at(area, inner)), area))
}

func (c Child) width() ColumnWidth {
	if c.Fixed > 0 {
		return Pixels(c.Fixed)
	}
	return Share(c.Ratio)
}

func (c *Child) setWidth(w ColumnWidth, area, inner int) {
	w = w.clamped(area, inner)
	c.Ratio, c.Fixed = w.shareOf(area, inner), w.pixels
}

type WidthChange interface {
	fmt.Stringer
	next(current ColumnWidth, area, inner int, presets []ColumnWidth) (ColumnWidth, bool)
}

type (
	PresetStep int
	WidthBy    ColumnWidth
	WidthTo    ColumnWidth
)

func (p PresetStep) String() string {
	if p < 0 {
		return "preset prev"
	}
	return "preset next"
}

func (p PresetStep) next(current ColumnWidth, area, inner int, presets []ColumnWidth) (ColumnWidth, bool) {
	if len(presets) == 0 {
		return current, false
	}
	now := current.at(area, inner)
	sorted := slices.SortedStableFunc(slices.Values(presets), func(a, b ColumnWidth) int {
		return cmp.Compare(a.at(area, inner), b.at(area, inner))
	})
	if p > 0 {
		for _, w := range sorted {
			if w.at(area, inner) > now {
				return w, true
			}
		}
		return sorted[0], true
	}
	for _, w := range slices.Backward(sorted) {
		if w.at(area, inner) < now {
			return w, true
		}
	}
	return sorted[len(sorted)-1], true
}

func (b WidthBy) String() string {
	text := ColumnWidth(b).String()
	if b.share > 0 || b.pixels > 0 {
		return "+" + text
	}
	return text
}

func (b WidthBy) next(current ColumnWidth, area, inner int, _ []ColumnWidth) (ColumnWidth, bool) {
	if b.pixels != 0 {
		return Pixels(current.at(area, inner) + b.pixels), true
	}
	return Share(current.shareOf(area, inner) + b.share), true
}

func (w WidthTo) String() string { return ColumnWidth(w).String() }

func (w WidthTo) next(ColumnWidth, int, int, []ColumnWidth) (ColumnWidth, bool) {
	return ColumnWidth(w), true
}

const minColumnWidth = 0.05

func (t Tree) ColumnWidth(c WidthChange, presets []ColumnWidth, area, inner int) (Tree, bool) {
	col, ok := t.focusedColumn()
	if !ok {
		return t, false
	}
	before := t.root.Children[col]
	w, changes := c.next(before.width(), area, inner, presets)
	if !changes {
		return t, false
	}
	after := before
	after.setWidth(w, area, inner)
	if after.Ratio == before.Ratio && after.Fixed == before.Fixed {
		return t, false
	}
	t = t.clone()
	t.root.Children[col].Ratio, t.root.Children[col].Fixed = after.Ratio, after.Fixed
	return t, true
}

func (t Tree) Consume(d Direction) (Tree, bool) {
	col, ok := t.focusedColumn()
	if !ok || d.vertical() {
		return t, false
	}
	j := step(col, d)
	if j < 0 || j >= len(t.root.Children) {
		return t, false
	}
	original := t
	t = t.clone()
	root := t.root
	id, _ := t.Focused()
	target, isGroup := root.Children[j].Node.(*Group)
	if !isGroup {
		target = t.newGroup(SplitV)
		target.insert(0, root.Children[j].Node)
		root.Children[j].Node = target
	}
	if target.Locked {
		return original, false
	}
	groups, idx, _ := t.path(id)
	groups[len(groups)-1].remove(idx[len(idx)-1])
	target.insert(len(target.Children), Window{ID: id})
	t.collapse(groups, idx, CollapseInvalid)
	t.focusNode(Window{ID: id})
	return t, true
}

func (t Tree) Expel() (Tree, bool) {
	if !t.scroll || t.root == nil {
		return t, false
	}
	groups, _ := t.focusPath()
	if len(groups) < 2 || groups[len(groups)-1].Locked {
		return t, false
	}
	return t.MoveOut()
}

func (t Tree) Center(area image.Rectangle, inner int) (Tree, bool) {
	col, ok := t.focusedColumn()
	if !ok {
		return t, false
	}
	view := followedView(t.root.view, area.Dx(), spanOf(t.root, area.Dx(), inner, col), CenterAlways)
	if view == t.root.view {
		return t, false
	}
	return t.withView(view), true
}

func (t Tree) ColumnDisplay(l Layout) (Tree, bool) {
	col, ok := t.focusedColumn()
	if !ok || l != SplitV && l != Tabbed {
		return t, false
	}
	t = t.clone()
	ch := &t.root.Children[col]
	g, isGroup := ch.Node.(*Group)
	if !isGroup && l == SplitV {
		return t, false
	}
	if !isGroup {
		g = t.newGroup(l)
		g.insert(0, ch.Node)
		ch.Node = g
		return t, true
	}
	return t, g.setLayout(l)
}

func (t Tree) ScrollBy(dx int) Tree {
	if !t.scroll || t.root == nil || dx == 0 {
		return t
	}
	return t.withView(t.root.view + dx)
}

func (t Tree) SnapView(area image.Rectangle, inner int) (Tree, int) {
	if !t.scroll || t.root == nil || len(t.root.Children) == 0 {
		return t, 0
	}
	view, best, col := t.root.view, math.MaxInt, 0
	for i := range t.root.Children {
		c := spanOf(t.root, area.Dx(), inner, i)
		if d := abs(c.lo - view); d < best {
			best, col = d, i
		}
	}
	c := spanOf(t.root, area.Dx(), inner, col)
	return t.withView(max(min(c.lo, c.total-area.Dx()), 0)), col
}

func (s State) ScrollView(dx int) State {
	w := s.activeWorkspace()
	if w == nil || !w.Tree.scroll {
		return s
	}
	s = s.sharingWorkspaces()
	active := s.activeWorkspace()
	active.Tree = active.Tree.ScrollBy(dx)
	return s
}

func (s State) SettleView(sp Spacing) (State, error) {
	w := s.activeWorkspace()
	o := s.output(s.focused)
	if w == nil || o == nil || !w.Tree.scroll || w.Tree.root == nil {
		return s, nil
	}
	_, wsp := s.spacingFor(w, sp)
	tree, col := w.Tree.SnapView(wsp.Gaps.Outer.shrink(o.Area), wsp.Gaps.Inner)
	s = s.sharingWorkspaces()
	active := s.activeWorkspace()
	active.Tree = tree
	if tree.root.Focus != col {
		return s.FocusWindow(entered(tree.root.Children[col].Node, Right), Raise)
	}
	return s, nil
}

func toScroll(t Tree) Tree {
	if t.scroll {
		return t
	}
	focused, hasFocus := t.Focused()
	out := Tree{scroll: true, minted: t.minted}
	if t.root != nil {
		root := out.newGroup(Scrolled)
		total := ratioTotal(t.root.Children)
		for _, c := range t.root.Children {
			col := c.Node
			if g, isGroup := c.Node.(*Group); isGroup {
				layout := SplitV
				if g.Layout == Tabbed {
					layout = Tabbed
				}
				stack := out.newGroup(layout)
				focus := firstWindow(g)
				for _, id := range appendWindows(nil, g) {
					if id == focus {
						stack.Focus = len(stack.Children)
					}
					stack.insert(len(stack.Children), Window{ID: id})
				}
				col = stack
			}
			root.Children = append(root.Children, Child{Node: col, Ratio: c.Ratio / total})
		}
		out.root = root
	}
	if hasFocus {
		out.focusNode(Window{ID: focused})
	}
	return out
}

func toTree(t Tree) Tree {
	if !t.scroll {
		return t
	}
	t = t.clone()
	t.scroll = false
	if t.root != nil {
		t.root.Layout, t.root.view = SplitH, 0
		for i := range t.root.Children {
			t.root.Children[i].Fixed = 0
		}
	}
	return t
}

func (s State) SetLayout(k LayoutKind) State {
	w := s.activeWorkspace()
	if w == nil || w.Tree.Kind() == k {
		return s
	}
	s = s.clone()
	active := s.activeWorkspace()
	if k == ScrollLayout {
		active.Tree = toScroll(active.Tree)
	} else {
		active.Tree = toTree(active.Tree)
	}
	return s
}

type (
	SetLayoutAction     struct{ Kind LayoutKind }
	ColumnWidthAction   struct{ Change WidthChange }
	ConsumeAction       struct{ Dir Direction }
	ExpelAction         struct{}
	CenterAction        struct{}
	ColumnDisplayAction struct{ Layout Layout }
)

func (a SetLayoutAction) String() string     { return "setlayout " + a.Kind.String() }
func (a ColumnWidthAction) String() string   { return "columnwidth " + a.Change.String() }
func (a ConsumeAction) String() string       { return "consume " + a.Dir.String() }
func (ExpelAction) String() string           { return "expel" }
func (CenterAction) String() string          { return "center" }
func (a ColumnDisplayAction) String() string { return "columndisplay " + columnDisplays[a.Layout] }

var columnDisplays = []string{SplitV: "stack", Tabbed: "tab"}

func (a SetLayoutAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.SetLayout(a.Kind), nil, nil
}

func (a ColumnWidthAction) apply(s State, settings Settings) (State, []Effect, error) {
	return s.withScrollTree(settings, func(t Tree, area image.Rectangle, inner int) (Tree, bool) {
		return t.ColumnWidth(a.Change, settings.Scroll.Presets, area.Dx(), inner)
	}), nil, nil
}

func (a ConsumeAction) apply(s State, settings Settings) (State, []Effect, error) {
	return s.withScrollTree(settings, func(t Tree, _ image.Rectangle, _ int) (Tree, bool) { return t.Consume(a.Dir) }), nil, nil
}

func (ExpelAction) apply(s State, settings Settings) (State, []Effect, error) {
	return s.withScrollTree(settings, func(t Tree, _ image.Rectangle, _ int) (Tree, bool) { return t.Expel() }), nil, nil
}

func (CenterAction) apply(s State, settings Settings) (State, []Effect, error) {
	return s.withScrollTree(settings, func(t Tree, area image.Rectangle, inner int) (Tree, bool) {
		return t.Center(area, inner)
	}), nil, nil
}

func (a ColumnDisplayAction) apply(s State, settings Settings) (State, []Effect, error) {
	return s.withScrollTree(settings, func(t Tree, _ image.Rectangle, _ int) (Tree, bool) { return t.ColumnDisplay(a.Layout) }), nil, nil
}

func parseSetLayout(spec ActionSpec, args []string, _ string) (Action, error) {
	if err := exactly(spec, args, 1); err != nil {
		return nil, err
	}
	k, ok := ParseLayoutKind(args[0])
	if !ok {
		return nil, badArgs(spec, "%q is not tree or scroll", args[0])
	}
	return SetLayoutAction{Kind: k}, nil
}

func ParsePixels(s string) (int, bool) {
	text, ok := strings.CutSuffix(s, "px")
	n, err := strconv.Atoi(text)
	return n, ok && err == nil && text[0] >= '1' && text[0] <= '9'
}

func relative(arg string) bool { return arg != "" && (arg[0] == '+' || arg[0] == '-') }

func parseColumnWidth(spec ActionSpec, args []string, _ string) (Action, error) {
	if len(args) == 2 && args[0] == "preset" {
		switch args[1] {
		case "next":
			return ColumnWidthAction{Change: PresetStep(1)}, nil
		case "prev":
			return ColumnWidthAction{Change: PresetStep(-1)}, nil
		}
		return nil, badArgs(spec, "%q is not next or prev", args[1])
	}
	if len(args) != 1 {
		return nil, badArgs(spec, "takes preset next, preset prev, a percentage or pixels")
	}
	if text, ok := strings.CutSuffix(args[0], "px"); ok {
		return parsePixelWidth(spec, args[0], text)
	}
	text, ok := strings.CutSuffix(args[0], "%")
	v, err := strconv.ParseFloat(text, 64)
	if !ok || err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, badArgs(spec, "%q is not a percentage or pixels", args[0])
	}
	if relative(text) {
		if v == 0 {
			return nil, badArgs(spec, "%q changes nothing", args[0])
		}
		return ColumnWidthAction{Change: WidthBy(Share(v / 100))}, nil
	}
	if v <= 0 || v > 100 {
		return nil, badArgs(spec, "%q is not above 0%% and up to 100%%", args[0])
	}
	return ColumnWidthAction{Change: WidthTo(Share(v / 100))}, nil
}

func parsePixelWidth(spec ActionSpec, arg, text string) (Action, error) {
	if !relative(text) {
		n, ok := ParsePixels(arg)
		if !ok {
			return nil, badArgs(spec, "%q is not a width above 0px", arg)
		}
		return ColumnWidthAction{Change: WidthTo(Pixels(n))}, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return nil, badArgs(spec, "%q is not a change in pixels", arg)
	}
	if n == 0 {
		return nil, badArgs(spec, "%q changes nothing", arg)
	}
	return ColumnWidthAction{Change: WidthBy(Pixels(n))}, nil
}

func parseConsume(spec ActionSpec, args []string, rest string) (Action, error) {
	a, err := directional(func(d Direction) Action { return ConsumeAction{Dir: d} })(spec, args, rest)
	if err != nil {
		return nil, err
	}
	if c, _ := a.(ConsumeAction); c.Dir.vertical() {
		return nil, badArgs(spec, "%q is not left or right", args[0])
	}
	return a, nil
}

func parseColumnDisplay(spec ActionSpec, args []string, _ string) (Action, error) {
	if err := exactly(spec, args, 1); err != nil {
		return nil, err
	}
	l, ok := parseEnum[Layout](columnDisplays, args[0])
	if !ok {
		return nil, badArgs(spec, "%q is not stack or tab", args[0])
	}
	return ColumnDisplayAction{Layout: l}, nil
}
