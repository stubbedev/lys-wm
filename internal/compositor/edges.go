package compositor

import (
	"image"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type Corner uint8

const (
	TopLeft Corner = iota
	TopRight
	BottomLeft
	BottomRight
	CornerCount
)

var cornerNames = [CornerCount]string{TopLeft: "top_left", TopRight: "top_right", BottomLeft: "bottom_left", BottomRight: "bottom_right"}

func (k Corner) String() string {
	return cornerNames[k]
}

func (k Corner) right() bool {
	return k == TopRight || k == BottomRight
}

func (k Corner) bottom() bool {
	return k == BottomLeft || k == BottomRight
}

func (k Corner) pixel(r image.Rectangle) image.Point {
	p := r.Min
	if k.right() {
		p.X = r.Max.X - 1
	}
	if k.bottom() {
		p.Y = r.Max.Y - 1
	}
	return p
}

func (k Corner) free(s wm.SharedEdges) bool {
	sideways, vertical := s.Left, s.Top
	if k.right() {
		sideways = s.Right
	}
	if k.bottom() {
		vertical = s.Bottom
	}
	return !sideways && !vertical
}

func cornerAt(r image.Rectangle, p image.Point) (Corner, bool) {
	for k := range CornerCount {
		if k.pixel(r) == p {
			return k, true
		}
	}
	return 0, false
}

func (c *Compositor) sharedEdges(o *output.Output) wm.SharedEdges {
	r := o.Logical()
	var shared wm.SharedEdges
	for _, other := range c.outputs {
		n := other.Logical()
		if other == o {
			continue
		}
		overlapsY, overlapsX := n.Min.Y < r.Max.Y && r.Min.Y < n.Max.Y, n.Min.X < r.Max.X && r.Min.X < n.Max.X
		shared.Left = shared.Left || (n.Max.X == r.Min.X && overlapsY)
		shared.Right = shared.Right || (n.Min.X == r.Max.X && overlapsY)
		shared.Top = shared.Top || (n.Max.Y == r.Min.Y && overlapsX)
		shared.Bottom = shared.Bottom || (n.Min.Y == r.Max.Y && overlapsX)
	}
	return shared
}
