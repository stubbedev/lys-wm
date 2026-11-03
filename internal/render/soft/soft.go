// Package soft is the CPU renderer over premultiplied ARGB8888 buffers.
package soft

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"slices"
	"sync"

	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shader"
)

type Buffer struct {
	Pix    []byte
	Stride int
	W, H   int
}

func NewBuffer(w, h int) *Buffer {
	return &Buffer{Pix: make([]byte, w*h*4), Stride: w * 4, W: w, H: h}
}

func (b *Buffer) Size() image.Point {
	return image.Pt(b.W, b.H)
}

func (b *Buffer) Read(pix []byte, stride int) error {
	if err := render.CheckPixels(b.Size(), stride, len(pix)); err != nil {
		return err
	}
	for y := range b.H {
		copy(pix[y*stride:y*stride+b.W*4], b.Pix[y*b.Stride:])
	}
	return nil
}

func (b *Buffer) Destroy() error {
	b.Pix, b.W, b.H = nil, 0, 0
	return nil
}

func (b *Buffer) bounds() image.Rectangle {
	return image.Rect(0, 0, b.W, b.H)
}

type texture struct {
	owner  *Renderer
	pix    []byte
	stride int
	size   image.Point
	format render.Format
	drawn  *Buffer
	float  *workingTarget
}

func (t *texture) Size() image.Point {
	return t.size
}

func (t *texture) Update(dst image.Rectangle, stride int, data []byte) error {
	if t.pix == nil {
		return errors.New("soft: update of a destroyed texture")
	}
	if t.drawn != nil {
		return errors.New("soft: a target's texture is drawn by a pass, not uploaded")
	}
	if err := render.CheckUpdate(t.size, dst, stride, len(data)); err != nil {
		return err
	}
	for y := dst.Min.Y; y < dst.Max.Y; y++ {
		row := t.pix[y*t.stride+dst.Min.X*4 : y*t.stride+dst.Max.X*4]
		copy(row, data[(y-dst.Min.Y)*stride:])
		t.format.DecodeRow(row)
	}
	return nil
}

func (t *texture) Destroy() error {
	if t.drawn == nil {
		t.pix = nil
	}
	return nil
}

type Renderer struct {
	pass pass
}

func (*Renderer) DeviceName() string {
	return "software renderer"
}

func New() *Renderer {
	r := &Renderer{}
	r.pass.owner = r
	return r
}

var formats = []render.Format{
	render.ARGB8888, render.XRGB8888, render.ABGR8888, render.XBGR8888,
	render.ARGB2101010, render.XRGB2101010, render.ABGR2101010, render.XBGR2101010,
}

func (r *Renderer) Speed() render.Speed { return render.Software }

func (r *Renderer) Formats() []render.Format {
	return formats
}

func (r *Renderer) NewTexture(format render.Format, size image.Point, stride int, data []byte) (render.Texture, error) {
	if !slices.Contains(r.Formats(), format) {
		return nil, fmt.Errorf("soft: unsupported texture format %d", format)
	}
	if err := render.CheckPixels(size, stride, len(data)); err != nil {
		return nil, err
	}
	t := &texture{owner: r, pix: make([]byte, size.X*size.Y*4), stride: size.X * 4, size: size, format: format}
	if err := t.Update(image.Rectangle{Max: size}, stride, data); err != nil {
		return nil, err
	}
	return t, nil
}

func (r *Renderer) NewTarget(size image.Point) (render.ReadTarget, error) {
	if size.X <= 0 || size.Y <= 0 {
		return nil, fmt.Errorf("soft: cannot make a %v target", size)
	}
	return NewBuffer(size.X, size.Y), nil
}

type TextureTarget struct {
	*Buffer
	tex *texture
}

func (t *TextureTarget) Texture() render.Texture {
	return t.tex
}

func (t *TextureTarget) Destroy() error {
	t.tex.pix = nil
	return t.Buffer.Destroy()
}

func (r *Renderer) NewTextureTarget(size image.Point) (render.TextureTarget, error) {
	if size.X <= 0 || size.Y <= 0 {
		return nil, fmt.Errorf("soft: cannot make a %v target", size)
	}
	b := NewBuffer(size.X, size.Y)
	return &TextureTarget{Buffer: b, tex: &texture{owner: r, pix: b.Pix, stride: b.Stride, size: size, format: render.ARGB8888, drawn: b}}, nil
}

func (r *Renderer) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	buf, ok := target.(*Buffer)
	if tt, isTT := target.(*TextureTarget); isTT {
		buf, ok = tt.Buffer, true
	}
	work, isWork := target.(*workingTarget)
	if !ok && !isWork {
		return nil, fmt.Errorf("soft: cannot render into %T", target)
	}
	if r.pass.active {
		return nil, errors.New("soft: Begin while a pass is active")
	}
	p := &r.pass
	p.active, p.buf, p.work, p.color, p.err = true, buf, work, nil, nil
	if isWork {
		damage.ClipInto(&p.damage, work.bounds())
		return p, nil
	}
	damage.ClipInto(&p.damage, buf.bounds())
	return p, nil
}

type pass struct {
	owner  *Renderer
	active bool
	buf    *Buffer
	work   *workingTarget
	color  *render.ColorTransform
	damage region.Set
	err    error
	cols   []int
	rows   []int
}

func (p *pass) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *pass) End() error {
	if !p.active {
		return errors.New("soft: End without an active pass")
	}
	p.active, p.buf, p.work = false, nil, nil
	return p.err
}

func over(dst []byte, b, g, r, a byte) {
	if a == 0xff {
		dst[0], dst[1], dst[2], dst[3] = b, g, r, a
		return
	}
	inv := uint32(0xff - a)
	dst[0] = b + byte((uint32(dst[0])*inv+127)/255)
	dst[1] = g + byte((uint32(dst[1])*inv+127)/255)
	dst[2] = r + byte((uint32(dst[2])*inv+127)/255)
	dst[3] = a + byte((uint32(dst[3])*inv+127)/255)
}

func scale(c byte, a uint32) byte {
	return byte((uint32(c)*a + 127) / 255)
}

func (p *pass) Clear(c render.Color) {
	if !p.active {
		p.fail(errors.New("soft: Clear outside a pass"))
		return
	}
	if !c.Premultiplied() {
		p.fail(fmt.Errorf("soft: color %+v is not premultiplied", c))
		return
	}
	if p.floatPath(nil) {
		p.clearFloat(c)
		return
	}
	for _, d := range p.damage.Rects() {
		for y := d.Min.Y; y < d.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			for x := d.Min.X; x < d.Max.X; x++ {
				row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = c.B, c.G, c.R, c.A
			}
		}
	}
}

func coverage(m render.Mask, x, y int) float32 {
	return m.Coverage(float32(x)+0.5, float32(y)+0.5)
}

func solidSpan(m render.Mask, y int) (from, to int) {
	k := m.Keep
	if k.Rect.Empty() || !m.Cut.Rect.Empty() || !m.Merge.Empty() || m.Falloff != (render.Falloff{}) {
		return 0, 0
	}
	r := int(math.Ceil(float64(k.Radius))) + 1
	switch {
	case y <= k.Rect.Min.Y || y >= k.Rect.Max.Y-1:
		return 0, 0
	case y < k.Rect.Min.Y+r || y >= k.Rect.Max.Y-r:
		return k.Rect.Min.X + r, k.Rect.Max.X - r
	}
	return k.Rect.Min.X + 1, k.Rect.Max.X - 1
}

func toByte(f float32) uint32 {
	return uint32(math.Round(float64(f) * 255))
}

func (p *pass) FillRect(r image.Rectangle, c render.Color, m render.Mask) {
	if !p.active {
		p.fail(errors.New("soft: FillRect outside a pass"))
		return
	}
	if !c.Premultiplied() {
		p.fail(fmt.Errorf("soft: color %+v is not premultiplied", c))
		return
	}
	if err := m.Check(); err != nil {
		p.fail(err)
		return
	}
	if p.floatPath(nil) {
		p.fillFloat(r, c, func(x, y int) float32 { return coverage(m, x, y) })
		return
	}
	masked := m != (render.Mask{})
	for _, d := range p.damage.Rects() {
		clip := r.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			from, to := solidSpan(m, y)
			for x := clip.Min.X; x < clip.Max.X; x++ {
				k := uint32(0xff)
				if masked && (x < from || x >= to) {
					k = toByte(coverage(m, x, y))
				}
				switch k {
				case 0:
				case 0xff:
					over(row[x*4:x*4+4], c.B, c.G, c.R, c.A)
				default:
					over(row[x*4:x*4+4], scale(c.B, k), scale(c.G, k), scale(c.R, k), scale(c.A, k))
				}
			}
		}
	}
}

func (p *pass) FillGoo(r image.Rectangle, c render.Color, g render.Goo) {
	switch {
	case !p.active:
		p.fail(errors.New("soft: FillGoo outside a pass"))
		return
	case !c.Premultiplied():
		p.fail(fmt.Errorf("soft: color %+v is not premultiplied", c))
		return
	case g.A.Rect.Empty() || g.B.Rect.Empty() || !(g.Smooth >= 0) || !(g.A.Radius >= 0) || !(g.B.Radius >= 0):
		p.fail(fmt.Errorf("soft: goo %+v is not two rounded rectangles with a smoothing of zero or more", g))
		return
	}
	if p.floatPath(nil) {
		p.fillFloat(r, c, func(x, y int) float32 { return lgpu.GooCoverage(g, float32(x)+0.5, float32(y)+0.5) })
		return
	}
	for _, d := range p.damage.Rects() {
		clip := r.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			for x := clip.Min.X; x < clip.Max.X; x++ {
				switch k := toByte(lgpu.GooCoverage(g, float32(x)+0.5, float32(y)+0.5)); k {
				case 0:
				case 0xff:
					over(row[x*4:x*4+4], c.B, c.G, c.R, c.A)
				default:
					over(row[x*4:x*4+4], scale(c.B, k), scale(c.G, k), scale(c.R, k), scale(c.A, k))
				}
			}
		}
	}
}

func (p *pass) Kawase(t render.Texture, dst image.Rectangle, offset float32, up bool) {
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("soft: Kawase outside a pass"))
		return
	case !ok || tex.owner != p.owner:
		p.fail(fmt.Errorf("soft: texture %T belongs to another renderer", t))
		return
	case tex.pix == nil:
		p.fail(errors.New("soft: texture used after Destroy"))
		return
	case tex.owned(p):
		p.fail(errors.New("soft: Kawase of the pass's own target"))
		return
	case !(offset >= 0) || math.IsInf(float64(offset), 0):
		p.fail(fmt.Errorf("soft: Kawase offset %v is not zero or more", offset))
		return
	case dst.Empty():
		return
	}
	if p.work != nil || tex.float != nil {
		p.kawaseFloat(tex, dst, offset, up)
		return
	}
	if !up && offset == 1 && tex.size == dst.Size().Mul(2) {
		p.halve(tex, dst)
		return
	}
	sample := func(u, v float32) [4]float32 { return lgpu.Bilinear(tex.pix, tex.stride, tex.size.X, tex.size.Y, u, v) }
	hx, hy := 0.5/float32(tex.size.X)*offset, 0.5/float32(tex.size.Y)*offset
	for _, d := range p.damage.Rects() {
		clip := dst.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			for x := clip.Min.X; x < clip.Max.X; x++ {
				u := (float32(x-dst.Min.X) + 0.5) / float32(dst.Dx())
				v := (float32(y-dst.Min.Y) + 0.5) / float32(dst.Dy())
				c := lgpu.KawaseAt(sample, up, u, v, hx, hy)
				b := func(f float32) byte { return byte(min(max(math.Round(float64(f)), 0), 255)) }
				over(row[x*4:x*4+4], b(c[0]), b(c[1]), b(c[2]), b(c[3]))
			}
		}
	}
}

func (p *pass) halve(tex *texture, dst image.Rectangle) {
	for _, d := range p.damage.Rects() {
		clip := dst.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			from := (y - dst.Min.Y) * 2 * tex.stride
			top, bottom := tex.pix[from:], tex.pix[from+tex.stride:]
			for x := clip.Min.X; x < clip.Max.X; x++ {
				at := (x - dst.Min.X) * 8
				u, v := top[at:at+8:at+8], bottom[at:at+8:at+8]
				over(row[x*4:x*4+4], mean4(u[0], u[4], v[0], v[4]), mean4(u[1], u[5], v[1], v[5]), mean4(u[2], u[6], v[2], v[6]), mean4(u[3], u[7], v[3], v[7]))
			}
		}
	}
}

func mean4(a, b, c, d byte) byte {
	return byte((uint32(a) + uint32(b) + uint32(c) + uint32(d) + 2) >> 2)
}

type screenShader struct {
	owner   *Renderer
	program *shader.Program
	tex     *texture
	runners []*shader.Runner
}

func (r *Renderer) NewScreenShader(p *shader.Program) (render.ScreenShader, error) {
	s := &screenShader{owner: r, program: p}
	for range runtime.GOMAXPROCS(0) {
		s.runners = append(s.runners, p.Runner(s.sample))
	}
	return s, nil
}

func (s *screenShader) sample(u, v float32) [4]float32 {
	t := s.tex
	c := lgpu.Bilinear(t.pix, t.stride, t.size.X, t.size.Y, u, v)
	return [4]float32{c[2] / 255, c[1] / 255, c[0] / 255, c[3] / 255}
}

func (*screenShader) Prepare(render.Target) error { return nil }

func (s *screenShader) Destroy() error {
	s.runners, s.tex = nil, nil
	return nil
}

func (p *pass) DrawScreenShader(s render.ScreenShader, t render.Texture, dst image.Rectangle, seconds float32) {
	sh, ok := s.(*screenShader)
	tex, texOK := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("soft: DrawScreenShader outside a pass"))
		return
	case !ok || sh.owner != p.owner || sh.runners == nil:
		p.fail(fmt.Errorf("soft: %T is no screen shader of this renderer", s))
		return
	case !texOK || tex.owner != p.owner:
		p.fail(fmt.Errorf("soft: texture %T belongs to another renderer", t))
		return
	case tex.pix == nil:
		p.fail(errors.New("soft: texture used after Destroy"))
		return
	case tex.drawn == p.buf:
		p.fail(errors.New("soft: DrawScreenShader of the pass's own target"))
		return
	case dst.Empty():
		return
	}
	sh.tex = tex
	for _, d := range p.damage.Rects() {
		p.shadeRect(sh, dst, dst.Intersect(d), seconds)
	}
}

func (p *pass) shadeRect(s *screenShader, dst, clip image.Rectangle, seconds float32) {
	if clip.Empty() {
		return
	}
	var wg sync.WaitGroup
	n := len(s.runners)
	for i, runner := range s.runners {
		from, to := clip.Min.Y+clip.Dy()*i/n, clip.Min.Y+clip.Dy()*(i+1)/n
		wg.Go(func() {
			w, h := float32(dst.Dx()), float32(dst.Dy())
			in := shader.Inputs{Time: seconds, Width: w, Height: h}
			for y := from; y < to; y++ {
				row := p.buf.Pix[y*p.buf.Stride:]
				in.Y = float32(y) + 0.5
				in.V = (float32(y-dst.Min.Y) + 0.5) / h
				for x := clip.Min.X; x < clip.Max.X; x++ {
					in.X = float32(x) + 0.5
					in.U = (float32(x-dst.Min.X) + 0.5) / w
					c := runner.Shade(in)
					px := row[x*4 : x*4+4]
					px[0], px[1], px[2] = shadeByte(c[2]), shadeByte(c[1]), shadeByte(c[0])
				}
			}
		})
	}
	wg.Wait()
}

func shadeByte(v float32) byte {
	if !(v > 0) {
		return 0
	}
	if v >= 1 {
		return 0xff
	}
	return byte(float32(v*255) + 0.5)
}

func (p *pass) FillGradient(r image.Rectangle, g render.Gradient, m render.Mask) {
	tex, ok := g.Stops.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("soft: FillGradient outside a pass"))
		return
	case !ok || tex.owner != p.owner:
		p.fail(fmt.Errorf("soft: gradient stops %T belong to another renderer", g.Stops))
		return
	case tex.pix == nil:
		p.fail(errors.New("soft: gradient stops used after Destroy"))
		return
	case tex.owned(p):
		p.fail(errors.New("soft: FillGradient with the pass's own target as stops"))
		return
	}
	if err := render.CheckGradient(g, m); err != nil {
		p.fail(err)
		return
	}
	stops := make([]color.RGBA, tex.size.X)
	for i := range stops {
		px := tex.pix[i*4:]
		stops[i] = color.RGBA{R: px[2], G: px[1], B: px[0], A: px[3]}
	}
	if p.floatPath(nil) {
		p.gradientFloat(r, g, stops, m)
		return
	}
	for _, d := range p.damage.Rects() {
		clip := r.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			for x := clip.Min.X; x < clip.Max.X; x++ {
				c := lgpu.GradientAt(g.From, g.To, stops, float32(x)+0.5, float32(y)+0.5)
				switch k := toByte(g.Alpha * coverage(m, x, y)); k {
				case 0:
				case 0xff:
					over(row[x*4:x*4+4], c.B, c.G, c.R, c.A)
				default:
					over(row[x*4:x*4+4], scale(c.B, k), scale(c.G, k), scale(c.R, k), scale(c.A, k))
				}
			}
		}
	}
}

func (p *pass) DrawTexture(t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform, alpha float32, m render.Mask) {
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("soft: DrawTexture outside a pass"))
		return
	case !ok || tex.owner != p.owner:
		p.fail(fmt.Errorf("soft: texture %T belongs to another renderer", t))
		return
	case tex.pix == nil:
		p.fail(errors.New("soft: texture used after Destroy"))
		return
	case tex.owned(p):
		p.fail(errors.New("soft: DrawTexture of the pass's own target"))
		return
	case dst.Empty():
		return
	}
	if err := render.CheckDraw(tex.size, src, transform, alpha, m); err != nil {
		p.fail(err)
		return
	}
	full := toByte(alpha)
	if full == 0 {
		return
	}
	masked := m != (render.Mask{})
	horizontal, vertical := transform.Axes()
	p.cols = sampleMap(p.cols[:0], dst.Dx(), horizontal, src, tex.size)
	p.rows = sampleMap(p.rows[:0], dst.Dy(), vertical, src, tex.size)
	swap := transform.SwapsAxes()
	if p.floatPath(tex) {
		p.drawFloat(tex, dst, alpha, m, swap)
		return
	}
	for _, d := range p.damage.Rects() {
		clip := dst.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			out := p.buf.Pix[y*p.buf.Stride:]
			row := p.rows[y-dst.Min.Y]
			if !masked && full == 0xff && !swap {
				drawRow(out, tex.pix[row*tex.stride:], p.cols, clip.Min.X, clip.Max.X, dst.Min.X)
				continue
			}
			from, to := solidSpan(m, y)
			for x := clip.Min.X; x < clip.Max.X; x++ {
				col := p.cols[x-dst.Min.X]
				sx, sy := col, row
				if swap {
					sx, sy = row, col
				}
				px := tex.pix[sy*tex.stride+sx*4:]
				a := full
				if masked && (x < from || x >= to) {
					a = toByte(alpha * coverage(m, x, y))
				}
				switch a {
				case 0:
				case 0xff:
					over(out[x*4:x*4+4], px[0], px[1], px[2], px[3])
				default:
					over(out[x*4:x*4+4], scale(px[0], a), scale(px[1], a), scale(px[2], a), scale(px[3], a))
				}
			}
		}
	}
}

func drawRow(out, src []byte, cols []int, from, to, origin int) {
	for x := from; x < to; x++ {
		col := cols[x-origin] * 4
		px := src[col : col+4 : col+4]
		d := out[x*4 : x*4+4 : x*4+4]
		if px[3] == 0xff {
			d[0], d[1], d[2], d[3] = px[0], px[1], px[2], 0xff
			continue
		}
		over(d, px[0], px[1], px[2], px[3])
	}
}

func (p *pass) DrawGlass(t render.Texture, src render.FRect, dst image.Rectangle, alpha float32, m render.Mask, g render.Glass) {
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("soft: DrawGlass outside a pass"))
		return
	case !ok || tex.owner != p.owner:
		p.fail(fmt.Errorf("soft: texture %T belongs to another renderer", t))
		return
	case tex.pix == nil:
		p.fail(errors.New("soft: texture used after Destroy"))
		return
	case tex.owned(p):
		p.fail(errors.New("soft: DrawGlass of the pass's own target"))
		return
	case dst.Empty():
		return
	}
	if err := render.CheckGlass(tex.size, src, alpha, m, g); err != nil {
		p.fail(err)
		return
	}
	if alpha == 0 {
		return
	}
	m = g.Mask(m, dst)
	if p.floatPath(tex) {
		p.glassFloat(tex, src, dst, alpha, m, g)
		return
	}
	plan := g.RefractPlan(m.Keep)
	bends := g.Refracts()
	w, h := float32(tex.size.X), float32(tex.size.Y)
	kx, ky := float32(src.W)/float32(dst.Dx()), float32(src.H)/float32(dst.Dy())
	b := func(f float32) byte { return byte(min(max(f+0.5, 0), 255)) }
	direct := kx == 1 && ky == 1 && src.X == math.Trunc(src.X) && src.Y == math.Trunc(src.Y)
	ix, iy := int(src.X)-dst.Min.X, int(src.Y)-dst.Min.Y
	for _, d := range p.damage.Rects() {
		clip := dst.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			for x := clip.Min.X; x < clip.Max.X; x++ {
				a := toByte(alpha * coverage(m, x, y))
				if a == 0 {
					continue
				}
				var dx, dy int32
				bent := false
				if bends {
					dx, dy, bent = plan.At(x, y)
				}
				var c [4]float32
				if direct && !bent {
					at := tex.pix[(y+iy)*tex.stride+(x+ix)*4:]
					c = g.Adjust([4]float32{float32(at[2]), float32(at[1]), float32(at[0]), float32(at[3])})
				} else {
					sx := float32(src.X) + float32((float32(x-dst.Min.X)+0.5)*kx) + float32(float32(dx)/256*kx)
					sy := float32(src.Y) + float32((float32(y-dst.Min.Y)+0.5)*ky) + float32(float32(dy)/256*ky)
					s := lgpu.Bilinear(tex.pix, tex.stride, tex.size.X, tex.size.Y, sx/w, sy/h)
					c = g.Adjust([4]float32{s[2], s[1], s[0], s[3]})
				}
				px := row[x*4 : x*4+4]
				if a == 0xff {
					over(px, b(c[2]), b(c[1]), b(c[0]), b(c[3]))
					continue
				}
				over(px, scale(b(c[2]), a), scale(b(c[1]), a), scale(b(c[0]), a), scale(b(c[3]), a))
			}
		}
	}
}

func sampleMap(out []int, n int, a render.Axis, src render.FRect, size image.Point) []int {
	origin, extent, limit := src.X, src.W, size.X
	if a.Source == 1 {
		origin, extent, limit = src.Y, src.H, size.Y
	}
	for i := range n {
		f := (float64(i) + 0.5) / float64(n)
		if a.Reversed {
			f = 1 - f
		}
		s := int(math.Floor(origin + float64(f*extent)))
		out = append(out, min(max(s, 0), limit-1))
	}
	return out
}
