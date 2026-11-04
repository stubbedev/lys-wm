package region

import "image"

const tightUnionShare = 16

type Set struct {
	rects, pieces, scratch, kept, keptNext []image.Rectangle
	limit                                  int
}

func Capped(limit int) Set {
	return Set{limit: limit}
}

func (s *Set) Reset() {
	s.rects = s.rects[:0]
}

func (s *Set) Empty() bool {
	return len(s.rects) == 0
}

func (s *Set) Rects() []image.Rectangle {
	return s.rects
}

func (s *Set) Covers(r image.Rectangle) bool {
	return covers(s.rects, r)
}

func (s *Set) Area() int {
	return area(s.rects)
}

func (s *Set) Bounds() image.Rectangle {
	return bounds(s.rects)
}

func (s *Set) Add(r image.Rectangle) {
	from := len(s.rects)
	s.rects = appendUncovered(s.rects, r, &s.pieces, &s.scratch)
	if s.limit <= 0 {
		return
	}
	s.mergeTight(from)
	for len(s.rects) > s.limit {
		s.mergeCheapest()
	}
}

func (s *Set) AddSet(o *Set) {
	for _, r := range o.rects {
		s.Add(r)
	}
}

func (s *Set) AddRegion(g Region) {
	for _, r := range g.rects {
		s.Add(r)
	}
}

func (s *Set) CopyFrom(o *Set) {
	s.rects = append(s.rects[:0], o.rects...)
}

func (s *Set) Clip(bounds image.Rectangle) {
	s.rects = appendClipped(s.rects[:0], s.rects, bounds)
}

func (s *Set) AddExcept(r image.Rectangle, except []image.Rectangle) {
	s.kept = append(s.kept[:0], r)
	for _, e := range except {
		s.keptNext = s.keptNext[:0]
		for _, p := range s.kept {
			s.keptNext = minus(p, e, s.keptNext)
		}
		s.kept, s.keptNext = s.keptNext, s.kept
	}
	for _, p := range s.kept {
		s.Add(p)
	}
}

func addedByUnion(a, b, u image.Rectangle) int {
	return u.Dx()*u.Dy() - a.Dx()*a.Dy() - b.Dx()*b.Dy()
}

func (s *Set) mergeTight(from int) {
	for i := len(s.rects) - 1; i >= from && i >= 0; {
		j := s.tightPartner(i)
		if j < 0 {
			i--
			continue
		}
		s.merge(i, j)
		i = len(s.rects) - 1
		from = min(from, i)
	}
}

func (s *Set) tightPartner(i int) int {
	a := s.rects[i]
	for j, b := range s.rects {
		if j == i {
			continue
		}
		u := a.Union(b)
		if tightUnionShare*addedByUnion(a, b, u) <= u.Dx()*u.Dy() {
			return j
		}
	}
	return -1
}

func (s *Set) mergeCheapest() {
	bestI, bestJ, least := 0, 1, -1
	for i, a := range s.rects {
		for j, b := range s.rects[i+1:] {
			if added := addedByUnion(a, b, a.Union(b)); least < 0 || added < least {
				bestI, bestJ, least = i, i+1+j, added
			}
		}
	}
	s.merge(bestI, bestJ)
}

func (s *Set) merge(i, j int) {
	u := s.rects[i].Union(s.rects[j])
	s.swapRemove(max(i, j))
	s.swapRemove(min(i, j))
	for k := 0; k < len(s.rects); {
		if s.rects[k].Overlaps(u) {
			u = u.Union(s.rects[k])
			s.swapRemove(k)
			k = 0
			continue
		}
		k++
	}
	s.rects = append(s.rects, u)
}

func (s *Set) swapRemove(i int) {
	last := len(s.rects) - 1
	s.rects[i] = s.rects[last]
	s.rects = s.rects[:last]
}
