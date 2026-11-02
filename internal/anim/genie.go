package anim

import (
	"image"
	"math"
)

const (
	GenieStrips  = 64
	genieOpening = 0.35
	genieLag     = 0.3
)

type GenieStrip struct {
	From, Size float64
	Rect       image.Rectangle
}

func (s GenieStrip) Source(size image.Point, alongX bool) (x, y, w, h float64) {
	if alongX {
		return s.From * float64(size.X), 0, s.Size * float64(size.X), float64(size.Y)
	}
	return 0, s.From * float64(size.Y), float64(size.X), s.Size * float64(size.Y)
}

func GenieAlongX(from, to image.Rectangle) bool {
	dx := math.Abs(float64(to.Min.X+to.Max.X-from.Min.X-from.Max.X)) / 2
	dy := math.Abs(float64(to.Min.Y+to.Max.Y-from.Min.Y-from.Max.Y)) / 2
	return dx > dy
}

type genieSpan struct {
	along0, along1, cross0, cross1 float64
}

func genieSpanOf(r image.Rectangle, alongX, reversed bool) genieSpan {
	s := genieSpan{float64(r.Min.Y), float64(r.Max.Y), float64(r.Min.X), float64(r.Max.X)}
	if alongX {
		s = genieSpan{float64(r.Min.X), float64(r.Max.X), float64(r.Min.Y), float64(r.Max.Y)}
	}
	if reversed {
		s.along0, s.along1 = -s.along1, -s.along0
	}
	return s
}

func smoothstep(t float64) float64 {
	t = min(max(t, 0), 1)
	return t * t * (3 - float64(2*t))
}

// GenieLayout cuts from into strips across the direction to to lies in and places
// them at progress p, 0 at from and 1 at to: the strips nearest to set off
// first, sliding down a funnel that narrows from from's width to to's.
func GenieLayout(out []GenieStrip, from, to image.Rectangle, p float64) (strips []GenieStrip, alongX bool) {
	p = min(max(p, 0), 1)
	alongX = GenieAlongX(from, to)
	reversed := !alongX && to.Min.Y+to.Max.Y < from.Min.Y+from.Max.Y || alongX && to.Min.X+to.Max.X < from.Min.X+from.Max.X
	a, b := genieSpanOf(from, alongX, reversed), genieSpanOf(to, alongX, reversed)
	opening := min(p/genieOpening, 1)
	place := func(v float64) float64 {
		travel := smoothstep((p - float64(genieLag*(1-v))) / (1 - genieLag))
		return Lerp(a.along0+float64((a.along1-a.along0)*v), b.along0+float64((b.along1-b.along0)*v), travel)
	}
	funnel := func(at float64) float64 {
		if b.along0 <= a.along0 {
			return p
		}
		return smoothstep((at - a.along0) / (b.along0 - a.along0))
	}
	cross := func(at float64) (float64, float64) {
		k := float64(opening * funnel(at))
		return Lerp(a.cross0, b.cross0, k), Lerp(a.cross1, b.cross1, k)
	}
	out = out[:0]
	for i := range GenieStrips {
		v0, v1 := float64(float64(i)/GenieStrips), float64(float64(i+1)/GenieStrips)
		at0, at1 := place(v0), place(v1)
		lo, hi := cross(float64((at0 + at1) / 2))
		r := image.Rect(int(math.Round(lo)), int(math.Round(at0)), int(math.Round(hi)), int(math.Round(at1)))
		if reversed {
			r = image.Rect(r.Min.X, -r.Max.Y, r.Max.X, -r.Min.Y)
		}
		if alongX {
			r = image.Rect(r.Min.Y, r.Min.X, r.Max.Y, r.Max.X)
		}
		src := v0
		if reversed {
			src = 1 - v1
		}
		out = append(out, GenieStrip{From: src, Size: v1 - v0, Rect: r})
	}
	return out, alongX
}
