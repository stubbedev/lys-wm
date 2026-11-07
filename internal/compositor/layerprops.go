package compositor

import (
	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
)

func (c *Compositor) layerCut(o *output.Output, e *layer) (render.Texture, render.AlphaCut, bool) {
	threshold := c.layerProps(e).BlurAlpha
	if threshold == nil {
		return nil, render.AlphaCut{}, true
	}
	s := e.surface.Surface()
	t, ok := c.textures[s]
	if !ok {
		return nil, render.AlphaCut{}, false
	}
	if f, fill := t.(filled); fill {
		return nil, render.AlphaCut{}, float32(f.color.A)/0xff > *threshold
	}
	return t.texture(), render.AlphaCut{
		Src:       s.BufferSource(),
		Dst:       flat(e.bounds().Min).device(o, s.Size()),
		Transform: lgpu.Transform(s.Transform().Invert()),
		Threshold: *threshold,
	}, true
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
