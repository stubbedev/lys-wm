package region

import (
	"image"
	"testing"
)

func TestSetMatchesRegionAndReusesItsBuffers(t *testing.T) {
	rects := []image.Rectangle{
		image.Rect(0, 0, 10, 10), image.Rect(5, 5, 15, 15), image.Rect(20, 0, 30, 5),
		image.Rect(2, 2, 4, 4), image.Rect(8, 0, 22, 3), image.Rect(9, 9, 9, 20),
	}
	var s Set
	var g Region
	for _, r := range rects {
		s.Add(r)
		g = g.Add(r)
	}
	if s.Area() != g.Area() {
		t.Fatalf("set area %d, region area %d", s.Area(), g.Area())
	}
	for i, a := range s.Rects() {
		for _, b := range s.Rects()[i+1:] {
			if a.Overlaps(b) {
				t.Fatalf("%v and %v overlap", a, b)
			}
		}
	}
	for y := range 20 {
		for x := range 32 {
			p := image.Pt(x, y)
			in := false
			for _, r := range s.Rects() {
				in = in || p.In(r)
			}
			if in != g.Contains(p) {
				t.Fatalf("%v: set %v, region %v", p, in, g.Contains(p))
			}
		}
	}
	s.Clip(image.Rect(0, 0, 12, 12))
	if s.Area() != g.Intersect(image.Rect(0, 0, 12, 12)).Area() {
		t.Fatalf("clipped area %d", s.Area())
	}
	var copied Set
	copied.CopyFrom(&s)
	if copied.Area() != s.Area() {
		t.Fatal("CopyFrom lost rectangles")
	}
	allocs := testing.AllocsPerRun(100, func() {
		s.Reset()
		for _, r := range rects {
			s.Add(r)
		}
		s.Clip(image.Rect(0, 0, 25, 25))
		copied.CopyFrom(&s)
		copied.AddSet(&s)
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per reuse", allocs)
	}
}

func checkDisjoint(t *testing.T, rects []image.Rectangle) {
	t.Helper()
	for i, a := range rects {
		for _, b := range rects[i+1:] {
			if a.Overlaps(b) {
				t.Fatalf("%v and %v overlap", a, b)
			}
		}
	}
}

func TestACappedSetNeverPassesItsLimitAndMergesWhatCostsLeast(t *testing.T) {
	s := Capped(3)
	for i := range 3 {
		s.Add(image.Rect(i*100, 0, i*100+5, 5))
	}
	if len(s.Rects()) != 3 {
		t.Fatalf("at the limit: %v", s.Rects())
	}
	s.Add(image.Rect(215, 0, 220, 5))
	if len(s.Rects()) > 3 {
		t.Fatalf("over the limit: %v", s.Rects())
	}
	if !s.Covers(image.Rect(215, 0, 220, 5)) || !s.Covers(image.Rect(0, 0, 5, 5)) {
		t.Fatalf("merging lost a rect: %v", s.Rects())
	}
	if got := s.Area(); got > 150 {
		t.Fatalf("area %d after the cheapest merge, want the two neighbours merged: %v", got, s.Rects())
	}
	s.Reset()
	s.Add(image.Rect(0, 0, 1, 1))
	s.Add(image.Rect(200, 0, 201, 1))
	if len(s.Rects()) != 2 {
		t.Fatalf("a reset set forgot its rects or its limit: %v", s.Rects())
	}
}

func TestScatteredRectsOverACappedSetsLimitStayClose(t *testing.T) {
	const limit, n = 32, 33
	s := Capped(limit)
	truth := 0
	for i := range n {
		x, y := 10+i*220, 10+(i%4)*500
		s.Add(image.Rect(x%3600, y, x%3600+50, y+20))
		truth += 50 * 20
		checkDisjoint(t, s.Rects())
		if len(s.Rects()) > limit {
			t.Fatalf("after %d: %d rects", i+1, len(s.Rects()))
		}
	}
	if got := s.Area(); got > 2*truth {
		t.Fatalf("%d scattered rects: area %d, true %d", n, got, truth)
	}
}

func TestACappedSetMergesATightUnion(t *testing.T) {
	s := Capped(32)
	s.Add(image.Rect(0, 0, 100, 100))
	s.Add(image.Rect(100, 0, 200, 100))
	s.Add(image.Rect(3, 2, 103, 102))
	checkDisjoint(t, s.Rects())
	if len(s.Rects()) > 2 {
		t.Fatalf("a window moved 3px left %d rects: %v", len(s.Rects()), s.Rects())
	}
}

func TestAddExceptAddsOnlyWhatIsOutside(t *testing.T) {
	s := Set{}
	s.AddExcept(image.Rect(0, 0, 10, 10), []image.Rectangle{image.Rect(0, 0, 10, 5), image.Rect(0, 5, 5, 10)})
	if got := s.Area(); got != 25 {
		t.Fatalf("area %d: %v", got, s.Rects())
	}
	allocs := testing.AllocsPerRun(100, func() {
		s.Reset()
		s.AddExcept(image.Rect(0, 0, 10, 10), []image.Rectangle{image.Rect(2, 2, 4, 4)})
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per reuse", allocs)
	}
}

func TestACappedSetAddsWithoutAllocating(t *testing.T) {
	s := Capped(8)
	add := func() {
		s.Reset()
		for i := range 20 {
			s.Add(image.Rect(i*50, i%3*50, i*50+20, i%3*50+20))
		}
	}
	add()
	if allocs := testing.AllocsPerRun(100, add); allocs != 0 {
		t.Fatalf("%v allocations per reuse", allocs)
	}
}
