// Package gles is the GLES2 renderer: shm textures, premultiplied blending,
// and passes into framebuffers backed by textures or dmabufs.
package gles

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/stubbedev/lys-lib/fourcc"
	lgpu "github.com/stubbedev/lys-lib/gpu"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
)

const vertexShader = `attribute vec2 corner;
uniform vec2 size;
uniform vec4 quad;
uniform vec2 uvOrigin;
uniform vec4 uvAxes;
varying vec2 v_texcoord;
void main() {
	gl_Position = vec4((quad.xy + corner * quad.zw) / size * 2.0 - 1.0, 0.0, 1.0);
	v_texcoord = uvOrigin + corner.x * uvAxes.xy + corner.y * uvAxes.zw;
}`

const (
	maskUniforms = `precision highp float;
uniform vec4 keep;
uniform vec4 cut;
uniform vec4 radii;
uniform vec4 merge;
uniform float smoothing;
`
	coverage    = "maskCoverage(gl_FragCoord.xy, keep, cut, radii, merge, smoothing)"
	textureMain = `uniform float alpha;
varying vec2 v_texcoord;
void main() { gl_FragColor = %s * (alpha * ` + coverage + `); }`
	glassMain = `uniform sampler2D tex;
uniform float alpha;
uniform vec4 adjust;
uniform vec4 bend;
varying vec2 v_texcoord;
void main() { gl_FragColor = %s * (alpha * ` + coverage + `); }`
)

var (
	maskHeader     = maskUniforms + lgpu.MaskGLSL
	solidShader    = maskHeader + "uniform vec4 color;\nvoid main() { gl_FragColor = color * " + coverage + "; }"
	rgbaShader     = maskHeader + "uniform sampler2D tex;\n" + fmt.Sprintf(textureMain, "texture2D(tex, v_texcoord)")
	rgbxShader     = maskHeader + "uniform sampler2D tex;\n" + fmt.Sprintf(textureMain, "vec4(texture2D(tex, v_texcoord).rgb, 1.0)")
	gradientShader = maskHeader + "uniform sampler2D tex;\nuniform vec4 line;\nuniform float stops;\nuniform float alpha;\nvoid main() { gl_FragColor = texture2D(tex, gradientUV(gl_FragCoord.xy, line, stops)) * (alpha * " + coverage + "); }"
	glassShader    = "#define SAMPLE texture2D\n" + maskHeader + lgpu.GlassGLSL + fmt.Sprintf(glassMain, "(glassAdjust(glassBackdrop(tex, v_texcoord, gl_FragCoord.xy, bend.yz, keep, radii.x, bend.x, adjust.w), adjust.xyz))")
	kawaseShader   = "#define SAMPLE texture2D\n" + maskHeader + "uniform sampler2D tex;\nuniform vec2 taps;\nuniform float up;\nvarying vec2 v_texcoord;\nvoid main() { gl_FragColor = up > 0.5 ? kawaseUp(tex, v_texcoord, taps) : kawaseDown(tex, v_texcoord, taps); }"
	gooShader      = maskHeader + "uniform vec4 color;\nvoid main() { gl_FragColor = color * gooCoverage(gl_FragCoord.xy, keep, merge, radii.xyz); }"
	externalShader = "#extension GL_OES_EGL_image_external : require\n" + maskHeader + "uniform samplerExternalOES tex;\n" + fmt.Sprintf(textureMain, "texture2D(tex, v_texcoord)")

	glassColorShader    = "#define SAMPLE texture2D\n" + colorHeader + lgpu.GlassGLSL + fmt.Sprintf(glassMain, "colorApply(glassAdjust(glassBackdrop(tex, v_texcoord, gl_FragCoord.xy, bend.yz, keep, radii.x, bend.x, adjust.w), adjust.xyz))")
	colorHeader         = maskHeader + "uniform vec4 cp[" + strconv.Itoa(lgpu.ColorUniforms) + "];\n#define CP cp\n" + lgpu.ColorGLSL
	rgbaColorShader     = colorHeader + "uniform sampler2D tex;\n" + fmt.Sprintf(textureMain, "colorApply(texture2D(tex, v_texcoord))")
	rgbxColorShader     = colorHeader + "uniform sampler2D tex;\n" + fmt.Sprintf(textureMain, "colorApply(vec4(texture2D(tex, v_texcoord).rgb, 1.0))")
	gradientColorShader = colorHeader + "uniform sampler2D tex;\nuniform vec4 line;\nuniform float stops;\nuniform float alpha;\nvoid main() { gl_FragColor = colorApply(texture2D(tex, gradientUV(gl_FragCoord.xy, line, stops))) * (alpha * " + coverage + "); }"
	externalColorShader = "#extension GL_OES_EGL_image_external : require\n" + colorHeader + "uniform samplerExternalOES tex;\n" + fmt.Sprintf(textureMain, "colorApply(texture2D(tex, v_texcoord))")
)

const float32Mantissa = 23

var unitQuad = [8]float32{0, 0, 1, 0, 0, 1, 1, 1}

var requiredGL = []string{"GL_EXT_read_format_bgra", "GL_EXT_texture_format_BGRA8888", "GL_NV_pack_subimage", "GL_OES_EGL_image"}

type uniform struct {
	loc   int32
	value [4]float32
}

func (u *uniform) set1(gl *gpu.GL, a float32) {
	if u.value[0] != a {
		u.value[0] = a
		gl.Uniform1f(u.loc, a)
	}
}

func (u *uniform) set2(gl *gpu.GL, a, b float32) {
	if u.value[0] != a || u.value[1] != b {
		u.value[0], u.value[1] = a, b
		gl.Uniform2f(u.loc, a, b)
	}
}

func (u *uniform) set4(gl *gpu.GL, v [4]float32) {
	if u.value != v {
		u.value = v
		gl.Uniform4f(u.loc, v[0], v[1], v[2], v[3])
	}
}

func rectUniform(r image.Rectangle) [4]float32 {
	return [4]float32{float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)}
}

type program struct {
	id                 uint32
	serial             uint64
	cutting            bool
	corner             int32
	size, color, alpha uniform
	quad               uniform
	uvOrigin, uvAxes   uniform
	keep, cut, radii   uniform
	line, stops        uniform
	taps, up, merge    uniform
	smoothing          uniform
	time, resolution   uniform
	adjust, bend       uniform
	cutAxes, cutBase   uniform
	cp                 [lgpu.ColorUniforms]int32
	transform          render.ColorTransform
	transformed        bool
}

func (r *Renderer) newProgram(id uint32) program {
	at := func(name string) uniform {
		return uniform{loc: r.gl.GetUniformLocation(id, name)}
	}
	r.programIDs++
	return program{
		id:         id,
		serial:     r.programIDs,
		corner:     r.gl.GetAttribLocation(id, "corner"),
		size:       at("size"),
		quad:       at("quad"),
		uvOrigin:   at("uvOrigin"),
		uvAxes:     at("uvAxes"),
		color:      at("color"),
		alpha:      at("alpha"),
		keep:       at("keep"),
		cut:        at("cut"),
		radii:      at("radii"),
		line:       at("line"),
		stops:      at("stops"),
		taps:       at("taps"),
		up:         at("up"),
		merge:      at("merge"),
		smoothing:  at("smoothing"),
		adjust:     at("adjust"),
		bend:       at("bend"),
		cutAxes:    at("cutAxes"),
		cutBase:    at("cutBase"),
		time:       at("time"),
		resolution: at("resolution"),
	}
}

type Renderer struct {
	counts       render.Counts
	drawnOps     uint64
	opaqueRuns   uint64
	nativeFences bool
	unflushed    bool
	d            *gpu.Display
	device       uint64
	gl           *gpu.GL
	name         string
	solid        program
	gradient     program
	kawase       program
	glass        program
	goo          program
	rgba         program
	rgbx         program
	external     program
	colored      coloredPrograms
	cuts         map[cutVariant]*program
	effects      render.Effects
	warmed       map[fourcc.Format]struct{}
	drawn        map[drawState]struct{}
	programIDs   uint64
	modifiers    map[fourcc.Format][]gpu.ModifierSupport
	vbo          uint32
	pass         pass
	closed       bool
	es3          bool
	async        bool
	unpack       uint32
	ringCap      int
	ringAt       int
	band         []byte
	floatTargets bool
	robust       bool
	lost         error
	textures     map[*texture]struct{}
	framebuffers map[*Framebuffer]struct{}
	shaders      map[*screenShader]struct{}
}

func (r *Renderer) enter() error {
	return r.note(r.d.Enter())
}

func (r *Renderer) leave(op string) error {
	return r.note(errors.Join(r.gl.Check(op), r.d.Leave()))
}

func (r *Renderer) note(err error) error {
	if !errors.Is(err, gpu.ErrContextLost) {
		return err
	}
	if r.lost == nil {
		r.lost = err
	}
	return nil
}

func (r *Renderer) Lost() error {
	if r.lost != nil || !r.robust {
		return r.lost
	}
	if err := r.enter(); err != nil {
		return err
	}
	r.counts.ResetChecks++
	status := r.gl.ResetStatus()
	if err := r.leave("gles: ask for a GPU reset"); err != nil {
		return err
	}
	if status != nil && r.lost == nil {
		r.lost = fmt.Errorf("gles: %w", status)
	}
	return r.lost
}

func (r *Renderer) LoseDevice() {
	r.lost = fmt.Errorf("gles: a GPU reset raised by LoseDevice: %w", gpu.ErrContextLost)
}

func (r *Renderer) Recover() error {
	if r.pass.active {
		return errors.New("gles: Recover during a pass")
	}
	if err := r.d.Renew(); err != nil {
		return fmt.Errorf("gles: renew the context lost to a GPU reset: %w", err)
	}
	r.lost = nil
	for _, p := range r.programs() {
		*p.prog = program{}
	}
	clear(r.cuts)
	clear(r.warmed)
	clear(r.drawn)
	r.pass.last = drawState{}
	r.vbo, r.unpack, r.ringCap, r.ringAt = 0, 0, 0, 0
	var errs []error
	for t := range r.textures {
		if t.imported {
			errs = append(errs, r.reimport(&t.img, t.dmabuf))
		}
	}
	for f := range r.framebuffers {
		if f.imported {
			errs = append(errs, r.reimport(&f.img, f.dmabuf))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("gles: import the dmabufs again after a GPU reset: %w", err)
	}
	gpu.Compiling()
	if err := r.enter(); err != nil {
		return err
	}
	errs = append(errs, r.setup())
	for t := range r.textures {
		t.make()
	}
	for f := range r.framebuffers {
		errs = append(errs, f.make())
	}
	for s := range r.shaders {
		errs = append(errs, s.link())
	}
	if err := errors.Join(append(errs, r.leave("gles: rebuild after a GPU reset"))...); err != nil {
		return err
	}
	for f := range r.framebuffers {
		if err := r.warm(f); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) reimport(img **gpu.Image, b gpu.DMABUF) error {
	var err error
	if *img != nil {
		err = (*img).Destroy()
	}
	fresh, ierr := r.d.ImportDMABUF(b)
	if ierr != nil {
		*img = nil
		return errors.Join(err, ierr)
	}
	*img = fresh
	r.counts.Imports++
	return err
}

func (r *Renderer) flushed() {
	if r.unflushed {
		r.unflushed = false
		r.counts.Submits++
	}
}

func New(d *gpu.Display) (*Renderer, error) {
	r := &Renderer{
		d: d, gl: d.GL(), modifiers: map[fourcc.Format][]gpu.ModifierSupport{},
		textures: map[*texture]struct{}{}, framebuffers: map[*Framebuffer]struct{}{}, shaders: map[*screenShader]struct{}{},
		cuts: map[cutVariant]*program{}, warmed: map[fourcc.Format]struct{}{}, drawn: map[drawState]struct{}{},
	}
	r.pass.owner = r
	gpu.Compiling()
	if err := r.start(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Renderer) start() error {
	if err := r.enter(); err != nil {
		return err
	}
	if err := errors.Join(r.setup(), r.leave("gles: set up")); err != nil {
		return errors.Join(err, r.Close())
	}
	if err := r.makeEffects(); err != nil {
		return errors.Join(err, r.Close())
	}
	return nil
}

type shader struct {
	prog      *program
	fragment  string
	extension string
	colored   bool
}

func (r *Renderer) programs() [13]shader {
	return [13]shader{
		{prog: &r.glass, fragment: glassShader},
		{prog: &r.colored.glass, fragment: glassColorShader, colored: true},
		{prog: &r.colored.rgba, fragment: rgbaColorShader, colored: true},
		{prog: &r.colored.rgbx, fragment: rgbxColorShader, colored: true},
		{prog: &r.colored.gradient, fragment: gradientColorShader, colored: true},
		{prog: &r.colored.external, fragment: externalColorShader, extension: "GL_OES_EGL_image_external", colored: true},
		{prog: &r.solid, fragment: solidShader},
		{prog: &r.rgba, fragment: rgbaShader},
		{prog: &r.rgbx, fragment: rgbxShader},
		{prog: &r.gradient, fragment: gradientShader},
		{prog: &r.kawase, fragment: kawaseShader},
		{prog: &r.goo, fragment: gooShader},
		{prog: &r.external, fragment: externalShader, extension: "GL_OES_EGL_image_external"},
	}
}

func (r *Renderer) setup() error {
	r.name = r.gl.Renderer()
	exts := strings.Fields(r.gl.Extensions())
	for _, ext := range requiredGL {
		if !slices.Contains(exts, ext) {
			return fmt.Errorf("gles: %s lacks %s", r.name, ext)
		}
	}
	if bits := r.gl.PrecisionBits(gpu.FragmentShader, gpu.HighFloat); bits < float32Mantissa {
		return fmt.Errorf("gles: %s has %d-bit highp floats in fragment shaders, fewer than float32's %d", r.name, bits, float32Mantissa)
	}
	for _, p := range r.programs() {
		if p.extension != "" && !slices.Contains(exts, p.extension) {
			continue
		}
		if err := r.link(p); err != nil {
			return err
		}
	}
	if err := r.linkCuts(r.external.id != 0); err != nil {
		return err
	}
	r.gl.GenBuffers(1, &r.vbo)
	r.gl.BindBuffer(gpu.ArrayBuffer, r.vbo)
	r.gl.BufferData(gpu.ArrayBuffer, unitQuad[:], gpu.StaticDraw)
	r.es3 = esMajor(r.gl.Version()) >= 3
	if r.es3 {
		r.gl.GenBuffers(1, &r.unpack)
	}
	r.async = r.es3 && r.d.Has("EGL_KHR_fence_sync")
	r.nativeFences = r.d.Has("EGL_ANDROID_native_fence_sync")
	r.floatTargets = r.es3 && (slices.Contains(exts, "GL_EXT_color_buffer_float") || slices.Contains(exts, "GL_EXT_color_buffer_half_float"))
	r.robust = r.d.Robust() && (slices.Contains(exts, "GL_KHR_robustness") || slices.Contains(exts, "GL_EXT_robustness"))
	return nil
}

func (r *Renderer) link(p shader) error {
	id, err := r.gl.Link(vertexShader, p.fragment)
	if err != nil {
		return err
	}
	r.counts.Programs++
	*p.prog = r.newProgram(id)
	if p.colored {
		for i := range p.prog.cp {
			p.prog.cp[i] = r.gl.GetUniformLocation(id, "cp["+strconv.Itoa(i)+"]")
		}
	}
	r.gl.UseProgram(id)
	r.gl.Uniform1i(r.gl.GetUniformLocation(id, "tex"), 0)
	return nil
}

func (r *Renderer) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	destroyErr := r.destroyEffects()
	if err := r.enter(); err != nil {
		return errors.Join(destroyErr, err)
	}
	r.gl.UseProgram(0)
	for _, p := range r.programs() {
		if p.prog.id != 0 {
			r.gl.DeleteProgram(p.prog.id)
			p.prog.id = 0
		}
	}
	for _, p := range r.cuts {
		r.gl.DeleteProgram(p.id)
	}
	clear(r.cuts)
	r.gl.DeleteBuffers(1, &r.unpack)
	r.gl.DeleteBuffers(1, &r.vbo)
	err := errors.Join(destroyErr, r.gl.Check("gles: close"), r.d.Release())
	runtime.UnlockOSThread()
	return err
}

func (r *Renderer) DeviceName() string {
	return r.name
}

func (r *Renderer) Finish() error {
	if r.lost != nil {
		return nil
	}
	if err := r.enter(); err != nil {
		return err
	}
	r.gl.Finish()
	r.flushed()
	r.counts.GPUWaits++
	return r.leave("gles: finish")
}

var (
	eightBitFormats = []render.Format{render.ARGB8888, render.XRGB8888, render.ABGR8888, render.XBGR8888}
	allFormats      = append(slices.Clone(eightBitFormats), render.ARGB2101010, render.XRGB2101010, render.ABGR2101010, render.XBGR2101010)
)

func (r *Renderer) Speed() render.Speed { return render.DriverSpeed(r.name) }

func (r *Renderer) Counts() render.Counts {
	return r.counts
}

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
	r        *Renderer
	id       uint32
	size     image.Point
	opaque   bool
	target   uint32
	img      *gpu.Image
	dmabuf   gpu.DMABUF
	imported bool
	filter   int32
	upload   pixelUpload
	drawn    *Framebuffer
}

func (t *texture) make() {
	gl := t.r.gl
	gl.GenTextures(1, &t.id)
	gl.BindTexture(t.target, t.id)
	gl.TexParameteri(t.target, gpu.TextureWrapS, gpu.ClampToEdge)
	gl.TexParameteri(t.target, gpu.TextureWrapT, gpu.ClampToEdge)
	t.filter = 0
	if t.imported {
		gl.EGLImageTargetTexture2D(t.target, t.img)
		return
	}
	if t.upload.swapRB {
		gl.TexParameteri(gpu.Texture2D, gpu.TextureSwizzleR, gpu.Blue)
		gl.TexParameteri(gpu.Texture2D, gpu.TextureSwizzleB, gpu.Red)
	}
	u := t.upload
	gl.TexImage2D(gpu.Texture2D, u.internal, int32(t.size.X), int32(t.size.Y), u.format, u.typ, nil) //nolint:gosec // texture sizes fit GLsizei
}

func (r *Renderer) build(t *texture, op string) error {
	r.textures[t] = struct{}{}
	if r.lost != nil {
		return nil
	}
	if err := r.enter(); err != nil {
		return errors.Join(err, t.Destroy())
	}
	t.make()
	if err := r.leave(op); err != nil {
		return errors.Join(err, t.Destroy())
	}
	return nil
}

func (r *Renderer) NewTexture(format render.Format, size image.Point, stride int, data []byte) (render.Texture, error) {
	r.counts.Uploads++
	if err := render.CheckPixels(size, stride, len(data)); err != nil {
		return nil, err
	}
	blank, err := r.newBlank(format, size)
	if err != nil {
		return nil, err
	}
	t, ok := blank.(*texture)
	if !ok {
		return nil, fmt.Errorf("gles: a new texture is a %T", blank)
	}
	if err := t.Update(image.Rectangle{Max: size}, stride, data); err != nil {
		return nil, errors.Join(err, t.Destroy())
	}
	return t, nil
}

func (r *Renderer) NewBlankTexture(format render.Format, size image.Point) (render.Texture, error) {
	return r.newBlank(format, size)
}

func (r *Renderer) newBlank(format render.Format, size image.Point) (render.Texture, error) {
	if !slices.Contains(r.Formats(), format) {
		return nil, fmt.Errorf("gles: unsupported texture format %d", format)
	}
	t := &texture{r: r, size: size, opaque: format.Opaque(), target: gpu.Texture2D, upload: uploadOf(format)}
	if err := r.build(t, "gles: new texture"); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *texture) Working() bool {
	return t.drawn != nil && t.drawn.float
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
	sampledAs := b.Modifier
	if sampledAs == fourcc.Invalid {
		sampledAs = fourcc.Linear
	}
	i := slices.IndexFunc(mods, func(m gpu.ModifierSupport) bool { return m.Modifier == sampledAs })
	return i >= 0 && mods[i].ExternalOnly, nil
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
		return "", fmt.Errorf("%w: %s has no render node", gpu.ErrUnsupported, r.name)
	}
	return node, nil
}

func (r *Renderer) SyncFile() (int, error) {
	if r.lost != nil {
		return -1, nil
	}
	if err := r.enter(); err != nil {
		return -1, err
	}
	fd := -1
	sync, err := r.d.Fence()
	if err == nil {
		r.flushed()
		fd, err = sync.FD()
		err = errors.Join(err, sync.Destroy())
	}
	if err = errors.Join(err, r.leave("gles: fence")); err != nil && fd >= 0 {
		return -1, errors.Join(err, unix.Close(fd))
	}
	return fd, err
}

func (r *Renderer) WaitFence(fd int) error {
	if r.lost != nil {
		return unix.Close(fd)
	}
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
	return r.nativeFences
}

func (r *Renderer) MainDevice() (uint64, error) {
	if r.device != 0 {
		return r.device, nil
	}
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
		return nil, fmt.Errorf("%w: %s samples format %v with modifier %v only through GL_OES_EGL_image_external", gpu.ErrUnsupported, r.name, b.Format, b.Modifier)
	}
	t := &texture{r: r, size: b.Size, target: gpu.Texture2D, dmabuf: b, imported: true}
	if external {
		t.target = gpu.TextureExternalOES
	}
	if r.lost == nil {
		if t.img, err = r.d.ImportDMABUF(b); err != nil {
			return nil, err
		}
		r.counts.Imports++
	}
	if err := r.build(t, "gles: import a dmabuf texture"); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *texture) Update(dst image.Rectangle, stride int, data []byte) (err error) {
	t.r.counts.Uploads++
	if t.drawn != nil {
		return errors.New("gles: a target's texture is drawn by a pass, not uploaded")
	}
	if _, live := t.r.textures[t]; !live {
		return errors.New("gles: update of a destroyed texture")
	}
	if t.imported {
		return errors.New("gles: a dmabuf texture is updated by its client, not uploaded")
	}
	if err := render.CheckUpdate(t.size, dst, stride, len(data)); err != nil {
		return err
	}
	if t.r.lost != nil {
		return nil
	}
	if err := t.r.enter(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, t.r.leave("gles: update texture")) }()
	t.r.gl.BindTexture(gpu.Texture2D, t.id)
	return t.r.upload(t.upload, dst, stride, data)
}

func (t *texture) Destroy() error {
	if _, live := t.r.textures[t]; !live || t.drawn != nil {
		return nil
	}
	delete(t.r.textures, t)
	var err error
	if t.r.lost == nil && t.id != 0 {
		if err = t.r.enter(); err == nil {
			t.r.gl.DeleteTextures(1, &t.id)
			err = t.r.leave("gles: destroy texture")
		}
	}
	t.id = 0
	if t.img != nil {
		err = errors.Join(err, t.img.Destroy())
		t.img = nil
	}
	return err
}

type Framebuffer struct {
	r        *Renderer
	size     image.Point
	fbo      uint32
	tex      uint32
	rb       uint32
	pbo      uint32
	reading  bool
	img      *gpu.Image
	dmabuf   gpu.DMABUF
	imported bool
	float    bool
	texture  *texture
}

func (f *Framebuffer) format() fourcc.Format {
	switch {
	case f.imported:
		return f.dmabuf.Format
	case f.float:
		return fourcc.ABGR16161616F
	}
	return fourcc.ABGR8888
}

func (f *Framebuffer) make() error {
	gl := f.r.gl
	f.pbo = 0
	if f.imported {
		gl.GenRenderbuffers(1, &f.rb)
		gl.BindRenderbuffer(gpu.Renderbuffer, f.rb)
		gl.EGLImageTargetRenderbuffer(f.img)
		gl.GenFramebuffers(1, &f.fbo)
		gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
		gl.FramebufferRenderbuffer(gpu.Framebuffer, gpu.ColorAttachment0, gpu.Renderbuffer, f.rb)
	} else {
		internal, typ := int32(gpu.RGBA), uint32(gpu.UnsignedByte)
		if f.float {
			internal, typ = gpu.RGBA16F, gpu.HalfFloat
		}
		gl.GenTextures(1, &f.tex)
		gl.BindTexture(gpu.Texture2D, f.tex)
		gl.TexParameteri(gpu.Texture2D, gpu.TextureWrapS, gpu.ClampToEdge)
		gl.TexParameteri(gpu.Texture2D, gpu.TextureWrapT, gpu.ClampToEdge)
		gl.TexImage2D(gpu.Texture2D, internal, int32(f.size.X), int32(f.size.Y), gpu.RGBA, typ, nil) //nolint:gosec // framebuffer sizes fit GLsizei
		gl.GenFramebuffers(1, &f.fbo)
		gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
		gl.FramebufferTexture2D(gpu.Framebuffer, gpu.ColorAttachment0, gpu.Texture2D, f.tex, 0)
		if f.texture != nil {
			f.texture.id, f.texture.filter = f.tex, 0
		}
	}
	status := gl.CheckFramebufferStatus(gpu.Framebuffer)
	if status == gpu.FramebufferComplete && f.float {
		gl.ClearColor(0, 0, 0, 0)
		gl.Clear(gpu.ColorBufferBit)
	}
	gl.BindFramebuffer(gpu.Framebuffer, 0)
	if status != gpu.FramebufferComplete {
		return fmt.Errorf("gles: %v framebuffer is incomplete: %#x", f.size, status)
	}
	return nil
}

func (r *Renderer) buildFramebuffer(f *Framebuffer) (*Framebuffer, error) {
	r.framebuffers[f] = struct{}{}
	if r.lost != nil {
		return f, nil
	}
	if err := r.enter(); err != nil {
		return nil, errors.Join(err, f.Destroy())
	}
	if err := errors.Join(f.make(), r.leave("gles: make a framebuffer")); err != nil {
		return nil, errors.Join(err, f.Destroy())
	}
	return f, nil
}

func (r *Renderer) NewFramebuffer(size image.Point) (*Framebuffer, error) {
	if size.X <= 0 || size.Y <= 0 {
		return nil, fmt.Errorf("gles: cannot make a %v framebuffer", size)
	}
	return r.buildFramebuffer(&Framebuffer{r: r, size: size})
}

func (r *Renderer) textureTarget(f *Framebuffer) *TextureTarget {
	f.texture = &texture{r: r, id: f.tex, size: f.size, target: gpu.Texture2D, drawn: f}
	return &TextureTarget{Framebuffer: f, tex: f.texture}
}

func (r *Renderer) NewTarget(size image.Point) (render.ReadTarget, error) {
	r.counts.Targets++
	return r.NewFramebuffer(size)
}

func (r *Renderer) NewWorkingTarget(size image.Point) (render.TextureTarget, error) {
	r.counts.Targets++
	switch {
	case !r.floatTargets:
		return nil, fmt.Errorf("%w: %s has no float render targets: they take GLES 3 and GL_EXT_color_buffer_float or GL_EXT_color_buffer_half_float", gpu.ErrUnsupported, r.name)
	case size.X <= 0 || size.Y <= 0:
		return nil, fmt.Errorf("gles: cannot make a %v working target", size)
	}
	f, err := r.buildFramebuffer(&Framebuffer{r: r, size: size, float: true})
	if err != nil {
		return nil, err
	}
	return r.textureTarget(f), nil
}

type TextureTarget struct {
	*Framebuffer
	tex *texture
}

func (t *TextureTarget) Texture() render.Texture {
	return t.tex
}

func (t *TextureTarget) Destroy() error {
	t.tex.id = 0
	return t.Framebuffer.Destroy()
}

func (r *Renderer) NewTextureTarget(size image.Point) (render.TextureTarget, error) {
	r.counts.Targets++
	f, err := r.NewFramebuffer(size)
	if err != nil {
		return nil, err
	}
	return r.textureTarget(f), nil
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
	f := &Framebuffer{r: r, size: b.Size, dmabuf: b, imported: true}
	if r.lost == nil {
		img, err := r.d.ImportDMABUF(b)
		if err != nil {
			return nil, err
		}
		f.img = img
		r.counts.Imports++
	}
	if _, err := r.buildFramebuffer(f); err != nil {
		return nil, err
	}
	if err := r.warm(f); err != nil {
		return nil, errors.Join(err, f.Destroy())
	}
	return f, nil
}

func (f *Framebuffer) Size() image.Point {
	return f.size
}

func (f *Framebuffer) Read(pix []byte, stride int) error {
	return f.ReadRows(pix, stride, render.AllRows(f.size))
}

func (f *Framebuffer) ReadRows(pix []byte, stride int, rows render.Rows) error {
	if f.float {
		return errors.New("gles: a working target's pixels are not read back")
	}
	if err := render.CheckRows(f.size, stride, len(pix), rows); err != nil {
		return err
	}
	if f.r.lost != nil {
		return nil
	}
	if err := f.r.enter(); err != nil {
		return err
	}
	gl := f.r.gl
	gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
	gl.PixelStorei(gpu.PackRowLengthNV, int32(stride/4))                                                               //nolint:gosec // bounded by CheckPixels
	gl.ReadPixels(0, int32(rows.From), int32(f.size.X), int32(rows.To-rows.From), gpu.BGRAExt, pix[rows.From*stride:]) //nolint:gosec // framebuffer sizes fit GLsizei
	f.r.flushed()
	gl.PixelStorei(gpu.PackRowLengthNV, 0)
	gl.BindFramebuffer(gpu.Framebuffer, 0)
	return f.r.leave("gles: read a framebuffer")
}

func (f *Framebuffer) Destroy() error {
	if _, live := f.r.framebuffers[f]; !live {
		return nil
	}
	delete(f.r.framebuffers, f)
	var err error
	if f.r.lost == nil {
		if err = f.r.enter(); err != nil {
			return err
		}
		gl := f.r.gl
		gl.DeleteFramebuffers(1, &f.fbo)
		gl.DeleteTextures(1, &f.tex)
		gl.DeleteRenderbuffers(1, &f.rb)
		gl.DeleteBuffers(1, &f.pbo)
		err = f.r.leave("gles: destroy a framebuffer")
	}
	f.fbo, f.tex, f.rb, f.pbo = 0, 0, 0, 0
	if f.img != nil {
		err = errors.Join(err, f.img.Destroy())
		f.img = nil
	}
	return err
}

type pass struct {
	blending bool
	warming  bool
	sampled  sampling
	last     drawState
	owner    *Renderer
	active   bool
	fb       *Framebuffer
	damage   region.Set
	err      error
	prog     *program
	color    *render.ColorTransform
	corner   int32
	cut      *texture
	cutU     lgpu.CutUniforms
	cutBound uint32
}

func (r *Renderer) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	r.counts.Passes++
	return r.begin(target, damage)
}

func framebufferOf(target render.Target) (*Framebuffer, bool) {
	if tt, ok := target.(*TextureTarget); ok {
		return tt.Framebuffer, true
	}
	fb, ok := target.(*Framebuffer)
	return fb, ok
}

func (r *Renderer) begin(target render.Target, damage render.Region) (render.Pass, error) {
	fb, ok := framebufferOf(target)
	switch {
	case !ok:
		return nil, fmt.Errorf("gles: cannot render into %T", target)
	case fb.r != r:
		return nil, errors.New("gles: the framebuffer belongs to another renderer")
	case r.pass.active:
		return nil, errors.New("gles: Begin while a pass is active")
	}
	if _, live := r.framebuffers[fb]; !live {
		return nil, errors.New("gles: Begin on a destroyed framebuffer")
	}
	if r.lost != nil {
		return render.LostPass{}, nil
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	p := &r.pass
	p.active, p.fb, p.err, p.prog, p.color, p.corner, p.cut, p.cutBound = true, fb, nil, nil, nil, -1, nil, 0
	p.blending = true
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
	p.owner.unflushed = true
	p.active, p.fb, p.warming = false, nil, false
	return errors.Join(p.err, p.owner.leave("gles: render pass"))
}

func (p *pass) setBlending(on bool) {
	if p.blending == on {
		return
	}
	p.blending = on
	if on {
		p.owner.gl.Enable(gpu.Blend)
		return
	}
	p.owner.opaqueRuns++
	p.owner.gl.Disable(gpu.Blend)
}

func (p *pass) culled(r image.Rectangle) bool {
	if p.warming {
		return false
	}
	for _, d := range p.damage.Rects() {
		if d.Overlaps(r) {
			return false
		}
	}
	return true
}

func (p *pass) use(prog *program) {
	gl := p.owner.gl
	p.owner.drawnOps++
	p.setBlending(true)
	if p.prog != prog {
		p.prog = prog
		gl.UseProgram(prog.id)
		if p.corner != prog.corner {
			p.corner = prog.corner
			gl.VertexAttribPointer(uint32(prog.corner), 2, gpu.Float, 0, 0, 0) //nolint:gosec // the vertex shader reads corner, so its location is not -1
			gl.EnableVertexAttribArray(uint32(prog.corner))                    //nolint:gosec // as above
		}
	}
	prog.size.set2(gl, float32(p.fb.size.X), float32(p.fb.size.Y))
}

func (p *pass) setMask(prog *program, m render.Mask) {
	gl := p.owner.gl
	prog.keep.set4(gl, rectUniform(m.Keep.Rect))
	prog.cut.set4(gl, rectUniform(m.Cut.Rect))
	prog.merge.set4(gl, rectUniform(m.Merge))
	prog.radii.set4(gl, [4]float32{m.Keep.Radius, m.Cut.Radius, m.Falloff.Range, m.Falloff.Power})
	prog.smoothing.set1(gl, m.Smooth)
}

type uvMap struct {
	origin [2]float32
	axes   [4]float32
}

var wholeTexture = uvMap{axes: [4]float32{1, 0, 0, 1}}

func textureMap(size image.Point, src render.FRect, transform render.Transform) uvMap {
	h, v := transform.Axes()
	at := func(cx, cy float64) [2]float32 {
		var s [2]float64
		s[h.Source] = flip(cx, h.Reversed)
		s[v.Source] = flip(cy, v.Reversed)
		return [2]float32{
			float32((src.X + float64(s[0]*src.W)) / float64(size.X)),
			float32((src.Y + float64(s[1]*src.H)) / float64(size.Y)),
		}
	}
	o, x, y := at(0, 0), at(1, 0), at(0, 1)
	return uvMap{origin: o, axes: [4]float32{x[0] - o[0], x[1] - o[1], y[0] - o[0], y[1] - o[1]}}
}

func (p *pass) setUV(m uvMap) {
	gl := p.owner.gl
	p.prog.uvOrigin.set2(gl, m.origin[0], m.origin[1])
	p.prog.uvAxes.set4(gl, m.axes)
}

func (p *pass) draw(dst image.Rectangle) {
	gl := p.owner.gl
	p.prog.quad.set4(gl, [4]float32{float32(dst.Min.X), float32(dst.Min.Y), float32(dst.Dx()), float32(dst.Dy())})
	p.noteState()
	if p.warming {
		gl.Scissor(0, 0, 0, 0)
		gl.DrawArrays(gpu.TriangleStrip, 0, 4)
		return
	}
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
	p.owner.counts.Clears++
	switch {
	case !p.active:
		p.fail(errors.New("gles: Clear outside a pass"))
		return
	case !c.Premultiplied():
		p.fail(fmt.Errorf("gles: color %+v is not premultiplied", c))
		return
	}
	if p.warming {
		return
	}
	gl := p.owner.gl
	v := p.fillColor(c)
	gl.ClearColor(v[0], v[1], v[2], v[3])
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
	if p.culled(r) {
		return
	}
	prog := &p.owner.solid
	if p.cut != nil {
		if prog = p.cutProgram(cutSolid); prog == nil {
			return
		}
	}
	p.use(prog)
	p.setBlending(c.A != 0xff || m != (render.Mask{}) || p.cut != nil)
	p.setMask(prog, m)
	p.bindCut(prog)
	prog.color.set4(p.owner.gl, p.fillColor(c))
	p.sampled = sampling{}
	p.draw(r)
}

func (p *pass) FillGoo(r image.Rectangle, c render.Color, g render.Goo) {
	switch {
	case !p.active:
		p.fail(errors.New("gles: FillGoo outside a pass"))
		return
	case !c.Premultiplied():
		p.fail(fmt.Errorf("gles: color %+v is not premultiplied", c))
		return
	case g.A.Rect.Empty() || g.B.Rect.Empty() || !(g.Smooth >= 0) || !(g.A.Radius >= 0) || !(g.B.Radius >= 0):
		p.fail(fmt.Errorf("gles: goo %+v is not two rounded rectangles with a smoothing of zero or more", g))
		return
	case r.Empty():
		return
	}
	if p.culled(r) {
		return
	}
	prog := &p.owner.goo
	gl := p.owner.gl
	p.use(prog)
	prog.keep.set4(gl, rectUniform(g.A.Rect))
	prog.merge.set4(gl, rectUniform(g.B.Rect))
	prog.radii.set4(gl, [4]float32{g.A.Radius, g.B.Radius, g.Smooth, 0})
	prog.color.set4(gl, p.fillColor(c))
	p.sampled = sampling{}
	p.draw(r)
}

func (p *pass) Kawase(t render.Texture, dst image.Rectangle, offset float32, up bool) {
	p.owner.counts.Filters++
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("gles: Kawase outside a pass"))
		return
	case !ok || tex.r != p.owner || tex.target != gpu.Texture2D:
		p.fail(fmt.Errorf("gles: %T is not this renderer's RGB texture", t))
		return
	case tex.id == 0:
		p.fail(errors.New("gles: texture used after Destroy"))
		return
	case tex.drawn == p.fb:
		p.fail(errors.New("gles: Kawase of the pass's own target"))
		return
	case !(offset >= 0) || math.IsInf(float64(offset), 0):
		p.fail(fmt.Errorf("gles: Kawase offset %v is not zero or more", offset))
		return
	case dst.Empty():
		return
	}
	prog := &p.owner.kawase
	gl := p.owner.gl
	p.use(prog)
	p.setBlending(false)
	prog.taps.set2(gl, 0.5/float32(tex.size.X)*offset, 0.5/float32(tex.size.Y)*offset)
	mode := float32(0)
	if up {
		mode = 1
	}
	prog.up.set1(gl, mode)
	p.bindLinear(tex)
	p.drawWhole(dst)
}

func (p *pass) bindLinear(tex *texture) {
	gl := p.owner.gl
	gl.BindTexture(tex.target, tex.id)
	if tex.filter != gpu.Linear {
		tex.filter = gpu.Linear
		gl.TexParameteri(tex.target, gpu.TextureMinFilter, gpu.Linear)
		gl.TexParameteri(tex.target, gpu.TextureMagFilter, gpu.Linear)
	}
	p.sampled = tex.sampling()
}

func (p *pass) drawWhole(dst image.Rectangle) {
	p.setUV(wholeTexture)
	p.draw(dst)
}

func (p *pass) FillGradient(r image.Rectangle, g render.Gradient, m render.Mask) {
	tex, ok := g.Stops.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("gles: FillGradient outside a pass"))
		return
	case !ok || tex.r != p.owner || tex.target != gpu.Texture2D:
		p.fail(fmt.Errorf("gles: gradient stops %T are not this renderer's RGB texture", g.Stops))
		return
	case tex.id == 0:
		p.fail(errors.New("gles: gradient stops used after Destroy"))
		return
	case tex.drawn == p.fb:
		p.fail(errors.New("gles: FillGradient with the pass's own target as stops"))
		return
	case r.Empty():
		return
	}
	if err := render.CheckGradient(g, m); err != nil {
		p.fail(err)
		return
	}
	if p.culled(r) {
		return
	}
	prog := p.textureProgram(&p.owner.gradient, &p.owner.colored.gradient)
	gl := p.owner.gl
	p.use(prog)
	p.setTransform(prog)
	p.setMask(prog, m)
	prog.line.set4(gl, [4]float32{g.From[0], g.From[1], g.To[0], g.To[1]})
	prog.stops.set1(gl, float32(tex.size.X))
	prog.alpha.set1(gl, g.Alpha)
	gl.BindTexture(tex.target, tex.id)
	if tex.filter != gpu.Linear {
		tex.filter = gpu.Linear
		gl.TexParameteri(tex.target, gpu.TextureMinFilter, gpu.Linear)
		gl.TexParameteri(tex.target, gpu.TextureMagFilter, gpu.Linear)
	}
	p.sampled = tex.sampling()
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
	case tex.drawn == p.fb:
		p.fail(errors.New("gles: DrawTexture of the pass's own target"))
		return
	case dst.Empty():
		return
	}
	if err := render.CheckDraw(tex.size, src, transform, alpha, m); err != nil {
		p.fail(err)
		return
	}
	if alpha == 0 || p.culled(dst) {
		return
	}
	prog := p.textureProgram(&p.owner.rgba, &p.owner.colored.rgba)
	switch {
	case tex.target == gpu.TextureExternalOES:
		prog = p.textureProgram(&p.owner.external, &p.owner.colored.external)
	case tex.opaque:
		prog = p.textureProgram(&p.owner.rgbx, &p.owner.colored.rgbx)
	}
	if p.cut != nil {
		switch {
		case tex.target == gpu.TextureExternalOES:
			p.fail(errors.New("gles: a cut DrawTexture of an external texture"))
			return
		case tex.opaque:
			prog = p.cutProgram(cutRGBX)
		default:
			prog = p.cutProgram(cutRGBA)
		}
	}
	if prog == nil {
		return
	}
	gl := p.owner.gl
	p.use(prog)
	p.setBlending(!tex.opaque || alpha != 1 || m != (render.Mask{}) || p.cut != nil)
	p.setTransform(prog)
	p.setMask(prog, m)
	p.bindCut(prog)
	prog.alpha.set1(gl, alpha)
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
	p.setUV(textureMap(tex.size, src, transform))
	p.sampled = tex.sampling()
	p.draw(dst)
}

type coloredPrograms struct {
	rgba, rgbx, gradient, external, glass program
}

func (p *pass) SetColor(t *render.ColorTransform) {
	p.color = t
}

func (p *pass) fillColor(c render.Color) [4]float32 {
	v := [4]float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255, float32(c.A) / 255}
	if p.color == nil {
		return v
	}
	return p.color.Apply(v)
}

func (p *pass) textureProgram(plain, colored *program) *program {
	if p.color == nil {
		return plain
	}
	return colored
}

func (p *pass) setTransform(prog *program) {
	if p.color == nil || prog.transformed && prog.transform == *p.color {
		return
	}
	prog.transformed, prog.transform = true, *p.color
	u := p.color.Uniforms()
	for i, v := range u {
		p.owner.gl.Uniform4f(prog.cp[i], v[0], v[1], v[2], v[3])
	}
}

func (p *pass) DrawGlass(t render.Texture, src render.FRect, dst image.Rectangle, alpha float32, m render.Mask, g render.Glass) {
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("gles: DrawGlass outside a pass"))
		return
	case !ok || tex.r != p.owner || tex.target != gpu.Texture2D:
		p.fail(fmt.Errorf("gles: %T is not this renderer's RGB texture", t))
		return
	case tex.id == 0:
		p.fail(errors.New("gles: texture used after Destroy"))
		return
	case tex.drawn == p.fb:
		p.fail(errors.New("gles: DrawGlass of the pass's own target"))
		return
	case dst.Empty():
		return
	}
	if err := render.CheckGlass(tex.size, src, alpha, m, g); err != nil {
		p.fail(err)
		return
	}
	if alpha == 0 || p.culled(dst) {
		return
	}
	prog := p.textureProgram(&p.owner.glass, &p.owner.colored.glass)
	if p.cut != nil {
		if prog = p.cutProgram(cutGlass); prog == nil {
			return
		}
	}
	gl := p.owner.gl
	p.use(prog)
	p.setTransform(prog)
	p.setMask(prog, g.Mask(m, dst))
	p.bindCut(prog)
	prog.alpha.set1(gl, alpha)
	var invBezel, amount float32
	if g.Refracts() {
		invBezel, amount = 1/g.Bezel, g.Amount
	}
	prog.adjust.set4(gl, [4]float32{g.Saturation, g.Brightness, g.Contrast, amount})
	prog.bend.set4(gl, [4]float32{invBezel, float32(src.W) / float32(dst.Dx()) / float32(tex.size.X), float32(src.H) / float32(dst.Dy()) / float32(tex.size.Y), 0})
	gl.BindTexture(tex.target, tex.id)
	if tex.filter != gpu.Linear {
		tex.filter = gpu.Linear
		gl.TexParameteri(tex.target, gpu.TextureMinFilter, gpu.Linear)
		gl.TexParameteri(tex.target, gpu.TextureMagFilter, gpu.Linear)
	}
	p.setUV(textureMap(tex.size, src, render.Normal))
	p.sampled = tex.sampling()
	p.draw(dst)
}

func flip(f float64, reversed bool) float64 {
	if reversed {
		return 1 - f
	}
	return f
}

// Name is GL_RENDERER.
func (r *Renderer) Name() string {
	return r.name
}
