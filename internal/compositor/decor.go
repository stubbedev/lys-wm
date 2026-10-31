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

type Decor struct {
	BorderWidth                  int
	Rounding                     int
	Opacity                      Opacity
	Dim                          Dim
	ActiveBorder, InactiveBorder render.Color
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
		BorderWidth:    1,
		Opacity:        Opacity{Active: 1, Inactive: 1, Fullscreen: 1},
		Dim:            Dim{Strength: 0.5},
		ActiveBorder:   mauve,
		InactiveBorder: crust,
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
	color := c.decor.InactiveBorder
	if w == c.active {
		color = c.decor.ActiveBorder
	}
	outer := deviceRect(o, w.tile)
	inner := deviceRect(o, w.rect)
	if c.rounded(w) {
		radius := deviceLength(o, c.decor.Rounding)
		pass.fillMasked(outer, color, render.Mask{
			Keep: render.RoundRect{Rect: outer, Radius: radius + deviceLength(o, c.decor.BorderWidth)},
			Cut:  render.RoundRect{Rect: inner, Radius: radius},
		})
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
