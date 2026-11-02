package compositor

import (
	"image"
	"math"
	"slices"
	"unique"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

const maxBufferAge = 4

type drawOp struct {
	fill      bool
	color     render.Color
	tex       render.Texture
	src       render.FRect
	dst       image.Rectangle
	transform render.Transform
	alpha     float32
	surface   *surface.Surface
	size      image.Point
	opaque    bool
	mask      render.Mask
	gradient  render.Gradient
	goo       render.Goo
	version   uint64
	space     *colorspace.Description
	glass     unique.Handle[render.Glass]
	pending   int32
	whole     image.Rectangle
}

const (
	clipMinPixels   = 128 * 128
	clipMaxPieces   = 24
	clipPiecePixels = 16384
)

func (op drawOp) clippable() bool {
	if op.dst.Dx()*op.dst.Dy() < clipMinPixels || !op.goo.A.Rect.Empty() {
		return false
	}
	if op.glass != (unique.Handle[render.Glass]{}) && op.glass.Value().Refracts() {
		return false
	}
	return op.fill || op.transform == render.Normal
}

func (op drawOp) piece(p image.Rectangle) drawOp {
	if !op.fill {
		x, w := subSpan(op.src.X, op.src.W, p.Min.X-op.dst.Min.X, p.Dx(), op.dst.Dx())
		y, h := subSpan(op.src.Y, op.src.H, p.Min.Y-op.dst.Min.Y, p.Dy(), op.dst.Dy())
		op.src = render.FRect{X: x, Y: y, W: w, H: h}
	}
	if op.whole.Empty() {
		op.whole = op.dst
	}
	op.dst = p
	return op
}

func subSpan(from, extent float64, at, n, whole int) (start, length float64) {
	k := extent / float64(whole)
	start = from + float64(float64(at)*k)
	end := from + extent
	if at+n < whole {
		end = from + float64(float64(at+n)*k)
	}
	length = end - start
	for start+length > end {
		length = math.Nextafter(length, 0)
	}
	return start, length
}

func (op drawOp) mapped() image.Rectangle {
	if op.whole.Empty() {
		return op.dst
	}
	return op.whole
}

func worthClipping(pieces []image.Rectangle, whole image.Rectangle) bool {
	if len(pieces) == 0 || len(pieces) > clipMaxPieces {
		return false
	}
	left := 0
	for _, p := range pieces {
		left += p.Dx() * p.Dy()
	}
	saved := whole.Dx()*whole.Dy() - left
	return 4*saved >= whole.Dx()*whole.Dy() && saved >= clipPiecePixels*len(pieces)
}

func (op drawOp) covers() bool {
	return op.opaque && op.mask == (render.Mask{})
}

func (op drawOp) solid(add func(image.Rectangle)) {
	switch {
	case op.covers():
		add(op.dst)
	case op.opaque && op.mask.Cut.Rect.Empty() && !op.mask.Keep.Rect.Empty():
		k := op.mask.Keep
		r := int(math.Ceil(float64(k.Radius)))
		add(image.Rect(k.Rect.Min.X+r, k.Rect.Min.Y, k.Rect.Max.X-r, k.Rect.Max.Y).Intersect(op.dst))
		add(image.Rect(k.Rect.Min.X, k.Rect.Min.Y+r, k.Rect.Max.X, k.Rect.Max.Y-r).Intersect(op.dst))
	}
}

func mapGradient(g render.Gradient, at func([2]float32) [2]float32) render.Gradient {
	if g.Stops != nil {
		g.From, g.To = at(g.From), at(g.To)
	}
	return g
}

func shiftMask(m render.Mask, d image.Point) render.Mask {
	if !m.Keep.Rect.Empty() {
		m.Keep.Rect = m.Keep.Rect.Sub(d)
	}
	if !m.Cut.Rect.Empty() {
		m.Cut.Rect = m.Cut.Rect.Sub(d)
	}
	if !m.Merge.Empty() {
		m.Merge = m.Merge.Sub(d)
	}
	return m
}

type frame struct {
	ops        []drawOp
	cursorFrom int
	background int
	sharedBlur bool
	blurs      []pendingBlur
	below      []drawOp
	rims       []rim
	clipBack   []drawOp
	clipOut    []drawOp
	clipPieces []image.Rectangle
}

func (f *frame) FillRect(r image.Rectangle, c render.Color) {
	f.ops = append(f.ops, drawOp{fill: true, color: c, dst: r, opaque: c.A == 0xff})
}

func (f *frame) DrawTexture(t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform, alpha float32) {
	f.ops = append(f.ops, drawOp{tex: t, src: src, dst: dst, transform: transform, alpha: alpha})
}

func (f *frame) drawMasked(t render.Texture, src render.FRect, dst image.Rectangle, m render.Mask) {
	f.ops = append(f.ops, drawOp{tex: t, src: src, dst: dst, transform: render.Normal, alpha: 1, mask: m})
}

func (f *frame) fillMasked(r image.Rectangle, c render.Color, m render.Mask) {
	f.ops = append(f.ops, drawOp{fill: true, color: c, dst: r, opaque: c.A == 0xff, mask: m})
}

func (f *frame) fillGradient(r image.Rectangle, g render.Gradient, m render.Mask) {
	f.ops = append(f.ops, drawOp{fill: true, gradient: g, dst: r, mask: m})
}

func (f *frame) fillGoo(r image.Rectangle, c render.Color, g render.Goo) {
	f.ops = append(f.ops, drawOp{fill: true, color: c, dst: r, goo: g})
}

func (f *frame) drawSurface(s *surface.Surface, space *colorspace.Description, t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform, alpha float32, opaque bool, m render.Mask) {
	f.ops = append(f.ops, drawOp{tex: t, src: src, dst: dst, transform: transform, alpha: alpha, surface: s, size: s.Size(), opaque: opaque && alpha == 1, mask: m, space: space})
}

func mapRect(r, src, dst image.Rectangle) image.Rectangle {
	if r.Empty() {
		return r
	}
	axis := func(v, s0, sn, d0, dn int) int {
		return d0 + int(math.Round(float64(v-s0)*float64(dn)/float64(sn)))
	}
	at := func(p image.Point) image.Point {
		return image.Pt(axis(p.X, src.Min.X, src.Dx(), dst.Min.X, dst.Dx()), axis(p.Y, src.Min.Y, src.Dy(), dst.Min.Y, dst.Dy()))
	}
	return image.Rectangle{Min: at(r.Min), Max: at(r.Max)}
}

func (f *frame) stretch(from int, src, dst image.Rectangle) {
	if src.Empty() {
		return
	}
	for i := range f.ops[from:] {
		op := &f.ops[from+i]
		op.dst = mapRect(op.dst, src, dst)
	}
}

func (f *frame) remap(from int, src, dst image.Rectangle) {
	if src.Empty() {
		return
	}
	rect := func(r image.Rectangle) image.Rectangle { return mapRect(r, src, dst) }
	sx, sy := float32(dst.Dx())/float32(src.Dx()), float32(dst.Dy())/float32(src.Dy())
	point := func(v [2]float32) [2]float32 {
		return [2]float32{float32(dst.Min.X) + float32((v[0]-float32(src.Min.X))*sx), float32(dst.Min.Y) + float32((v[1]-float32(src.Min.Y))*sy)}
	}
	for i := range f.ops[from:] {
		op := &f.ops[from+i]
		op.dst, op.mask.Keep.Rect, op.mask.Cut.Rect, op.mask.Merge = rect(op.dst), rect(op.mask.Keep.Rect), rect(op.mask.Cut.Rect), rect(op.mask.Merge)
		op.gradient = mapGradient(op.gradient, point)
	}
}

func (f *frame) roundBare(from int, r image.Rectangle, radius float32) {
	if radius <= 0 {
		return
	}
	for i := range f.ops[from:] {
		if op := &f.ops[from+i]; op.mask == (render.Mask{}) {
			op.mask.Keep = render.RoundRect{Rect: r, Radius: radius}
		}
	}
}

func (f *frame) fade(from int, a float32) {
	for i := range f.ops[from:] {
		op := &f.ops[from+i]
		op.opaque = false
		switch {
		case op.gradient.Stops != nil:
			op.gradient.Alpha *= a
		case op.fill:
			op.color = scaleColor(op.color, a)
		default:
			op.alpha *= a
		}
	}
}

func (f *frame) cull(covered *region.Set) {
	covered.Reset()
	scene := f.ops[:f.cursorFrom]
	kept := len(scene)
	for _, op := range slices.Backward(scene) {
		if covered.Covers(op.dst) {
			continue
		}
		op.solid(covered.Add)
		kept--
		scene[kept] = op
	}
	n := copy(f.ops, scene[kept:])
	n += copy(f.ops[n:], f.ops[f.cursorFrom:])
	f.ops, f.cursorFrom = f.ops[:n], f.cursorFrom-kept
}

func (f *frame) clip(covered *region.Set) {
	covered.Reset()
	scene, tail := f.ops[:f.cursorFrom], f.ops[f.cursorFrom:]
	back := f.clipBack[:0]
	for i := len(scene) - 1; i >= 0; i-- {
		op := &scene[i]
		if covered.Covers(op.dst) {
			continue
		}
		split := false
		if op.clippable() && !covered.Empty() {
			f.clipPieces = covered.AppendUncovered(op.dst, f.clipPieces[:0])
			if worthClipping(f.clipPieces, op.dst) {
				split = true
				for _, p := range f.clipPieces {
					back = append(back, op.piece(p))
				}
			}
		}
		if !split {
			back = append(back, *op)
		}
		op.solid(covered.Add)
	}
	f.clipBack = back
	out := f.clipOut[:0]
	for i := len(back) - 1; i >= 0; i-- {
		out = append(out, back[i])
	}
	f.cursorFrom = len(out)
	out = append(out, tail...)
	f.clipOut, f.ops = f.ops[:0], out
}

func (f *frame) replay(p render.Pass, to colorOut) {
	f.replayFrom(p, image.Point{}, to)
}

func (f *frame) replayFrom(p render.Pass, origin image.Point, to colorOut) {
	var transform *render.ColorTransform
	var last *colorspace.Description
	settled := false
	active := to.to != nil || to.working != nil
	for i := range f.ops {
		op := f.ops[i]
		if active || op.space != last {
			if src := to.source(&op); !settled || src != last {
				settled, last = true, src
				if t := to.plan(src); t != transform {
					p.SetColor(t)
					transform = t
				}
			}
		}
		m := shiftMask(op.mask, origin)
		if g := op.goo; !g.A.Rect.Empty() {
			g.A.Rect, g.B.Rect = g.A.Rect.Sub(origin), g.B.Rect.Sub(origin)
			p.FillGoo(op.dst.Sub(origin), op.color, g)
			continue
		}
		if op.gradient.Stops != nil {
			p.FillGradient(op.dst.Sub(origin), mapGradient(op.gradient, func(v [2]float32) [2]float32 {
				return [2]float32{v[0] - float32(origin.X), v[1] - float32(origin.Y)}
			}), m)
			continue
		}
		if op.fill && op.color == (render.Color{}) {
			continue
		}
		if op.fill {
			p.FillRect(op.dst.Sub(origin), op.color, m)
			continue
		}
		if op.glass != (unique.Handle[render.Glass]{}) {
			p.DrawGlass(op.tex, op.src, op.dst.Sub(origin), op.alpha, m, op.glass.Value())
			continue
		}
		p.DrawTexture(op.tex, op.src, op.dst.Sub(origin), op.transform, op.alpha, m)
	}
	if transform != nil {
		p.SetColor(nil)
	}
}

func (f *frame) bounds() image.Rectangle {
	var r image.Rectangle
	for _, op := range f.ops {
		r = r.Union(op.dst)
	}
	return r
}

type scene struct {
	prev, cur frame
	covered   region.Set
	shown     []*surface.Surface
	content   map[*surface.Surface]*region.Set
	damage    region.Set
	repaint   region.Set
	history   [maxBufferAge]region.Set
	head      int
	drawn     bool
	stale     bool
	last      backend.Presentation
	blurs     blurCaches
	shader    *screenShader
	image     render.TextureTarget
	working   render.TextureTarget
	zoom      *outputZoom
	whole     [1]image.Rectangle
}

func newScene() *scene {
	return &scene{content: map[*surface.Surface]*region.Set{}}
}

func (sc *scene) damaged(s *surface.Surface, d []image.Rectangle) {
	set, ok := sc.content[s]
	if !ok {
		set = &region.Set{}
		sc.content[s] = set
	}
	for _, r := range d {
		set.Add(r)
	}
}

func (sc *scene) addSurfaceDamage(op drawOp, d *region.Set) {
	if op.size.X <= 0 || op.size.Y <= 0 {
		return
	}
	whole := op.mapped()
	w, h := whole.Dx(), whole.Dy()
	for _, r := range d.Rects() {
		sc.damage.Add(image.Rect(
			whole.Min.X+r.Min.X*w/op.size.X, whole.Min.Y+r.Min.Y*h/op.size.Y,
			whole.Min.X+(r.Max.X*w+op.size.X-1)/op.size.X, whole.Min.Y+(r.Max.Y*h+op.size.Y-1)/op.size.Y,
		).Intersect(op.dst))
	}
}

func (sc *scene) contentChanged(ops []drawOp) bool {
	return slices.ContainsFunc(ops, func(op drawOp) bool {
		if op.surface == nil {
			return false
		}
		content, ok := sc.content[op.surface]
		return ok && !content.Empty()
	})
}

func (sc *scene) diff(bounds image.Rectangle) *region.Set {
	sc.damage.Reset()
	if !sc.drawn {
		sc.damage.Add(bounds)
	}
	prev, cur := sc.prev.ops, sc.cur.ops
	for i := range max(len(prev), len(cur)) {
		switch {
		case i >= len(prev):
			sc.damage.Add(cur[i].dst)
		case i >= len(cur):
			sc.damage.Add(prev[i].dst)
		case prev[i] != cur[i]:
			sc.damage.Add(prev[i].dst)
			sc.damage.Add(cur[i].dst)
		case cur[i].surface != nil:
			if content, ok := sc.content[cur[i].surface]; ok {
				sc.addSurfaceDamage(cur[i], content)
			}
		}
	}
	for _, content := range sc.content {
		content.Reset()
	}
	sc.damage.Clip(bounds)
	return &sc.damage
}

func (sc *scene) repaintFor(f backend.Frame, bounds image.Rectangle) *region.Set {
	sc.repaint.Reset()
	if f.Age <= 0 || f.Age > maxBufferAge {
		sc.repaint.Add(bounds)
		return &sc.repaint
	}
	sc.repaint.CopyFrom(&sc.damage)
	for i := range f.Age - 1 {
		sc.repaint.AddSet(&sc.history[(sc.head+i)%maxBufferAge])
	}
	return &sc.repaint
}

func (sc *scene) presented() {
	sc.head = (sc.head + maxBufferAge - 1) % maxBufferAge
	sc.history[sc.head].CopyFrom(&sc.damage)
	sc.prev, sc.cur = sc.cur, sc.prev
	sc.drawn, sc.stale = true, false
}

func (sc *scene) replayable() bool {
	return sc.drawn && !sc.stale
}

func (sc *scene) draws(t render.Texture) bool {
	return slices.ContainsFunc(sc.prev.ops, func(op drawOp) bool { return op.tex == t })
}

func (sc *scene) begin() *frame {
	sc.cur.ops, sc.cur.cursorFrom, sc.cur.background, sc.cur.sharedBlur = sc.cur.ops[:0], 0, -1, false
	sc.cur.blurs, sc.cur.below, sc.cur.rims = sc.cur.blurs[:0], sc.cur.below[:0], sc.cur.rims[:0]
	return &sc.cur
}

func (sc *scene) invalidate() {
	sc.drawn = false
}

func (sc *scene) forget(s *surface.Surface) {
	delete(sc.content, s)
}

func (c *Compositor) FrameCost(o *output.Output) (draws, pixels int) {
	for _, op := range c.sceneOf(o).prev.ops {
		draws++
		pixels += op.dst.Dx() * op.dst.Dy()
	}
	return draws, pixels
}
