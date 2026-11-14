package gles

import (
	"errors"
	"image"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
)

type sampling struct {
	target uint32
	format uint64
	filter int32
}

const (
	sampledUpload = iota + 1
	sampledDMABUF
	sampledTarget
)

func (t *texture) sampling() sampling {
	s := sampling{target: t.target, filter: t.filter}
	switch {
	case t.drawn != nil:
		s.format = sampledTarget<<40 | uint64(t.drawn.format())
	case t.imported:
		s.format = sampledDMABUF<<40 | uint64(t.dmabuf.Format)
	default:
		s.format = sampledUpload<<40 | uint64(uint32(t.upload.internal))<<1 //nolint:gosec // a GL internal format is a GLenum
		if t.upload.swapRB {
			s.format |= 1
		}
	}
	return s
}

type drawState struct {
	program uint64
	target  uint64
	blend   bool
	sampled sampling
	cut     sampling
}

func (p *pass) noteState() {
	s := drawState{program: p.prog.serial, target: uint64(p.fb.format()), blend: p.blending, sampled: p.sampled}
	if p.prog.cutting {
		s.cut = p.cut.sampling()
	}
	if s == p.last {
		return
	}
	p.last = s
	if _, ok := p.owner.drawn[s]; ok {
		return
	}
	p.owner.drawn[s] = struct{}{}
	p.owner.counts.ColdDraws++
	gpu.Compiling()
}

func (r *Renderer) makeEffects() error {
	for _, f := range []render.Format{render.ARGB8888, render.XRGB8888} {
		t, err := r.newBlank(f, image.Pt(2, 2))
		if err != nil {
			return err
		}
		r.effects.Textures = append(r.effects.Textures, t)
	}
	stops, err := r.newBlank(render.ARGB8888, image.Pt(2, 1))
	if err != nil {
		return err
	}
	r.effects.Stops = stops
	f, err := r.buildFramebuffer(&Framebuffer{r: r, size: image.Pt(2, 2), float: r.floatTargets})
	if err != nil {
		return err
	}
	r.effects.Sources = []render.Texture{r.textureTarget(f).tex}
	return nil
}

func (r *Renderer) destroyEffects() error {
	var errs []error
	for _, t := range append(append(r.effects.Textures, r.effects.Stops), r.effects.Sources...) {
		tex, ok := t.(*texture)
		switch {
		case !ok:
		case tex.drawn != nil:
			tex.id = 0
			errs = append(errs, tex.drawn.Destroy())
		default:
			errs = append(errs, tex.Destroy())
		}
	}
	r.effects = render.Effects{}
	return errors.Join(errs...)
}

func (r *Renderer) warm(f *Framebuffer) error {
	if !f.imported {
		return nil
	}
	return r.warmInto(f)
}

func (r *Renderer) warmInto(f *Framebuffer) error {
	format := f.format()
	if _, ok := r.warmed[format]; ok || r.lost != nil {
		return nil
	}
	if _, err := r.begin(f, nil); err != nil {
		return err
	}
	r.pass.warming = true
	render.DrawEffects(&r.pass, r.effects)
	if err := r.pass.End(); err != nil {
		return err
	}
	r.warmed[format] = struct{}{}
	return nil
}

func (r *Renderer) forget(serial uint64) {
	for s := range r.drawn {
		if s.program == serial {
			delete(r.drawn, s)
		}
	}
	r.pass.last = drawState{}
}
