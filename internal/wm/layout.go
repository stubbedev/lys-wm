package wm

import (
	"image"
	"math"
)

type Placement struct {
	ID        WindowID
	Rect      image.Rectangle
	Visible   bool
	Laid      bool
	Layer     PlacementLayer
	Minimized bool
	Workspace WorkspaceID
	Fake      bool
	Scratch   bool
	Pinned    bool
	Look      WorkspaceLook
}

func (p Placement) Told() PlacementLayer {
	return p.Layer.Told(p.Fake)
}

type TabBar struct {
	Rect      image.Rectangle
	Tabs      []WindowID
	Active    int
	Workspace WorkspaceID
	Shown     bool
}

type Arrangement struct {
	Windows []Placement
	TabBars []TabBar
}

type Insets struct {
	Top, Right, Bottom, Left int
}

func (i Insets) shrink(r image.Rectangle) image.Rectangle {
	return clampRect(r.Min.X+i.Left, r.Min.Y+i.Top, r.Max.X-i.Right, r.Max.Y-i.Bottom)
}

type Gaps struct {
	Inner int
	Outer Insets
}

type Spacing struct {
	TabBar int
	Gaps   Gaps
}

func clampRect(x0, y0, x1, y1 int) image.Rectangle {
	return image.Rectangle{Min: image.Pt(x0, y0), Max: image.Pt(max(x0, x1), max(y0, y1))}
}

func gapAround(i, n, inner int) (lead, trail int) {
	if i > 0 {
		lead = inner - inner/2
	}
	if i < n-1 {
		trail = inner / 2
	}
	return lead, trail
}

func ratioTotal(children []Child) float64 {
	total := 0.0
	for _, c := range children {
		total += c.Ratio
	}
	return total
}

func edgeAfter(lo, span, i int, acc, total float64, n int) int {
	if i == n-1 {
		return lo + span
	}
	return lo + int(math.Round(float64(span)*acc/total))
}

func firstWindow(n Node) WindowID {
	for {
		switch v := n.(type) {
		case Window:
			return v.ID
		case *Group:
			n = v.Children[v.Focus].Node
		}
	}
}

func appendChildRects(out []image.Rectangle, g *Group, r image.Rectangle, sp Spacing) []image.Rectangle {
	n, inner := len(g.Children), sp.Gaps.Inner
	if g.Layout == Tabbed {
		content := image.Rect(r.Min.X, r.Min.Y+min(sp.TabBar, r.Dy()), r.Max.X, r.Max.Y)
		for range n {
			out = append(out, content)
		}
		return out
	}
	lo, span := r.Min.X, r.Dx()
	if g.Layout == SplitV {
		lo, span = r.Min.Y, r.Dy()
	}
	total, acc, from := ratioTotal(g.Children), 0.0, lo
	for i, c := range g.Children {
		acc += c.Ratio
		to := edgeAfter(lo, span, i, acc, total, n)
		lead, trail := gapAround(i, n, inner)
		if g.Layout == SplitV {
			out = append(out, clampRect(r.Min.X, from+lead, r.Max.X, to-trail))
		} else {
			out = append(out, clampRect(from+lead, r.Min.Y, to-trail, r.Max.Y))
		}
		from = to
	}
	return out
}

func childRects(g *Group, r image.Rectangle, sp Spacing) []image.Rectangle {
	return appendChildRects(make([]image.Rectangle, 0, len(g.Children)), g, r, sp)
}

func gaplessChildRect(g *Group, r image.Rectangle, target int) image.Rectangle {
	if g.Layout == Tabbed {
		return r
	}
	lo, span := r.Min.X, r.Dx()
	if g.Layout == SplitV {
		lo, span = r.Min.Y, r.Dy()
	}
	n := len(g.Children)
	total, acc, from := ratioTotal(g.Children), 0.0, lo
	for i, c := range g.Children {
		acc += c.Ratio
		to := edgeAfter(lo, span, i, acc, total, n)
		if i == target {
			if g.Layout == SplitV {
				return clampRect(r.Min.X, from, r.Max.X, to)
			}
			return clampRect(from, r.Min.Y, to, r.Max.Y)
		}
		from = to
	}
	return image.Rectangle{}
}

func (t Tree) rectOf(area image.Rectangle, target *Group) image.Rectangle {
	var find func(g *Group, r image.Rectangle) (image.Rectangle, bool)
	find = func(g *Group, r image.Rectangle) (image.Rectangle, bool) {
		if g == target {
			return r, true
		}
		rects := childRects(g, r, Spacing{})
		for i, c := range g.Children {
			if sub, ok := c.Node.(*Group); ok {
				if found, ok := find(sub, rects[i]); ok {
					return found, true
				}
			}
		}
		return image.Rectangle{}, false
	}
	r, _ := find(t.root, area)
	return r
}

type Arranger struct {
	Arrangement
	rects []image.Rectangle
	tabs  []WindowID
}

func (x *Arranger) reset() {
	x.Windows, x.TabBars, x.rects, x.tabs = x.Windows[:0], x.TabBars[:0], x.rects[:0], x.tabs[:0]
}

func (x *Arranger) place(n Node, r image.Rectangle, visible bool, sp Spacing) {
	switch n := n.(type) {
	case Window:
		x.Windows = append(x.Windows, Placement{ID: n.ID, Rect: r, Visible: visible})
	case *Group:
		if n.Layout == Tabbed && visible {
			from := len(x.tabs)
			for _, c := range n.Children {
				x.tabs = append(x.tabs, firstWindow(c.Node))
			}
			x.TabBars = append(x.TabBars, TabBar{Rect: image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+min(sp.TabBar, r.Dy())), Active: n.Focus, Tabs: x.tabs[from:len(x.tabs):len(x.tabs)]})
		}
		base := len(x.rects)
		x.rects = appendChildRects(x.rects, n, r, sp)
		for i, c := range n.Children {
			x.place(c.Node, x.rects[base+i], visible && (n.Layout != Tabbed || i == n.Focus), sp)
		}
		x.rects = x.rects[:base]
	}
}

func (t Tree) arrangeInto(x *Arranger, area image.Rectangle, sp Spacing) {
	if t.root != nil {
		x.place(t.root, sp.Gaps.Outer.shrink(area), true, sp)
	}
}

func (t Tree) Arrange(area image.Rectangle, sp Spacing) Arrangement {
	var x Arranger
	t.arrangeInto(&x, area, sp)
	return x.Arrangement
}

func (a *Arrangement) inWorkspace(from int, id WorkspaceID) {
	for i := from; i < len(a.Windows); i++ {
		a.Windows[i].Workspace = id
	}
}
