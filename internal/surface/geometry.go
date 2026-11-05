package surface

import (
	"errors"
	"image"
	"math"

	"github.com/stubbedev/lys-wm/internal/render"
)

var (
	errInvalidSize = errors.New("buffer is not a multiple of its scale")
	errBadSize     = errors.New("viewport source is not integral and has no destination")
	errOutOfBuffer = errors.New("viewport source extends outside the buffer")
)

type geometry struct {
	buffer    Buffer
	scale     int32
	transform render.Transform
	source    opt[render.FRect]
	dest      opt[image.Point]
}

func (g geometry) unscaled() image.Point {
	if g.buffer == nil {
		return image.Point{}
	}
	size := g.transform.Size(g.buffer.Size())
	return image.Pt(size.X/int(g.scale), size.Y/int(g.scale))
}

const sourceTolerance = 1.0

func (g geometry) limit() (x, y float64) {
	size := g.transform.Size(g.buffer.Size())
	k := float64(g.scale)
	return float64(size.X) / k, float64(size.Y) / k
}

func clampAxis(start, length, limit float64) (float64, float64, bool) {
	end := start + length
	if start-limit > sourceTolerance || end-limit > sourceTolerance {
		return start, length, false
	}
	start, end = min(start, limit), min(end, limit)
	return start, end - start, end > start
}

func (g geometry) clampedSource(source render.FRect) (render.FRect, bool) {
	lx, ly := g.limit()
	x, w, okX := clampAxis(source.X, source.W, lx)
	y, h, okY := clampAxis(source.Y, source.H, ly)
	return render.FRect{X: x, Y: y, W: w, H: h}, okX && okY
}

func (g geometry) src() render.FRect {
	if g.buffer == nil {
		return render.FRect{}
	}
	if source, ok := g.source.get(); ok {
		clamped, _ := g.clampedSource(source)
		return clamped
	}
	lx, ly := g.limit()
	return render.FRect{W: lx, H: ly}
}

func (g geometry) size() image.Point {
	if g.buffer == nil {
		return image.Point{}
	}
	if dest, ok := g.dest.get(); ok {
		return dest
	}
	if source, ok := g.source.get(); ok {
		return image.Pt(int(source.W), int(source.H))
	}
	return g.unscaled()
}

func (g geometry) validate(anyScale bool) error {
	if g.buffer == nil {
		return nil
	}
	source, viewported := g.source.get()
	_, scaled := g.dest.get()
	size := g.transform.Size(g.buffer.Size())
	if !anyScale && !viewported && !scaled && (size.X%int(g.scale) != 0 || size.Y%int(g.scale) != 0) {
		return errInvalidSize
	}
	if !viewported {
		return nil
	}
	if !scaled && (source.W != math.Trunc(source.W) || source.H != math.Trunc(source.H)) {
		return errBadSize
	}
	if _, ok := g.clampedSource(source); !ok {
		return errOutOfBuffer
	}
	return nil
}

func (g geometry) bufferSource() render.FRect {
	if g.buffer == nil {
		return render.FRect{}
	}
	k := float64(g.scale)
	r := g.src()
	r = render.FRect{X: r.X * k, Y: r.Y * k, W: r.W * k, H: r.H * k}
	return g.transform.FRect(r, g.transform.Invert().Size(g.buffer.Size()))
}

func outward(x0, y0, x1, y1 float64) image.Rectangle {
	return image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1)))
}

func (g geometry) toSurface(r image.Rectangle) image.Rectangle {
	size := g.buffer.Size()
	r = g.transform.Invert().Rect(r.Intersect(image.Rectangle{Max: size}), size)
	k := float64(g.scale)
	src, dst := g.src(), g.size()
	if src.W <= 0 || src.H <= 0 {
		return image.Rectangle{}
	}
	sx, sy := float64(dst.X)/src.W, float64(dst.Y)/src.H
	out := outward((float64(r.Min.X)/k-src.X)*sx, (float64(r.Min.Y)/k-src.Y)*sy, (float64(r.Max.X)/k-src.X)*sx, (float64(r.Max.Y)/k-src.Y)*sy)
	return out.Intersect(image.Rectangle{Max: dst})
}

func (g geometry) toBuffer(r image.Rectangle) image.Rectangle {
	size := g.buffer.Size()
	k := float64(g.scale)
	src, dst := g.src(), g.size()
	if dst.X <= 0 || dst.Y <= 0 {
		return image.Rectangle{}
	}
	sx, sy := src.W/float64(dst.X), src.H/float64(dst.Y)
	scaled := outward((src.X+float64(float64(r.Min.X)*sx))*k, (src.Y+float64(float64(r.Min.Y)*sy))*k, (src.X+float64(float64(r.Max.X)*sx))*k, (src.Y+float64(float64(r.Max.Y)*sy))*k)
	return g.transform.Rect(scaled, g.transform.Invert().Size(size)).Intersect(image.Rectangle{Max: size})
}
