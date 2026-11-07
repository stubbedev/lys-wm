package compositor

import (
	"errors"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
)

const (
	btnLeft    = 0x110
	btnRight   = 0x111
	btnMiddle  = 0x112
	btnStylus  = 0x14b
	btnStylus2 = 0x14c
)

var emulatedButtons = map[uint32]uint32{btnStylus: btnRight, btnStylus2: btnMiddle}

func (c *Compositor) tabletAdded(d backend.Device) error {
	var errs []error
	if t, ok := d.(tabletinput.Tablet); ok {
		if id, tablet := t.Tablet(); tablet {
			errs = append(errs, c.seat.AddTablet(d, t.Group(), d.Name(), id))
		}
	}
	if p, ok := d.(tabletinput.Pad); ok {
		if layout, pad := p.Pad(); pad {
			errs = append(errs, c.seat.AddPad(d, p.Group(), layout))
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) tabletRemoved(d backend.Device) error {
	return errors.Join(c.seat.RemovePad(d), c.seat.RemoveTablet(d))
}

func (c *Compositor) tabletInput(d backend.Device, e backend.Event) error {
	switch e := e.(type) {
	case *tabletinput.Proximity:
		if !e.In {
			return c.tabletLeave(e.Time, e.Tool)
		}
		return c.tabletEvent(d, e.Time, e.Tool, e.Axes, nil)
	case *tabletinput.Motion:
		return c.tabletEvent(d, e.Time, e.Tool, e.Axes, nil)
	case *tabletinput.Tip:
		return c.tabletEvent(d, e.Time, e.Tool, e.Axes, func() error { return c.tabletTip(e.Time, e.Tool, e.Down) })
	case *tabletinput.PadButton:
		return c.seat.PadButton(d, e)
	case *tabletinput.PadRing:
		return c.seat.PadRing(d, e)
	case *tabletinput.PadStrip:
		return c.seat.PadStrip(d, e)
	case *tabletinput.PadDial:
		return c.seat.PadDial(d, e)
	case *tabletinput.Button:
		if err := c.seat.TabletTool(e.Tool); err != nil {
			return err
		}
		return errors.Join(c.tabletButton(e.Time, e.Tool, e.Button, e.Pressed), c.seat.TabletToolFrame(e.Time, e.Tool))
	}
	return nil
}

func (c *Compositor) tabletEvent(d backend.Device, at time.Duration, tool *tabletinput.Tool, axes tabletinput.Axes, then func() error) error {
	if err := c.seat.TabletTool(tool); err != nil {
		return err
	}
	err := c.tabletMove(d, at, tool, axes)
	if then != nil {
		err = errors.Join(err, then())
	}
	return errors.Join(err, c.seat.TabletToolFrame(at, tool))
}

func (c *Compositor) tabletMove(d backend.Device, at time.Duration, tool *tabletinput.Tool, axes tabletinput.Axes) error {
	area, ok := c.deviceArea(d, nil)
	if !ok {
		return nil
	}
	p := c.inArea(area, axes.X, axes.Y)
	target := c.seat.TabletToolFocus(tool)
	var err error
	if target != nil && c.seat.TabletToolDown(tool) {
		c.cursor = p
		err = c.cursorMoved()
	} else {
		err = errors.Join(c.moveCursor(at, p), c.seat.PointerFrame())
		target = c.seat.PointerFocus()
	}
	org, found := c.originOf(target)
	if !found {
		target = nil
	}
	local := org.local(c.cursor)
	return errors.Join(err, c.seat.TabletToolMotion(at, d, tool, target, local.X, local.Y), c.seat.TabletToolAxes(tool, axes))
}

func (c *Compositor) tabletTip(at time.Duration, tool *tabletinput.Tool, down bool) error {
	if !down && slices.Contains(c.tabletButtons[tool], btnLeft) {
		return c.emulateTabletButton(at, tool, btnLeft, false)
	}
	target := c.seat.TabletToolFocus(tool)
	if target == nil {
		if down {
			return c.emulateTabletButton(at, tool, btnLeft, true)
		}
		return nil
	}
	if down {
		if err := c.clickFocus(target); err != nil {
			return err
		}
	}
	return c.seat.TabletToolTip(tool, down)
}

func (c *Compositor) tabletButton(at time.Duration, tool *tabletinput.Tool, button uint32, pressed bool) error {
	emulated, ok := emulatedButtons[button]
	if !ok {
		emulated = button
	}
	if !pressed && slices.Contains(c.tabletButtons[tool], emulated) {
		return c.emulateTabletButton(at, tool, emulated, false)
	}
	if c.seat.TabletToolFocus(tool) == nil {
		if pressed {
			return c.emulateTabletButton(at, tool, emulated, true)
		}
		return nil
	}
	return c.seat.TabletToolButton(tool, button, pressed)
}

func (c *Compositor) emulateTabletButton(at time.Duration, tool *tabletinput.Tool, button uint32, pressed bool) error {
	if c.tabletButtons == nil {
		c.tabletButtons = map[*tabletinput.Tool][]uint32{}
	}
	held := slices.DeleteFunc(c.tabletButtons[tool], func(b uint32) bool { return b == button })
	if pressed {
		held = append(held, button)
	}
	c.tabletButtons[tool] = held
	if len(held) == 0 {
		delete(c.tabletButtons, tool)
	}
	return errors.Join(c.inputButton(backend.PointerButton{Time: at, Button: button, Pressed: pressed}), c.seat.PointerFrame())
}

func (c *Compositor) tabletLeave(at time.Duration, tool *tabletinput.Tool) error {
	var errs []error
	for _, b := range slices.Clone(c.tabletButtons[tool]) {
		errs = append(errs, c.emulateTabletButton(at, tool, b, false))
	}
	return errors.Join(append(errs, c.seat.TabletToolOut(tool))...)
}
