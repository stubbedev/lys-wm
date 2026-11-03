package region

import (
	"image"
	"slices"
)

const occluderSlots = 6

type Occluders struct {
	rects   [occluderSlots]image.Rectangle
	n       int
	scratch []image.Rectangle
}

func (o *Occluders) Reset() {
	o.n = 0
}

func (o *Occluders) Add(r image.Rectangle) {
	if r.Empty() {
		return
	}
	smallest, least := 0, -1
	kept := 0
	for _, have := range o.rects[:o.n] {
		if r.In(have) {
			return
		}
		if have.In(r) {
			continue
		}
		o.rects[kept] = have
		if a := have.Dx() * have.Dy(); least < 0 || a < least {
			smallest, least = kept, a
		}
		kept++
	}
	o.n = kept
	switch {
	case o.n < occluderSlots:
		o.rects[o.n] = r
		o.n++
	case r.Dx()*r.Dy() > least:
		o.rects[smallest] = r
	}
}

func (o *Occluders) Hides(r image.Rectangle) bool {
	return slices.ContainsFunc(o.rects[:o.n], r.In)
}

func (o *Occluders) Visible(r image.Rectangle, out []image.Rectangle) []image.Rectangle {
	start := len(out)
	out = append(out, r)
	for _, have := range o.rects[:o.n] {
		if !have.Overlaps(r) {
			continue
		}
		o.scratch = o.scratch[:0]
		for _, p := range out[start:] {
			o.scratch = minus(p, have, o.scratch)
		}
		out = append(out[:start], o.scratch...)
		if len(out) == start {
			return out
		}
	}
	return out
}
