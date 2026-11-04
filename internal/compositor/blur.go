package compositor

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type Blur struct {
	Enabled  bool
	Withhold bool
	Size     int
	Passes   int
	XRay     bool
	Look     wm.Look
	Mica     Mica
}

type Mica struct {
	Enabled      bool
	Size, Passes int
	Tint         color.RGBA
}

const (
	micaSize   = 16
	micaPasses = 4
)

type chainSpec struct {
	offset float32
	passes int
}

func (s chainSpec) reach() int {
	return int(math.Ceil(float64(s.offset) * float64(int(1)<<(s.passes+1))))
}

type blurCache struct {
	area    image.Rectangle
	below   []drawOp
	chain   []render.TextureTarget
	version uint64
	used    bool
	to      colorOut
	spec    chainSpec
	sizes   []image.Point
	whole   [1]image.Rectangle
}

func (b *blurCache) destroy() error {
	var errs []error
	for _, t := range b.chain {
		errs = append(errs, t.Destroy())
	}
	b.chain = nil
	return errors.Join(errs...)
}

func (b *blurCache) areaFor(needed, extent image.Rectangle) image.Rectangle {
	pixels := func(r image.Rectangle) int { return r.Dx() * r.Dy() }
	switch {
	case len(b.chain) == 0:
		return needed
	case needed.In(b.area) && pixels(b.area) <= 4*pixels(needed):
		return b.area
	case !needed.In(b.area) && needed.In(extent):
		return extent
	}
	return needed
}

type blurCaches struct {
	bySurface map[*surface.Surface]*blurCache
	xray      *blurCache
	mica      *blurCache
	shape     []image.Rectangle
	keepMica  bool
}

func (b *blurCache) trim() error {
	if len(b.chain) < 2 {
		return nil
	}
	var errs []error
	last := len(b.chain) - 1
	for _, t := range b.chain[:last] {
		errs = append(errs, t.Destroy())
	}
	b.chain = append(b.chain[:0], b.chain[last])
	return errors.Join(errs...)
}

func (bc *blurCaches) dropMica() error {
	bc.keepMica = false
	if bc.mica == nil {
		return nil
	}
	err := bc.mica.destroy()
	bc.mica = nil
	return err
}

func (bc *blurCaches) one(r image.Rectangle) []image.Rectangle {
	bc.shape = append(bc.shape[:0], r)
	return bc.shape
}

func (bc *blurCaches) of(s *surface.Surface) *blurCache {
	b, ok := bc.bySurface[s]
	if !ok {
		if bc.bySurface == nil {
			bc.bySurface = map[*surface.Surface]*blurCache{}
		}
		b = &blurCache{}
		bc.bySurface[s] = b
	}
	return b
}

func (bc *blurCaches) shared() *blurCache {
	if bc.xray == nil {
		bc.xray = &blurCache{}
	}
	return bc.xray
}

func (bc *blurCaches) micaChain() *blurCache {
	if bc.mica == nil {
		bc.mica = &blurCache{}
	}
	return bc.mica
}

func (bc *blurCaches) dropUnused() error {
	var errs []error
	for s, b := range bc.bySurface {
		if !b.used {
			errs = append(errs, b.destroy())
			delete(bc.bySurface, s)
		}
		b.used = false
	}
	if bc.xray != nil && !bc.xray.used {
		errs = append(errs, bc.xray.destroy())
		bc.xray = nil
	}
	if bc.xray != nil {
		bc.xray.used = false
	}
	if bc.mica != nil && !bc.mica.used && !bc.keepMica {
		errs = append(errs, bc.mica.destroy())
		bc.mica = nil
	}
	if bc.mica != nil {
		bc.mica.used = false
	}
	return errors.Join(errs...)
}

func (bc *blurCaches) drop() error {
	var errs []error
	for s, b := range bc.bySurface {
		errs = append(errs, b.destroy())
		delete(bc.bySurface, s)
	}
	if bc.xray != nil {
		errs = append(errs, bc.xray.destroy())
		bc.xray = nil
	}
	bc.keepMica = false
	if bc.mica != nil {
		errs = append(errs, bc.mica.destroy())
		bc.mica = nil
	}
	return errors.Join(errs...)
}

func (c *Compositor) BlurRedraws() uint64 {
	return c.blurGen
}

func (c *Compositor) dropBlurs() error {
	var errs []error
	for _, sc := range c.scenes {
		errs = append(errs, sc.blurs.drop())
	}
	return errors.Join(errs...)
}

func (c *Compositor) blurred(w *window) bool {
	if r := c.decision(w).Blur; r != nil {
		return *r
	}
	return c.decor.Blur.Enabled
}

func (c *Compositor) honoursBlurRegions(w *window) bool {
	r := c.decision(w).Blur
	return r == nil || *r
}

func (c *Compositor) translucent(s *surface.Surface, opacity float32) bool {
	return opacity*c.alpha.Alpha(s) < 1 || !c.opaque(s)
}

func (c *Compositor) blurSpec(o *output.Output) chainSpec {
	return chainSpec{offset: deviceLength(o, c.decor.Blur.Size), passes: c.decor.Blur.Passes}
}

func (c *Compositor) micaSpec(o *output.Output) chainSpec {
	m := c.decor.Blur.Mica
	if m.Size == 0 {
		m.Size = micaSize
	}
	if m.Passes == 0 {
		m.Passes = micaPasses
	}
	return chainSpec{offset: deviceLength(o, m.Size), passes: m.Passes}
}

func blurChanged(sc *scene, cache *blurCache, spec chainSpec, area image.Rectangle, below []drawOp, to colorOut) bool {
	if cache.area != area || cache.spec != spec || cache.to != to || !slices.Equal(cache.below, below) || len(cache.chain) == 0 {
		return true
	}
	return sc.contentChanged(below, area)
}

func (c *Compositor) drawBlurRegion(pass *frame, o *output.Output, s *surface.Surface, org origin, m render.Mask, look *wm.Look) {
	if c.effects.BlurRegion(s).Empty() {
		return
	}
	sc := c.sceneOf(o)
	sc.blurs.shape = c.appendBlurRegion(sc.blurs.shape[:0], o, s, org)
	c.drawBlur(pass, o, glassDraw{surface: s, shape: sc.blurs.shape, extent: org.device(o, s.Size()), mask: m, alpha: c.alpha.Alpha(s), look: look})
}

func (c *Compositor) appendBlurRegion(rects []image.Rectangle, o *output.Output, s *surface.Surface, org origin) []image.Rectangle {
	shape := c.effects.BlurRegion(s)
	if shape.Empty() {
		return rects
	}
	alpha := c.alpha.Alpha(s)
	t, ok := c.textures[s]
	if alpha == 1 && ok && t.opaque() {
		return rects
	}
	bounds := image.Rectangle{Max: s.Size()}
	for _, r := range shape.Rects() {
		r = r.Intersect(bounds)
		if r.Empty() || alpha == 1 && s.Opaque().Covers(r) {
			continue
		}
		rects = append(rects, org.child(r.Min).device(o, r.Size()))
	}
	return rects
}

type glassDraw struct {
	surface *surface.Surface
	shape   []image.Rectangle
	extent  image.Rectangle
	mask    render.Mask
	alpha   float32
	look    *wm.Look
	mica    bool
}

type pendingBlur struct {
	cache    *blurCache
	spec     chainSpec
	area     image.Rectangle
	from, to int
	visible  bool
	resolved bool
	failed   bool
}

func (f *frame) pend(b *blurCache, spec chainSpec, area image.Rectangle, ops []drawOp, shared bool) int32 {
	if shared {
		for i := range f.blurs {
			if f.blurs[i].cache == b {
				return int32(i + 1)
			}
		}
	}
	from := len(f.below)
	for _, op := range ops {
		if op.dst.Overlaps(area) {
			f.below = append(f.below, op)
		}
	}
	f.blurs = append(f.blurs, pendingBlur{cache: b, spec: spec, area: area, from: from, to: len(f.below)})
	return int32(len(f.blurs)) //nolint:gosec // one per drawn surface
}

func (c *Compositor) drawBlur(pass *frame, o *output.Output, g glassDraw) {
	if g.alpha <= 0 {
		return
	}
	var under image.Rectangle
	for _, r := range g.shape {
		under = under.Union(r)
	}
	bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
	spec := c.blurSpec(o)
	plain := spec
	look := *g.look
	if g.mica {
		spec = c.micaSpec(o)
		if t := c.decor.Blur.Mica.Tint; t.A > 0 {
			look.Tint = t
		}
	}
	glass, handle := c.glassOf(o, look)
	reach := spec.reach()
	if glass.Refracts() {
		reach += int(math.Ceil(float64(glass.Amount)))
	}
	area := under.Inset(-reach).Intersect(bounds)
	if area.Empty() || !under.Overlaps(area) {
		return
	}
	ops := pass.ops
	sc := c.sceneOf(o)
	var b *blurCache
	shared := pass.background >= 0 && (g.mica || c.decor.Blur.XRay || pass.sharedBlur)
	switch {
	case shared && spec != plain:
		b = sc.blurs.micaChain()
	case shared:
		b = sc.blurs.shared()
	default:
		b = sc.blurs.of(g.surface)
		area = b.areaFor(area, g.extent.Inset(-reach).Intersect(bounds))
	}
	if shared {
		area, ops = bounds, pass.ops[:pass.background]
	}
	pending := pass.pend(b, spec, area, ops, shared)
	m := g.mask
	if glass.Refracts() && m.Keep.Rect.Empty() {
		m.Keep.Rect = under
	}
	tint := scaleColor(render.Color(look.Tint), g.alpha)
	for _, r := range g.shape {
		r = r.Intersect(area)
		if r.Empty() {
			continue
		}
		src := r.Sub(area.Min)
		pass.ops = append(pass.ops, drawOp{
			src: render.FRect{X: float64(src.Min.X), Y: float64(src.Min.Y), W: float64(src.Dx()), H: float64(src.Dy())}, dst: r,
			alpha: g.alpha, mask: m, glass: handle, pending: pending, opaque: g.mica && g.alpha == 1,
		})
		if tint.A > 0 {
			pass.fillMasked(r, tint, m)
		}
		if look.Grain > 0 {
			if err := c.drawGrain(pass, o, r, g.extent.Min, m, g.alpha, look.Grain); err != nil {
				c.report(fmt.Errorf("compositor: grain over a blur: %w", err))
			}
		}
	}
	pass.addRim(g, m, under, o)
}

func (c *Compositor) resolveBlurs(o *output.Output, f *frame, from int) {
	if len(f.blurs) == 0 {
		return
	}
	sc, to := c.sceneOf(o), c.sceneOut(o)
	for _, op := range f.ops[from:] {
		if op.pending != 0 {
			f.blurs[op.pending-1].visible = true
		}
	}
	for i := len(f.blurs) - 1; i >= 0; i-- {
		p := &f.blurs[i]
		if !p.visible || p.resolved {
			continue
		}
		below := frame{ops: f.below[p.from:p.to], cursorFrom: p.to - p.from}
		below.cull(&sc.covered)
		p.to = p.from + len(below.ops)
		for _, op := range below.ops {
			if op.pending != 0 {
				f.blurs[op.pending-1].visible = true
			}
		}
	}
	for i := range f.blurs {
		p := &f.blurs[i]
		if !p.visible || p.resolved {
			continue
		}
		p.resolved = true
		below := f.settle(f.below[p.from:p.to])
		p.to = p.from + len(below)
		p.cache.used = true
		if blurChanged(sc, p.cache, p.spec, p.area, below, to) {
			if err := c.renderBlur(func(size image.Point) (render.TextureTarget, error) { return c.newSceneTarget(o, size) }, p, below, to); err != nil {
				p.failed = true
				c.report(fmt.Errorf("compositor: redraw a blur: %w", err))
			}
		}
	}
	kept := f.settle(f.ops[from:])
	removed := len(f.ops) - from - len(kept)
	f.ops = f.ops[:from+len(kept)]
	if f.cursorFrom > from {
		f.cursorFrom -= removed
	}
}

func (f *frame) settle(ops []drawOp) []drawOp {
	kept := ops[:0]
	for _, op := range ops {
		if op.pending != 0 {
			p := &f.blurs[op.pending-1]
			if p.failed || len(p.cache.chain) == 0 {
				continue
			}
			op.tex, op.version, op.pending = p.cache.chain[len(p.cache.chain)-1].Texture(), p.cache.version, 0
		}
		kept = append(kept, op)
	}
	return kept
}

func (c *Compositor) renderBlur(newTarget func(image.Point) (render.TextureTarget, error), pb *pendingBlur, below []drawOp, to colorOut) error {
	b, area := pb.cache, pb.area
	sizes := append(b.sizes[:0], area.Size())
	for range pb.spec.passes {
		last := sizes[len(sizes)-1]
		sizes = append(sizes, image.Pt(max(last.X/2, 1), max(last.Y/2, 1)))
	}
	for i := len(sizes) - 2; i >= 0; i-- {
		sizes = append(sizes, sizes[i])
	}
	b.sizes = sizes
	if b.area.Size() != area.Size() || len(b.chain) != len(sizes) || b.to != to {
		if err := b.destroy(); err != nil {
			return err
		}
		for _, s := range sizes {
			t, err := newTarget(s)
			if err != nil {
				return errors.Join(err, b.destroy())
			}
			b.chain = append(b.chain, t)
		}
	}
	b.whole[0] = image.Rectangle{Max: sizes[0]}
	p, err := c.renderer.Begin(b.chain[0], b.whole[:])
	if err != nil {
		return err
	}
	p.Clear(render.Color{})
	replayOps(below, p, area.Min, to, false)
	if err := p.End(); err != nil {
		return err
	}
	for i := 1; i < len(sizes); i++ {
		b.whole[0] = image.Rectangle{Max: sizes[i]}
		p, err := c.renderer.Begin(b.chain[i], b.whole[:])
		if err != nil {
			return err
		}
		p.Clear(render.Color{})
		p.Kawase(b.chain[i-1].Texture(), image.Rectangle{Max: sizes[i]}, pb.spec.offset, i > pb.spec.passes)
		if err := p.End(); err != nil {
			return err
		}
	}
	c.blurGen++
	b.area, b.below, b.version, b.to, b.spec = area, append(b.below[:0], below...), c.blurGen, to, pb.spec
	return nil
}
