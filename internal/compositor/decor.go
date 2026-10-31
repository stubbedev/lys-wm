package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"

	gtext "github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-ui/icontheme"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type TabColors struct {
	Background, Text render.Color
}

type TabStyle struct {
	Height, Padding, TextHeight, TextPadding int
	Active, Inactive, Urgent                 TabColors
}

type Opacity struct {
	Active, Inactive, Fullscreen float32
}

type Dim struct {
	Inactive bool
	Strength float32
}

type Shadow struct {
	Enabled          bool
	Range            int
	Power            int
	Sharp            bool
	Offset           image.Point
	Scale            float64
	Active, Inactive render.Color
}

const MaxBorderColors = 10

type Border struct {
	colors [MaxBorderColors]render.Color
	n      int
	angle  float64
}

func SolidBorder(c render.Color) Border {
	return Border{colors: [MaxBorderColors]render.Color{c}, n: 1}
}

func GradientBorder(colors []render.Color, degrees float64) (Border, error) {
	if len(colors) < 2 || len(colors) > MaxBorderColors {
		return Border{}, fmt.Errorf("compositor: a gradient border takes 2 to %d colours, not %d", MaxBorderColors, len(colors))
	}
	if math.IsNaN(degrees) || math.IsInf(degrees, 0) {
		return Border{}, fmt.Errorf("compositor: gradient angle %v is not finite", degrees)
	}
	b := Border{n: len(colors), angle: degrees}
	copy(b.colors[:], colors)
	return b, nil
}

func (b Border) Colors() []render.Color {
	return b.colors[:b.n]
}

func (b Border) stops() stopsKey {
	return stopsKey{colors: b.colors, n: b.n}
}

type stopsKey struct {
	colors [MaxBorderColors]render.Color
	n      int
}

func (b Border) line(r image.Rectangle) (from, to [2]float32) {
	s, c := math.Sincos(b.angle * math.Pi / 180)
	cx, cy := float64(r.Min.X+r.Max.X)/2, float64(r.Min.Y+r.Max.Y)/2
	h := math.Abs(float64(r.Dx())/2*c) + math.Abs(float64(r.Dy())/2*s)
	return [2]float32{float32(cx - h*c), float32(cy - h*s)}, [2]float32{float32(cx + h*c), float32(cy + h*s)}
}

type Decor struct {
	BorderWidth                  int
	Rounding                     int
	Opacity                      Opacity
	Dim                          Dim
	Shadow                       Shadow
	ActiveBorder, InactiveBorder Border
	Tab                          TabStyle
}

var (
	mauve    = render.Color{R: 0xcb, G: 0xa6, B: 0xf7, A: 0xff}
	crust    = render.Color{R: 0x11, G: 0x11, B: 0x1b, A: 0xff}
	surface0 = render.Color{R: 0x31, G: 0x32, B: 0x44, A: 0xff}
	textFg   = render.Color{R: 0xcd, G: 0xd6, B: 0xf4, A: 0xff}
	red      = render.Color{R: 0xf3, G: 0x8b, B: 0xa8, A: 0xff}
)

func DefaultDecor() Decor {
	return Decor{
		BorderWidth: 1,
		Opacity:     Opacity{Active: 1, Inactive: 1, Fullscreen: 1},
		Dim:         Dim{Strength: 0.5},
		Shadow: Shadow{
			Range: 4, Power: 3, Scale: 1,
			Active:   render.Color{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xee},
			Inactive: render.Color{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xee},
		},
		ActiveBorder:   SolidBorder(mauve),
		InactiveBorder: SolidBorder(crust),
		Tab: TabStyle{
			Height: 15, Padding: 5, TextHeight: 8, TextPadding: 3,
			Active:   TabColors{Background: mauve, Text: crust},
			Inactive: TabColors{Background: surface0, Text: textFg},
			Urgent:   TabColors{Background: red, Text: crust},
		},
	}
}

func (c *Compositor) spacing() wm.Spacing {
	return wm.Spacing{TabBar: c.decor.Tab.Height + c.decor.Tab.Padding, Gaps: c.settings.Gaps}
}

const (
	tabIconRoom = 4
	iconScale   = 120
)

type labelKey struct {
	text   string
	icon   *shell.Icon
	theme  *icontheme.Theme
	size   image.Point
	px     int
	colors TabColors
}

type label struct {
	tex  render.Texture
	used bool
}

func lysUIColor(c render.Color) gtext.Color {
	return gtext.RGBA(c.R, c.G, c.B, c.A)
}

func (c *Compositor) label(o *output.Output, key labelKey) (render.Texture, error) {
	cache := c.labels[o]
	if cache == nil {
		cache = map[labelKey]*label{}
		c.labels[o] = cache
	}
	if l, ok := cache[key]; ok {
		l.used = true
		return l.tex, nil
	}
	stride := key.size.X * 4
	pix := make([]byte, stride*key.size.Y)
	cv := gtext.New(pix, stride, key.size.X, key.size.Y)
	cv.Clear(gtext.Rect{W: key.size.X, H: key.size.Y}, lysUIColor(key.colors.Background))
	pad := o.Scale().ToDevice(c.decor.Tab.TextPadding)
	box := gtext.Rect{X: pad, W: key.size.X - 2*pad, H: key.size.Y}
	if side, err := c.drawTabIcon(cv, key, pad); err != nil {
		return nil, fmt.Errorf("compositor: tab icon of %q: %w", key.text, err)
	} else if side > 0 {
		box.X += side + pad
		box.W -= side + pad
	}
	text := c.font.Ellipsize(key.text, float64(box.W), float64(key.px))
	c.font.DrawAligned(cv, text, box, float64(key.px), lysUIColor(key.colors.Text), gtext.AlignStart)
	tex, err := c.renderer.NewTexture(render.ARGB8888, key.size, stride, pix)
	if err != nil {
		return nil, fmt.Errorf("compositor: tab label %q: %w", key.text, err)
	}
	cache[key] = &label{tex: tex, used: true}
	return tex, nil
}

func (c *Compositor) drawTabIcon(cv *gtext.Canvas, key labelKey, pad int) (int, error) {
	side := key.size.Y - 2*pad
	if side <= 0 || key.size.X < tabIconRoom*side || key.icon == nil {
		return 0, nil
	}
	ic, err := c.tabIcon(key, side)
	if err != nil || ic == nil {
		return 0, err
	}
	ic.Draw(cv, pad, pad)
	return side, nil
}

func (c *Compositor) tabIcon(key labelKey, side int) (*gtext.Icon, error) {
	if key.icon.Name != "" {
		ic, err := key.theme.Icon(key.icon.Name, side, iconScale)
		if err == nil {
			return ic, nil
		}
		if !errors.Is(err, icontheme.ErrNotFound) {
			c.report(fmt.Errorf("compositor: tab icon %q from the icon theme: %w", key.icon.Name, err))
		}
	}
	best, ok := key.icon.Best(side)
	if !ok {
		return nil, nil
	}
	return gtext.IconFromImage(best.Image, side, side)
}

func (c *Compositor) evictLabels(o *output.Output) error {
	var errs []error
	for key, l := range c.labels[o] {
		if !l.used {
			errs = append(errs, c.destroyTexture(l.tex))
			delete(c.labels[o], key)
			continue
		}
		l.used = false
	}
	return errors.Join(errs...)
}

func deviceLength(o *output.Output, v int) float32 {
	return float32(float64(v) * o.Scale().Float())
}

func (c *Compositor) opacity(w *window) float32 {
	if o := c.decision(w).Opacity; o != nil {
		return *o
	}
	switch {
	case w.layer == wm.LayerFullscreen:
		return c.decor.Opacity.Fullscreen
	case w == c.active:
		return c.decor.Opacity.Active
	}
	return c.decor.Opacity.Inactive
}

func (c *Compositor) dimColor(dimmed bool) render.Color {
	if !dimmed {
		return render.Color{}
	}
	return render.Color{A: uint8(math.Round(float64(c.decor.Dim.Strength) * 0xff))}
}

func (c *Compositor) outerRadius(o *output.Output, w *window) float32 {
	if !c.rounded(w) {
		return 0
	}
	return deviceLength(o, c.decor.Rounding+c.decor.BorderWidth)
}

func (c *Compositor) drawShadow(pass *frame, o *output.Output, w *window) {
	sh := c.decor.Shadow
	if !sh.Enabled || w.layer == wm.LayerFullscreen {
		return
	}
	tile := deviceRect(o, w.tile)
	size := image.Pt(int(math.Round(float64(tile.Dx())*sh.Scale)), int(math.Round(float64(tile.Dy())*sh.Scale)))
	corner := tile.Min.Add(tile.Size().Sub(size).Div(2)).Add(image.Pt(o.Scale().ToDevice(sh.Offset.X), o.Scale().ToDevice(sh.Offset.Y)))
	base := image.Rectangle{Min: corner, Max: corner.Add(size)}
	radius := c.outerRadius(o, w)
	var falloff render.Falloff
	reach := 0
	if !sh.Sharp && sh.Range > 0 {
		falloff = render.Falloff{Range: deviceLength(o, sh.Range), Power: float32(sh.Power)}
		reach = int(math.Ceil(float64(falloff.Range)))
	}
	color := sh.Inactive
	if w == c.active {
		color = sh.Active
	}
	pass.fillMasked(base.Inset(-reach), color.Premultiply(), render.Mask{
		Keep:    render.RoundRect{Rect: base, Radius: radius},
		Cut:     render.RoundRect{Rect: tile, Radius: radius},
		Falloff: falloff,
	})
}

func (c *Compositor) rounded(w *window) bool {
	return c.decor.Rounding > 0 && w.layer != wm.LayerFullscreen
}

func (c *Compositor) windowMask(o *output.Output, w *window) render.Mask {
	if !c.rounded(w) {
		return render.Mask{}
	}
	return render.Mask{Keep: render.RoundRect{Rect: deviceRect(o, w.rect), Radius: deviceLength(o, c.decor.Rounding)}}
}

func (c *Compositor) drawBorder(pass *frame, o *output.Output, w *window) {
	if c.decor.BorderWidth <= 0 || w.layer == wm.LayerFullscreen {
		return
	}
	border := c.decor.InactiveBorder
	if w == c.active {
		border = c.decor.ActiveBorder
	}
	outer := deviceRect(o, w.tile)
	inner := deviceRect(o, w.rect)
	ring := render.Mask{Keep: render.RoundRect{Rect: outer}, Cut: render.RoundRect{Rect: inner}}
	if c.rounded(w) {
		ring.Keep.Radius, ring.Cut.Radius = c.outerRadius(o, w), deviceLength(o, c.decor.Rounding)
	}
	if border.n > 1 {
		stops, err := c.borderStops(border)
		if err != nil {
			c.report(err)
			return
		}
		from, to := border.line(outer)
		pass.fillGradient(outer, render.Gradient{Stops: stops, From: from, To: to, Alpha: 1}, ring)
		return
	}
	color := border.colors[0].Premultiply()
	if c.rounded(w) {
		pass.fillMasked(outer, color, ring)
		return
	}
	for _, strip := range []image.Rectangle{
		{Min: outer.Min, Max: image.Pt(outer.Max.X, inner.Min.Y)},
		{Min: image.Pt(outer.Min.X, inner.Max.Y), Max: outer.Max},
		{Min: image.Pt(outer.Min.X, inner.Min.Y), Max: image.Pt(inner.Min.X, inner.Max.Y)},
		{Min: image.Pt(inner.Max.X, inner.Min.Y), Max: image.Pt(outer.Max.X, inner.Max.Y)},
	} {
		if !strip.Empty() {
			pass.FillRect(strip, color)
		}
	}
}

func (c *Compositor) borderStops(b Border) (render.Texture, error) {
	key := b.stops()
	if t, ok := c.stops[key]; ok {
		return t, nil
	}
	pix := make([]byte, 0, 4*b.n)
	for _, col := range b.Colors() {
		p := col.Premultiply()
		pix = append(pix, p.B, p.G, p.R, p.A)
	}
	t, err := c.renderer.NewTexture(render.ARGB8888, image.Pt(b.n, 1), 4*b.n, pix)
	if err != nil {
		return nil, fmt.Errorf("compositor: border gradient stops %v: %w", b.Colors(), err)
	}
	c.stops[key] = t
	return t, nil
}

func (c *Compositor) dropStops() error {
	var errs []error
	for key, t := range c.stops {
		errs = append(errs, c.destroyTexture(t))
		delete(c.stops, key)
	}
	return errors.Join(errs...)
}

func (c *Compositor) tabColors(id wm.WindowID, active bool) TabColors {
	switch w := c.windowByID(id); {
	case w != nil && w.urgent:
		return c.decor.Tab.Urgent
	case active:
		return c.decor.Tab.Active
	}
	return c.decor.Tab.Inactive
}

func (c *Compositor) drawTabBars(pass *frame, o *output.Output) error {
	for _, bar := range c.tabBars {
		if !bar.Rect.Overlaps(o.Logical()) {
			continue
		}
		strip := image.Rect(bar.Rect.Min.X, bar.Rect.Min.Y, bar.Rect.Max.X, bar.Rect.Min.Y+min(c.decor.Tab.Height, bar.Rect.Dy()))
		n := len(bar.Tabs)
		for i, id := range bar.Tabs {
			x0 := strip.Min.X + strip.Dx()*i/n
			x1 := strip.Min.X + strip.Dx()*(i+1)/n
			dst := o.Scale().Rect(image.Rect(x0, strip.Min.Y, x1, strip.Max.Y).Sub(o.Position()))
			if dst.Empty() {
				continue
			}
			title := ""
			var icon *shell.Icon
			if w := c.windowByID(id); w != nil {
				title, icon = w.top.Title(), w.top.Icon()
			}
			tex, err := c.label(o, labelKey{text: title, icon: icon, theme: c.icons, size: dst.Size(), px: o.Scale().ToDevice(c.decor.Tab.TextHeight), colors: c.tabColors(id, i == bar.Active)})
			if err != nil {
				return err
			}
			size := tex.Size()
			pass.DrawTexture(tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, dst, render.Normal, 1)
		}
	}
	return nil
}

func (c *Compositor) dropLabels(o *output.Output) error {
	var errs []error
	for _, l := range c.labels[o] {
		errs = append(errs, c.destroyTexture(l.tex))
	}
	delete(c.labels, o)
	return errors.Join(errs...)
}
