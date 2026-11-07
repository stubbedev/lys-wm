package compositor

import (
	"errors"
	"image"

	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) swapOrigin(by wm.FocusOrigin) wm.FocusOrigin {
	outer := c.focusBy
	c.focusBy = by
	return outer
}

func (c *Compositor) focusWindow(w *window, by wm.FocusOrigin) error {
	outer := c.swapOrigin(by)
	defer c.swapOrigin(outer)
	c.layerFocus = nil
	return c.update(c.wm.FocusWindow(w.id, c.settings.Focus.Stacking(by)))
}

func (c *Compositor) focusMoved() {
	c.noteFocus()
	if c.focusBy != wm.FocusHover {
		c.hoverHeld = true
	}
}

func (c *Compositor) hoverFocus(w *window) error {
	if w != c.hovered {
		c.hovered, c.hoverHeld = w, false
	}
	if w == nil || c.grabClient() != nil {
		return nil
	}
	if c.hoverFollows() && (c.layerFocus != nil || !c.wm.Settled(w.id, c.settings.Focus.Stacking(wm.FocusHover))) {
		return c.focusWindow(w, wm.FocusHover)
	}
	if c.layerFocus == nil && c.wm.Settled(w.id, wm.KeepStacking) && c.seat.Focus() != w.top.Surface() {
		return c.refocus()
	}
	return nil
}

func (c *Compositor) hoverFollows() bool {
	switch c.settings.Focus.FollowsMouse {
	case wm.FollowsMouseAlways:
		return true
	case wm.FollowsMouseLoose:
		return !c.hoverHeld
	case wm.FollowsMouseOff:
	}
	return false
}

func (c *Compositor) clickWindow() error {
	if c.seat.PointerGrabbed() || c.grabClient() != nil {
		return nil
	}
	_, _, w := c.pointerTarget()
	if w == nil || c.layerFocus == nil && c.wm.Settled(w.id, wm.Raise) {
		return nil
	}
	return c.focusWindow(w, wm.FocusClick)
}

func (c *Compositor) focusUnderPointer() error {
	if c.settings.Focus.OnClose != wm.CloseFocusesUnderPointer {
		return nil
	}
	_, _, w := c.pointerTarget()
	if w == nil || w == c.active {
		return nil
	}
	return c.focusWindow(w, wm.FocusOther)
}

type focusMark struct {
	active      *window
	output      string
	workspace   wm.WorkspaceID
	constrained bool
}

func (c *Compositor) markFocus() focusMark {
	out := c.wm.FocusedOutput()
	ws, _ := c.wm.ActiveOn(out)
	return focusMark{active: c.active, output: out, workspace: ws, constrained: c.constraint != nil}
}

func (c *Compositor) warpAfterKeyboard(before focusMark) error {
	after := c.markFocus()
	if before.constrained {
		return nil
	}
	switch c.settings.Focus.WarpPointer {
	case wm.WarpOff:
		return nil
	case wm.WarpOnKeyboardFocus:
		if after.active == before.active || after.active == nil {
			return nil
		}
		return c.warpPointer(after.active.rect)
	case wm.WarpOnWorkspaceSwitch:
		if after.output == before.output && after.workspace == before.workspace {
			return nil
		}
		if after.active != nil {
			return c.warpPointer(after.active.rect)
		}
		for _, o := range c.outputs {
			if o.Backend().Name() == after.output {
				return c.warpPointer(o.Logical())
			}
		}
	}
	return nil
}

func (c *Compositor) warpPointer(r image.Rectangle) error {
	if r.Empty() || c.cursor.pixel().In(r) || c.constraint != nil || c.capturing != nil || c.interaction != nil {
		return nil
	}
	if _, hidden := c.seat.Cursor().(seat.HiddenCursor); hidden {
		return nil
	}
	centre := r.Min.Add(r.Size().Div(2))
	c.cursor = point{X: float64(centre.X), Y: float64(centre.Y)}
	c.warps++
	return errors.Join(c.refreshPointer(), c.refreshConstraint(), c.rimsMoved(), c.cursorMoved())
}

func (c *Compositor) StateUpdates() uint64 {
	return c.stateUpdates
}

func (c *Compositor) PointerWarps() uint64 {
	return c.warps
}
