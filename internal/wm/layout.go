package wm

import (
	"image"
	"math"
)

type Placement struct {
	ID      WindowID
	Rect    image.Rectangle
	Visible bool
	Layer   PlacementLayer
}

type TabBar struct {
	Rect   image.Rectangle
	Tabs   []WindowID
	Active int
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

func edges(lo, span int, children []Child) []int {
	total := 0.0
	for _, c := range children {
		total += c.Ratio
	}
	out := make([]int, len(children)+1)
	out[0] = lo
	acc := 0.0
	for i, c := range children[:len(children)-1] {
		acc += c.Ratio
		out[i+1] = lo + int(math.Round(float64(span)*acc/total))
	}
	out[len(children)] = lo + span
	return out
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

func childRects(g *Group, r image.Rectangle, sp Spacing) []image.Rectangle {
	out := make([]image.Rectangle, len(g.Children))
	n, inner := len(g.Children), sp.Gaps.Inner
	switch g.Layout {
	case SplitH:
		e := edges(r.Min.X, r.Dx(), g.Children)
		for i := range out {
			lead, trail := gapAround(i, n, inner)
			out[i] = clampRect(e[i]+lead, r.Min.Y, e[i+1]-trail, r.Max.Y)
		}
	case SplitV:
		e := edges(r.Min.Y, r.Dy(), g.Children)
		for i := range out {
			lead, trail := gapAround(i, n, inner)
			out[i] = clampRect(r.Min.X, e[i]+lead, r.Max.X, e[i+1]-trail)
		}
	case Tabbed:
		content := image.Rect(r.Min.X, r.Min.Y+min(sp.TabBar, r.Dy()), r.Max.X, r.Max.Y)
		for i := range out {
			out[i] = content
		}
	}
	return out
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

func (t Tree) Arrange(area image.Rectangle, sp Spacing) Arrangement {
	var a Arrangement
	var place func(n Node, r image.Rectangle, visible bool)
	place = func(n Node, r image.Rectangle, visible bool) {
		switch n := n.(type) {
		case Window:
			a.Windows = append(a.Windows, Placement{ID: n.ID, Rect: r, Visible: visible})
		case *Group:
			if n.Layout == Tabbed && visible {
				tb := TabBar{Rect: image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+min(sp.TabBar, r.Dy())), Active: n.Focus}
				for _, c := range n.Children {
					tb.Tabs = append(tb.Tabs, firstWindow(c.Node))
				}
				a.TabBars = append(a.TabBars, tb)
			}
			rects := childRects(n, r, sp)
			for i, c := range n.Children {
				place(c.Node, rects[i], visible && (n.Layout != Tabbed || i == n.Focus))
			}
		}
	}
	if t.root != nil {
		place(t.root, sp.Gaps.Outer.shrink(area), true)
	}
	return a
}
