package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"
	"unique"

	gtext "github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/third_party/gomath"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) lookOf(w *window) *wm.Look {
	c.decision(w)
	return &w.decided.look
}

func (c *Compositor) micaOn(w *window) bool {
	c.decision(w)
	return w.decided.mica
}

func micaOf(d *wm.Decision, on bool) bool {
	if d.Blur != nil && !*d.Blur {
		return false
	}
	if d.Mica != nil {
		return *d.Mica
	}
	return on
}

func (c *Compositor) glassOf(o *output.Output, l wm.Look) (render.Glass, unique.Handle[render.Glass]) {
	bends := l.Refraction.Bezel > 0 && l.Refraction.Amount > 0
	if l.SaturationShift == 0 && l.BrightnessShift == 0 && l.ContrastShift == 0 && !bends {
		return render.Glass{}, unique.Handle[render.Glass]{}
	}
	g := render.Glass{Saturation: 1 + l.SaturationShift, Brightness: 1 + l.BrightnessShift, Contrast: 1 + l.ContrastShift}
	if bends {
		g.Bezel, g.Amount = deviceLength(o, l.Refraction.Bezel), deviceLength(o, l.Refraction.Amount)
	}
	return g, unique.Make(g)
}

type grainTile struct {
	tex  render.Texture
	size image.Point
}

func (c *Compositor) grainFor(need image.Point) (grainTile, error) {
	if c.grain.tex != nil && need.In(image.Rectangle{Max: c.grain.size.Add(image.Pt(1, 1))}) {
		return c.grain, nil
	}
	size := image.Pt(max(need.X, c.grain.size.X), max(need.Y, c.grain.size.Y))
	tex, err := c.renderer.NewTexture(render.ARGB8888, size, size.X*4, gtext.GrainTile(size.X, size.Y))
	if err != nil {
		return grainTile{}, fmt.Errorf("the %v noise tile: %w", size, err)
	}
	err = c.dropGrain()
	c.grain = grainTile{tex: tex, size: size}
	return c.grain, err
}

func (c *Compositor) dropGrain() error {
	if c.grain.tex == nil {
		return nil
	}
	err := c.destroyTexture(c.grain.tex)
	c.grain = grainTile{}
	return err
}

func (c *Compositor) drawGrain(pass *frame, o *output.Output, r image.Rectangle, origin image.Point, m render.Mask, alpha, strength float32) error {
	tile, err := c.grainFor(o.Backend().State().Mode.Size)
	if err != nil {
		return err
	}
	at := r.Min.Sub(origin)
	at.X = max(at.X, 0) % max(tile.size.X-r.Dx()+1, 1)
	at.Y = max(at.Y, 0) % max(tile.size.Y-r.Dy()+1, 1)
	a := float32(gtext.GrainStrength(float64(strength))) / 255
	pass.ops = append(pass.ops, drawOp{
		tex: tile.tex, src: render.FRect{X: float64(at.X), Y: float64(at.Y), W: float64(r.Dx()), H: float64(r.Dy())}, dst: r,
		alpha: float32(alpha * a), mask: m,
	})
	return nil
}

const (
	rimSteps   = 64
	rimResting = 40
)

var rimStops = func() Border {
	alphas := [8]uint8{0xff, 0x96, 0x32, 0, 0, 0, 0x28, 0x6e}
	colors := make([]render.Color, len(alphas))
	for i, a := range alphas {
		colors[i] = render.Color{R: 0xff, G: 0xff, B: 0xff, A: a}
	}
	b, err := GradientBorder(colors, 0)
	if err != nil {
		panic(err)
	}
	return b
}()

type rim struct {
	box      render.RoundRect
	width    int
	strength float32
	step     int
	from, to [2]float32
}

func (f *frame) addRim(g glassDraw, m render.Mask, under image.Rectangle, o *output.Output) {
	s := g.look.Specular
	if s.Strength <= 0 || s.Width <= 0 {
		return
	}
	box := m.Keep
	if box.Rect.Empty() {
		box = render.RoundRect{Rect: under}
	}
	if n := len(f.rims); n > 0 && f.rims[n-1].box == box {
		return
	}
	width := int(math.Ceil(float64(deviceLength(o, s.Width))))
	f.rims = append(f.rims, rim{box: box, width: width, strength: float32(s.Strength * g.alpha)})
}

func (c *Compositor) rimStep(tip image.Point, box image.Rectangle) int {
	if c.animations.Curve(anim.Global) == anim.Instant {
		return rimResting
	}
	centre := box.Min.Add(box.Size().Div(2))
	d := tip.Sub(centre)
	if d == (image.Point{}) {
		return rimResting
	}
	turn := gomath.Atan2(float64(d.Y), float64(d.X)) / (2 * math.Pi)
	return (int(math.Round(turn*rimSteps)) + rimSteps) % rimSteps
}

func rimLine(box image.Rectangle, step int) (from, to [2]float32) {
	s, co := gomath.Sincos(float64(step) * 2 * math.Pi / rimSteps)
	cx, cy := float64(box.Min.X+box.Max.X)/2, float64(box.Min.Y+box.Max.Y)/2
	reach := gomath.Hypot(float64(box.Dx()), float64(box.Dy())) / 2
	dx, dy := float32(co*reach), float32(s*reach)
	x, y := float32(cx), float32(cy)
	return [2]float32{x + dx, y + dy}, [2]float32{x - dx, y - dy}
}

func (c *Compositor) drawRims(pass *frame, o *output.Output, mark int) error {
	if mark == len(pass.rims) {
		return nil
	}
	stops, err := c.borderStops(rimStops)
	if err != nil {
		return err
	}
	tip := c.cursorTip(o)
	for i := mark; i < len(pass.rims); i++ {
		r := &pass.rims[i]
		r.step = c.rimStep(tip, r.box.Rect)
		r.from, r.to = rimLine(r.box.Rect, r.step)
		inner := r.box.Rect.Inset(r.width)
		ring := render.Mask{Keep: r.box, Cut: render.RoundRect{Rect: inner, Radius: max(r.box.Radius-float32(r.width), 0)}}
		g := render.Gradient{Stops: stops, From: r.from, To: r.to, Alpha: r.strength}
		for _, strip := range ringStrips(r.box.Rect, inner, r.box.Radius) {
			if !strip.Empty() {
				pass.fillGradient(strip, g, ring)
			}
		}
	}
	return nil
}

func (f *frame) dropHiddenRims() {
	if len(f.rims) == 0 {
		return
	}
	kept := f.rims[:0]
	for _, r := range f.rims {
		for _, op := range f.ops {
			if op.gradient.Stops != nil && op.gradient.From == r.from && op.gradient.To == r.to {
				kept = append(kept, r)
				break
			}
		}
	}
	f.rims = kept
}

func (c *Compositor) rimsMoved() error {
	var errs []error
	for _, o := range c.outputs {
		sc, ok := c.scenes[o]
		if !ok || len(sc.prev.rims) == 0 {
			continue
		}
		tip := c.cursorTip(o)
		for _, r := range sc.prev.rims {
			if c.rimStep(tip, r.box.Rect) != r.step {
				errs = append(errs, c.schedule(o))
				break
			}
		}
	}
	return errors.Join(errs...)
}

type frost struct {
	surface *surface.Surface
	shape   []image.Rectangle
	extent  image.Rectangle
	box     image.Rectangle
	mask    render.Mask
	alpha   float32
	look    wm.Look
	mica    bool
}

func (c *Compositor) windowFrost(w *window) *frost {
	o := w.output
	if o == nil {
		return nil
	}
	top := w.top.Surface()
	f := &frost{surface: top, extent: deviceRect(o, w.rect), box: deviceRect(o, w.tile), mask: c.windowMask(o, w), alpha: c.alpha.Alpha(top), look: *c.lookOf(w), mica: c.micaOn(w)}
	if (c.blurred(w) || f.mica) && c.effects.BlurRegion(top).Empty() && c.translucent(top, c.opacity(w)) {
		f.shape = append(f.shape, f.extent)
	}
	if c.honoursBlurRegions(w) {
		f.shape = c.appendTreeRegions(f.shape, o, top, c.windowOrigin(w))
	}
	if len(f.shape) == 0 {
		return nil
	}
	return f
}

func (c *Compositor) layerFrost(l *layer) *frost {
	s := l.surface.Surface()
	box := deviceRect(l.output, l.bounds())
	f := &frost{surface: s, extent: box, box: box, alpha: c.alpha.Alpha(s), look: c.decor.Blur.Look}
	if c.blursLayer(l.surface) && c.effects.BlurRegion(s).Empty() && c.translucent(s, 1) {
		f.shape = append(f.shape, box)
	}
	f.shape = c.appendTreeRegions(f.shape, l.output, s, flat(l.bounds().Min))
	if len(f.shape) == 0 {
		return nil
	}
	return f
}

func (c *Compositor) appendTreeRegions(rects []image.Rectangle, o *output.Output, s *surface.Surface, org origin) []image.Rectangle {
	for member := range s.Stack() {
		if member == s {
			rects = c.appendBlurRegion(rects, o, s, org)
			continue
		}
		if sub := member.Subsurface(); sub != nil {
			rects = c.appendTreeRegions(rects, o, member, org.child(sub.Position()))
		}
	}
	return rects
}

func (c *Compositor) drawFrost(pass *frame, o *output.Output, cl *closing, at image.Rectangle, a float32) int {
	f := cl.frost
	mark := len(pass.rims)
	if f == nil {
		return mark
	}
	from := len(pass.ops)
	c.drawBlur(pass, o, glassDraw{surface: f.surface, shape: f.shape, extent: f.extent, mask: f.mask, alpha: f.alpha, look: &f.look, mica: f.mica})
	pass.remap(from, f.box, at)
	pass.fade(from, a)
	return mark
}

func (c *Compositor) drawFrostRims(pass *frame, o *output.Output, cl *closing, mark int, at image.Rectangle, a float32) {
	if cl.frost == nil || mark == len(pass.rims) {
		return
	}
	from := len(pass.ops)
	if err := c.drawRims(pass, o, mark); err != nil {
		c.report(fmt.Errorf("compositor: the rim of a closing window: %w", err))
		return
	}
	pass.remap(from, cl.frost.box, at)
	pass.fade(from, a)
}
