package compositor

import (
	"errors"
	"fmt"
	"image"
	"iter"
	"slices"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	swlr "github.com/stubbedev/lys-wm/internal/protocol/server/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type layer struct {
	surface  *shell.LayerSurface
	output   *output.Output
	box      image.Rectangle
	placed   bool
	appeared bool
	motion   anim.Tween[image.Rectangle]
	fade     anim.Tween[float64]
	snapshot render.TextureTarget
	frost    *frost
}

func (e *layer) bounds() image.Rectangle {
	return e.box.Add(e.output.Position())
}

func (e *layer) shown() bool {
	return e.placed && e.surface.Mapped()
}

func (c *Compositor) layerOf(l *shell.LayerSurface) *layer {
	for _, e := range c.layers {
		if e.surface == l {
			return e
		}
	}
	return nil
}

func (c *Compositor) layerOutput(l *shell.LayerSurface) *output.Output {
	if res := l.Output(); res != nil {
		return c.layout.Owner(res)
	}
	if o := c.outputAt(c.cursor); o != nil {
		return o
	}
	return c.firstOutput()
}

type layerChanges struct{ usable, moved bool }

func (c *Compositor) arrange(o *output.Output) (layerChanges, error) {
	var entries []*layer
	var states []shell.LayerState
	for _, e := range c.layers {
		if e.output != o {
			continue
		}
		if !e.surface.Live() {
			e.placed = false
			continue
		}
		entries = append(entries, e)
		states = append(states, e.surface.Current())
	}
	result, usable := arrangeLayers(image.Rectangle{Max: o.LogicalSize()}, states)
	var errs []error
	var got layerChanges
	for i, e := range entries {
		moved := e.box != result[i].box
		got.moved = got.moved || moved || e.placed != result[i].ok
		e.box, e.placed = result[i].box, result[i].ok
		if !e.placed {
			continue
		}
		switch {
		case !e.appeared && e.shown():
			c.layerAppear(e, c.clock())
		case moved:
			e.motion.Jump(e.box)
		}
		errs = append(errs, e.surface.Configure(e.box.Size()))
		if moved {
			for _, p := range c.layerPopups(e) {
				errs = append(errs, p.popup.Reconstrain())
			}
		}
	}
	got.usable = usable != c.usable[o]
	c.usable[o] = usable
	return got, errors.Join(errs...)
}

func (c *Compositor) rearrange(o *output.Output) (bool, error) {
	got, err := c.arrange(o)
	if got.usable {
		err = errors.Join(err, c.syncAreas())
	}
	return got.moved, err
}

func (c *Compositor) layersChanged(o *output.Output) error {
	c.overviewLayersChanged(o)
	var err error
	if o != nil {
		_, err = c.rearrange(o)
	}
	return errors.Join(err, c.refocus(), c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) LayerCommitted(l *shell.LayerSurface) error {
	if c.layerFocus == l && l.Current().Interactivity == swlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone {
		c.layerFocus = nil
	}
	e := c.layerOf(l)
	if e == nil {
		o := c.layerOutput(l)
		if o == nil {
			return l.Close()
		}
		c.layers = append(c.layers, &layer{surface: l, output: o})
		return c.layersChanged(o)
	}
	moved, err := c.rearrange(e.output)
	if moved {
		err = errors.Join(err, c.scheduleAll())
	}
	return errors.Join(err, c.refocus(), c.refreshPointer())
}

func (c *Compositor) LayerUnmapped(l *shell.LayerSurface) error {
	if c.layerFocus == l {
		c.layerFocus = nil
	}
	var o *output.Output
	var err error
	if e := c.layerOf(l); e != nil {
		o = e.output
		err = c.layerClosing(e, c.clock())
	}
	return errors.Join(err, c.layersChanged(o))
}

func (c *Compositor) LayerDestroyed(l *shell.LayerSurface) error {
	if c.layerFocus == l {
		c.layerFocus = nil
	}
	e := c.layerOf(l)
	if e == nil {
		return nil
	}
	var err error
	if e.appeared {
		err = c.layerClosing(e, c.clock())
	}
	if e.snapshot != nil {
		err = errors.Join(err, e.snapshot.Destroy())
	}
	c.layers = slices.DeleteFunc(c.layers, func(x *layer) bool { return x == e })
	return errors.Join(err, c.layersChanged(e.output))
}

func (c *Compositor) closeLayers(o *output.Output) error {
	var errs []error
	for _, e := range c.layers {
		if e.output == o {
			errs = append(errs, e.surface.Close())
		}
	}
	c.layers = slices.DeleteFunc(c.layers, func(e *layer) bool { return e.output == o })
	return errors.Join(errs...)
}

func (c *Compositor) layersAt(o *output.Output, levels []shell.Layer) iter.Seq[*layer] {
	return func(yield func(*layer) bool) {
		for _, level := range levels {
			for _, e := range c.layers {
				if e.output == o && e.shown() && e.surface.Current().Layer == level && !yield(e) {
					return
				}
			}
		}
	}
}

func (c *Compositor) blursLayer(l *shell.LayerSurface) bool {
	return slices.Contains(c.blurLayers, l.Namespace())
}

func (c *Compositor) drawLayers(pass *frame, o *output.Output, levels []shell.Layer, shown []*surface.Surface) []*surface.Surface {
	for _, lv := range levels {
		for e := range c.layersAt(o, []shell.Layer{lv}) {
			from := len(pass.ops)
			rims := len(pass.rims)
			look := &c.decor.Blur.Look
			if c.blursLayer(e.surface) && c.effects.BlurRegion(e.surface.Surface()).Empty() && c.translucent(e.surface.Surface(), 1) {
				under := deviceRect(o, e.bounds())
				c.drawBlur(pass, o, glassDraw{surface: e.surface.Surface(), shape: c.sceneOf(o).blurs.one(under), extent: under, alpha: c.alphaOf(e.surface.Surface()), look: look})
			}
			shown = c.drawBlurredTree(pass, o, e.surface.Surface(), flat(e.bounds().Min), shown, render.Mask{}, 1, look)
			if err := c.drawRims(pass, o, rims); err != nil {
				c.report(fmt.Errorf("compositor: the rim of the %q layer surface: %w", e.surface.Namespace(), err))
			}
			if at := e.motion.At(c.frameTime); e.appeared && at != e.box {
				pass.remap(from, deviceRect(o, e.bounds()), deviceRect(o, at.Add(o.Position())))
			}
			if a := e.fade.At(c.frameTime); e.appeared && a < 1 {
				pass.fade(from, float32(max(a, 0)))
			}
		}
		c.drawClosing(pass, o, level{layer: lv})
	}
	return shown
}

var (
	belowWindows = []shell.Layer{swlr.ZwlrLayerShellV1LayerBackground, swlr.ZwlrLayerShellV1LayerBottom}
	aboveWindows = []shell.Layer{swlr.ZwlrLayerShellV1LayerTop}
	overlay      = []shell.Layer{swlr.ZwlrLayerShellV1LayerOverlay}
)

func (c *Compositor) layerPopups(e *layer) []placedPopup {
	if !e.shown() {
		return nil
	}
	return c.popupTree(e.surface.Popups(), e.bounds().Min)
}

func (c *Compositor) exclusiveLayer() *shell.LayerSurface {
	for _, level := range []shell.Layer{swlr.ZwlrLayerShellV1LayerOverlay, swlr.ZwlrLayerShellV1LayerTop} {
		for _, e := range slices.Backward(c.layers) {
			s := e.surface.Current()
			if e.surface.Mapped() && s.Layer == level && s.Interactivity == swlr.ZwlrLayerSurfaceV1KeyboardInteractivityExclusive {
				return e.surface
			}
		}
	}
	return nil
}

func (c *Compositor) layerOwning(s *surface.Surface) *shell.LayerSurface {
	if s == nil {
		return nil
	}
	for _, e := range c.layers {
		if _, ok := treeOrigin(e.surface.Surface(), flat(image.Point{}), s); ok {
			return e.surface
		}
		for _, p := range c.layerPopups(e) {
			if _, ok := treeOrigin(p.popup.Surface(), flat(image.Point{}), s); ok {
				return e.surface
			}
		}
	}
	return nil
}

func (c *Compositor) clickFocus(s *surface.Surface) error {
	l := c.layerOwning(s)
	if l == nil || l.Current().Interactivity == swlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone {
		return nil
	}
	c.layerFocus = l
	return c.refocus()
}
