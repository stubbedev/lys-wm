package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) borderWidthOf(d *wm.Decision) int {
	return c.frameBorder(d, wm.WorkspaceLook{})
}

func (c *Compositor) borderWidth(w *window) int {
	return c.frameBorder(c.decision(w), w.wsLook)
}

func (c *Compositor) rounding(w *window) int {
	switch {
	case c.decision(w).Rounding != nil:
		return *c.decision(w).Rounding
	case w.wsLook.Rounding != nil:
		return *w.wsLook.Rounding
	}
	return c.decor.Rounding
}

func (c *Compositor) shadowed(w *window) bool {
	switch {
	case c.decision(w).Shadow != nil:
		return *c.decision(w).Shadow && c.decor.Shadow.Enabled
	case w.wsLook.Shadow != nil:
		return *w.wsLook.Shadow && c.decor.Shadow.Enabled
	}
	return c.decor.Shadow.Enabled
}

func (c *Compositor) dimsInactive(w *window) bool {
	if d := c.decision(w).DimInactive; d != nil {
		return *d
	}
	return c.decor.Dim.Inactive
}

func (c *Compositor) borderColours(w *window) (active, inactive render.Border) {
	d := c.decision(w)
	active, inactive = c.decor.ActiveBorder, c.decor.InactiveBorder
	if d.ActiveBorder != nil {
		active = *d.ActiveBorder
	}
	if d.InactiveBorder != nil {
		inactive = *d.InactiveBorder
	}
	return active, inactive
}

func ruledLimits(d *wm.Decision, minimum, maximum image.Point) (image.Point, image.Point) {
	if d.MinSize != (image.Point{}) {
		minimum = image.Pt(max(minimum.X, d.MinSize.X), max(minimum.Y, d.MinSize.Y))
	}
	if d.MaxSize != (image.Point{}) {
		clamp := func(have, rule int) int {
			if have <= 0 {
				return rule
			}
			return min(have, rule)
		}
		maximum = image.Pt(clamp(maximum.X, d.MaxSize.X), clamp(maximum.Y, d.MaxSize.Y))
	}
	return minimum, maximum
}

func (c *Compositor) stillWindow(w *window) bool {
	a := c.decision(w).Animation
	return a != nil && a.Off
}

func (c *Compositor) windowStyle(w *window, e anim.Event) anim.Style {
	a := c.decision(w).Animation
	switch {
	case a == nil:
	case e == anim.WindowsIn && a.In.Kind != anim.NoStyle:
		return a.In
	case e == anim.WindowsOut && a.Out.Kind != anim.NoStyle:
		return a.Out
	}
	return c.animations.Style(e)
}

func (c *Compositor) rulesInhibitIdle() bool {
	for _, w := range c.windows {
		if !w.visible {
			continue
		}
		switch c.decision(w).IdleInhibit {
		case wm.IdleInhibitVisible:
			return true
		case wm.IdleInhibitFocus:
			if w == c.active {
				return true
			}
		case wm.IdleInhibitFullscreen:
			if w.layer == wm.LayerFullscreen {
				return true
			}
		}
	}
	return false
}

func (c *Compositor) rulesWantVRR(o *output.Output) bool {
	return slices.ContainsFunc(c.windows, func(w *window) bool {
		v := c.decision(w).VRR
		return w.visible && w.output == o && v != nil && *v
	})
}

func (c *Compositor) setProp(w *window, key, value string) error {
	if c.hooks.ParseProp == nil {
		return errors.New("no config parser to read the property with")
	}
	d, err := c.hooks.ParseProp(key, value)
	if err != nil {
		return err
	}
	k := &w.decided
	if k.override != nil {
		d = k.override.Then(d)
	}
	k.override = &d
	k.decider.Forget()
	w.border = c.borderTarget(w)
	return errors.Join(c.refreshLimits(w.top), c.reconcile(), c.scheduleAll())
}

func (c *Compositor) captureSetChanged() error {
	c.stateChanged()
	return c.scheduleAll()
}
