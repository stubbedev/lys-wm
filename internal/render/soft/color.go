package soft

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"

	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/render"
)

const floatChannels = 4

type workingTarget struct {
	pix  []float32
	w, h int
	tex  *texture
}

func (t *workingTarget) Size() image.Point {
	return image.Pt(t.w, t.h)
}

func (t *workingTarget) Read([]byte, int) error {
	return errors.New("soft: a working target's pixels are not read back")
}

func (t *workingTarget) Destroy() error {
	t.pix, t.tex.pix, t.tex.float = nil, nil, nil
	return nil
}

func (t *workingTarget) Texture() render.Texture {
	return t.tex
}

func (t *workingTarget) bounds() image.Rectangle {
	return image.Rect(0, 0, t.w, t.h)
}

func (t *workingTarget) at(x, y int) []float32 {
	i := (y*t.w + x) * floatChannels
	return t.pix[i : i+floatChannels]
}

func (r *Renderer) NewWorkingTarget(size image.Point) (render.TextureTarget, error) {
	if size.X <= 0 || size.Y <= 0 {
		return nil, fmt.Errorf("soft: cannot make a %v working target", size)
	}
	t := &workingTarget{pix: make([]float32, size.X*size.Y*floatChannels), w: size.X, h: size.Y}
	t.tex = &texture{owner: r, pix: []byte{}, size: size, float: t, format: render.ABGR8888}
	return t, nil
}

func (t *texture) Working() bool {
	return t.float != nil
}

func (t *texture) owned(p *pass) bool {
	return t.drawn != nil && t.drawn == p.buf || t.float != nil && t.float == p.work
}

func (p *pass) SetColor(t *render.ColorTransform) {
	p.color = t
}

func (p *pass) floatPath(tex *texture) bool {
	return p.color != nil || p.work != nil || tex != nil && tex.float != nil
}

func (p *pass) put(x, y int, c [4]float32) {
	if p.work != nil {
		d := p.work.at(x, y)
		k := 1 - c[3]
		d[0], d[1], d[2], d[3] = c[0]+float32(d[0]*k), c[1]+float32(d[1]*k), c[2]+float32(d[2]*k), c[3]+float32(d[3]*k)
		return
	}
	row := p.buf.Pix[y*p.buf.Stride:]
	over(row[x*4:x*4+4], unit(c[2]), unit(c[1]), unit(c[0]), unit(c[3]))
}

func unit(v float32) byte {
	return byte(math.Round(float64(min(max(v, 0), 1)) * 255))
}

func (p *pass) through(c [4]float32) [4]float32 {
	if p.color == nil {
		return c
	}
	return p.color.Apply(c)
}

func floats(c render.Color) [4]float32 {
	return [4]float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255, float32(c.A) / 255}
}

func scaled(c [4]float32, k float32) [4]float32 {
	return [4]float32{float32(c[0] * k), float32(c[1] * k), float32(c[2] * k), float32(c[3] * k)}
}

func (p *pass) clearFloat(c render.Color) {
	v := p.through(floats(c))
	for _, d := range p.damage.Rects() {
		for y := d.Min.Y; y < d.Max.Y; y++ {
			for x := d.Min.X; x < d.Max.X; x++ {
				if p.work != nil {
					copy(p.work.at(x, y), v[:])
					continue
				}
				row := p.buf.Pix[y*p.buf.Stride:]
				row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = unit(v[2]), unit(v[1]), unit(v[0]), unit(v[3])
			}
		}
	}
}

func (p *pass) fillFloat(r image.Rectangle, c render.Color, cover func(x, y int) float32) {
	v := p.through(floats(c))
	for _, d := range p.damage.Rects() {
		clip := r.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			for x := clip.Min.X; x < clip.Max.X; x++ {
				if k := cover(x, y); k > 0 {
					p.put(x, y, scaled(v, k))
				}
			}
		}
	}
}

func (p *pass) gradientFloat(r image.Rectangle, g render.Gradient, stops []color.RGBA, m render.Mask) {
	for _, d := range p.damage.Rects() {
		clip := r.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			for x := clip.Min.X; x < clip.Max.X; x++ {
				k := g.Alpha * coverage(m, x, y)
				if k <= 0 {
					continue
				}
				c := lgpu.GradientAt(g.From, g.To, stops, float32(x)+0.5, float32(y)+0.5)
				p.put(x, y, scaled(p.through(floats(render.Color{R: c.R, G: c.G, B: c.B, A: c.A})), k))
			}
		}
	}
}

func (t *texture) sample(sx, sy int) [4]float32 {
	if t.float != nil {
		var c [4]float32
		copy(c[:], t.float.at(sx, sy))
		return c
	}
	px := t.pix[sy*t.stride+sx*4:]
	return [4]float32{float32(px[2]) / 255, float32(px[1]) / 255, float32(px[0]) / 255, float32(px[3]) / 255}
}

func (p *pass) drawFloat(tex *texture, dst image.Rectangle, alpha float32, m render.Mask, swap bool) {
	for _, d := range p.damage.Rects() {
		clip := dst.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			row := p.rows[y-dst.Min.Y]
			for x := clip.Min.X; x < clip.Max.X; x++ {
				k := alpha * coverage(m, x, y)
				if k <= 0 {
					continue
				}
				sx, sy := p.cols[x-dst.Min.X], row
				if swap {
					sx, sy = sy, sx
				}
				p.put(x, y, scaled(p.through(tex.sample(sx, sy)), k))
			}
		}
	}
}

func (p *pass) glassFloat(tex *texture, src render.FRect, dst image.Rectangle, alpha float32, m render.Mask, g render.Glass) {
	plan := g.RefractPlan(m.Keep)
	bends := g.Refracts()
	w, h := float32(tex.size.X), float32(tex.size.Y)
	kx, ky := float32(src.W)/float32(dst.Dx()), float32(src.H)/float32(dst.Dy())
	for _, d := range p.damage.Rects() {
		clip := dst.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			for x := clip.Min.X; x < clip.Max.X; x++ {
				k := alpha * coverage(m, x, y)
				if k <= 0 {
					continue
				}
				sx := float32(src.X) + float32((float32(x-dst.Min.X)+0.5)*kx)
				sy := float32(src.Y) + float32((float32(y-dst.Min.Y)+0.5)*ky)
				if bends {
					if dx, dy, ok := plan.At(x, y); ok {
						sx += float32(float32(dx) / 256 * kx)
						sy += float32(float32(dy) / 256 * ky)
					}
				}
				c := scaled(g.Adjust(scaled(tex.bilinear(sx/w, sy/h), 255)), float32(1.0/255))
				p.put(x, y, scaled(p.through(c), k))
			}
		}
	}
}

func (t *texture) bilinear(u, v float32) [4]float32 {
	fx := float64(float32(u*float32(t.size.X))) - 0.5
	fy := float64(float32(v*float32(t.size.Y))) - 0.5
	x0, y0 := math.Floor(fx), math.Floor(fy)
	ax, ay := float32(fx-x0), float32(fy-y0)
	clampX := func(x int) int { return min(max(x, 0), t.size.X-1) }
	clampY := func(y int) int { return min(max(y, 0), t.size.Y-1) }
	a, b := t.sample(clampX(int(x0)), clampY(int(y0))), t.sample(clampX(int(x0)+1), clampY(int(y0)))
	c, d := t.sample(clampX(int(x0)), clampY(int(y0)+1)), t.sample(clampX(int(x0)+1), clampY(int(y0)+1))
	var out [4]float32
	for i := range out {
		top := a[i] + float32((b[i]-a[i])*ax)
		bottom := c[i] + float32((d[i]-c[i])*ax)
		out[i] = top + float32((bottom-top)*ay)
	}
	return out
}

func (p *pass) kawaseFloat(tex *texture, dst image.Rectangle, offset float32, up bool) {
	hx, hy := 0.5/float32(tex.size.X)*offset, 0.5/float32(tex.size.Y)*offset
	for _, d := range p.damage.Rects() {
		clip := dst.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			for x := clip.Min.X; x < clip.Max.X; x++ {
				u := (float32(x-dst.Min.X) + 0.5) / float32(dst.Dx())
				v := (float32(y-dst.Min.Y) + 0.5) / float32(dst.Dy())
				p.put(x, y, lgpu.KawaseAt(tex.bilinear, up, u, v, hx, hy))
			}
		}
	}
}
