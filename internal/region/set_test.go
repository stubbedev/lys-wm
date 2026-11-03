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

func TestACappedSetKeepsRectsUpToItsLimitAndThenTheirBounds(t *testing.T) {
	s := Capped(3)
	for i := range 3 {
		s.Add(image.Rect(i*10, 0, i*10+5, 5))
	}
	if len(s.Rects()) != 3 {
		t.Fatalf("at the limit: %v", s.Rects())
	}
	s.Add(image.Rect(30, 0, 35, 5))
	if got, want := s.Rects(), image.Rect(0, 0, 35, 5); len(got) != 1 || got[0] != want {
		t.Fatalf("over the limit: %v, want %v", got, want)
	}
	s.Reset()
	s.Add(image.Rect(0, 0, 1, 1))
	s.Add(image.Rect(2, 0, 3, 1))
	if len(s.Rects()) != 2 {
		t.Fatalf("a reset set forgot its rects or its limit: %v", s.Rects())
	}
}
