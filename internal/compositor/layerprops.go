package compositor

import (
	"fmt"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

func alphaShape(format render.Format, size image.Point, data []byte, stride int, threshold float32, into []image.Rectangle) []image.Rectangle {
	into = into[:0]
	if format.Opaque() {
		return append(into, image.Rectangle{Max: size})
	}
	cut := uint8(math.Floor(float64(threshold) * 0xff))
	open, openLen := 0, -1
	for y := range size.Y {
		row := data[y*stride:]
		from := len(into)
		start := -1
		for x := 0; x <= size.X; x++ {
			above := x < size.X && format.Decode(row[x*4:x*4+4]).A > cut
			switch {
			case above && start < 0:
				start = x
			case !above && start >= 0:
				into = append(into, image.Rect(start, y, x, y+1))
				start = -1
			}
		}
		cur := into[from:]
		if len(cur) == openLen && slices.EqualFunc(into[open:open+openLen], cur, func(a, b image.Rectangle) bool { return a.Min.X == b.Min.X && a.Max.X == b.Max.X }) {
			for i := range openLen {
				into[open+i].Max.Y = y + 1
			}
			into = into[:from]
			continue
		}
		open, openLen = from, len(cur)
	}
	return into
}

func (c *Compositor) noteAlphaShape(s *surface.Surface, format render.Format, size image.Point, data []byte, stride int) {
	e := c.layerWithSurface(s)
	if e == nil {
		return
	}
	threshold := c.layerProps(e).BlurAlpha
	if threshold == nil || !c.blursLayer(e) {
		delete(c.alphaShapes, s)
		return
	}
	shape := alphaShape(format, size, data, stride, *threshold, c.alphaShapes[s].rects)
	c.alphaShapes[s] = bufferShape{rects: shape, size: size}
}

type bufferShape struct {
	rects []image.Rectangle
	size  image.Point
}

func (c *Compositor) layerBlurShape(o *output.Output, e *layer, under image.Rectangle) []image.Rectangle {
	sc := c.sceneOf(o)
	if c.layerProps(e).BlurAlpha == nil {
		return sc.blurs.one(under)
	}
	bs, ok := c.alphaShapes[e.surface.Surface()]
	if !ok || bs.size.X <= 0 || bs.size.Y <= 0 {
		if !e.shapeless {
			e.shapeless = true
			c.report(fmt.Errorf("compositor: blur_alpha_threshold of the %q layer surface needs a shared-memory buffer to read alpha from; it draws no blur", e.surface.Namespace()))
		}
		return nil
	}
	e.shapeless = false
	sc.blurs.shape = sc.blurs.shape[:0]
	for _, r := range bs.rects {
		sc.blurs.shape = append(sc.blurs.shape, image.Rect(
			under.Min.X+r.Min.X*under.Dx()/bs.size.X, under.Min.Y+r.Min.Y*under.Dy()/bs.size.Y,
			under.Min.X+r.Max.X*under.Dx()/bs.size.X, under.Min.Y+r.Max.Y*under.Dy()/bs.size.Y))
	}
	return sc.blurs.shape
}

func (c *Compositor) layerStyle(l *layer, e anim.Event) (anim.Style, bool) {
	a := c.layerProps(l).Animation
	switch {
	case a == nil:
	case a.Off:
		return anim.Style{}, false
	case e == anim.LayersIn && a.In.Kind != anim.NoStyle:
		return a.In, true
	case e == anim.LayersOut && a.Out.Kind != anim.NoStyle:
		return a.Out, true
	}
	return c.animations.Style(e), true
}
