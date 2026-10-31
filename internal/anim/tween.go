package anim

import (
	"image"
	"math"
	"time"
)

type Tween[V any] struct {
	from, to V
	start    time.Duration
	curve    Curve
	lerp     func(from, to V, p float64) V
}

func NewTween[V any](at V, lerp func(from, to V, p float64) V) Tween[V] {
	return Tween[V]{from: at, to: at, lerp: lerp}
}

func (t Tween[V]) At(now time.Duration) V {
	if t.Done(now) {
		return t.to
	}
	return t.lerp(t.from, t.to, t.curve.At(now-t.start))
}

func (t Tween[V]) Done(now time.Duration) bool {
	if t.curve == nil {
		return true
	}
	d := t.curve.Duration()
	return d == 0 || now-t.start >= d
}

func (t Tween[V]) Target() V {
	return t.to
}

func (t Tween[V]) Source() V {
	return t.from
}

func (t *Tween[V]) Set(to V, now time.Duration, c Curve) {
	t.from, t.to, t.start, t.curve = t.At(now), to, now, c
}

func (t *Tween[V]) Jump(to V) {
	t.from, t.to, t.curve = to, to, nil
}

func Lerp(from, to, p float64) float64 {
	return from + (to-from)*p
}

func lerpInt(from, to int, p float64) int {
	return int(math.Round(Lerp(float64(from), float64(to), p)))
}

func LerpColor(from, to [4]uint8, p float64) [4]uint8 {
	var out [4]uint8
	for i := range out {
		out[i] = uint8(min(max(math.Round(Lerp(float64(from[i]), float64(to[i]), p)), 0), 255))
	}
	return out
}

func LerpRect(from, to image.Rectangle, p float64) image.Rectangle {
	x0, y0 := lerpInt(from.Min.X, to.Min.X, p), lerpInt(from.Min.Y, to.Min.Y, p)
	x1, y1 := lerpInt(from.Max.X, to.Max.X, p), lerpInt(from.Max.Y, to.Max.Y, p)
	return image.Rect(x0, y0, max(x0, x1), max(y0, y1))
}
