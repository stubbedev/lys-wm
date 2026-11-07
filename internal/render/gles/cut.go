package gles

import (
	"errors"
	"fmt"
	"strings"

	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
)

const cutCoverage = "(" + coverage + " * cutCoverage(cutTex, gl_FragCoord.xy, cutAxes, cutBase))"

type cutKind uint8

const (
	cutSolid cutKind = iota
	cutRGBA
	cutRGBX
	cutGlass
)

type cutVariant struct {
	kind     cutKind
	colored  bool
	external bool
}

func (v cutVariant) fragment() string {
	sources := [...][2]string{
		cutSolid: {solidShader, solidShader},
		cutRGBA:  {rgbaShader, rgbaColorShader},
		cutRGBX:  {rgbxShader, rgbxColorShader},
		cutGlass: {glassShader, glassColorShader},
	}[v.kind]
	base := sources[0]
	if v.colored {
		base = sources[1]
	}
	sampler, extension := "sampler2D", ""
	if v.external {
		sampler, extension = "samplerExternalOES", "#extension GL_OES_EGL_image_external : require\n"
	}
	decl := maskUniforms + "#define CUT_SAMPLER " + sampler + "\n#ifndef SAMPLE\n#define SAMPLE texture2D\n#endif\nuniform CUT_SAMPLER cutTex;\nuniform vec4 cutAxes;\nuniform vec4 cutBase;\n" + lgpu.CutGLSL
	return extension + strings.ReplaceAll(strings.Replace(base, maskUniforms, decl, 1), coverage, cutCoverage)
}

func (p *pass) SetCut(t render.Texture, c render.AlphaCut) {
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("gles: SetCut outside a pass"))
		return
	case !ok || tex.r != p.owner:
		p.fail(fmt.Errorf("gles: a cut over a texture %T of another renderer", t))
		return
	case tex.id == 0:
		p.fail(errors.New("gles: a cut over a texture used after Destroy"))
		return
	case tex.drawn == p.fb:
		p.fail(errors.New("gles: a cut over the pass's own target"))
		return
	}
	if err := c.Check(tex.size); err != nil {
		p.fail(err)
		return
	}
	p.cut, p.cutU = tex, c.Uniforms(tex.size, tex.opaque)
}

func (p *pass) ClearCut() {
	p.cut = nil
}

func (p *pass) cutProgram(kind cutKind) *program {
	v := cutVariant{kind: kind, colored: p.color != nil && kind != cutSolid, external: p.cut.target == gpu.TextureExternalOES}
	if prog, ok := p.owner.cuts[v]; ok {
		return prog
	}
	prog := &program{}
	if err := p.owner.link(shader{prog: prog, fragment: v.fragment(), lazy: true}); err != nil {
		p.fail(fmt.Errorf("gles: the cut shader %+v: %w", v, err))
		return nil
	}
	p.owner.gl.Uniform1i(p.owner.gl.GetUniformLocation(prog.id, "cutTex"), 1)
	p.owner.cuts[v] = prog
	p.prog = nil
	return prog
}

func (p *pass) bindCut(prog *program) {
	if p.cut == nil {
		return
	}
	gl := p.owner.gl
	prog.cutAxes.set4(gl, p.cutU[0])
	prog.cutBase.set4(gl, p.cutU[1])
	if p.cutBound == p.cut.id {
		return
	}
	gl.ActiveTexture(gpu.Texture1)
	gl.BindTexture(p.cut.target, p.cut.id)
	if p.cut.filter == 0 {
		p.cut.filter = gpu.Linear
		gl.TexParameteri(p.cut.target, gpu.TextureMinFilter, gpu.Linear)
		gl.TexParameteri(p.cut.target, gpu.TextureMagFilter, gpu.Linear)
	}
	gl.ActiveTexture(gpu.Texture0)
	p.cutBound = p.cut.id
}
