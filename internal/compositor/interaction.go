package compositor

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type interaction struct {
	mode   wm.DragMode
	w      *window
	button uint32
	start  image.Point
	last   image.Point
	rect   image.Rectangle
	right  bool
	bottom bool
}

var ErrNotFromMouseBind = errors.New("only a mouse bind can start a drag")

func (c *Compositor) beginDrag(mode wm.DragMode, trigger binds.Trigger) error {
	button, ok := trigger.(binds.Button)
	if !ok {
		return ErrNotFromMouseBind
	}
	_, _, w := c.surfaceAt(c.cursor)
	if w == nil || w.layer == wm.LayerFullscreen {
		return nil
	}
	at := c.cursor.pixel()
	mid := w.tile.Min.Add(w.tile.Size().Div(2))
	c.interaction = &interaction{mode: mode, w: w, button: button.Code, start: at, last: at, rect: w.tile, right: at.X >= mid.X, bottom: at.Y >= mid.Y}
	if err := c.focusWindow(w); err != nil {
		return err
	}
	return errors.Join(c.seat.SetPointerFocus(nil, 0, 0), c.seat.PointerFrame())
}

func (c *Compositor) dragMoved() error {
	it := c.interaction
	at := c.cursor.pixel()
	total, step := at.Sub(it.start), at.Sub(it.last)
	it.last = at
	floating := it.w.layer == wm.LayerFloating
	switch {
	case floating && it.mode == wm.DragMove:
		return c.update(c.wm.SetFloatingRect(it.w.id, it.rect.Add(total)))
	case floating:
		return c.update(c.wm.ResizeFloating(it.w.id, it.rect, total, it.right, it.bottom))
	case it.mode == wm.DragResize:
		s := c.wm
		var err error
		horizontal, vertical := wm.Left, wm.Up
		dx, dy := -step.X, -step.Y
		if it.right {
			horizontal, dx = wm.Right, step.X
		}
		if it.bottom {
			vertical, dy = wm.Down, step.Y
		}
		if dx != 0 {
			if s, err = s.ResizeTiled(it.w.id, horizontal, dx); err != nil {
				return err
			}
		}
		if dy != 0 {
			if s, err = s.ResizeTiled(it.w.id, vertical, dy); err != nil {
				return err
			}
		}
		return c.update(s, nil)
	}
	return c.scheduleAll()
}

func (c *Compositor) endDrag() error {
	it := c.interaction
	c.interaction = nil
	if it.mode == wm.DragMove && it.w.layer == wm.LayerTiled && it.w.tile != (image.Rectangle{}) {
		if err := c.update(c.wm.Reinsert(it.w.id, c.cursor.pixel(), c.settings.Autotile)); err != nil {
			return fmt.Errorf("compositor: drop window %d: %w", it.w.id, err)
		}
	}
	return errors.Join(c.refreshPointer(), c.seat.PointerFrame())
}
