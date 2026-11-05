// Package vulkan renders the compositor's frames with Vulkan through lys-lib's
// gpu package, the stack lys-ui and lys-shell draw with.
package vulkan

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"
	lgpu "github.com/stubbedev/lys-lib/gpu"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shader"
)

var formats = []struct {
	render render.Format
	fourcc fourcc.Format
}{
	{render.ARGB8888, fourcc.ARGB8888},
	{render.XRGB8888, fourcc.XRGB8888},
	{render.ABGR8888, fourcc.ABGR8888},
	{render.XBGR8888, fourcc.XBGR8888},
	{render.ARGB2101010, fourcc.ARGB2101010},
	{render.XRGB2101010, fourcc.XRGB2101010},
	{render.ABGR2101010, fourcc.ABGR2101010},
	{render.XBGR2101010, fourcc.XBGR2101010},
}

type Renderer struct {
	d      *lgpu.Device
	node   uint64
	pass   *pass
	idle   pass
	counts render.Counts
}

func (r *Renderer) Counts() render.Counts {
	counts := r.counts
	counts.GPUWaits = r.d.BlockingWaits()
	return counts
}

func Open(node uint64) (*Renderer, error) {
	d, err := lgpu.OpenNode(node)
	if err != nil {
		return nil, fmt.Errorf("vulkan: %w", err)
	}
	return &Renderer{d: d, node: node}, nil
}

func OpenDefault() (*Renderer, error) {
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil {
		return nil, fmt.Errorf("vulkan: list render nodes: %w", err)
	}
	slices.Sort(nodes)
	for _, n := range nodes {
		if r, err := OpenRenderNode(n); err == nil {
			return r, nil
		}
	}
	d, err := lgpu.Open()
	if err != nil {
		return nil, fmt.Errorf("vulkan: %w", err)
	}
	return &Renderer{d: d}, nil
}

func OpenRenderNode(path string) (*Renderer, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return nil, fmt.Errorf("vulkan: stat %s: %w", path, err)
	}
	return Open(st.Rdev)
}

func (r *Renderer) Device() *lgpu.Device {
	return r.d
}

func (r *Renderer) DeviceName() string {
	return r.d.Name()
}

func (r *Renderer) Close() error {
	r.d.Close()
	return nil
}

func (r *Renderer) Speed() render.Speed { return render.DriverSpeed(r.d.Name()) }

func (r *Renderer) Formats() []render.Format {
	out := make([]render.Format, len(formats))
	for i, f := range formats {
		out[i] = f.render
	}
	return out
}

func fourccOf(f render.Format) (fourcc.Format, error) {
	for _, e := range formats {
		if e.render == f {
			return e.fourcc, nil
		}
	}
	return 0, fmt.Errorf("vulkan: no texture format %d", f)
}

type texture struct {
	r       *Renderer
	t       *lgpu.Texture
	working bool
}

func (t *texture) Working() bool {
	return t.working
}

func (t *texture) Size() image.Point {
	return t.t.Size()
}

func (t *texture) Update(dst image.Rectangle, stride int, data []byte) error {
	t.r.counts.Uploads++
	if err := render.CheckUpdate(t.Size(), dst, stride, len(data)); err != nil {
		return err
	}
	return t.t.Update(dst, stride, data)
}

func (t *texture) Destroy() error {
	t.t.Close()
	return nil
}

func (r *Renderer) NewBlankTexture(format render.Format, size image.Point) (render.Texture, error) {
	f, err := fourccOf(format)
	if err != nil {
		return nil, err
	}
	t, err := r.d.NewTexture(f, size.X, size.Y)
	if err != nil {
		return nil, err
	}
	return &texture{r: r, t: t}, nil
}

func (r *Renderer) NewTexture(format render.Format, size image.Point, stride int, data []byte) (render.Texture, error) {
	r.counts.Uploads++
	f, err := fourccOf(format)
	if err != nil {
		return nil, err
	}
	if err := render.CheckPixels(size, stride, len(data)); err != nil {
		return nil, err
	}
	t, err := r.d.NewTexture(f, size.X, size.Y)
	if err != nil {
		return nil, err
	}
	tex := &texture{r: r, t: t}
	uploaded := false
	defer func() {
		if !uploaded {
			_ = tex.Destroy()
		}
	}()
	if err := t.Update(image.Rectangle{Max: size}, stride, data); err != nil {
		return nil, err
	}
	uploaded = true
	return tex, nil
}

type Target struct {
	r       *Renderer
	t       *lgpu.Target
	back    *lgpu.Readback
	reading *pendingRead
}

func (t *Target) Size() image.Point {
	return t.t.Size()
}

func (t *Target) Read(pix []byte, stride int) error {
	size := t.Size()
	if err := render.CheckPixels(size, stride, len(pix)); err != nil {
		return err
	}
	img, err := t.t.ReadPixels()
	if err != nil {
		return err
	}
	for y := range size.Y {
		src, dst := img.Pix[y*img.Stride:], pix[y*stride:]
		for x := range size.X {
			s, d := src[x*4:x*4+4], dst[x*4:x*4+4]
			d[0], d[1], d[2], d[3] = s[2], s[1], s[0], s[3]
		}
	}
	return nil
}

func (t *Target) Destroy() error {
	switch {
	case t.reading != nil:
		t.reading.t = nil
		t.reading = nil
	case t.back != nil:
		t.back.Close()
	}
	t.back = nil
	t.t.Close()
	return nil
}

func (r *Renderer) NewTarget(size image.Point) (render.ReadTarget, error) {
	r.counts.Targets++
	t, err := r.d.NewOffscreen(size.X, size.Y)
	if err != nil {
		return nil, err
	}
	return &Target{r: r, t: t}, nil
}

type TextureTarget struct {
	*Target
	tex *texture
}

func (t *TextureTarget) Texture() render.Texture {
	return t.tex
}

func (r *Renderer) NewTextureTarget(size image.Point) (render.TextureTarget, error) {
	r.counts.Targets++
	t, err := r.d.NewOffscreen(size.X, size.Y)
	if err != nil {
		return nil, err
	}
	tex, err := t.Texture()
	if err != nil {
		t.Close()
		return nil, err
	}
	return &TextureTarget{Target: &Target{r: r, t: t}, tex: &texture{r: r, t: tex}}, nil
}

func (r *Renderer) NewWorkingTarget(size image.Point) (render.TextureTarget, error) {
	r.counts.Targets++
	t, err := r.d.NewOffscreenFloat(size.X, size.Y)
	if err != nil {
		return nil, fmt.Errorf("vulkan: a %v working target: %w", size, err)
	}
	tex, err := t.Texture()
	if err != nil {
		t.Close()
		return nil, err
	}
	return &TextureTarget{Target: &Target{r: r, t: t}, tex: &texture{r: r, t: tex, working: true}}, nil
}

func (r *Renderer) RenderableModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return r.d.TargetModifiers(format)
}

func (r *Renderer) ExportModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return r.d.Modifiers(format)
}

func (r *Renderer) NewExportedTarget(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (render.ReadTarget, gpu.DMABUF, error) {
	t, err := r.d.NewTarget(size.X, size.Y, format, modifiers)
	if err != nil {
		return nil, gpu.DMABUF{}, fmt.Errorf("vulkan: a %v target to share in %v: %w", size, format, err)
	}
	return &Target{r: r, t: t}, gpu.FromDmabuf(t.Dmabuf()), nil
}

// LinearCopyTarget is a target whose frames reach a linear dmabuf by the
// copy engine, for a device that renders into no linear layout. Copy
// after each frame; Free releases all of it.
type LinearCopyTarget struct {
	Target render.ReadTarget
	DMABUF gpu.DMABUF
	Copy   func() error
	Free   func() error
}

func (r *Renderer) NewLinearCopyTarget(size image.Point, format fourcc.Format) (LinearCopyTarget, error) {
	shared, err := r.d.NewBuffer(size.X, size.Y, format, []fourcc.Modifier{fourcc.Linear})
	if err != nil {
		return LinearCopyTarget{}, fmt.Errorf("vulkan: a linear %v buffer to share: %w", size, err)
	}
	dst, err := r.d.ImportLinear(shared.Dmabuf())
	if err != nil {
		shared.Close()
		return LinearCopyTarget{}, fmt.Errorf("vulkan: copy into the linear %v buffer: %w", size, err)
	}
	src, err := r.d.NewOffscreen(size.X, size.Y)
	if err != nil {
		dst.Close()
		shared.Close()
		return LinearCopyTarget{}, err
	}
	all := []image.Rectangle{{Max: size}}
	target := &Target{r: r, t: src}
	return LinearCopyTarget{
		Target: target,
		DMABUF: gpu.FromDmabuf(shared.Dmabuf()),
		Copy:   func() error { return r.d.CopyToLinear(dst, src, all) },
		Free: func() error {
			err := target.Destroy()
			dst.Close()
			shared.Close()
			return err
		},
	}, nil
}

func (r *Renderer) ImportTarget(b gpu.DMABUF) (render.ReadTarget, error) {
	t, err := r.d.ImportTarget(b.Dmabuf())
	if err != nil {
		return nil, err
	}
	return &Target{r: r, t: t}, nil
}

func sitingOf(s gpu.Siting) lgpu.Siting {
	switch s {
	case gpu.SitedAt0:
		return lgpu.SitedAt0
	case gpu.SitedAtHalf:
		return lgpu.SitedAtHalf
	}
	return lgpu.SitingDefault
}

func ycbcrOf(e gpu.YUVEncoding) lgpu.YCbCr {
	c := lgpu.YCbCr{Chroma: lgpu.ChromaSiting{X: sitingOf(e.Chroma.Horizontal), Y: sitingOf(e.Chroma.Vertical)}}
	switch e.Coefficients {
	case gpu.BT709:
		c.Matrix = lgpu.BT709
	case gpu.BT2020:
		c.Matrix = lgpu.BT2020
	}
	if e.Range == gpu.FullRange {
		c.Range = lgpu.FullRange
	}
	return c
}

func (r *Renderer) ImportTexture(b gpu.DMABUF) (render.Texture, error) {
	t, err := r.d.ImportTextureYCbCr(b.Dmabuf(), ycbcrOf(b.YUV))
	if err != nil {
		return nil, err
	}
	return &texture{r: r, t: t}, nil
}

func (r *Renderer) CheckDMABUF(b gpu.DMABUF) error {
	t, err := r.ImportTexture(b)
	if err != nil {
		return err
	}
	return t.Destroy()
}

func (r *Renderer) DMABUFFormats() ([]gpu.FormatModifier, error) {
	if !r.d.Exports() {
		return nil, fmt.Errorf("%w: %s shares no dmabufs", gpu.ErrUnsupported, r.d.Name())
	}
	var out []gpu.FormatModifier
	for _, f := range r.d.ImportFormats() {
		mods, err := r.d.TextureModifiers(f)
		if err != nil {
			return nil, err
		}
		for _, m := range mods {
			out = append(out, gpu.FormatModifier{Format: f, Modifier: m})
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

func (r *Renderer) MainDevice() (uint64, error) {
	if r.node == 0 {
		return 0, fmt.Errorf("%w: %s was opened without a DRM node", gpu.ErrUnsupported, r.d.Name())
	}
	return r.node, nil
}

func (r *Renderer) RenderNode() (string, error) {
	if _, err := r.MainDevice(); err != nil {
		return "", err
	}
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		var st unix.Stat_t
		if unix.Stat(n, &st) == nil && sameGPU(n, r.node) {
			return n, nil
		}
	}
	return "", fmt.Errorf("%w: %s has no render node", gpu.ErrUnsupported, r.d.Name())
}

func sameGPU(renderNode string, node uint64) bool {
	device := func(major, minor uint32) string {
		p, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/dev/char/%d:%d/device", major, minor))
		if err != nil {
			return ""
		}
		return p
	}
	var st unix.Stat_t
	if unix.Stat(renderNode, &st) != nil {
		return false
	}
	a := device(unix.Major(st.Rdev), unix.Minor(st.Rdev))
	return a != "" && a == device(unix.Major(node), unix.Minor(node))
}

func (r *Renderer) NativeFences() bool {
	return r.d.SyncFiles()
}

func (r *Renderer) SyncFile() (int, error) {
	return r.d.SyncFile()
}

func (r *Renderer) WaitFence(fd int) error {
	return r.d.WaitSyncFile(fd)
}

func (r *Renderer) Finish() error {
	return r.d.Finish()
}

type pass struct {
	r   *Renderer
	p   *lgpu.Pass
	err error
}

func (r *Renderer) target(target render.Target) (*Target, error) {
	t, ok := target.(*Target)
	if tt, isTT := target.(*TextureTarget); isTT {
		t, ok = tt.Target, true
	}
	switch {
	case !ok:
		return nil, fmt.Errorf("vulkan: cannot render into %T", target)
	case t.r != r:
		return nil, errors.New("vulkan: the target belongs to another renderer")
	}
	return t, nil
}

func (r *Renderer) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	r.counts.Passes++
	t, err := r.target(target)
	if err != nil {
		return nil, err
	}
	if r.pass != nil {
		return nil, errors.New("vulkan: Begin while a pass is open")
	}
	p, err := r.d.Begin(t.t, damage)
	if err != nil {
		return nil, err
	}
	r.idle = pass{r: r, p: p}
	r.pass = &r.idle
	return r.pass, nil
}

func rgba(c render.Color) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A}
}

func (p *pass) SetColor(t *render.ColorTransform) {
	p.p.SetColor(t)
}

func (p *pass) Clear(c render.Color) {
	p.p.Clear(rgba(c))
}

func (p *pass) FillRect(r image.Rectangle, c render.Color, m render.Mask) {
	p.p.FillRect(r, rgba(c), m)
}

func (p *pass) FillGoo(r image.Rectangle, c render.Color, g render.Goo) {
	p.p.FillGoo(r, rgba(c), g)
}

func (p *pass) Kawase(t render.Texture, dst image.Rectangle, offset float32, up bool) {
	p.r.counts.Filters++
	tex, ok := t.(*texture)
	switch {
	case !ok || tex.r != p.r:
		p.fail(fmt.Errorf("vulkan: a texture of another renderer (%T)", t))
		return
	case p.err != nil:
		return
	}
	p.p.Kawase(tex.t, dst, offset, up)
}

func (p *pass) FillGradient(r image.Rectangle, g render.Gradient, m render.Mask) {
	tex, ok := g.Stops.(*texture)
	switch {
	case !ok || tex.r != p.r:
		p.fail(fmt.Errorf("vulkan: gradient stops of another renderer (%T)", g.Stops))
		return
	case p.err != nil:
		return
	}
	if err := render.CheckGradient(g, m); err != nil {
		p.fail(err)
		return
	}
	p.p.FillGradient(r, lgpu.Gradient{Stops: tex.t, From: g.From, To: g.To, Alpha: g.Alpha}, m)
}

func (p *pass) DrawTexture(t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform, alpha float32, m render.Mask) {
	tex, ok := t.(*texture)
	switch {
	case !ok || tex.r != p.r:
		p.fail(fmt.Errorf("vulkan: a texture of another renderer (%T)", t))
		return
	case p.err != nil:
		return
	}
	if err := render.CheckDraw(tex.Size(), src, transform, alpha, m); err != nil {
		p.fail(err)
		return
	}
	p.p.DrawTexture(tex.t, lgpu.FRect{X: src.X, Y: src.Y, W: src.W, H: src.H}, dst, lgpu.Transform(transform), alpha, m)
}

type screenShader struct {
	r *Renderer
	s *lgpu.FragmentShader
}

func (r *Renderer) NewScreenShader(p *shader.Program) (render.ScreenShader, error) {
	s, err := r.d.NewFragmentShader(p.SPIRV())
	if err != nil {
		return nil, fmt.Errorf("vulkan: %w", err)
	}
	return &screenShader{r: r, s: s}, nil
}

func (s *screenShader) Prepare(target render.Target) error {
	t, err := s.r.target(target)
	if err != nil {
		return err
	}
	return s.s.Prepare(t.t)
}

func (s *screenShader) Destroy() error {
	return s.s.Close()
}

func (p *pass) DrawScreenShader(s render.ScreenShader, t render.Texture, dst image.Rectangle, seconds float32) {
	sh, ok := s.(*screenShader)
	tex, texOK := t.(*texture)
	switch {
	case !ok || sh.r != p.r:
		p.fail(fmt.Errorf("vulkan: a screen shader of another renderer (%T)", s))
		return
	case !texOK || tex.r != p.r:
		p.fail(fmt.Errorf("vulkan: a texture of another renderer (%T)", t))
		return
	case p.err != nil:
		return
	}
	p.p.DrawFragmentShader(sh.s, tex.t, dst, [4]float32{seconds, float32(dst.Dx()), float32(dst.Dy())})
}

func (p *pass) DrawGlass(t render.Texture, src render.FRect, dst image.Rectangle, alpha float32, m render.Mask, g render.Glass) {
	tex, ok := t.(*texture)
	switch {
	case !ok || tex.r != p.r:
		p.fail(fmt.Errorf("vulkan: a texture of another renderer (%T)", t))
		return
	case p.err != nil:
		return
	}
	if err := render.CheckGlass(tex.Size(), src, alpha, m, g); err != nil {
		p.fail(err)
		return
	}
	p.p.DrawGlass(tex.t, lgpu.FRect{X: src.X, Y: src.Y, W: src.W, H: src.H}, dst, alpha, m, g)
}

func (p *pass) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *pass) End() error {
	if p.r.pass != p {
		return errors.New("vulkan: End of a pass already ended")
	}
	p.r.pass = nil
	err := p.p.End()
	p.r.counts.Submits++
	return errors.Join(p.err, err)
}
