package vulkan

import (
	"errors"
	"image"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
)

type opKind uint8

const (
	opFill opKind = iota
	opGradient
	opTexture
	opGlass
	opKawase
	opGoo
	opShader
	opTarget
)

type textureKey struct {
	format   fourcc.Format
	imported bool
	working  bool
	yuv      gpu.YUVEncoding
}

type drawState struct {
	op      opKind
	target  uint64
	colored bool
	cut     bool
	opaque  bool
	texture textureKey
	shader  *screenShader
}

func (t *Target) key() uint64 {
	return uint64(t.kind)<<32 | uint64(t.t.Dmabuf().Format)
}

func (t *texture) key() textureKey {
	k := textureKey{format: t.t.Format(), imported: t.imported, working: t.working}
	if t.imported {
		k.yuv = t.dmabuf.YUV
	}
	return k
}

func (r *Renderer) compiling(s drawState) {
	if _, ok := r.drawn[s]; ok {
		return
	}
	r.drawn[s] = struct{}{}
	r.counts.ColdDraws++
	gpu.Compiling()
}

func (p *pass) note(op opKind, opaque bool, tex *texture) {
	s := drawState{op: op, target: p.target.key(), colored: p.colored, cut: p.cut, opaque: opaque}
	if tex != nil {
		s.texture = tex.key()
	}
	p.r.compiling(s)
}

func (r *Renderer) makeEffects() error {
	if r.effects.Stops != nil {
		return nil
	}
	for _, f := range []render.Format{render.ARGB8888, render.XRGB8888} {
		t, err := r.NewBlankTexture(f, image.Pt(2, 2))
		if err != nil {
			return err
		}
		r.effects.Textures = append(r.effects.Textures, t)
	}
	source, err := r.textureTarget(floatTarget, image.Pt(2, 2))
	if err != nil {
		return err
	}
	r.effectSource = source
	r.effects.Sources = []render.Texture{source.tex}
	stops, err := r.NewBlankTexture(render.ARGB8888, image.Pt(2, 1))
	if err != nil {
		return err
	}
	r.effects.Stops = stops
	return nil
}

func (r *Renderer) destroyEffects() error {
	var errs []error
	for _, t := range append(r.effects.Textures, r.effects.Stops) {
		if t != nil {
			errs = append(errs, t.Destroy())
		}
	}
	if r.effectSource != nil {
		errs = append(errs, r.effectSource.Destroy())
	}
	r.effects, r.effectSource = render.Effects{}, nil
	return errors.Join(errs...)
}

func (r *Renderer) warm(t *Target) error {
	if r.lost != nil {
		return nil
	}
	if _, ok := r.warmed[t.key()]; ok {
		return nil
	}
	if err := r.makeEffects(); err != nil {
		return err
	}
	p, err := r.begin(t, nil)
	if err != nil {
		return err
	}
	render.DrawEffects(p, r.effects)
	if err := p.End(); err != nil {
		return err
	}
	r.warmed[t.key()] = struct{}{}
	return nil
}
