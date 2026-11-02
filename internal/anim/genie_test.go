package anim

import (
	"image"
	"testing"
)

func genieBounds(strips []GenieStrip) image.Rectangle {
	var r image.Rectangle
	for _, s := range strips {
		r = r.Union(s.Rect)
	}
	return r
}

func TestGenieRunsFromTheWindowToItsTarget(t *testing.T) {
	window := image.Rect(100, 100, 500, 400)
	for _, target := range []image.Rectangle{
		image.Rect(280, 700, 320, 720),
		image.Rect(280, 10, 320, 30),
		image.Rect(10, 240, 30, 260),
		image.Rect(900, 240, 920, 260),
	} {
		start, _ := GenieLayout(nil, window, target, 0)
		if got := genieBounds(start); got != window {
			t.Errorf("toward %v at 0 the strips cover %v, want the window", target, got)
		}
		for _, s := range start {
			if s.Rect.Empty() {
				t.Errorf("toward %v at 0 an empty strip %+v", target, s)
			}
		}
		end, _ := GenieLayout(nil, window, target, 1)
		if got := genieBounds(end); got != target {
			t.Errorf("toward %v at 1 the strips cover %v, want the target", target, got)
		}
	}
}

func TestGenieStripsSourceTheWholeWindowInOrder(t *testing.T) {
	window, target := image.Rect(0, 0, 400, 300), image.Rect(180, 0, 220, 20)
	strips, alongX := GenieLayout(nil, window, image.Rect(180, 700, 220, 720), 0.5)
	if alongX {
		t.Fatal("a target below travels along x")
	}
	at := 0.0
	for _, s := range strips {
		if s.From != at {
			t.Fatalf("strip starts at %v of the source, want %v", s.From, at)
		}
		at += s.Size
	}
	if at != 1 {
		t.Errorf("the strips source %v of the window", at)
	}
	up, _ := GenieLayout(nil, image.Rect(0, 400, 400, 700), target, 0.5)
	for i := 1; i < len(up); i++ {
		if up[i].Rect.Min.Y > up[i-1].Rect.Min.Y {
			t.Fatalf("strips fold over at %d: %v after %v", i, up[i].Rect, up[i-1].Rect)
		}
	}
}

func TestGenieNarrowsTowardTheTarget(t *testing.T) {
	window, target := image.Rect(100, 100, 500, 400), image.Rect(280, 700, 320, 720)
	strips, _ := GenieLayout(nil, window, target, 0.6)
	first, last := strips[0].Rect, strips[len(strips)-1].Rect
	if last.Dx() >= first.Dx() {
		t.Errorf("the strip nearest the target is %d wide, the farthest %d", last.Dx(), first.Dx())
	}
	if last.Min.Y-window.Max.Y <= first.Min.Y-window.Min.Y {
		t.Errorf("the strip nearest the target did not lead: first %v, last %v", first, last)
	}
}

func TestMinimizeEventInheritsAndTakesGenie(t *testing.T) {
	if s := (Table{}).Style(WindowsMinimize); s.Kind != Genie {
		t.Errorf("default style %v", s)
	}
	if s, err := ParseStyle("genie"); err != nil || s.Kind != Genie {
		t.Errorf("parse genie: %v, %v", s, err)
	}
	off := false
	table, errs := Resolve(true, map[Event]Setting{Windows: {Enabled: &off}})
	if len(errs) != 0 || table.Curve(WindowsMinimize) != Instant {
		t.Errorf("windows off leaves minimize at %v, errors %v", table.Curve(WindowsMinimize), errs)
	}
}
