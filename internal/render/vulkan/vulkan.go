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
	d        *lgpu.Device
	node     uint64
	pass     *pass
	idle     pass
	counts   render.Counts
	waits    uint64
	submits  uint64
	lost     error
	textures map[*texture]struct{}
	targets  map[*Target]struct{}
	linears  map[*linearCopy]struct{}
	shaders  map[*screenShader]struct{}
}

func newRenderer(d *lgpu.Device, node uint64) *Renderer {
	return &Renderer{
		d: d, node: node,
		textures: map[*texture]struct{}{}, targets: map[*Target]struct{}{}, linears: map[*linearCopy]struct{}{}, shaders: map[*screenShader]struct{}{},
	}
}

func (r *Renderer) Counts() render.Counts {
	counts := r.counts
	counts.GPUWaits = r.waits + r.d.BlockingWaits()
	counts.Submits = r.submits + r.d.Submissions()
	return counts
}

func (r *Renderer) note(err error) error {
	if !errors.Is(err, lgpu.ErrDeviceLost) {
		return err
	}
	if r.lost == nil {
		r.lost = err
	}
	return nil
}

func (r *Renderer) Lost() error {
	return r.lost
}

func (r *Renderer) LoseDevice() {
	r.lost = fmt.Errorf("vulkan: a device loss raised by LoseDevice: %w", lgpu.ErrDeviceLost)
}

func (r *Renderer) open() (*lgpu.Device, error) {
	if r.node == 0 {
		return lgpu.Open()
	}
	return lgpu.OpenNode(r.node)
}

func (r *Renderer) Recover() error {
	if r.pass != nil {
		return errors.New("vulkan: Recover during a pass")
	}
	for t := range r.targets {
		if err := t.keepDmabuf(); err != nil {
			return err
		}
		t.dropReads()
	}
	for l := range r.linears {
		if err := l.keepDmabuf(); err != nil {
			return err
		}
		l.dst.Close()
	}
	for s := range r.shaders {
		if s.s == nil {
			continue
		}
		if err := s.s.Close(); err != nil {
			return fmt.Errorf("vulkan: close a screen shader of the lost device: %w", err)
		}
	}
	r.waits += r.d.BlockingWaits()
	r.submits += r.d.Submissions()
	r.d.Close()
	d, err := r.open()
	if err != nil {
		return fmt.Errorf("vulkan: a new device after a device loss: %w", err)
	}
	r.d, r.lost = d, nil
	var errs []error
	for t := range r.textures {
		errs = append(errs, t.make())
	}
	for t := range r.targets {
		errs = append(errs, t.make())
	}
	for l := range r.linears {
		errs = append(errs, l.make())
	}
	for s := range r.shaders {
		errs = append(errs, s.make())
	}
	return errors.Join(errs...)
}

func Open(node uint64) (*Renderer, error) {
	d, err := lgpu.OpenNode(node)
	if err != nil {
		return nil, fmt.Errorf("vulkan: %w", err)
	}
	return newRenderer(d, node), nil
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
	return newRenderer(d, 0), nil
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

func (r *Renderer) CopyToLinear(dst *lgpu.Linear, src *lgpu.Target, rects []image.Rectangle) error {
	if r.lost != nil {
		return nil
	}
	return r.note(r.d.CopyToLinear(dst, src, rects))
}

func (r *Renderer) DeviceName() string {
	return r.d.Name()
}

func (r *Renderer) Close() error {
	errs := make([]error, 0, len(r.targets)+len(r.linears))
	for t := range r.targets {
		errs = append(errs, t.closeDmabuf())
	}
	for l := range r.linears {
		errs = append(errs, l.closeDmabuf())
	}
	r.d.Close()
	return errors.Join(errs...)
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
	r        *Renderer
	t        *lgpu.Texture
	working  bool
	format   fourcc.Format
	size     image.Point
	dmabuf   gpu.DMABUF
	imported bool
}

func (t *texture) make() error {
	r := t.r
	var err error
	if t.imported {
		t.t, err = r.d.ImportTextureYCbCr(t.dmabuf.Dmabuf(), ycbcrOf(t.dmabuf.YUV))
		r.counts.Imports++
	} else {
		t.t, err = r.d.NewTexture(t.format, t.size.X, t.size.Y)
	}
	return r.note(err)
}

func (r *Renderer) build(t *texture) (*texture, error) {
	if r.lost != nil {
		r.textures[t] = struct{}{}
		return t, nil
	}
	if err := t.make(); err != nil {
		return nil, err
	}
	r.textures[t] = struct{}{}
	return t, nil
}

func (t *texture) Working() bool {
	return t.working
}

func (t *texture) Size() image.Point {
	return t.size
}

func (t *texture) Update(dst image.Rectangle, stride int, data []byte) error {
	t.r.counts.Uploads++
	if err := render.CheckUpdate(t.Size(), dst, stride, len(data)); err != nil {
		return err
	}
	return t.update(dst, stride, data)
}

func (t *texture) update(dst image.Rectangle, stride int, data []byte) error {
	if t.r.lost != nil {
		return nil
	}
	return t.r.note(t.t.Update(dst, stride, data))
}

func (t *texture) Destroy() error {
	delete(t.r.textures, t)
	if t.t != nil {
		t.t.Close()
	}
	return nil
}

func (r *Renderer) NewBlankTexture(format render.Format, size image.Point) (render.Texture, error) {
	f, err := fourccOf(format)
	if err != nil {
		return nil, err
	}
	return r.build(&texture{r: r, format: f, size: size})
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
	tex, err := r.build(&texture{r: r, format: f, size: size})
	if err != nil {
		return nil, err
	}
	if err := tex.update(image.Rectangle{Max: size}, stride, data); err != nil {
		return nil, errors.Join(err, tex.Destroy())
	}
	return tex, nil
}

type targetKind uint8

const (
	offscreenTarget targetKind = iota
	floatTarget
	importedTarget
	exportedTarget
)

type Target struct {
	r       *Renderer
	t       *lgpu.Target
	back    *lgpu.Readback
	reading *pendingRead
	kind    targetKind
	size    image.Point
	dmabuf  gpu.DMABUF
	kept    *gpu.Buffer
	tex     *texture
}

func (t *Target) make() error {
	r := t.r
	var err error
	switch t.kind {
	case offscreenTarget:
		t.t, err = r.d.NewOffscreen(t.size.X, t.size.Y)
	case floatTarget:
		t.t, err = r.d.NewOffscreenFloat(t.size.X, t.size.Y)
	case importedTarget, exportedTarget:
		t.t, err = r.d.ImportTarget(t.dmabuf.Dmabuf())
		r.counts.Imports++
	}
	if err != nil {
		return r.note(err)
	}
	if t.tex == nil {
		return nil
	}
	if t.tex.t, err = t.t.Texture(); err != nil {
		t.t.Close()
		return r.note(err)
	}
	return nil
}

func (t *Target) keepDmabuf() error {
	if t.kind != exportedTarget || t.kept != nil || t.t == nil {
		return nil
	}
	kept, err := gpu.FromDmabuf(t.t.Dmabuf()).Dup()
	if err != nil {
		return fmt.Errorf("vulkan: keep the %v exported target's dmabuf for the next device: %w", t.size, err)
	}
	t.dmabuf, t.kept = kept.DMABUF, kept
	return nil
}

func (t *Target) closeDmabuf() error {
	return closeKept(&t.kept)
}

func closeKept(kept **gpu.Buffer) error {
	b := *kept
	if b == nil {
		return nil
	}
	*kept = nil
	return b.Close()
}

func (t *Target) dropReads() {
	if t.reading != nil {
		t.reading.t = nil
		t.reading = nil
	}
	if t.back != nil {
		t.back.Close()
		t.back = nil
	}
}

func (r *Renderer) buildTarget(t *Target) (*Target, error) {
	if r.lost != nil {
		r.targets[t] = struct{}{}
		return t, nil
	}
	if err := t.make(); err != nil {
		return nil, err
	}
	r.targets[t] = struct{}{}
	return t, nil
}

func (t *Target) Size() image.Point {
	return t.size
}

func (t *Target) Read(pix []byte, stride int) error {
	return t.ReadRows(pix, stride, render.AllRows(t.Size()))
}

func (t *Target) ReadRows(pix []byte, stride int, rows render.Rows) error {
	size := t.Size()
	if err := render.CheckRows(size, stride, len(pix), rows); err != nil {
		return err
	}
	if t.r.lost != nil {
		return nil
	}
	img, err := t.t.ReadRect(image.Rect(0, rows.From, size.X, rows.To))
	if err != nil {
		return t.r.note(err)
	}
	for y := rows.From; y < rows.To; y++ {
		src, dst := img.Pix[(y-rows.From)*img.Stride:], pix[y*stride:]
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
	delete(t.r.targets, t)
	if t.t != nil {
		t.t.Close()
	}
	return t.closeDmabuf()
}

func (r *Renderer) NewTarget(size image.Point) (render.ReadTarget, error) {
	r.counts.Targets++
	return r.buildTarget(&Target{r: r, kind: offscreenTarget, size: size})
}

type TextureTarget struct {
	*Target
	tex *texture
}

func (t *TextureTarget) Texture() render.Texture {
	return t.tex
}

func (r *Renderer) textureTarget(kind targetKind, size image.Point) (*TextureTarget, error) {
	tex := &texture{r: r, working: kind == floatTarget, size: size}
	t, err := r.buildTarget(&Target{r: r, kind: kind, size: size, tex: tex})
	if err != nil {
		return nil, err
	}
	return &TextureTarget{Target: t, tex: tex}, nil
}

func (r *Renderer) NewTextureTarget(size image.Point) (render.TextureTarget, error) {
	r.counts.Targets++
	return r.textureTarget(offscreenTarget, size)
}

func (r *Renderer) NewWorkingTarget(size image.Point) (render.TextureTarget, error) {
	r.counts.Targets++
	t, err := r.textureTarget(floatTarget, size)
	if err != nil {
		return nil, fmt.Errorf("vulkan: a %v working target: %w", size, err)
	}
	return t, nil
}

func (r *Renderer) RenderableModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return r.d.TargetModifiers(format)
}

func (r *Renderer) ExportModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return r.d.Modifiers(format)
}

func (r *Renderer) NewExportedTarget(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (render.ReadTarget, gpu.DMABUF, error) {
	if r.lost != nil {
		return nil, gpu.DMABUF{}, fmt.Errorf("vulkan: a %v target to share while the device is lost: %w", size, r.lost)
	}
	t, err := r.d.NewTarget(size.X, size.Y, format, modifiers)
	if err != nil {
		return nil, gpu.DMABUF{}, fmt.Errorf("vulkan: a %v target to share in %v: %w", size, format, r.note(err))
	}
	target := &Target{r: r, t: t, kind: exportedTarget, size: size}
	r.targets[target] = struct{}{}
	return target, gpu.FromDmabuf(t.Dmabuf()), nil
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

type linearCopy struct {
	r      *Renderer
	target *Target
	shared *lgpu.Buffer
	dmabuf gpu.DMABUF
	kept   *gpu.Buffer
	dst    *lgpu.Linear
	all    []image.Rectangle
}

func (l *linearCopy) make() error {
	dst, err := l.r.d.ImportLinear(l.dmabuf.Dmabuf())
	if err != nil {
		return fmt.Errorf("vulkan: copy into the linear %v buffer: %w", l.all[0].Size(), l.r.note(err))
	}
	l.dst = dst
	return nil
}

func (l *linearCopy) keepDmabuf() error {
	if l.kept != nil {
		return nil
	}
	kept, err := gpu.FromDmabuf(l.shared.Dmabuf()).Dup()
	if err != nil {
		return fmt.Errorf("vulkan: keep the linear %v buffer's dmabuf for the next device: %w", l.all[0].Size(), err)
	}
	l.shared.Close()
	l.shared, l.dmabuf, l.kept = nil, kept.DMABUF, kept
	return nil
}

func (l *linearCopy) closeDmabuf() error {
	return closeKept(&l.kept)
}

func (l *linearCopy) copy() error {
	if l.r.lost != nil {
		return nil
	}
	return l.r.note(l.r.d.CopyToLinear(l.dst, l.target.t, l.all))
}

func (l *linearCopy) free() error {
	delete(l.r.linears, l)
	err := l.target.Destroy()
	l.dst.Close()
	if l.shared != nil {
		l.shared.Close()
	}
	return errors.Join(err, l.closeDmabuf())
}

func (r *Renderer) NewLinearCopyTarget(size image.Point, format fourcc.Format) (LinearCopyTarget, error) {
	if r.lost != nil {
		return LinearCopyTarget{}, fmt.Errorf("vulkan: a linear %v buffer to share while the device is lost: %w", size, r.lost)
	}
	shared, err := r.d.NewBuffer(size.X, size.Y, format, []fourcc.Modifier{fourcc.Linear})
	if err != nil {
		return LinearCopyTarget{}, fmt.Errorf("vulkan: a linear %v buffer to share: %w", size, r.note(err))
	}
	l := &linearCopy{r: r, shared: shared, dmabuf: gpu.FromDmabuf(shared.Dmabuf()), all: []image.Rectangle{{Max: size}}}
	if err := l.make(); err != nil {
		shared.Close()
		return LinearCopyTarget{}, err
	}
	if l.target, err = r.buildTarget(&Target{r: r, kind: offscreenTarget, size: size}); err != nil {
		l.dst.Close()
		shared.Close()
		return LinearCopyTarget{}, err
	}
	r.linears[l] = struct{}{}
	return LinearCopyTarget{Target: l.target, DMABUF: l.dmabuf, Copy: l.copy, Free: l.free}, nil
}

func (r *Renderer) ImportTarget(b gpu.DMABUF) (render.ReadTarget, error) {
	return r.buildTarget(&Target{r: r, kind: importedTarget, size: b.Size, dmabuf: b})
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
	return r.build(&texture{r: r, size: b.Size, dmabuf: b, imported: true})
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
	if r.lost != nil {
		return -1, nil
	}
	fd, err := r.d.SyncFile()
	return fd, r.note(err)
}

func (r *Renderer) WaitFence(fd int) error {
	if r.lost != nil {
		return unix.Close(fd)
	}
	return r.note(r.d.WaitSyncFile(fd))
}

func (r *Renderer) Finish() error {
	if r.lost != nil {
		return nil
	}
	return r.note(r.d.Finish())
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
	if _, live := r.targets[t]; !live {
		return nil, errors.New("vulkan: Begin on a destroyed target")
	}
	if r.lost != nil {
		return render.LostPass{}, nil
	}
	p, err := r.d.Begin(t.t, damage)
	if err != nil {
		return nil, r.note(err)
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

func (p *pass) SetCut(t render.Texture, c render.AlphaCut) {
	tex, ok := t.(*texture)
	if !ok || tex.r != p.r {
		p.fail(fmt.Errorf("vulkan: a cut over a texture of another renderer (%T)", t))
		return
	}
	p.p.SetCut(tex.t, c)
}

func (p *pass) ClearCut() {
	p.p.ClearCut()
}

func (p *pass) Clear(c render.Color) {
	p.r.counts.Clears++
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
	p.p.DrawTexture(tex.t, src, dst, lgpu.Transform(transform), alpha, m)
}

type screenShader struct {
	r     *Renderer
	s     *lgpu.FragmentShader
	spirv []byte
}

func (s *screenShader) make() error {
	fs, err := s.r.d.NewFragmentShader(s.spirv)
	if err != nil {
		return fmt.Errorf("vulkan: %w", s.r.note(err))
	}
	s.s = fs
	return nil
}

func (r *Renderer) NewScreenShader(p *shader.Program) (render.ScreenShader, error) {
	s := &screenShader{r: r, spirv: p.SPIRV()}
	if r.lost == nil {
		if err := s.make(); err != nil {
			return nil, err
		}
	}
	r.shaders[s] = struct{}{}
	return s, nil
}

func (s *screenShader) Prepare(target render.Target) error {
	t, err := s.r.target(target)
	if err != nil {
		return err
	}
	if s.r.lost != nil {
		return nil
	}
	return s.r.note(s.s.Prepare(t.t))
}

func (s *screenShader) Destroy() error {
	delete(s.r.shaders, s)
	if s.s == nil {
		return nil
	}
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
	p.p.DrawGlass(tex.t, src, dst, alpha, m, g)
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
	return errors.Join(p.err, p.r.note(p.p.End()))
}
