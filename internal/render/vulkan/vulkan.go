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

var yuvFormats = []fourcc.Format{fourcc.NV12, fourcc.P010}

type Renderer struct {
	d    *lgpu.Device
	node uint64
	pass *pass
}

func Open(node uint64) (*Renderer, error) {
	d, err := lgpu.OpenNode(node)
	if err != nil {
		return nil, fmt.Errorf("vulkan: %w", err)
	}
	return &Renderer{d: d, node: node}, nil
}

func OpenDefault() (*Renderer, error) {
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

func (r *Renderer) Close() error {
	r.d.Close()
	return nil
}

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
	r *Renderer
	t *lgpu.Texture
}

func (t *texture) Size() image.Point {
	return t.t.Size()
}

func (t *texture) Update(dst image.Rectangle, stride int, data []byte) error {
	if err := render.CheckUpdate(t.Size(), dst, stride, len(data)); err != nil {
		return err
	}
	return t.t.Update(dst, stride, data)
}

func (t *texture) Destroy() error {
	t.t.Close()
	return nil
}

func (r *Renderer) NewTexture(format render.Format, size image.Point, stride int, data []byte) (render.Texture, error) {
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
	if err := t.Update(image.Rectangle{Max: size}, stride, data); err != nil {
		return nil, errors.Join(err, tex.Destroy())
	}
	return tex, nil
}

type Target struct {
	r *Renderer
	t *lgpu.Target
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
	t.t.Close()
	return nil
}

func (t *Target) Dmabuf() gpu.DMABUF {
	b := t.t.Dmabuf()
	planes := make([]gpu.Plane, len(b.Planes))
	for i, p := range b.Planes {
		planes[i] = gpu.Plane{FD: p.FD, Offset: p.Offset, Stride: p.Stride}
	}
	return gpu.DMABUF{Size: image.Pt(b.Width, b.Height), Format: b.Format, Modifier: b.Modifier, Planes: planes}
}

func (r *Renderer) NewTarget(size image.Point) (render.ReadTarget, error) {
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

func (r *Renderer) NewScanoutTarget(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (*Target, error) {
	t, err := r.d.NewTarget(size.X, size.Y, format, modifiers)
	if err != nil {
		return nil, err
	}
	return &Target{r: r, t: t}, nil
}

func (r *Renderer) RenderableModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return r.d.TargetModifiers(format)
}

func (r *Renderer) ImportTarget(b gpu.DMABUF) (render.ReadTarget, error) {
	t, err := r.d.ImportTarget(b.Dmabuf())
	if err != nil {
		return nil, err
	}
	return &Target{r: r, t: t}, nil
}

func ycbcrOf(e gpu.YUVEncoding) lgpu.YCbCr {
	var c lgpu.YCbCr
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
	all := append(r.d.TextureFormats(), yuvFormats...)
	for _, f := range all {
		mods, err := r.d.TextureModifiers(f)
		if err != nil {
			if slices.Contains(yuvFormats, f) {
				continue
			}
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

func (r *Renderer) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	t, ok := target.(*Target)
	if tt, isTT := target.(*TextureTarget); isTT {
		t, ok = tt.Target, true
	}
	switch {
	case !ok:
		return nil, fmt.Errorf("vulkan: cannot render into %T", target)
	case t.r != r:
		return nil, errors.New("vulkan: the target belongs to another renderer")
	case r.pass != nil:
		return nil, errors.New("vulkan: Begin while a pass is open")
	}
	p, err := r.d.Begin(t.t, damage)
	if err != nil {
		return nil, err
	}
	r.pass = &pass{r: r, p: p}
	return r.pass, nil
}

func rgba(c render.Color) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A}
}

func (p *pass) Clear(c render.Color) {
	p.p.Clear(rgba(c))
}

func (p *pass) FillRect(r image.Rectangle, c render.Color, m render.Mask) {
	p.p.FillRect(r, rgba(c), m)
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
	return errors.Join(p.err, err)
}
