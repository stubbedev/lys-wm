package compositor

import (
	"errors"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type Blur struct {
	Enabled bool
	Size    int
	Passes  int
	XRay    bool
}

type blurCache struct {
	area    image.Rectangle
	below   []drawOp
	chain   []render.TextureTarget
	version uint64
	used    bool
	to      colorOut
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
	below     []drawOp
	shape     []image.Rectangle
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
	return errors.Join(errs...)
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

func (c *Compositor) blurOffset(o *output.Output) float32 {
	return deviceLength(o, c.decor.Blur.Size)
}

func (c *Compositor) blurReach(o *output.Output) int {
	return int(math.Ceil(float64(c.blurOffset(o)) * float64(int(1)<<(c.decor.Blur.Passes+1))))
}

func blurChanged(sc *scene, cache *blurCache, area image.Rectangle, below []drawOp, to colorOut) bool {
	if cache.area != area || cache.to != to || !slices.Equal(cache.below, below) || len(cache.chain) == 0 {
		return true
	}
	return slices.ContainsFunc(below, func(op drawOp) bool {
		d, ok := sc.content[op.surface]
		return op.surface != nil && ok && len(d.Rects()) > 0
	})
}

func (c *Compositor) drawBlurRegion(pass *frame, o *output.Output, s *surface.Surface, org origin, m render.Mask) error {
	shape := c.effects.BlurRegion(s)
	if shape.Empty() {
		return nil
	}
	alpha := c.alpha.Alpha(s)
	t, ok := c.textures[s]
	if alpha == 1 && ok && t.opaque() {
		return nil
	}
	sc := c.sceneOf(o)
	sc.blurs.shape = sc.blurs.shape[:0]
	bounds := image.Rectangle{Max: s.Size()}
	for _, r := range shape.Rects() {
		r = r.Intersect(bounds)
		if r.Empty() || alpha == 1 && s.Opaque().Covers(r) {
			continue
		}
		sc.blurs.shape = append(sc.blurs.shape, org.child(r.Min).device(o, r.Size()))
	}
	return c.drawBlur(pass, o, s, sc.blurs.shape, org.device(o, s.Size()), m, alpha)
}

func (c *Compositor) drawBlur(pass *frame, o *output.Output, s *surface.Surface, shape []image.Rectangle, extent image.Rectangle, m render.Mask, alpha float32) error {
	if alpha <= 0 {
		return nil
	}
	var under image.Rectangle
	for _, r := range shape {
		under = under.Union(r)
	}
	bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
	reach := c.blurReach(o)
	area := under.Inset(-reach).Intersect(bounds)
	if area.Empty() || !under.Overlaps(area) {
		return nil
	}
	ops := pass.ops
	sc := c.sceneOf(o)
	var b *blurCache
	if c.decor.Blur.XRay && pass.background >= 0 {
		b = sc.blurs.shared()
		area, ops = bounds, pass.ops[:pass.background]
	} else {
		b = sc.blurs.of(s)
		area = b.areaFor(area, extent.Inset(-reach).Intersect(bounds))
	}
	b.used = true
	below := sc.blurs.below[:0]
	for _, op := range ops {
		if op.dst.Overlaps(area) {
			below = append(below, op)
		}
	}
	sc.blurs.below = below
	to := c.sceneOut(o)
	if blurChanged(sc, b, area, below, to) {
		if err := c.renderBlur(o, b, area, below, c.blurOffset(o), to); err != nil {
			return err
		}
	}
	final := b.chain[len(b.chain)-1].Texture()
	for _, r := range shape {
		r = r.Intersect(area)
		if r.Empty() {
			continue
		}
		src := r.Sub(area.Min)
		pass.ops = append(pass.ops, drawOp{tex: final, src: render.FRect{X: float64(src.Min.X), Y: float64(src.Min.Y), W: float64(src.Dx()), H: float64(src.Dy())}, dst: r, alpha: alpha, mask: m, version: b.version})
	}
	return nil
}

func (c *Compositor) renderBlur(o *output.Output, b *blurCache, area image.Rectangle, below []drawOp, offset float32, to colorOut) error {
	sizes := []image.Point{area.Size()}
	for range c.decor.Blur.Passes {
		last := sizes[len(sizes)-1]
		sizes = append(sizes, image.Pt(max(last.X/2, 1), max(last.Y/2, 1)))
	}
	for i := len(sizes) - 2; i >= 0; i-- {
		sizes = append(sizes, sizes[i])
	}
	if b.area.Size() != area.Size() || len(b.chain) != len(sizes) || b.to != to {
		if err := b.destroy(); err != nil {
			return err
		}
		for _, s := range sizes {
			t, err := c.newSceneTarget(o, s)
			if err != nil {
				return errors.Join(err, b.destroy())
			}
			b.chain = append(b.chain, t)
		}
	}
	f := frame{ops: below}
	p, err := c.renderer.Begin(b.chain[0], render.Region{{Max: sizes[0]}})
	if err != nil {
		return err
	}
	p.Clear(render.Color{})
	f.replayFrom(p, area.Min, to)
	if err := p.End(); err != nil {
		return err
	}
	for i := 1; i < len(sizes); i++ {
		p, err := c.renderer.Begin(b.chain[i], render.Region{{Max: sizes[i]}})
		if err != nil {
			return err
		}
		p.Clear(render.Color{})
		p.Kawase(b.chain[i-1].Texture(), image.Rectangle{Max: sizes[i]}, offset, i > c.decor.Blur.Passes)
		if err := p.End(); err != nil {
			return err
		}
	}
	c.blurGen++
	b.area, b.below, b.version, b.to = area, slices.Clone(below), c.blurGen, to
	return nil
}
