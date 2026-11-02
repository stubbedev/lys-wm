package wm

import (
	"image"
	"slices"
)

type OverviewPlan struct {
	Strip     []image.Rectangle
	Plus      image.Rectangle
	Windows   []image.Rectangle
	Minimized []image.Rectangle
}

const (
	overviewStripShare     = 7
	overviewMinimizedShare = 8
	overviewMarginShare    = 40
	overviewMinMargin      = 8
)

func PlanOverview(full image.Rectangle, spaces int, windows, minimized []image.Rectangle) OverviewPlan {
	margin := max(full.Dy()/overviewMarginShare, overviewMinMargin)
	inner := full.Inset(margin)
	stripHeight := min(full.Dy()/overviewStripShare, inner.Dy())
	strip, plus := planStrip(image.Rect(inner.Min.X, inner.Min.Y, inner.Max.X, inner.Min.Y+stripHeight), spaces, full.Size(), margin)
	area := inner
	area.Min.Y += stripHeight + margin
	plan := OverviewPlan{Strip: strip, Plus: plus}
	if len(minimized) > 0 {
		row := image.Rect(inner.Min.X, inner.Max.Y-full.Dy()/overviewMinimizedShare, inner.Max.X, inner.Max.Y)
		plan.Minimized = packRows(row, minimized, margin, 1)
		area.Max.Y = row.Min.Y - margin
	}
	plan.Windows = packWindows(area, windows, margin)
	return plan
}

func planStrip(row image.Rectangle, spaces int, screen image.Point, gap int) ([]image.Rectangle, image.Rectangle) {
	if row.Empty() || screen.X <= 0 || screen.Y <= 0 {
		return make([]image.Rectangle, spaces), image.Rectangle{}
	}
	h := row.Dy()
	w := max(h*screen.X/screen.Y, 1)
	plusW := max(h*3/4, 1)
	room := row.Dx() - gap*spaces
	if total := w*spaces + plusW; total > room && room > 0 {
		w = max(w*room/total, 1)
		plusW = max(plusW*room/total, 1)
		h = max(w*screen.Y/screen.X, 1)
	}
	x := row.Min.X + (row.Dx()-w*spaces-plusW-gap*spaces)/2
	y := row.Min.Y + (row.Dy()-h)/2
	strip := make([]image.Rectangle, spaces)
	for i := range strip {
		strip[i] = image.Rect(x, y, x+w, y+h)
		x += w + gap
	}
	return strip, image.Rect(x, y, x+plusW, y+h)
}

func packWindows(area image.Rectangle, rects []image.Rectangle, gap int) []image.Rectangle {
	var best []image.Rectangle
	bestScale := -1.0
	for rows := 1; rows <= len(rects); rows++ {
		order := rowsOf(rects, rows)
		if s := scaleFor(area, rects, order, gap); s > bestScale {
			bestScale, best = s, place(area, rects, order, gap, s)
		}
	}
	return best
}

func packRows(area image.Rectangle, rects []image.Rectangle, gap, rows int) []image.Rectangle {
	order := rowsOf(rects, rows)
	return place(area, rects, order, gap, scaleFor(area, rects, order, gap))
}

func centreOf(r image.Rectangle) image.Point {
	return image.Pt(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
}

func rowsOf(rects []image.Rectangle, rows int) [][]int {
	byHeight := make([]int, len(rects))
	total := 0
	for i, r := range rects {
		byHeight[i] = i
		total += max(r.Dx(), 1)
	}
	slices.SortStableFunc(byHeight, func(a, b int) int {
		ca, cb := centreOf(rects[a]), centreOf(rects[b])
		if ca.Y != cb.Y {
			return ca.Y - cb.Y
		}
		return ca.X - cb.X
	})
	out := make([][]int, 0, rows)
	start, width := 0, 0
	target := total / max(rows, 1)
	for n, i := range byHeight {
		width += max(rects[i].Dx(), 1)
		left := len(byHeight) - n - 1
		if len(out) < rows-1 && (width >= target || left == rows-1-len(out)) {
			out = append(out, slices.Clone(byHeight[start:n+1]))
			start, width = n+1, 0
		}
	}
	if start < len(byHeight) {
		out = append(out, slices.Clone(byHeight[start:]))
	}
	for _, row := range out {
		slices.SortStableFunc(row, func(a, b int) int { return centreOf(rects[a]).X - centreOf(rects[b]).X })
	}
	return out
}

func scaleFor(area image.Rectangle, rects []image.Rectangle, rows [][]int, gap int) float64 {
	scale := 1.0
	heights := 0
	for _, row := range rows {
		width, height := 0, 0
		for _, i := range row {
			width += max(rects[i].Dx(), 1)
			height = max(height, max(rects[i].Dy(), 1))
		}
		heights += height
		scale = min(scale, float64(area.Dx()-gap*(len(row)-1))/float64(width))
	}
	if heights == 0 {
		return scale
	}
	return max(min(scale, float64(area.Dy()-gap*(len(rows)-1))/float64(heights)), 0)
}

func scaled(v int, s float64) int {
	return max(int(float64(max(v, 1))*s), 1)
}

func place(area image.Rectangle, rects []image.Rectangle, rows [][]int, gap int, s float64) []image.Rectangle {
	out := make([]image.Rectangle, len(rects))
	total := gap * (len(rows) - 1)
	heights := make([]int, len(rows))
	for r, row := range rows {
		for _, i := range row {
			heights[r] = max(heights[r], scaled(rects[i].Dy(), s))
		}
		total += heights[r]
	}
	y := area.Min.Y + (area.Dy()-total)/2
	for r, row := range rows {
		width := gap * (len(row) - 1)
		for _, i := range row {
			width += scaled(rects[i].Dx(), s)
		}
		x := area.Min.X + (area.Dx()-width)/2
		for _, i := range row {
			w, h := scaled(rects[i].Dx(), s), scaled(rects[i].Dy(), s)
			top := y + (heights[r]-h)/2
			out[i] = image.Rect(x, top, x+w, top+h)
			x += w + gap
		}
		y += heights[r] + gap
	}
	return out
}

func OverviewNeighbour(rects []image.Rectangle, from int, d Direction) (int, bool) {
	c := centreOf(rects[from])
	best, bestCost := 0, 0
	found := false
	for i, r := range rects {
		if i == from {
			continue
		}
		along, across := reach(c, centreOf(r), d)
		if along <= 0 {
			continue
		}
		if cost := along + 2*across; !found || cost < bestCost {
			best, bestCost, found = i, cost, true
		}
	}
	return best, found
}

func reach(from, to image.Point, d Direction) (along, across int) {
	delta := to.Sub(from)
	if d.vertical() {
		along, across = delta.Y, delta.X
	} else {
		along, across = delta.X, delta.Y
	}
	if !d.forward() {
		along = -along
	}
	return along, max(across, -across)
}

func (s State) WorkspacePlacements(id WorkspaceID, sp Spacing) []Placement {
	w := s.workspace(id)
	if w == nil {
		return nil
	}
	o := s.output(w.Output)
	if o == nil {
		return nil
	}
	var a Arrangement
	placeWorkspace(&a, w, o, true, sp)
	return a.Windows
}

func (s State) UnusedWorkspace() WorkspaceID {
	return s.firstUnused()
}
