package compositor

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-wm/internal/binds"
	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type interaction struct {
	mode  wm.DragMode
	w     *window
	by    dragInput
	start image.Point
	last  image.Point
	rect  image.Rectangle
	edges wm.Edges
	drop  tiledDrop
}

type buttonInput struct{ code uint32 }

func (buttonInput) at(c *Compositor) point { return c.cursor }

var ErrNotFromMouseBind = errors.New("only a mouse bind can start a drag")

func (c *Compositor) beginDrag(mode wm.DragMode, trigger binds.Trigger) error {
	button, ok := trigger.(binds.Button)
	if !ok {
		return ErrNotFromMouseBind
	}
	if err := c.landInFlight(); err != nil {
		return err
	}
	_, _, w := c.surfaceAt(c.cursor)
	if w == nil {
		return nil
	}
	at := c.cursor.pixel()
	mid := w.tile.Min.Add(w.tile.Size().Div(2))
	horizontal, vertical := wm.EdgeLeft, wm.EdgeTop
	if at.X >= mid.X {
		horizontal = wm.EdgeRight
	}
	if at.Y >= mid.Y {
		vertical = wm.EdgeBottom
	}
	return c.startDrag(w, mode, buttonInput{button.Code}, horizontal|vertical)
}

func (c *Compositor) clientDrag(w *window, mode wm.DragMode, by dragInput, edges wm.Edges) error {
	if c.interaction != nil || c.drag != nil {
		return nil
	}
	if err := c.landInFlight(); err != nil {
		return err
	}
	if err := c.startDrag(w, mode, by, edges); err != nil || c.interaction == nil {
		return err
	}
	if b, ok := by.(buttonInput); ok {
		c.consumed = append(c.consumed, b.code)
	}
	return nil
}

func (c *Compositor) pressedBy(cl *server.Client, serial uint32) (dragInput, bool) {
	if code, ok := c.seat.PointerPress(cl, serial); ok {
		return buttonInput{code}, true
	}
	if id, ok := c.seat.TouchPress(cl, serial); ok {
		return touchInput{id}, true
	}
	return nil, false
}

func (c *Compositor) startDrag(w *window, mode wm.DragMode, by dragInput, edges wm.Edges) error {
	if w.layer == wm.LayerFullscreen {
		return nil
	}
	at := by.at(c).pixel()
	c.interaction = &interaction{mode: mode, w: w, by: by, start: at, last: at, rect: w.tile, edges: edges}
	if err := c.focusWindow(w); err != nil {
		return err
	}
	if _, ok := by.(buttonInput); !ok {
		return nil
	}
	return errors.Join(c.seat.SetPointerFocus(nil, 0, 0), c.seat.PointerFrame())
}

func (c *Compositor) Dragging() bool {
	return c.interaction != nil
}

func (c *Compositor) draggedBy(by dragInput) bool {
	return c.interaction != nil && c.interaction.by == by
}

func (c *Compositor) cancelDrag(w *window) error {
	if c.interaction == nil || c.interaction.w != w {
		return nil
	}
	return c.endDrag()
}

func (c *Compositor) flushDrag() error {
	if !c.dragPending || c.interaction == nil {
		c.dragPending = false
		return nil
	}
	c.dragPending = false
	return errors.Join(c.dragTo(c.interaction), c.trackDrop(c.interaction, c.dragMovedAt))
}

func (c *Compositor) dragTo(it *interaction) error {
	at := it.by.at(c).pixel()
	total, step := at.Sub(it.start), at.Sub(it.last)
	it.last = at
	floating := it.w.layer == wm.LayerFloating
	switch {
	case floating && it.mode == wm.DragMove:
		return c.dragged(c.wm.SetFloatingRect(it.w.id, it.rect.Add(total)))
	case floating:
		return c.dragged(c.wm.ResizeFloating(it.w.id, it.rect, total, it.edges))
	case it.mode == wm.DragResize:
		s := c.wm
		var err error
		for _, axis := range [...]struct {
			delta      int
			lead, tail wm.Edges
			ahead      wm.Direction
			behind     wm.Direction
		}{{step.X, wm.EdgeLeft, wm.EdgeRight, wm.Right, wm.Left}, {step.Y, wm.EdgeTop, wm.EdgeBottom, wm.Down, wm.Up}} {
			switch {
			case axis.delta == 0:
			case it.edges.Has(axis.lead):
				s, err = s.ResizeTiled(it.w.id, axis.behind, -axis.delta)
			case it.edges.Has(axis.tail):
				s, err = s.ResizeTiled(it.w.id, axis.ahead, axis.delta)
			}
			if err != nil {
				return err
			}
		}
		return c.dragged(s, nil)
	}
	return nil
}

func (c *Compositor) dragged(s wm.State, err error) error {
	if err != nil {
		return err
	}
	c.wm = s
	c.stateChanged()
	c.layoutDirty = true
	return nil
}

func (c *Compositor) endDrag() error {
	it := c.interaction
	if err := c.flushDrag(); err != nil {
		return fmt.Errorf("compositor: end the drag of window %d: %w", it.w.id, err)
	}
	if c.layoutDirty {
		if err := c.reconcile(); err != nil {
			return fmt.Errorf("compositor: end the drag of window %d: %w", it.w.id, err)
		}
	}
	c.interaction = nil
	if c.lagging {
		if err := c.reconcile(); err != nil {
			return fmt.Errorf("compositor: end the drag of window %d: %w", it.w.id, err)
		}
	}
	c.hidePreview(c.clock())
	if err := c.endDrop(it); err != nil {
		return fmt.Errorf("compositor: snap window %d: %w", it.w.id, err)
	}
	if it.mode == wm.DragMove && it.w.layer == wm.LayerTiled && it.w.tile != (image.Rectangle{}) {
		if err := c.update(c.wm.Reinsert(it.w.id, it.by.at(c).pixel(), c.settings.Autotile)); err != nil {
			return fmt.Errorf("compositor: drop window %d: %w", it.w.id, err)
		}
	}
	return errors.Join(c.refreshPointer(), c.seat.PointerFrame(), c.scheduleAll())
}

func (c *Compositor) markDragged() {
	if !c.dragPending {
		c.dragPending, c.dragMovedAt = true, c.clock()
	}
}

var xdgEdgeBits = [...]struct {
	xdg sxdg.XdgToplevelResizeEdge
	wm  wm.Edges
}{
	{sxdg.XdgToplevelResizeEdgeTop, wm.EdgeTop},
	{sxdg.XdgToplevelResizeEdgeBottom, wm.EdgeBottom},
	{sxdg.XdgToplevelResizeEdgeLeft, wm.EdgeLeft},
	{sxdg.XdgToplevelResizeEdgeRight, wm.EdgeRight},
}

func xdgEdges(e sxdg.XdgToplevelResizeEdge) wm.Edges {
	var edges wm.Edges
	for _, b := range xdgEdgeBits {
		if e&b.xdg != 0 {
			edges |= b.wm
		}
	}
	return edges
}

func (c *Compositor) xdgDrag(w *window, t *shell.Toplevel, serial uint32, mode wm.DragMode, edges wm.Edges) error {
	if mode == wm.DragResize && edges == 0 {
		return nil
	}
	by, ok := c.pressedBy(t.Surface().Client(), serial)
	if !ok {
		return nil
	}
	return c.clientDrag(w, mode, by, edges)
}
