package wm

import (
	"errors"
	"fmt"
	"image"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func describe(n Node) string {
	switch n := n.(type) {
	case Window:
		return fmt.Sprint(n.ID)
	case *Group:
		parts := make([]string, len(n.Children))
		for i, c := range n.Children {
			parts[i] = describe(c.Node)
			if i == n.Focus {
				parts[i] = "*" + parts[i]
			}
		}
		e := ""
		if n.ephemeral != ephemeralOff {
			e = "~"
		}
		return fmt.Sprintf("%s%s[%s]", n.Layout, e, strings.Join(parts, " "))
	}
	return "?"
}

func (t Tree) String() string {
	if t.root == nil {
		return "empty"
	}
	return describe(t.root)
}

func on(t *testing.T) func(Tree, error) Tree {
	return func(tree Tree, err error) Tree {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return tree
	}
}

func build(t *testing.T, ids ...WindowID) Tree {
	t.Helper()
	var tree Tree
	for _, id := range ids {
		tree = on(t)(tree.Insert(id, SplitH))
	}
	return tree
}

func TestInsertGoesAfterTheFocusedWindow(t *testing.T) {
	tree := build(t, 1, 2, 3)
	tree = on(t)(tree.Focus(1))
	tree = on(t)(tree.Insert(4, SplitH))
	if got := tree.String(); got != "h[1 *4 2 3]" {
		t.Fatalf("tree %s", got)
	}
	if _, err := tree.Insert(4, SplitH); err == nil {
		t.Fatal("a window was inserted twice")
	}
	if _, err := tree.Focus(9); !errors.Is(err, ErrUnknownWindow) {
		t.Fatalf("focus of an unknown window: %v", err)
	}
}

func TestRemoveMovesFocusToTheNeighbour(t *testing.T) {
	tree := on(t)(build(t, 1, 2, 3).Focus(2))
	tree = on(t)(tree.Remove(2))
	if got := tree.String(); got != "h[*1 3]" {
		t.Fatalf("removing the focused middle window: %s, want focus on the previous one", got)
	}
	tree = on(t)(on(t)(tree.Focus(1)).Remove(1))
	if got := tree.String(); got != "h[*3]" {
		t.Fatalf("removing the focused first window: %s, want focus on the next one", got)
	}
	tree = on(t)(tree.Remove(3))
	if !tree.Empty() {
		t.Fatalf("tree %s after removing every window", tree)
	}
}

func TestCollapseOnRemoval(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) Tree
		gone  WindowID
		want  string
	}{
		{"a split keeps a lone window", func(t *testing.T) Tree {
			return on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
		}, 1, "v[*2]"},
		{"a nested split keeps a lone window under the invalid-only rule", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
			return on(t)(tree.Insert(3, SplitH))
		}, 3, "h[1 *v[*2]]"},
		{"an ephemeral group dissolves", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, true))
			return on(t)(tree.Insert(3, SplitH))
		}, 3, "h[1 *2]"},
		{"a staged ephemeral group stays until it has held two", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2, 3).Wrap(2, SplitV, true))
			return tree
		}, 3, "h[1 *v~[*2]]"},
		{"the root takes over a lone split of the other direction", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
			return on(t)(tree.Insert(3, SplitH))
		}, 1, "v[2 *3]"},
		{"an emptied group disappears", func(t *testing.T) Tree {
			return on(t)(build(t, 1, 2, 3).Wrap(3, Tabbed, false))
		}, 3, "h[1 *2]"},
		{"a tab group inside a tab group with one tab collapses", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1).Wrap(1, Tabbed, false))
			tree = on(t)(tree.Wrap(1, Tabbed, false))
			return on(t)(tree.Insert(2, SplitH))
		}, 2, "h[*tab[*1]]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tree := c.build(t)
			before := tree.String()
			got := on(t)(tree.Remove(c.gone)).String()
			if got != c.want {
				t.Fatalf("%s minus %d is %s, want %s", before, c.gone, got, c.want)
			}
		})
	}
}

func TestArrangeRoundsEdgesDeterministically(t *testing.T) {
	tree := build(t, 1, 2, 3)
	a := tree.Arrange(image.Rect(0, 0, 100, 50), Spacing{TabBar: 10})
	want := []Placement{
		{ID: 1, Rect: image.Rect(0, 0, 33, 50), Visible: true},
		{ID: 2, Rect: image.Rect(33, 0, 67, 50), Visible: true},
		{ID: 3, Rect: image.Rect(67, 0, 100, 50), Visible: true},
	}
	if !slices.Equal(a.Windows, want) || len(a.TabBars) != 0 {
		t.Fatalf("arranged %v %v", a.Windows, a.TabBars)
	}
	tree = on(t)(tree.Wrap(3, Tabbed, false))
	tree = on(t)(tree.Insert(4, SplitH))
	a = tree.Arrange(image.Rect(0, 0, 100, 50), Spacing{TabBar: 10})
	if want := (TabBar{Rect: image.Rect(67, 0, 100, 10), Tabs: []WindowID{3, 4}, Active: 1}); len(a.TabBars) != 1 || a.TabBars[0].Rect != want.Rect || !slices.Equal(a.TabBars[0].Tabs, want.Tabs) || a.TabBars[0].Active != 1 {
		t.Fatalf("tab bars %v, want %v", a.TabBars, want)
	}
	if got := a.Windows[2:]; got[0] != (Placement{ID: 3, Rect: image.Rect(67, 10, 100, 50), Visible: false}) || got[1] != (Placement{ID: 4, Rect: image.Rect(67, 10, 100, 50), Visible: true}) {
		t.Fatalf("tabs placed %v", got)
	}
}

func TestArrangeSeparatesTilesByTheGaps(t *testing.T) {
	tree := build(t, 1, 2)
	tree = on(t)(tree.Wrap(2, SplitV, false))
	tree = on(t)(tree.Insert(3, SplitH))
	sp := Spacing{Gaps: Gaps{Inner: 5, Outer: Insets{Top: 10, Right: 4, Bottom: 6, Left: 2}}}
	a := tree.Arrange(image.Rect(0, 0, 103, 80), sp)
	want := []Placement{
		{ID: 1, Rect: image.Rect(2, 10, 49, 74), Visible: true},
		{ID: 2, Rect: image.Rect(54, 10, 99, 40), Visible: true},
		{ID: 3, Rect: image.Rect(54, 45, 99, 74), Visible: true},
	}
	if !slices.Equal(a.Windows, want) {
		t.Fatalf("%s arranged %v, want %v", tree, a.Windows, want)
	}
	if b := tree.Arrange(image.Rect(0, 0, 103, 80), Spacing{}); b.Windows[0].Rect.Max.X != b.Windows[1].Rect.Min.X {
		t.Fatalf("without gaps neighbours share an edge: %v", b.Windows)
	}
}

func TestArrangeKeepsTilesValidWhenGapsExceedTheArea(t *testing.T) {
	a := build(t, 1, 2, 3).Arrange(image.Rect(0, 0, 20, 10), Spacing{Gaps: Gaps{Inner: 30, Outer: Insets{Top: 8, Bottom: 8}}})
	for _, p := range a.Windows {
		if p.Rect.Dx() < 0 || p.Rect.Dy() < 0 || p.Rect != p.Rect.Canon() {
			t.Fatalf("placed %v", a.Windows)
		}
	}
}

func checkInvariants(t *testing.T, tree Tree, live []WindowID) {
	t.Helper()
	if got := slices.Sorted(slices.Values(tree.Windows())); !slices.Equal(got, slices.Sorted(slices.Values(live))) {
		t.Fatalf("tree %s holds %v, want %v", tree, got, live)
	}
	var check func(g *Group)
	check = func(g *Group) {
		if len(g.Children) == 0 {
			t.Fatalf("tree %s has an empty group", tree)
		}
		if g.Focus < 0 || g.Focus >= len(g.Children) {
			t.Fatalf("tree %s has focus index %d of %d", tree, g.Focus, len(g.Children))
		}
		sum := 0.0
		for _, c := range g.Children {
			if c.Ratio <= 0 {
				t.Fatalf("tree %s has a child with ratio %g", tree, c.Ratio)
			}
			sum += c.Ratio
			if sub, ok := c.Node.(*Group); ok {
				check(sub)
			}
		}
		if math.Abs(sum-float64(len(g.Children))) > 1e-9 {
			t.Fatalf("tree %s has ratios summing to %g over %d children", tree, sum, len(g.Children))
		}
	}
	if tree.root != nil {
		check(tree.root)
		if _, ok := tree.Focused(); !ok {
			t.Fatalf("tree %s has no focused window", tree)
		}
	}
	area := image.Rect(3, 7, 1283, 727)
	a := tree.Arrange(area, Spacing{})
	if b := tree.Arrange(area, Spacing{}); !slices.Equal(a.Windows, b.Windows) {
		t.Fatalf("arranging %s twice gave different placements", tree)
	}
	covered := 0
	var visible []image.Rectangle
	for _, p := range a.Windows {
		if !p.Visible {
			continue
		}
		if !p.Rect.In(area) {
			t.Fatalf("tree %s placed %d at %v outside %v", tree, p.ID, p.Rect, area)
		}
		for _, other := range visible {
			if other.Overlaps(p.Rect) {
				t.Fatalf("tree %s overlaps %v and %v", tree, other, p.Rect)
			}
		}
		visible = append(visible, p.Rect)
		covered += p.Rect.Dx() * p.Rect.Dy()
	}
	if tree.root != nil && covered != area.Dx()*area.Dy() {
		t.Fatalf("tree %s covers %d of %d pixels", tree, covered, area.Dx()*area.Dy())
	}
}

func TestRandomOperationsKeepTheTreeValid(t *testing.T) {
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 0x7e1de))
		var tree Tree
		var live []WindowID
		next := WindowID(1)
		for step := range 60 {
			var err error
			switch op := rng.IntN(9); {
			case op <= 1 || len(live) == 0:
				tree, err = tree.Insert(next, Layout(1+rng.IntN(2)))
				live = append(live, next)
				next++
			case op == 2:
				i := rng.IntN(len(live))
				tree, err = tree.Remove(live[i])
				live = slices.Delete(live, i, i+1)
			case op == 3:
				tree, err = tree.Focus(live[rng.IntN(len(live))])
			case op == 8:
				tree, _, err = tree.Resize(live[rng.IntN(len(live))], image.Rect(0, 0, 1920, 1080), Direction(1+rng.IntN(4)), rng.IntN(2000)-1000)
			case op >= 6:
				tree, _, err = tree.Move(live[rng.IntN(len(live))], Direction(1+rng.IntN(4)), CollapsePolicy(rng.IntN(3)))
			default:
				tree, err = tree.Wrap(live[rng.IntN(len(live))], Layout(1+rng.IntN(3)), rng.IntN(2) == 0)
			}
			if err != nil {
				t.Fatalf("seed %d step %d: %v", seed, step, err)
			}
			checkInvariants(t, tree, live)
		}
	}
}

func TestAutotileSplitsNarrowColumnsVertically(t *testing.T) {
	at := Autotile{Enabled: true, Ephemeral: true, MinTrigger: 400, Divisor: 3}
	wide := image.Rect(0, 0, 3840, 1080)
	var tree Tree
	for id := range WindowID(5) {
		tree = on(t)(tree.Place(id+1, wide, at))
	}
	if got := tree.String(); got != "h[1 2 *v~[3 4 *5]]" {
		t.Fatalf("five windows on 3840 wide with a 1280 trigger: %s", got)
	}
	narrow := image.Rect(0, 0, 1000, 1200)
	tree = Tree{}
	for id := range WindowID(3) {
		tree = on(t)(tree.Place(id+1, narrow, at))
	}
	if got := tree.String(); got != "v[1 2 *3]" {
		t.Fatalf("a portrait output starts vertical and stays so: %s", got)
	}
	tree = Tree{}
	for id := range WindowID(4) {
		tree = on(t)(tree.Place(id+1, wide, Autotile{}))
	}
	if got := tree.String(); got != "h[1 2 3 *4]" {
		t.Fatalf("without autotile: %s", got)
	}
}

func TestRemovingAChildKeepsItsSiblingsPositive(t *testing.T) {
	g := &Group{Children: []Child{{Ratio: 0.05}, {Ratio: 0.05}, {Ratio: 2.9}}}
	g.remove(0)
	sum := 0.0
	for _, c := range g.Children {
		if c.Ratio <= 0 {
			t.Fatalf("ratios %+v after removing a narrow child", g.Children)
		}
		sum += c.Ratio
	}
	if math.Abs(sum-2) > 1e-9 {
		t.Fatalf("ratios sum to %g, want 2", sum)
	}
}

func BenchmarkTreeContains(b *testing.B) {
	tree := Tree{}
	for id := WindowID(1); id <= 16; id++ {
		next, err := tree.Insert(id, SplitH)
		if err != nil {
			b.Fatal(err)
		}
		tree = next
	}
	b.ReportAllocs()
	for range b.N {
		if !tree.Contains(16) {
			b.Fatal("lost the window")
		}
	}
}

func split3(t *testing.T) Tree {
	t.Helper()
	tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
	tree = on(t)(tree.Focus(2))
	return on(t)(tree.Insert(3, SplitH))
}

func ephemeralNested(t *testing.T) Tree {
	t.Helper()
	tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, true))
	tree = on(t)(tree.Focus(2))
	return on(t)(tree.Insert(3, SplitH))
}

func lifted(t *testing.T, tree Tree) Tree {
	t.Helper()
	tree, changed := tree.FocusParent()
	if !changed {
		t.Fatalf("focusparent changed nothing in %s", tree)
	}
	return tree
}

func TestGroupOperations(t *testing.T) {
	cases := []struct {
		name    string
		tree    func(t *testing.T) Tree
		op      func(Tree) (Tree, bool)
		want    string
		changed bool
	}{
		{"changegroup turns the innermost group", split3, func(t Tree) (Tree, bool) { return t.ChangeGroup(ChangeToH) }, "h[1 *h[2 *3]]", true},
		{"changegroup to the layout it has", split3, func(t Tree) (Tree, bool) { return t.ChangeGroup(ChangeToV) }, "h[1 *v[2 *3]]", false},
		{"changegroup opposite", split3, func(t Tree) (Tree, bool) { return t.ChangeGroup(ChangeOpposite) }, "h[1 *h[2 *3]]", true},
		{"changegroup on a lifted group", func(t *testing.T) Tree { return lifted(t, lifted(t, split3(t))) }, func(t Tree) (Tree, bool) { return t.ChangeGroup(ChangeToTab) }, "tab[1 *v[2 *3]]", true},
		{"focustab next", func(t *testing.T) Tree { return on(t)(on(t)(build(t, 1, 2, 3).Wrap(1, Tabbed, false)).Focus(1)) }, func(t Tree) (Tree, bool) { return t.FocusTab(TabStep(1), false) }, "h[*tab[*1] 2 3]", false},
		{"focustab wraps", func(t *testing.T) Tree { return tabs(t, 1, 2, 3) }, func(t Tree) (Tree, bool) { return t.FocusTab(TabStep(1), true) }, "tab[*1 2 3]", true},
		{"focustab stops at the end", func(t *testing.T) Tree { return tabs(t, 1, 2, 3) }, func(t Tree) (Tree, bool) { return t.FocusTab(TabStep(1), false) }, "tab[1 2 *3]", false},
		{"focustab by number", func(t *testing.T) Tree { return tabs(t, 1, 2, 3) }, func(t Tree) (Tree, bool) { return t.FocusTab(TabIndex(2), false) }, "tab[1 *2 3]", true},
		{"focustab past the last", func(t *testing.T) Tree { return tabs(t, 1, 2, 3) }, func(t Tree) (Tree, bool) { return t.FocusTab(TabIndex(4), false) }, "tab[1 2 *3]", false},
		{"focustab in a split3 tab group", func(t *testing.T) Tree {
			tree := on(t)(tabs(t, 1, 2).Wrap(2, SplitV, false))
			tree = on(t)(tree.Focus(2))
			return on(t)(tree.Insert(3, SplitH))
		}, func(t Tree) (Tree, bool) { return t.FocusTab(TabStep(-1), false) }, "tab[*1 v[2 *3]]", true},
		{"swap keeps the slots", split3, func(t Tree) (Tree, bool) { return t.Swap(Left) }, "h[*3 v[2 *1]]", true},
		{"swap at the edge", split3, func(t Tree) (Tree, bool) { return t.Swap(Right) }, "h[1 *v[2 *3]]", false},
		{"swap a lifted group", func(t *testing.T) Tree { return lifted(t, split3(t)) }, func(t Tree) (Tree, bool) { return t.Swap(Left) }, "h[*v[2 *3] 1]", true},
		{"moveout lifts a window after its group", split3, Tree.MoveOut, "h[1 v[*2] *3]", true},
		{"moveout of an ephemeral group dissolves it", ephemeralNested, Tree.MoveOut, "h[1 2 *3]", true},
		{"moveout at the root", func(t *testing.T) Tree { return build(t, 1, 2) }, Tree.MoveOut, "h[1 *2]", false},
		{"moveout of a lifted group", func(t *testing.T) Tree {
			tree := on(t)(split3(t).Wrap(3, SplitH, false))
			tree = on(t)(tree.Focus(3))
			tree = on(t)(tree.Insert(4, SplitH))
			return lifted(t, tree)
		}, Tree.MoveOut, "h[1 v[*2] *h[3 *4]]", true},
		{"focuschild without a lift", split3, Tree.FocusChild, "h[1 *v[2 *3]]", false},
		{"focusparent at the root", func(t *testing.T) Tree { return lifted(t, lifted(t, split3(t))) }, Tree.FocusParent, "h[1 *v[2 *3]]", false},
		{"locktab on no tabs", split3, func(t Tree) (Tree, bool) { return t.LockTabs(SwitchToggle) }, "h[1 *v[2 *3]]", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tree := c.tree(t)
			before := tree.String()
			got, changed := c.op(tree)
			if got.String() != c.want || changed != c.changed {
				t.Fatalf("%s gave %s (changed %t), want %s (changed %t)", before, got, changed, c.want, c.changed)
			}
			if tree.String() != before {
				t.Fatalf("the operation changed its input to %s", tree)
			}
		})
	}
}

func tabs(t *testing.T, ids ...WindowID) Tree {
	t.Helper()
	var tree Tree
	for _, id := range ids {
		tree = on(t)(tree.Insert(id, Tabbed))
	}
	return tree
}

func ratios(g *Group) []float64 {
	out := make([]float64, len(g.Children))
	for i, c := range g.Children {
		out[i] = c.Ratio
	}
	return out
}

func TestToggleTabTwiceRestoresTheSplitAndItsRatios(t *testing.T) {
	tree := build(t, 1, 2, 3)
	tree, _, err := tree.Resize(1, image.Rect(0, 0, 900, 600), Right, 150)
	if err != nil {
		t.Fatal(err)
	}
	want := ratios(tree.Root())
	tree, _ = tree.ChangeGroup(ChangeToggleTab)
	if tree.String() != "tab[1 2 *3]" {
		t.Fatalf("toggletab gave %s", tree)
	}
	tree, _ = tree.ChangeGroup(ChangeToggleTab)
	if tree.String() != "h[1 2 *3]" || !slices.Equal(ratios(tree.Root()), want) {
		t.Fatalf("toggletab twice gave %s with ratios %v, want h with %v", tree, ratios(tree.Root()), want)
	}
	made := tabs(t, 1, 2)
	if made, _ = made.ChangeGroup(ChangeToggleTab); made.String() != "h[1 *2]" {
		t.Fatalf("a group made as tabs toggled to %s", made)
	}
}

func TestEqualizeResetsTheFocusedGroupOrTheWholeTree(t *testing.T) {
	area := image.Rect(0, 0, 1200, 800)
	tree := split3(t)
	tree, _, _ = tree.Resize(3, area, Up, 100)
	tree, _, _ = tree.Resize(1, area, Right, 100)
	inner, _ := tree.Root().Children[1].Node.(*Group)
	if slices.Equal(ratios(inner), []float64{1, 1}) || slices.Equal(ratios(tree.Root()), []float64{1, 1}) {
		t.Fatal("the resizes did not change the ratios")
	}
	one, changed := tree.Equalize(false)
	oneInner, _ := one.Root().Children[1].Node.(*Group)
	if !changed || !slices.Equal(ratios(oneInner), []float64{1, 1}) || slices.Equal(ratios(one.Root()), []float64{1, 1}) {
		t.Fatalf("equalize gave %v inside %v", ratios(oneInner), ratios(one.Root()))
	}
	all, _ := tree.Equalize(true)
	allInner, _ := all.Root().Children[1].Node.(*Group)
	if !slices.Equal(ratios(allInner), []float64{1, 1}) || !slices.Equal(ratios(all.Root()), []float64{1, 1}) {
		t.Fatalf("equalize all gave %v inside %v", ratios(allInner), ratios(all.Root()))
	}
	if _, changed := all.Equalize(true); changed {
		t.Fatal("equalizing an equal tree changed it")
	}
}

func TestALockedTabGroupKeepsWindowsOutAndIn(t *testing.T) {
	tree := on(t)(build(t, 1, 2).Wrap(2, Tabbed, false))
	tree = on(t)(tree.Focus(2))
	tree = on(t)(tree.Insert(3, SplitH))
	tree, changed := tree.LockTabs(SwitchOn)
	if !changed {
		t.Fatal("locktab on changed nothing")
	}
	tree = on(t)(tree.Insert(4, SplitH))
	if got := tree.String(); got != "h[1 tab[2 *3] *4]" {
		t.Fatalf("a window opened from a locked tab group landed in %s", got)
	}
	if moved, ok, _ := tree.Move(4, Left, CollapseEmptySplits); !ok || moved.String() != "h[1 *4 tab[2 *3]]" {
		t.Fatalf("moving into a locked group gave %s, want it passed over", moved)
	}
	tree = on(t)(tree.Focus(3))
	if moved, ok, _ := tree.Move(3, Left, CollapseEmptySplits); !ok || moved.String() != "h[1 *tab[*3 2] 4]" {
		t.Fatalf("reordering inside a locked group gave %s", moved)
	}
	if _, result := tree.MoveFocused(Up, CollapseEmptySplits); result != moveBlocked {
		t.Fatalf("leaving a locked group gave %d, want blocked", result)
	}
	if _, changed := tree.MoveOut(); changed {
		t.Fatal("moveout left a locked group")
	}
	if _, changed := tree.Swap(Right); changed {
		t.Fatal("swap left a locked group")
	}
	tree = on(t)(tree.Focus(1))
	if swapped, _ := tree.Swap(Right); swapped.String() != "h[tab[2 *3] *1 4]" {
		t.Fatalf("swapping with a locked group gave %s, want the whole group exchanged", swapped)
	}
	root := tabs(t, 1, 2)
	root, _ = root.LockTabs(SwitchToggle)
	if root = on(t)(root.Insert(3, SplitH)); root.String() != "h[tab[1 *2] *3]" {
		t.Fatalf("a window opened from a locked root landed in %s", root)
	}
}

func TestAMoveOfALiftedGroupCarriesItWhole(t *testing.T) {
	tree := lifted(t, split3(t))
	moved, result := tree.MoveFocused(Left, CollapseEmptySplits)
	if result != moveDone || moved.String() != "h[*v[2 *3] 1]" {
		t.Fatalf("moving the lifted group gave %s (%d)", moved, result)
	}
	if g, ok := moved.LiftedGroup(); !ok || describe(g) != "v[2 *3]" {
		t.Fatal("the moved group lost its focus")
	}
	if _, result := moved.MoveFocused(Left, CollapseEmptySplits); result != moveAtEdge {
		t.Fatalf("moving the group off the edge gave %d", result)
	}
}

func TestAGroupKeepsItsIDWhileItsMembersChange(t *testing.T) {
	tree := build(t, 1, 2)
	root, ok := tree.MembershipOf(1)
	if !ok || root.Layout != SplitH || root.Index != 0 || root.Group == 0 {
		t.Fatalf("window 1 in %+v, %v", root, ok)
	}
	tree = on(t)(tree.Wrap(2, Tabbed, false))
	tree = on(t)(tree.Insert(3, SplitH))
	tabs, _ := tree.MembershipOf(3)
	if tabs.Layout != Tabbed || tabs.Index != 1 || tabs.Group == root.Group {
		t.Fatalf("window 3 inserted beside 2 in its tab group: %+v in %s", tabs, tree)
	}
	tree = on(t)(tree.Insert(4, SplitH))
	tree = on(t)(tree.Remove(2))
	if got, _ := tree.MembershipOf(4); got.Group != tabs.Group || got.Index != 1 {
		t.Fatalf("window 4 after its sibling left: %+v, want group %d index 1 in %s", got, tabs.Group, tree)
	}
	if got, _ := tree.MembershipOf(1); got != root {
		t.Fatalf("window 1 moved to %+v, from %+v", got, root)
	}
	tree = on(t)(tree.Wrap(1, SplitV, false))
	if got, _ := tree.MembershipOf(1); got.Group == root.Group || got.Group == tabs.Group {
		t.Fatalf("a new group reused ID %d", got.Group)
	}
}

func TestGroupIDsSurviveSavingAndStayUnique(t *testing.T) {
	tree := on(t)(build(t, 1, 2).Wrap(2, Tabbed, false))
	before, _ := tree.MembershipOf(2)
	loaded, err := loadTree(saveGroup(tree.root), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := loaded.MembershipOf(2); got != before {
		t.Fatalf("window 2 in %+v after loading, %+v before", got, before)
	}
	loaded = on(t)(loaded.Wrap(1, SplitV, false))
	root, _ := tree.MembershipOf(1)
	if got, _ := loaded.MembershipOf(1); got.Group == before.Group || got.Group == root.Group {
		t.Fatalf("a group made after loading reused ID %d", got.Group)
	}
	saved := saveGroup(tree.root)
	saved.ID = 0
	unnumbered, err := loadTree(saved, 0)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := unnumbered.MembershipOf(1); a.Group == 0 || a.Group == before.Group {
		t.Fatalf("a group saved without an ID loaded as %d", a.Group)
	}
}
