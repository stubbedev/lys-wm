package compositor

import (
	"errors"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type TitlebarButton uint8

const (
	TitlebarMinimize TitlebarButton = iota + 1
	TitlebarMaximize
	TitlebarClose
)

const titlebarButtonKinds = 3

var titlebarGlyphs = [...]string{TitlebarMinimize: "–", TitlebarMaximize: "□", TitlebarClose: "×"}

type TitlebarSide uint8

const (
	TitlebarRight TitlebarSide = iota
	TitlebarLeft
)

type TitlebarDoubleClick uint8

const (
	DoubleClickMaximize TitlebarDoubleClick = iota
	DoubleClickFloat
)

type Titlebars struct {
	Floating    bool
	Height      int
	Buttons     [titlebarButtonKinds]TitlebarButton
	Side        TitlebarSide
	DoubleClick TitlebarDoubleClick
}

func (t Titlebars) buttons() []TitlebarButton {
	n := slices.Index(t.Buttons[:], 0)
	if n < 0 {
		n = len(t.Buttons)
	}
	return t.Buttons[:n]
}

const doubleClickWithin = 400 * time.Millisecond

type barHit struct {
	w      *window
	button TitlebarButton
}

type barPress struct {
	id wm.WindowID
	at time.Duration
}

func (c *Compositor) titlebarHeight(t toplevel, layer wm.PlacementLayer) int {
	if !c.decor.Titlebar.Floating || layer != wm.LayerFloating || !t.decorated() {
		return 0
	}
	return c.decor.Titlebar.Height
}

func (c *Compositor) barOf(w *window) (image.Rectangle, bool) {
	h := c.titlebarHeight(w.top, w.layer)
	if h == 0 {
		return image.Rectangle{}, false
	}
	return image.Rect(w.rect.Min.X, w.rect.Min.Y-h, w.rect.Max.X, w.rect.Min.Y), true
}

func (c *Compositor) barButton(bar image.Rectangle, i int) image.Rectangle {
	side := bar.Dy()
	if c.decor.Titlebar.Side == TitlebarLeft {
		return image.Rect(bar.Min.X+i*side, bar.Min.Y, bar.Min.X+(i+1)*side, bar.Max.Y)
	}
	n := len(c.decor.Titlebar.buttons())
	return image.Rect(bar.Max.X-(n-i)*side, bar.Min.Y, bar.Max.X-(n-i-1)*side, bar.Max.Y)
}

func (c *Compositor) barTitle(bar image.Rectangle) image.Rectangle {
	room := len(c.decor.Titlebar.buttons()) * bar.Dy()
	if c.decor.Titlebar.Side == TitlebarLeft {
		bar.Min.X = min(bar.Min.X+room, bar.Max.X)
		return bar
	}
	bar.Max.X = max(bar.Max.X-room, bar.Min.X)
	return bar
}

func (c *Compositor) drawTitlebar(pass *frame, o *output.Output, w *window, mask render.Mask) {
	bar, ok := c.barOf(w)
	if !ok {
		return
	}
	focused, hasFocus := c.wm.Focused()
	colors := c.tabColors(w.id, hasFocus && focused == w.id)
	px := o.Scale().ToDevice(c.decor.Tab.TextHeight)
	c.drawBarLabel(pass, o, c.barTitle(bar), labelKey{text: w.top.Title(), icon: w.top.Icon(), theme: c.icons, px: px, colors: colors}, mask)
	for i, b := range c.decor.Titlebar.buttons() {
		shown := colors
		if c.barHover == (barHover{id: w.id, button: b}) {
			shown = TabColors{Background: colors.Text, Text: colors.Background}
		}
		c.drawBarLabel(pass, o, c.barButton(bar, i), labelKey{text: titlebarGlyphs[b], px: px, colors: shown, centered: true}, mask)
	}
}

func (c *Compositor) drawBarLabel(pass *frame, o *output.Output, r image.Rectangle, key labelKey, mask render.Mask) {
	dst := deviceRect(o, r)
	if dst.Empty() {
		return
	}
	key.size = dst.Size()
	tex := c.label(o, key, labelSlot{})
	if tex == nil {
		pass.fillMasked(dst, key.colors.Background, mask)
		return
	}
	size := tex.Size()
	pass.drawMasked(tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, dst, mask)
}

type barHover struct {
	id     wm.WindowID
	button TitlebarButton
}

func (c *Compositor) titlebarAt(p point) (barHit, bool) {
	if !c.decor.Titlebar.Floating {
		return barHit{}, false
	}
	s, _, w := c.surfaceAt(p)
	return c.barAt(p, s, w)
}

func (c *Compositor) barAt(p point, s *surface.Surface, under *window) (barHit, bool) {
	if !c.decor.Titlebar.Floating || c.shell.Locked() || c.overviewHolds() || s != nil && under == nil {
		return barHit{}, false
	}
	o := c.outputAt(p)
	if o == nil {
		return barHit{}, false
	}
	at := p.pixel()
	for _, layer := range stackLayers {
		for _, w := range slices.Backward(c.stack) {
			if w.layer != layer || !w.visible || w.minimized || w.output != o || !at.In(w.tile) {
				continue
			}
			bar, ok := c.barOf(w)
			if !ok || !at.In(bar) {
				return barHit{}, false
			}
			for i, b := range c.decor.Titlebar.buttons() {
				if at.In(c.barButton(bar, i)) {
					return barHit{w: w, button: b}, true
				}
			}
			return barHit{w: w}, true
		}
	}
	return barHit{}, false
}

func (c *Compositor) hoverTitlebar(hit barHit) error {
	next := barHover{}
	if hit.w != nil && hit.button != 0 {
		next = barHover{id: hit.w.id, button: hit.button}
	}
	if next == c.barHover {
		return nil
	}
	c.barHover = next
	return c.scheduleAll()
}

func (c *Compositor) pressTitlebar(button uint32) (bool, error) {
	hit, ok := c.titlebarAt(c.cursor)
	if !ok {
		return false, nil
	}
	w := hit.w
	switch button {
	case btnMiddle:
		c.consumed = append(c.consumed, button)
		return true, c.minimize(w)
	case btnRight:
		c.consumed = append(c.consumed, button)
		if c.ipcEvents != nil {
			at := c.cursor.pixel()
			c.ipcEvents.Announce(ipc.WindowMenuEvent{Window: w.id, X: at.X, Y: at.Y})
		}
		return true, nil
	case btnLeft:
	default:
		return false, nil
	}
	switch hit.button {
	case TitlebarClose:
		c.consumed = append(c.consumed, button)
		return true, w.top.close()
	case TitlebarMinimize:
		c.consumed = append(c.consumed, button)
		return true, c.minimize(w)
	case TitlebarMaximize:
		c.consumed = append(c.consumed, button)
		return true, c.onWindow(w, wm.MaximizeAction{Switch: wm.SwitchToggle})
	}
	now := c.clock()
	last := c.lastBarPress
	c.lastBarPress = barPress{id: w.id, at: now}
	if last.id == w.id && now-last.at <= doubleClickWithin {
		c.consumed = append(c.consumed, button)
		c.lastBarPress = barPress{}
		if c.decor.Titlebar.DoubleClick == DoubleClickFloat {
			return true, c.onWindow(w, wm.FloatAction{})
		}
		return true, c.onWindow(w, wm.MaximizeAction{Switch: wm.SwitchToggle})
	}
	if err := c.clientDrag(w, wm.DragMove, buttonInput{button}, 0); err != nil || c.interaction != nil {
		return true, err
	}
	c.consumed = append(c.consumed, button)
	return true, c.focusWindow(w, wm.FocusClick)
}

func (c *Compositor) onWindow(w *window, a wm.Action) error {
	return errors.Join(c.focusWindow(w, wm.FocusClick), c.dispatch(a, nil, wm.FocusClick))
}
