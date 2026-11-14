package gles

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	shaderlang "github.com/stubbedev/lys-wm/internal/shader"
)

type screenShader struct {
	r      *Renderer
	source string
	prog   program
}

func (s *screenShader) link() error {
	gl := s.r.gl
	id, err := gl.Link(vertexShader, s.source)
	if err != nil {
		return fmt.Errorf("gles: %w", err)
	}
	s.r.counts.Programs++
	s.prog = s.r.newProgram(id)
	gl.UseProgram(id)
	gl.Uniform1i(gl.GetUniformLocation(id, "tex"), 0)
	return nil
}

func (r *Renderer) NewScreenShader(p *shaderlang.Program) (render.ScreenShader, error) {
	s := &screenShader{r: r, source: p.GLES()}
	if r.lost != nil {
		r.shaders[s] = struct{}{}
		return s, nil
	}
	gpu.Compiling()
	if err := s.make(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *screenShader) make() error {
	r := s.r
	if err := r.enter(); err != nil {
		return err
	}
	if err := s.link(); err != nil {
		return errors.Join(err, r.leave("gles: a screen shader"))
	}
	r.shaders[s] = struct{}{}
	if err := r.leave("gles: a screen shader"); err != nil {
		return errors.Join(err, s.Destroy())
	}
	return nil
}

func (*screenShader) Prepare(render.Target) error { return nil }

func (s *screenShader) Destroy() error {
	if _, live := s.r.shaders[s]; !live {
		return nil
	}
	delete(s.r.shaders, s)
	s.r.forget(s.prog.serial)
	if s.r.lost != nil || s.prog.id == 0 {
		s.prog.id = 0
		return nil
	}
	if err := s.r.enter(); err != nil {
		return err
	}
	s.r.gl.DeleteProgram(s.prog.id)
	s.prog.id = 0
	return s.r.leave("gles: destroy a screen shader")
}

func (p *pass) DrawScreenShader(s render.ScreenShader, t render.Texture, dst image.Rectangle, seconds float32) {
	sh, ok := s.(*screenShader)
	tex, texOK := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("gles: DrawScreenShader outside a pass"))
		return
	case !ok || sh.r != p.owner || sh.prog.id == 0:
		p.fail(fmt.Errorf("gles: %T is no live screen shader of this renderer", s))
		return
	case !texOK || tex.r != p.owner || tex.target != gpu.Texture2D:
		p.fail(fmt.Errorf("gles: %T is not this renderer's RGB texture", t))
		return
	case tex.id == 0:
		p.fail(errors.New("gles: texture used after Destroy"))
		return
	case tex.drawn == p.fb:
		p.fail(errors.New("gles: DrawScreenShader of the pass's own target"))
		return
	case dst.Empty():
		return
	}
	gl := p.owner.gl
	p.use(&sh.prog)
	sh.prog.time.set1(gl, seconds)
	sh.prog.resolution.set2(gl, float32(dst.Dx()), float32(dst.Dy()))
	p.bindLinear(tex)
	gl.Disable(gpu.Blend)
	gl.ColorMask(1, 1, 1, 0)
	p.drawWhole(dst)
	gl.ColorMask(1, 1, 1, 1)
	gl.Enable(gpu.Blend)
}
