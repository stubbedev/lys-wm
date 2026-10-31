package compositor

import (
	"errors"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
)

type Blur struct {
	Enabled bool
	Size    int
	Passes  int
}

type blurCache struct {
	area    image.Rectangle
	below   []drawOp
	chain   []render.TextureTarget
	version uint64
}

func (b *blurCache) destroy() error {
	var errs []error
	for _, t := range b.chain {
		errs = append(errs, t.Destroy())
	}
	b.chain = nil
	return errors.Join(errs...)
}

func (c *Compositor) dropBlurs() error {
	var errs []error
	for _, w := range c.windows {
		if w.blur != nil {
			errs = append(errs, w.blur.destroy())
		}
	}
	for _, e := range c.layers {
		if e.blur != nil {
			errs = append(errs, e.blur.destroy())
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) blurred(w *window) bool {
	if r := c.decision(w).Blur; r != nil {
		return *r
	}
	return c.decor.Blur.Enabled
}

func (c *Compositor) blurOffset(o *output.Output) float32 {
	return deviceLength(o, c.decor.Blur.Size)
}

func (c *Compositor) blurReach(o *output.Output) int {
	return int(math.Ceil(float64(c.blurOffset(o)) * float64(int(1)<<(c.decor.Blur.Passes+1))))
}

func blurChanged(sc *scene, cache *blurCache, area image.Rectangle, below []drawOp) bool {
	if cache.area != area || !slices.Equal(cache.below, below) || len(cache.chain) == 0 {
		return true
	}
	return slices.ContainsFunc(below, func(op drawOp) bool {
		d, ok := sc.content[op.surface]
		return op.surface != nil && ok && len(d.Rects()) > 0
	})
}

func (c *Compositor) drawBlur(pass *frame, o *output.Output, cache **blurCache, under image.Rectangle, m render.Mask) error {
	area := under.Inset(-c.blurReach(o)).Intersect(image.Rectangle{Max: o.Backend().State().Mode.Size})
	if area.Empty() {
		return nil
	}
	below := make([]drawOp, 0, len(pass.ops))
	for _, op := range pass.ops {
		if op.dst.Overlaps(area) {
			below = append(below, op)
		}
	}
	if *cache == nil {
		*cache = &blurCache{}
	}
	b := *cache
	if blurChanged(c.sceneOf(o), b, area, below) {
		if err := c.renderBlur(b, area, below, c.blurOffset(o)); err != nil {
			return err
		}
	}
	final := b.chain[len(b.chain)-1]
	src := under.Sub(area.Min)
	pass.ops = append(pass.ops, drawOp{tex: final.Texture(), src: render.FRect{X: float64(src.Min.X), Y: float64(src.Min.Y), W: float64(src.Dx()), H: float64(src.Dy())}, dst: under, alpha: 1, mask: m, version: b.version})
	return nil
}

func (c *Compositor) renderBlur(b *blurCache, area image.Rectangle, below []drawOp, offset float32) error {
	sizes := []image.Point{area.Size()}
	for range c.decor.Blur.Passes {
		last := sizes[len(sizes)-1]
		sizes = append(sizes, image.Pt(max(last.X/2, 1), max(last.Y/2, 1)))
	}
	for i := len(sizes) - 2; i >= 0; i-- {
		sizes = append(sizes, sizes[i])
	}
	if b.area.Size() != area.Size() || len(b.chain) != len(sizes) {
		if err := b.destroy(); err != nil {
			return err
		}
		for _, s := range sizes {
			t, err := c.renderer.NewTextureTarget(s)
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
	f.replayFrom(p, area.Min)
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
	b.area, b.below, b.version = area, slices.Clone(below), c.blurGen
	return nil
}
