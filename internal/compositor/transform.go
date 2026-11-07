package compositor

import (
	"errors"
	"image"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
)

func (c *Compositor) composedApart(o *output.Output) bool {
	return o.Transform() != render.Normal || c.mirrored(o)
}

func (c *Compositor) beginFrame(o *output.Output, sc *scene) (backend.Frame, error) {
	f, err := o.Backend().BeginFrame()
	if err != nil || !c.composedApart(o) {
		return f, err
	}
	size, age := o.PixelSize(), 1
	if sc.composed == nil || sc.composed.Size() != size {
		t, err := c.newSceneTarget(o, size)
		if err = errors.Join(err, sc.dropComposed()); err != nil {
			o.Backend().AbortFrame()
			return backend.Frame{}, err
		}
		sc.composed, age = t, 0
	}
	sc.real = f
	return backend.Frame{Renderer: f.Renderer, Target: sc.composed, Age: age}, nil
}

func (c *Compositor) present(o *output.Output, sc *scene, repaint render.Region) error {
	if !c.composedApart(o) {
		return o.Backend().Present(repaint)
	}
	f := sc.real
	sc.real = backend.Frame{}
	size := o.PixelSize()
	whole := image.Rectangle{Max: size}
	t := o.Transform()
	logical := []image.Rectangle(repaint)
	if len(repaint) != 1 || repaint[0] != whole {
		logical = sc.repaintFor(f, whole).Rects()
	}
	sc.blit = sc.blit[:0]
	for _, r := range logical {
		sc.blit = append(sc.blit, t.Rect(r, size))
	}
	p, err := f.Renderer.Begin(f.Target, sc.blit)
	if err != nil {
		o.Backend().AbortFrame()
		return err
	}
	p.DrawTexture(sc.composed.Texture(), render.FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Max: o.Backend().State().Mode.Size}, t, 1, render.Mask{})
	if err := p.End(); err != nil {
		o.Backend().AbortFrame()
		return err
	}
	return errors.Join(o.Backend().Present(sc.blit), c.scheduleMirrorsOf(o))
}

func (sc *scene) dropComposed() error {
	if sc.composed == nil {
		return nil
	}
	t := sc.composed
	sc.composed = nil
	return t.Destroy()
}
