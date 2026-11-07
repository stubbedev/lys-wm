package compositor

import (
	"errors"

	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	ErrPickCancelled = errors.New("compositor: the window pick was cancelled")
	errPicking       = errors.New("compositor: a window pick is already running")
)

type pick struct {
	answer func(*wm.WindowID, error)
}

func (c *Compositor) PickWindow(answer func(*wm.WindowID, error)) (func(), error) {
	if c.picking != nil {
		return nil, errPicking
	}
	p := &pick{answer: answer}
	c.picking = p
	cancel := func() {
		if c.picking == p {
			c.report(c.endPick())
		}
	}
	return cancel, errors.Join(c.seat.SetPointerFocus(nil, 0, 0), c.seat.PointerFrame(), c.scheduleAll())
}

func (c *Compositor) endPick() error {
	c.picking = nil
	return errors.Join(c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) pickAtCursor() error {
	p := c.picking
	_, _, w := c.surfaceAt(c.cursor)
	err := c.endPick()
	if w == nil {
		p.answer(nil, nil)
		return err
	}
	id := w.id
	p.answer(&id, nil)
	return err
}

func (c *Compositor) cancelPick() error {
	p := c.picking
	err := c.endPick()
	p.answer(nil, ErrPickCancelled)
	return err
}
