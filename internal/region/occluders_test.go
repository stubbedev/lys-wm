package region

import (
	"image"
	"testing"
)

func TestOccludersKeepTheLargest(t *testing.T) {
	var o Occluders
	for i := range occluderSlots + 4 {
		o.Add(image.Rect(i*1000, 0, i*1000+10+i, 10))
	}
	if o.n != occluderSlots {
		t.Fatalf("%d rectangles kept", o.n)
	}
	if !o.Hides(image.Rect((occluderSlots+3)*1000, 0, (occluderSlots+3)*1000+5, 5)) {
		t.Error("the largest rectangle was not kept")
	}
	if o.Hides(image.Rect(0, 0, 5, 5)) {
		t.Error("the smallest rectangle was kept over larger ones")
	}
}

func TestOccludersDropWhatAnotherContains(t *testing.T) {
	var o Occluders
	o.Add(image.Rect(10, 10, 20, 20))
	o.Add(image.Rect(0, 0, 100, 100))
	o.Add(image.Rect(30, 30, 40, 40))
	if o.n != 1 {
		t.Fatalf("%d rectangles kept: %v", o.n, o.rects[:o.n])
	}
}

func TestOccludersGiveWhatShows(t *testing.T) {
	var o Occluders
	o.Add(image.Rect(0, 0, 50, 100))
	o.Add(image.Rect(50, 0, 100, 50))
	got := o.Visible(image.Rect(0, 0, 100, 100), nil)
	if len(got) != 1 || got[0] != image.Rect(50, 50, 100, 100) {
		t.Fatalf("visible %v", got)
	}
	if len(o.Visible(image.Rect(10, 10, 90, 40), nil)) != 0 {
		t.Fatal("a rectangle hidden by two occluders together shows")
	}
}
