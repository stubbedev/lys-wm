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
	Groups    []image.Rectangle
}

const (
	overviewStripShare     = 7
	overviewMinimizedShare = 8
	overviewMarginShare    = 40
	overviewMinMargin      = 8
)

func PlanOverview(full image.Rectangle, spaces int, windows, minimized []image.Rectangle) OverviewPlan {
	return PlanOverviewGroups(full, spaces, [][]image.Rectangle{windows}, 0, minimized)
}

func PlanOverviewGroups(full image.Rectangle, spaces int, groups [][]image.Rectangle, label int, minimized []image.Rectangle) OverviewPlan {
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
	plan.Groups = bands(area, groups, margin)
	for i, g := range groups {
		band := plan.Groups[i]
		band.Min.Y = min(band.Min.Y+label, band.Max.Y)
		plan.Windows = append(plan.Windows, packWindows(band, g, margin)...)
	}
	return plan
}

func bands(area image.Rectangle, groups [][]image.Rectangle, gap int) []image.Rectangle {
	out := make([]image.Rectangle, len(groups))
	if len(groups) == 0 {
		return out
	}
	weights, sum := make([]int, len(groups)), 0
	for i, g := range groups {
		weights[i] = rowsFor(len(g))
		sum += weights[i]
	}
	total := max(area.Dy()-gap*(len(groups)-1), 0)
	y := area.Min.Y
	for i, w := range weights {
		h := total * w / sum
		if i == len(groups)-1 {
			h = area.Max.Y - y
		}
		out[i] = image.Rect(area.Min.X, y, area.Max.X, y+max(h, 0))
		y += h + gap
	}
	return out
}

func rowsFor(n int) int {
	rows := 1
	for rows*rows < n {
		rows++
	}
	return rows
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
	out := make([]image.Rectangle, len(rects))
	order := make([]int, len(rects))
	rows := make([][]int, 0, len(rects))
	bestScale, bestRows := -1.0, 0
	for n := 1; n <= len(rects); n++ {
		rows = rowsOf(rows[:0], order, rects, n)
		if s := scaleFor(area, rects, rows, gap); s > bestScale {
			bestScale, bestRows = s, n
		}
	}
	if bestRows > 0 {
		place(out, area, rects, rowsOf(rows[:0], order, rects, bestRows), gap, bestScale)
	}
	return out
}

func packRows(area image.Rectangle, rects []image.Rectangle, gap, n int) []image.Rectangle {
	out := make([]image.Rectangle, len(rects))
	rows := rowsOf(make([][]int, 0, n), make([]int, len(rects)), rects, n)
	place(out, area, rects, rows, gap, scaleFor(area, rects, rows, gap))
	return out
}

func centreOf(r image.Rectangle) image.Point {
	return image.Pt(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
}

func byHeight(rects []image.Rectangle) func(a, b int) int {
	return func(a, b int) int {
		ca, cb := centreOf(rects[a]), centreOf(rects[b])
		if ca.Y != cb.Y {
			return ca.Y - cb.Y
		}
		return ca.X - cb.X
	}
}

func rowsOf(rows [][]int, order []int, rects []image.Rectangle, n int) [][]int {
	total := 0
	for i, r := range rects {
		order[i] = i
		total += max(r.Dx(), 1)
	}
	slices.SortStableFunc(order, byHeight(rects))
	start, width := 0, 0
	target := total / max(n, 1)
	for at, i := range order {
		width += max(rects[i].Dx(), 1)
		left := len(order) - at - 1
		if len(rows) < n-1 && (width >= target || left == n-1-len(rows)) {
			rows = append(rows, order[start:at+1])
			start, width = at+1, 0
		}
	}
	if start < len(order) {
		rows = append(rows, order[start:])
	}
	for _, row := range rows {
		slices.SortStableFunc(row, func(a, b int) int { return centreOf(rects[a]).X - centreOf(rects[b]).X })
	}
	return rows
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

func rowHeight(rects []image.Rectangle, row []int, s float64) int {
	h := 0
	for _, i := range row {
		h = max(h, scaled(rects[i].Dy(), s))
	}
	return h
}

func place(out []image.Rectangle, area image.Rectangle, rects []image.Rectangle, rows [][]int, gap int, s float64) {
	total := gap * (len(rows) - 1)
	for _, row := range rows {
		total += rowHeight(rects, row, s)
	}
	y := area.Min.Y + (area.Dy()-total)/2
	for _, row := range rows {
		height := rowHeight(rects, row, s)
		width := gap * (len(row) - 1)
		for _, i := range row {
			width += scaled(rects[i].Dx(), s)
		}
		x := area.Min.X + (area.Dx()-width)/2
		for _, i := range row {
			w, h := scaled(rects[i].Dx(), s), scaled(rects[i].Dy(), s)
			top := y + (height-h)/2
			out[i] = image.Rect(x, top, x+w, top+h)
			x += w + gap
		}
		y += height + gap
	}
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
	var a Arranger
	placeWorkspace(&a, w, o, true, sp)
	return a.Windows
}

func (s State) UnusedWorkspace() WorkspaceID {
	return s.firstUnused()
}
