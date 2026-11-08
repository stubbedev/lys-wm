package compositor

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

const captureReleaseChord = "SUPER+Escape"

type inputCapture struct {
	c        *Compositor
	send     func(ipc.Event)
	barriers []ipc.Barrier
	enabled  bool
	keymap   string
}

type activeCapture struct {
	by         *inputCapture
	activation uint32
	unframed   bool
}

func (a *activeCapture) pointer(e ipc.Event) {
	a.unframed = true
	a.by.send(e)
}

func (a *activeCapture) frame() {
	if !a.unframed {
		return
	}
	a.unframed = false
	a.by.send(ipc.CaptureFrameEvent{})
}

func (c *Compositor) Capture(send func(ipc.Event)) (ipc.CaptureSession, error) {
	ic := &inputCapture{c: c, send: send}
	c.inputCaptures = append(c.inputCaptures, ic)
	ic.sendKeymap()
	return ic, nil
}

func (ic *inputCapture) sendKeymap() {
	text := ic.c.seatConfig.Keymap.Text()
	if text == ic.keymap {
		return
	}
	ic.keymap = text
	ic.send(ipc.CaptureKeymapEvent{Keymap: text})
}

func (c *Compositor) captureKeymapChanged() {
	for _, ic := range c.inputCaptures {
		ic.sendKeymap()
	}
}

func (ic *inputCapture) active() bool {
	return ic.c.capturing != nil && ic.c.capturing.by == ic
}

func (ic *inputCapture) Apply(cmd ipc.CaptureCommand) error {
	c := ic.c
	switch cmd := cmd.(type) {
	case ipc.SetBarriers:
		ic.barriers, ic.enabled = cmd.Barriers, false
		if ic.active() {
			return c.deactivateCapture()
		}
	case ipc.EnableCapture:
		ic.enabled = true
	case ipc.DisableCapture:
		ic.enabled = false
		if ic.active() {
			return c.endCapture()
		}
	case ipc.ReleaseCapture:
		if !ic.active() {
			return nil
		}
		activation := c.capturing.activation
		err := c.endCapture()
		if cmd.At != nil {
			err = errors.Join(err, c.moveCursor(backend.Now(), c.clampToOutputs(point{cmd.At.X, cmd.At.Y})), c.seat.PointerFrame())
		}
		ic.send(ipc.CaptureDeactivatedEvent{Activation: activation, At: capturePoint(c.cursor)})
		return err
	default:
		return fmt.Errorf("compositor: unknown capture command %T", cmd)
	}
	return nil
}

func (ic *inputCapture) Close() error {
	c := ic.c
	c.inputCaptures = slices.DeleteFunc(c.inputCaptures, func(o *inputCapture) bool { return o == ic })
	if ic.active() {
		return c.endCapture()
	}
	return nil
}

func capturePoint(p point) wmipc.CapturePoint {
	return wmipc.CapturePoint{X: p.X, Y: p.Y}
}

func captureModifiers(m xkb.Modifiers) ipc.CaptureModifiersEvent {
	return ipc.CaptureModifiersEvent{Depressed: m.Depressed, Latched: m.Latched, Locked: m.Locked, Group: m.Group}
}

func (c *Compositor) crossBarrier(dx, dy float64) (bool, error) {
	if c.capturing != nil || c.shell.Locked() || c.constraint != nil || c.seat.PointerGrabbed() || c.interaction != nil || c.pointerDrag() {
		return false, nil
	}
	from := c.cursor
	to := point{from.X + dx, from.Y + dy}
	for _, ic := range c.inputCaptures {
		if !ic.enabled {
			continue
		}
		for _, b := range ic.barriers {
			if crosses(b, from, to) {
				return true, c.activateCapture(ic, b.ID, to)
			}
		}
	}
	return false, nil
}

func crosses(b ipc.Barrier, from, to point) bool {
	if b.Vertical() {
		return crossesLine(float64(b.X1), from.X, to.X, from.Y, to.Y, b.Y1, b.Y2)
	}
	return crossesLine(float64(b.Y1), from.Y, to.Y, from.X, to.X, b.X1, b.X2)
}

func crossesLine(line, a0, a1, b0, b1 float64, lo, hi int) bool {
	if (a0 >= line || a1 < line) && (a0 < line || a1 >= line) {
		return false
	}
	t := (line - a0) / (a1 - a0)
	along := int(math.Floor(b0 + float64(t*(b1-b0))))
	return along >= lo && along <= hi
}

func (c *Compositor) activateCapture(ic *inputCapture, barrier ipc.BarrierID, at point) error {
	c.activations++
	c.capturing = &activeCapture{by: ic, activation: c.activations}
	c.interruptTap()
	err := errors.Join(c.repeat.stop(), c.seat.SuspendKeyboard(), c.seat.SetPointerFocus(nil, 0, 0), c.seat.PointerFrame())
	ic.send(ipc.CaptureActivatedEvent{Activation: c.activations, At: capturePoint(at), Barrier: barrier})
	ic.send(captureModifiers(c.seat.Modifiers()))
	return err
}

func (c *Compositor) endCapture() error {
	c.capturing = nil
	return errors.Join(c.seat.ResumeKeyboard(), c.refreshPointer())
}

func (c *Compositor) deactivateCapture() error {
	a := c.capturing
	if a == nil {
		return nil
	}
	err := c.endCapture()
	a.by.send(ipc.CaptureDeactivatedEvent{Activation: a.activation, At: capturePoint(c.cursor)})
	return err
}

func (c *Compositor) disableCapture() error {
	a := c.capturing
	if a == nil {
		return nil
	}
	a.by.enabled = false
	err := c.deactivateCapture()
	a.by.send(ipc.CaptureDisabledEvent{})
	return err
}

func (c *Compositor) releasesCapture(code uint32) (bool, error) {
	km := c.seatConfig.Keymap
	chord, err := binds.ParseChord(captureReleaseChord, km)
	if err != nil {
		return false, fmt.Errorf("compositor: the capture release chord: %w", err)
	}
	relevant, err := binds.Relevant(km)
	if err != nil {
		return false, fmt.Errorf("compositor: the capture release chord: %w", err)
	}
	return chord.Trigger == binds.Key{Code: code} && c.seat.DepressedModifiers()&relevant == chord.Mods, nil
}

func (c *Compositor) captureKey(e backend.Key) error {
	a := c.capturing
	if e.Pressed {
		release, err := c.releasesCapture(e.Code)
		if err != nil {
			return err
		}
		if release {
			c.held = append(c.held, e.Code)
			return c.disableCapture()
		}
	}
	before := c.seat.Modifiers()
	if err := c.seat.Key(e.Code, e.Pressed, e.Time); err != nil {
		return err
	}
	c.syncLEDs()
	a.by.send(ipc.CaptureKeyEvent{Key: e.Code, Pressed: e.Pressed})
	if m := c.seat.Modifiers(); m != before {
		a.by.send(captureModifiers(m))
	}
	return nil
}

func (c *Compositor) captureAxis(e backend.PointerAxis) {
	send := c.capturing.pointer
	dx, dy := 0.0, e.Value
	var dx120, dy120 int32 = 0, e.Value120
	if e.Orientation == backend.Horizontal {
		dx, dy, dx120, dy120 = e.Value, 0, e.Value120, 0
	}
	switch {
	case e.Value120 != 0:
		send(ipc.CaptureScrollDiscreteEvent{DX: dx120, DY: dy120})
	case e.Value == 0:
		send(ipc.CaptureScrollStopEvent{X: e.Orientation == backend.Horizontal, Y: e.Orientation == backend.Vertical})
	default:
		send(ipc.CaptureScrollEvent{DX: dx, DY: dy})
	}
}
