// Package gles is the GLES2 renderer: shm textures, premultiplied blending,
// and passes into framebuffers backed by textures or dmabufs.
package gles

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"runtime"
	"slices"
	"strings"

	"github.com/stubbedev/lys-lib/fourcc"
	lgpu "github.com/stubbedev/lys-lib/gpu"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
)

const vertexShader = `attribute vec2 pos;
attribute vec2 texcoord;
uniform vec2 size;
varying vec2 v_texcoord;
void main() {
	gl_Position = vec4(pos / size * 2.0 - 1.0, 0.0, 1.0);
	v_texcoord = texcoord;
}`

const (
	maskUniforms = `#ifdef GL_FRAGMENT_PRECISION_HIGH
precision highp float;
#else
precision mediump float;
#endif
uniform vec4 keep;
uniform vec4 cut;
uniform vec2 radii;
`
	coverage    = "maskCoverage(gl_FragCoord.xy, keep, cut, radii)"
	textureMain = `uniform float alpha;
varying vec2 v_texcoord;
void main() { gl_FragColor = %s * (alpha * ` + coverage + `); }`
)

var (
	maskHeader     = maskUniforms + lgpu.MaskGLSL
	solidShader    = maskHeader + "uniform vec4 color;\nvoid main() { gl_FragColor = color * " + coverage + "; }"
	rgbaShader     = maskHeader + "uniform sampler2D tex;\n" + fmt.Sprintf(textureMain, "texture2D(tex, v_texcoord)")
	rgbxShader     = maskHeader + "uniform sampler2D tex;\n" + fmt.Sprintf(textureMain, "vec4(texture2D(tex, v_texcoord).rgb, 1.0)")
	externalShader = "#extension GL_OES_EGL_image_external : require\n" + maskHeader + "uniform samplerExternalOES tex;\n" + fmt.Sprintf(textureMain, "texture2D(tex, v_texcoord)")
)

var requiredGL = []string{"GL_EXT_read_format_bgra", "GL_EXT_texture_format_BGRA8888", "GL_EXT_unpack_subimage", "GL_NV_pack_subimage", "GL_OES_EGL_image"}

type program struct {
	id                 uint32
	pos, texcoord      int32
	size, color, alpha int32
	keep, cut, radii   int32
	sized              image.Point
	alphaSet           float32
	colorSet           render.Color
	colored            bool
	maskSet            render.Mask
	masked             bool
}

type Renderer struct {
	d         *gpu.Display
	gl        *gpu.GL
	solid     program
	rgba      program
	rgbx      program
	external  program
	modifiers map[fourcc.Format][]gpu.ModifierSupport
	vbo       uint32
	verts     [16]float32
	pass      pass
	closed    bool
	es3       bool
	async     bool
}

func (r *Renderer) enter() error {
	return r.d.Enter()
}

func (r *Renderer) leave(op string) error {
	return errors.Join(r.gl.Check(op), r.d.Leave())
}

func New(d *gpu.Display) (*Renderer, error) {
	r := &Renderer{d: d, gl: d.GL(), modifiers: map[fourcc.Format][]gpu.ModifierSupport{}}
	r.pass.owner = r
	if err := r.enter(); err != nil {
		return nil, err
	}
	if err := errors.Join(r.setup(), r.leave("gles: set up")); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return r, nil
}

func (r *Renderer) setup() error {
	exts := strings.Fields(r.gl.Extensions())
	for _, ext := range requiredGL {
		if !slices.Contains(exts, ext) {
			return fmt.Errorf("gles: %s lacks %s", r.gl.Renderer(), ext)
		}
	}
	programs := []struct {
		prog     *program
		fragment string
	}{{&r.solid, solidShader}, {&r.rgba, rgbaShader}, {&r.rgbx, rgbxShader}}
	if slices.Contains(exts, "GL_OES_EGL_image_external") {
		programs = append(programs, struct {
			prog     *program
			fragment string
		}{&r.external, externalShader})
	}
	for _, p := range programs {
		id, err := r.gl.Link(vertexShader, p.fragment)
		if err != nil {
			return err
		}
		*p.prog = program{
			id:       id,
			pos:      r.gl.GetAttribLocation(id, "pos"),
			texcoord: r.gl.GetAttribLocation(id, "texcoord"),
			size:     r.gl.GetUniformLocation(id, "size"),
			color:    r.gl.GetUniformLocation(id, "color"),
			alpha:    r.gl.GetUniformLocation(id, "alpha"),
			keep:     r.gl.GetUniformLocation(id, "keep"),
			cut:      r.gl.GetUniformLocation(id, "cut"),
			radii:    r.gl.GetUniformLocation(id, "radii"),
			alphaSet: -1,
		}
		r.gl.UseProgram(id)
		r.gl.Uniform1i(r.gl.GetUniformLocation(id, "tex"), 0)
	}
	r.gl.GenBuffers(1, &r.vbo)
	r.es3 = esMajor(r.gl.Version()) >= 3
	r.async = r.es3 && r.d.Has("EGL_KHR_fence_sync")
	return nil
}

func (r *Renderer) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if err := r.enter(); err != nil {
		return err
	}
	for _, p := range []program{r.solid, r.rgba, r.rgbx, r.external} {
		if p.id != 0 {
			r.gl.DeleteProgram(p.id)
		}
	}
	r.gl.DeleteBuffers(1, &r.vbo)
	err := errors.Join(r.gl.Check("gles: close"), r.d.Release())
	runtime.UnlockOSThread()
	return err
}

func (r *Renderer) Finish() error {
	if err := r.enter(); err != nil {
		return err
	}
	r.gl.Finish()
	return r.leave("gles: finish")
}

var (
	eightBitFormats = []render.Format{render.ARGB8888, render.XRGB8888, render.ABGR8888, render.XBGR8888}
	allFormats      = append(slices.Clone(eightBitFormats), render.ARGB2101010, render.XRGB2101010, render.ABGR2101010, render.XBGR2101010)
)

func (r *Renderer) Formats() []render.Format {
	if r.es3 {
		return allFormats
	}
	return eightBitFormats
}

type pixelUpload struct {
	internal    int32
	format, typ uint32
	swapRB      bool
}

func uploadOf(f render.Format) pixelUpload {
	switch {
	case f.TenBit():
		return pixelUpload{internal: gpu.RGB10A2, format: gpu.RGBA, typ: gpu.UnsignedInt2101010Rev, swapRB: !f.RGBOrder()}
	case f.RGBOrder():
		return pixelUpload{internal: gpu.RGBA, format: gpu.RGBA, typ: gpu.UnsignedByte}
	}
	return pixelUpload{internal: gpu.BGRAExt, format: gpu.BGRAExt, typ: gpu.UnsignedByte}
}

type texture struct {
	r      *Renderer
	id     uint32
	size   image.Point
	opaque bool
	target uint32
	img    *gpu.Image
	filter int32
	upload pixelUpload
}

func (r *Renderer) NewTexture(format render.Format, size image.Point, stride int, data []byte) (render.Texture, error) {
	if !slices.Contains(r.Formats(), format) {
		return nil, fmt.Errorf("gles: unsupported texture format %d", format)
	}
	if err := render.CheckPixels(size, stride, len(data)); err != nil {
		return nil, err
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	t := &texture{r: r, size: size, opaque: format.Opaque(), target: gpu.Texture2D, upload: uploadOf(format)}
	r.gl.GenTextures(1, &t.id)
	r.gl.BindTexture(gpu.Texture2D, t.id)
	r.gl.TexParameteri(gpu.Texture2D, gpu.TextureWrapS, gpu.ClampToEdge)
	r.gl.TexParameteri(gpu.Texture2D, gpu.TextureWrapT, gpu.ClampToEdge)
	if t.upload.swapRB {
		r.gl.TexParameteri(gpu.Texture2D, gpu.TextureSwizzleR, gpu.Blue)
		r.gl.TexParameteri(gpu.Texture2D, gpu.TextureSwizzleB, gpu.Red)
	}
	u := t.upload
	r.gl.PixelStorei(gpu.UnpackRowLengthExt, int32(stride/4))                                       //nolint:gosec // bounded by CheckPixels
	r.gl.TexImage2D(gpu.Texture2D, u.internal, int32(size.X), int32(size.Y), u.format, u.typ, data) //nolint:gosec // texture sizes fit GLsizei
	r.gl.PixelStorei(gpu.UnpackRowLengthExt, 0)
	if err := r.leave("gles: new texture"); err != nil {
		return nil, errors.Join(err, t.Destroy())
	}
	return t, nil
}

func (t *texture) Size() image.Point {
	return t.size
}

func (r *Renderer) supported(format fourcc.Format) ([]gpu.ModifierSupport, error) {
	if mods, ok := r.modifiers[format]; ok {
		return mods, nil
	}
	mods, err := r.d.DMABUFModifiers(format)
	if err != nil {
		return nil, err
	}
	r.modifiers[format] = mods
	return mods, nil
}

func (r *Renderer) externalOnly(b gpu.DMABUF) (bool, error) {
	mods, err := r.supported(b.Format)
	if err != nil {
		return false, err
	}
	for _, m := range mods {
		if m.Modifier == b.Modifier {
			return m.ExternalOnly, nil
		}
	}
	return false, nil
}

func (r *Renderer) DMABUFFormats() ([]gpu.FormatModifier, error) {
	formats, err := r.d.DMABUFFormats()
	if err != nil {
		return nil, err
	}
	var out []gpu.FormatModifier
	for _, f := range formats {
		mods, err := r.supported(f)
		if err != nil {
			return nil, err
		}
		out = append(out, gpu.FormatModifier{Format: f, Modifier: fourcc.Invalid})
		for _, m := range mods {
			out = append(out, gpu.FormatModifier{Format: f, Modifier: m.Modifier})
		}
	}
	slices.SortFunc(out, func(a, b gpu.FormatModifier) int {
		if a.Format != b.Format {
			return cmp.Compare(a.Format, b.Format)
		}
		return cmp.Compare(a.Modifier, b.Modifier)
	})
	return slices.Compact(out), nil
}

func (r *Renderer) RenderNode() (string, error) {
	dev, err := r.d.Device()
	if err != nil {
		return "", err
	}
	node := dev.RenderNode()
	if node == "" {
		return "", fmt.Errorf("%w: %s has no render node", gpu.ErrUnsupported, r.gl.Renderer())
	}
	return node, nil
}

func (r *Renderer) SyncFile() (int, error) {
	if err := r.enter(); err != nil {
		return -1, err
	}
	fd := -1
	sync, err := r.d.Fence()
	if err == nil {
		fd, err = sync.FD()
		err = errors.Join(err, sync.Destroy())
	}
	if err = errors.Join(err, r.leave("gles: fence")); err != nil && fd >= 0 {
		return -1, errors.Join(err, unix.Close(fd))
	}
	return fd, err
}

func (r *Renderer) WaitFence(fd int) error {
	if err := r.enter(); err != nil {
		return errors.Join(err, unix.Close(fd))
	}
	sync, err := r.d.ImportFence(fd)
	if err != nil {
		return errors.Join(err, unix.Close(fd), r.leave("gles: import a fence"))
	}
	return errors.Join(sync.Wait(), sync.Destroy(), r.leave("gles: wait for a fence"))
}

func (r *Renderer) NativeFences() bool {
	return r.d.Has("EGL_ANDROID_native_fence_sync")
}

func (r *Renderer) MainDevice() (uint64, error) {
	node, err := r.RenderNode()
	if err != nil {
		return 0, err
	}
	var st unix.Stat_t
	if err := unix.Stat(node, &st); err != nil {
		return 0, fmt.Errorf("gles: stat %s: %w", node, err)
	}
	return st.Rdev, nil
}

func (r *Renderer) CheckDMABUF(b gpu.DMABUF) error {
	t, err := r.ImportTexture(b)
	if err != nil {
		return err
	}
	return t.Destroy()
}

func (r *Renderer) ImportTexture(b gpu.DMABUF) (render.Texture, error) {
	external, err := r.externalOnly(b)
	if err != nil {
		return nil, err
	}
	if external && r.external.id == 0 {
		return nil, fmt.Errorf("%w: %s samples format %v with modifier %v only through GL_OES_EGL_image_external", gpu.ErrUnsupported, r.gl.Renderer(), b.Format, b.Modifier)
	}
	img, err := r.d.ImportDMABUF(b)
	if err != nil {
		return nil, err
	}
	if err := r.enter(); err != nil {
		return nil, errors.Join(err, img.Destroy())
	}
	t := &texture{r: r, size: b.Size, target: gpu.Texture2D, img: img}
	if external {
		t.target = gpu.TextureExternalOES
	}
	r.gl.GenTextures(1, &t.id)
	r.gl.BindTexture(t.target, t.id)
	r.gl.TexParameteri(t.target, gpu.TextureWrapS, gpu.ClampToEdge)
	r.gl.TexParameteri(t.target, gpu.TextureWrapT, gpu.ClampToEdge)
	r.gl.EGLImageTargetTexture2D(t.target, img)
	if err := r.leave("gles: import a dmabuf texture"); err != nil {
		return nil, errors.Join(err, t.Destroy())
	}
	return t, nil
}

func (t *texture) Update(dst image.Rectangle, stride int, data []byte) error {
	if t.id == 0 {
		return errors.New("gles: update of a destroyed texture")
	}
	if t.img != nil {
		return errors.New("gles: a dmabuf texture is updated by its client, not uploaded")
	}
	if err := render.CheckUpdate(t.size, dst, stride, len(data)); err != nil {
		return err
	}
	gl := t.r.gl
	if err := t.r.enter(); err != nil {
		return err
	}
	gl.BindTexture(gpu.Texture2D, t.id)
	gl.PixelStorei(gpu.UnpackRowLengthExt, int32(stride/4))                                                                                    //nolint:gosec // bounded by CheckUpdate
	gl.TexSubImage2D(gpu.Texture2D, int32(dst.Min.X), int32(dst.Min.Y), int32(dst.Dx()), int32(dst.Dy()), t.upload.format, t.upload.typ, data) //nolint:gosec // inside the texture
	gl.PixelStorei(gpu.UnpackRowLengthExt, 0)
	return t.r.leave("gles: update texture")
}

func (t *texture) Destroy() error {
	if t.id == 0 {
		return nil
	}
	err := t.r.enter()
	if err == nil {
		t.r.gl.DeleteTextures(1, &t.id)
		err = t.r.leave("gles: destroy texture")
	}
	t.id = 0
	if t.img != nil {
		err = errors.Join(err, t.img.Destroy())
		t.img = nil
	}
	return err
}

type Framebuffer struct {
	r       *Renderer
	size    image.Point
	fbo     uint32
	tex     uint32
	rb      uint32
	pbo     uint32
	reading bool
	img     *gpu.Image
}

func (r *Renderer) NewFramebuffer(size image.Point) (*Framebuffer, error) {
	if size.X <= 0 || size.Y <= 0 {
		return nil, fmt.Errorf("gles: cannot make a %v framebuffer", size)
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	f := &Framebuffer{r: r, size: size}
	r.gl.GenTextures(1, &f.tex)
	r.gl.BindTexture(gpu.Texture2D, f.tex)
	r.gl.TexImage2D(gpu.Texture2D, gpu.RGBA, int32(size.X), int32(size.Y), gpu.RGBA, gpu.UnsignedByte, nil) //nolint:gosec // framebuffer sizes fit GLsizei
	r.gl.GenFramebuffers(1, &f.fbo)
	r.gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
	r.gl.FramebufferTexture2D(gpu.Framebuffer, gpu.ColorAttachment0, gpu.Texture2D, f.tex, 0)
	return f, f.complete()
}

func (r *Renderer) NewTarget(size image.Point) (render.ReadTarget, error) {
	return r.NewFramebuffer(size)
}

func (r *Renderer) ImportTarget(b gpu.DMABUF) (render.ReadTarget, error) {
	return r.ImportFramebuffer(b)
}

func (r *Renderer) RenderableModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	mods, err := r.supported(format)
	if err != nil {
		return nil, err
	}
	out := make([]fourcc.Modifier, 0, len(mods))
	for _, m := range mods {
		if !m.ExternalOnly {
			out = append(out, m.Modifier)
		}
	}
	return out, nil
}

func (r *Renderer) ImportFramebuffer(b gpu.DMABUF) (*Framebuffer, error) {
	img, err := r.d.ImportDMABUF(b)
	if err != nil {
		return nil, err
	}
	if err := r.enter(); err != nil {
		return nil, errors.Join(err, img.Destroy())
	}
	f := &Framebuffer{r: r, size: b.Size, img: img}
	r.gl.GenRenderbuffers(1, &f.rb)
	r.gl.BindRenderbuffer(gpu.Renderbuffer, f.rb)
	r.gl.EGLImageTargetRenderbuffer(img)
	r.gl.GenFramebuffers(1, &f.fbo)
	r.gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
	r.gl.FramebufferRenderbuffer(gpu.Framebuffer, gpu.ColorAttachment0, gpu.Renderbuffer, f.rb)
	return f, f.complete()
}

func (f *Framebuffer) complete() error {
	status := f.r.gl.CheckFramebufferStatus(gpu.Framebuffer)
	f.r.gl.BindFramebuffer(gpu.Framebuffer, 0)
	err := f.r.leave("gles: make a framebuffer")
	if status != gpu.FramebufferComplete {
		err = errors.Join(fmt.Errorf("gles: %v framebuffer is incomplete: %#x", f.size, status), err)
	}
	if err != nil {
		return errors.Join(err, f.Destroy())
	}
	return nil
}

func (f *Framebuffer) Size() image.Point {
	return f.size
}

func (f *Framebuffer) Read(pix []byte, stride int) error {
	if err := render.CheckPixels(f.size, stride, len(pix)); err != nil {
		return err
	}
	if err := f.r.enter(); err != nil {
		return err
	}
	gl := f.r.gl
	gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
	gl.PixelStorei(gpu.PackRowLengthNV, int32(stride/4))                    //nolint:gosec // bounded by CheckPixels
	gl.ReadPixels(0, 0, int32(f.size.X), int32(f.size.Y), gpu.BGRAExt, pix) //nolint:gosec // framebuffer sizes fit GLsizei
	gl.PixelStorei(gpu.PackRowLengthNV, 0)
	gl.BindFramebuffer(gpu.Framebuffer, 0)
	return f.r.leave("gles: read a framebuffer")
}

func (f *Framebuffer) Destroy() error {
	if f.fbo == 0 && f.tex == 0 && f.rb == 0 && f.pbo == 0 && f.img == nil {
		return nil
	}
	if err := f.r.enter(); err != nil {
		return err
	}
	gl := f.r.gl
	gl.DeleteFramebuffers(1, &f.fbo)
	gl.DeleteTextures(1, &f.tex)
	gl.DeleteRenderbuffers(1, &f.rb)
	gl.DeleteBuffers(1, &f.pbo)
	f.fbo, f.tex, f.rb, f.pbo = 0, 0, 0, 0
	err := f.r.leave("gles: destroy a framebuffer")
	if f.img != nil {
		err = errors.Join(err, f.img.Destroy())
		f.img = nil
	}
	return err
}

type pass struct {
	owner  *Renderer
	active bool
	fb     *Framebuffer
	damage region.Set
	err    error
	prog   *program
}

func (r *Renderer) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	fb, ok := target.(*Framebuffer)
	switch {
	case !ok:
		return nil, fmt.Errorf("gles: cannot render into %T", target)
	case fb.r != r || fb.fbo == 0:
		return nil, errors.New("gles: the framebuffer belongs to another renderer or was destroyed")
	case r.pass.active:
		return nil, errors.New("gles: Begin while a pass is active")
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	p := &r.pass
	p.active, p.fb, p.err, p.prog = true, fb, nil, nil
	damage.ClipInto(&p.damage, image.Rectangle{Max: fb.size})
	gl := r.gl
	gl.BindFramebuffer(gpu.Framebuffer, fb.fbo)
	gl.Viewport(0, 0, int32(fb.size.X), int32(fb.size.Y)) //nolint:gosec // framebuffer sizes fit GLsizei
	gl.Enable(gpu.Blend)
	gl.BlendFunc(gpu.One, gpu.OneMinusSrcAlpha)
	gl.Enable(gpu.ScissorTest)
	gl.ActiveTexture(gpu.Texture0)
	gl.BindBuffer(gpu.ArrayBuffer, r.vbo)
	return p, nil
}

func (p *pass) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *pass) End() error {
	if !p.active {
		return errors.New("gles: End without an active pass")
	}
	gl := p.owner.gl
	gl.Disable(gpu.ScissorTest)
	gl.Disable(gpu.Blend)
	gl.BindFramebuffer(gpu.Framebuffer, 0)
	gl.Flush()
	p.active, p.fb = false, nil
	return errors.Join(p.err, p.owner.leave("gles: render pass"))
}

func (p *pass) use(prog *program) {
	gl := p.owner.gl
	if p.prog != prog {
		p.prog = prog
		gl.UseProgram(prog.id)
		for _, a := range [2]struct {
			loc    int32
			offset uintptr
		}{{prog.pos, 0}, {prog.texcoord, 8}} {
			if a.loc >= 0 {
				gl.VertexAttribPointer(uint32(a.loc), 2, gpu.Float, 0, 16, a.offset)
				gl.EnableVertexAttribArray(uint32(a.loc))
			}
		}
	}
	if prog.sized != p.fb.size {
		prog.sized = p.fb.size
		gl.Uniform2f(prog.size, float32(p.fb.size.X), float32(p.fb.size.Y))
	}
}

func (p *pass) setMask(prog *program, m render.Mask) {
	if prog.masked && prog.maskSet == m {
		return
	}
	prog.masked, prog.maskSet = true, m
	gl := p.owner.gl
	for _, u := range [2]struct {
		loc int32
		r   image.Rectangle
	}{{prog.keep, m.Keep.Rect}, {prog.cut, m.Cut.Rect}} {
		gl.Uniform4f(u.loc, float32(u.r.Min.X), float32(u.r.Min.Y), float32(u.r.Max.X), float32(u.r.Max.Y))
	}
	gl.Uniform2f(prog.radii, m.Keep.Radius, m.Cut.Radius)
}

func (p *pass) draw(dst image.Rectangle) {
	gl := p.owner.gl
	v := &p.owner.verts
	x0, y0, x1, y1 := float32(dst.Min.X), float32(dst.Min.Y), float32(dst.Max.X), float32(dst.Max.Y)
	v[0], v[1], v[4], v[5], v[8], v[9], v[12], v[13] = x0, y0, x1, y0, x0, y1, x1, y1
	gl.BufferData(gpu.ArrayBuffer, v[:], gpu.StreamDraw)
	for _, d := range p.damage.Rects() {
		clip := dst.Intersect(d)
		if clip.Empty() {
			continue
		}
		gl.Scissor(int32(clip.Min.X), int32(clip.Min.Y), int32(clip.Dx()), int32(clip.Dy())) //nolint:gosec // inside the framebuffer
		gl.DrawArrays(gpu.TriangleStrip, 0, 4)
	}
}

func (p *pass) Clear(c render.Color) {
	switch {
	case !p.active:
		p.fail(errors.New("gles: Clear outside a pass"))
		return
	case !c.Premultiplied():
		p.fail(fmt.Errorf("gles: color %+v is not premultiplied", c))
		return
	}
	gl := p.owner.gl
	gl.ClearColor(float32(c.R)/255, float32(c.G)/255, float32(c.B)/255, float32(c.A)/255)
	for _, d := range p.damage.Rects() {
		gl.Scissor(int32(d.Min.X), int32(d.Min.Y), int32(d.Dx()), int32(d.Dy())) //nolint:gosec // inside the framebuffer
		gl.Clear(gpu.ColorBufferBit)
	}
}

func (p *pass) FillRect(r image.Rectangle, c render.Color, m render.Mask) {
	switch {
	case !p.active:
		p.fail(errors.New("gles: FillRect outside a pass"))
		return
	case !c.Premultiplied():
		p.fail(fmt.Errorf("gles: color %+v is not premultiplied", c))
		return
	case r.Empty():
		return
	}
	if err := m.Check(); err != nil {
		p.fail(err)
		return
	}
	prog := &p.owner.solid
	p.use(prog)
	p.setMask(prog, m)
	if !prog.colored || prog.colorSet != c {
		prog.colored, prog.colorSet = true, c
		p.owner.gl.Uniform4f(prog.color, float32(c.R)/255, float32(c.G)/255, float32(c.B)/255, float32(c.A)/255)
	}
	p.draw(r)
}

func (p *pass) DrawTexture(t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform, alpha float32, m render.Mask) {
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("gles: DrawTexture outside a pass"))
		return
	case !ok || tex.r != p.owner:
		p.fail(fmt.Errorf("gles: texture %T belongs to another renderer", t))
		return
	case tex.id == 0:
		p.fail(errors.New("gles: texture used after Destroy"))
		return
	case dst.Empty():
		return
	}
	if err := render.CheckDraw(tex.size, src, transform, alpha, m); err != nil {
		p.fail(err)
		return
	}
	if alpha == 0 {
		return
	}
	prog := &p.owner.rgba
	switch {
	case tex.target == gpu.TextureExternalOES:
		prog = &p.owner.external
	case tex.opaque:
		prog = &p.owner.rgbx
	}
	gl := p.owner.gl
	p.use(prog)
	p.setMask(prog, m)
	if prog.alphaSet != alpha {
		prog.alphaSet = alpha
		gl.Uniform1f(prog.alpha, alpha)
	}
	gl.BindTexture(tex.target, tex.id)
	filter := int32(gpu.Linear)
	if transform.Size(image.Pt(int(src.W), int(src.H))) == dst.Size() && src.W == float64(int(src.W)) && src.H == float64(int(src.H)) {
		filter = gpu.Nearest
	}
	if tex.filter != filter {
		tex.filter = filter
		gl.TexParameteri(tex.target, gpu.TextureMinFilter, filter)
		gl.TexParameteri(tex.target, gpu.TextureMagFilter, filter)
	}
	h, v := transform.Axes()
	verts := &p.owner.verts
	for i, corner := range [4][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		var s [2]float64
		s[h.Source] = flip(corner[0], h.Reversed)
		s[v.Source] = flip(corner[1], v.Reversed)
		verts[i*4+2] = float32((src.X + s[0]*src.W) / float64(tex.size.X))
		verts[i*4+3] = float32((src.Y + s[1]*src.H) / float64(tex.size.Y))
	}
	p.draw(dst)
}

func flip(f float64, reversed bool) float64 {
	if reversed {
		return 1 - f
	}
	return f
}
