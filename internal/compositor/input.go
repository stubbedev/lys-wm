package compositor

import (
	"errors"
	"image"
	"math"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/output"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type point struct {
	X, Y float64
}

func (p point) pixel() image.Point {
	return image.Pt(int(math.Floor(p.X)), int(math.Floor(p.Y)))
}

func (p point) sub(o image.Point) point {
	return point{p.X - float64(o.X), p.Y - float64(o.Y)}
}

type origin struct {
	base image.Point
	px   image.Point
	k    output.Scale
}

func flat(at image.Point) origin {
	return origin{base: at, k: output.Unit}
}

func (r origin) child(offset image.Point) origin {
	return origin{base: r.base, px: r.px.Add(offset), k: r.k}
}

func (r origin) at() point {
	return r.global(point{})
}

func (r origin) local(p point) point {
	f := r.k.Float()
	return point{(p.X-float64(r.base.X))*f - float64(r.px.X), (p.Y-float64(r.base.Y))*f - float64(r.px.Y)}
}

func (r origin) global(l point) point {
	f := r.k.Float()
	return point{float64(r.base.X) + (float64(r.px.X)+l.X)/f, float64(r.base.Y) + (float64(r.px.Y)+l.Y)/f}
}

func (r origin) device(o *output.Output, surface image.Point) image.Rectangle {
	base, s := r.base.Sub(o.Position()), o.Scale()
	return image.Rectangle{Min: s.DevicePointAt(base, r.px, r.k), Max: s.DevicePointAt(base, r.px.Add(surface), r.k)}
}

func wlCaps(c backend.Capability) sw.WlSeatCapability {
	var caps sw.WlSeatCapability
	if c.Has(backend.Keyboard) {
		caps |= sw.WlSeatCapabilityKeyboard
	}
	if c.Has(backend.Pointer) {
		caps |= sw.WlSeatCapabilityPointer
	}
	if c.Has(backend.Touch) {
		caps |= sw.WlSeatCapabilityTouch
	}
	return caps
}

func (c *Compositor) DeviceAdded(d backend.Device) error {
	if k, ok := d.(backend.KeyboardLEDs); ok && d.Capabilities().Has(backend.Keyboard) {
		c.keyboards = append(c.keyboards, k)
		k.SetLEDs(c.leds)
	}
	return c.seat.AddDevice(wlCaps(d.Capabilities()))
}

func (c *Compositor) DeviceRemoved(d backend.Device) error {
	c.keyboards = slices.DeleteFunc(c.keyboards, func(k backend.KeyboardLEDs) bool { return any(k) == any(d) })
	return c.seat.RemoveDevice(wlCaps(d.Capabilities()))
}

func (c *Compositor) syncLEDs() {
	l := c.seat.Locks()
	if l == c.leds {
		return
	}
	c.leds = l
	for _, k := range c.keyboards {
		k.SetLEDs(l)
	}
}

func (c *Compositor) Cursor() (float64, float64) {
	return c.cursor.X, c.cursor.Y
}

func (c *Compositor) layoutBounds() image.Rectangle {
	var b image.Rectangle
	for _, o := range c.outputs {
		b = b.Union(o.Logical())
	}
	return b
}

func denormalize(r image.Rectangle, x, y float64) point {
	return point{float64(r.Min.X) + x*float64(r.Dx()), float64(r.Min.Y) + y*float64(r.Dy())}
}

func (c *Compositor) clampToOutputs(p point) point {
	best, nearest := p, math.Inf(1)
	for _, o := range c.outputs {
		r := o.Logical()
		q := point{
			X: math.Min(math.Max(p.X, float64(r.Min.X)), float64(r.Max.X)-1),
			Y: math.Min(math.Max(p.Y, float64(r.Min.Y)), float64(r.Max.Y)-1),
		}
		if d := math.Hypot(q.X-p.X, q.Y-p.Y); d < nearest {
			best, nearest = q, d
		}
	}
	return best
}

func (c *Compositor) absoluteArea(o backend.Output) (image.Rectangle, bool) {
	if o == nil {
		return c.layoutBounds(), len(c.outputs) > 0
	}
	out := c.output(o)
	if out == nil {
		return image.Rectangle{}, false
	}
	return out.Logical(), true
}

func (c *Compositor) inArea(area image.Rectangle, x, y float64) point {
	return c.clampToOutputs(denormalize(area, x, y))
}

func (c *Compositor) Input(_ backend.Device, e backend.Event) error {
	if _, lid := e.(backend.Switch); !lid {
		if err := c.idle.Activity(); err != nil {
			return err
		}
	}
	switch e := e.(type) {
	case backend.Key:
		if bound, err := c.bindKey(e.Code, e.Pressed); bound || err != nil {
			return err
		}
		err := c.seat.Key(e.Code, e.Pressed, e.Time)
		c.syncLEDs()
		return err
	case backend.PointerMotion:
		if err := c.seat.RelativeMotion(e.Time, e.DX, e.DY, e.UnaccelDX, e.UnaccelDY); err != nil {
			return err
		}
		return c.moveCursor(e.Time, c.clampToOutputs(point{X: c.cursor.X + e.DX, Y: c.cursor.Y + e.DY}))
	case backend.PointerMotionAbsolute:
		area, ok := c.absoluteArea(e.Output)
		if !ok {
			return nil
		}
		return c.moveCursor(e.Time, c.inArea(area, e.X, e.Y))
	case backend.PointerButton:
		if c.pointerDrag() {
			if e.Pressed {
				return nil
			}
			return c.drag.Drop()
		}
		if i := slices.Index(c.consumed, e.Button); !e.Pressed && i >= 0 {
			c.consumed = slices.Delete(c.consumed, i, i+1)
			released := c.releaseShortcuts(binds.Button{Code: e.Button})
			if c.interaction != nil && c.interaction.button == e.Button {
				return errors.Join(released, c.endDrag())
			}
			return released
		}
		if e.Pressed && !c.seat.PointerGrabbed() && c.outsideGrab(c.seat.PointerFocus()) {
			c.consumed = append(c.consumed, e.Button)
			return c.shell.DismissGrab()
		}
		if b, ok := c.match(binds.Button{Code: e.Button}); e.Pressed && ok {
			c.consumed = append(c.consumed, e.Button)
			c.run(b.Action, binds.Button{Code: e.Button})
			return nil
		}
		if e.Pressed {
			if err := c.clickFocus(c.seat.PointerFocus()); err != nil {
				return err
			}
		}
		return c.seat.PointerButton(e.Time, e.Button, e.Pressed)
	case backend.PointerAxis:
		if e.Orientation == backend.Vertical && e.Value != 0 {
			dir := binds.ScrollDown
			if e.Value < 0 {
				dir = binds.ScrollUp
			}
			if b, ok := c.match(binds.Scroll{Dir: dir}); ok {
				c.run(b.Action, binds.Scroll{Dir: dir})
				return nil
			}
		}
		a := seat.Axis{Orientation: sw.WlPointerAxisVerticalScroll, Source: sw.WlPointerAxisSource(e.Source), Value: e.Value, Value120: e.Value120}
		if e.Orientation == backend.Horizontal {
			a.Orientation = sw.WlPointerAxisHorizontalScroll
		}
		return c.seat.PointerAxis(e.Time, a)
	case backend.PointerFrame:
		return c.seat.PointerFrame()
	case backend.GestureBegin:
		return c.seat.GestureBegin(e.Time, e.Gesture, e.Fingers)
	case backend.SwipeUpdate:
		return c.seat.SwipeUpdate(e.Time, e.DX, e.DY)
	case backend.PinchUpdate:
		return c.seat.PinchUpdate(e.Time, e.DX, e.DY, e.Scale, e.Rotation)
	case backend.GestureEnd:
		return c.seat.GestureEnd(e.Time, e.Gesture, e.Cancelled)
	case backend.TouchDown:
		area, ok := c.absoluteArea(e.Output)
		if !ok {
			return nil
		}
		p := c.inArea(area, e.X, e.Y)
		s, local, _ := c.surfaceAt(p)
		if c.outsideGrab(s) {
			return c.shell.DismissGrab()
		}
		if s == nil {
			return nil
		}
		c.touches[e.ID] = touchAt{area: area, at: p}
		return c.seat.TouchDown(e.Time, e.ID, s, local.X, local.Y)
	case backend.TouchMotion:
		t, ok := c.touches[e.ID]
		if !ok {
			return nil
		}
		t.at = c.inArea(t.area, e.X, e.Y)
		c.touches[e.ID] = t
		if c.touchDrag(e.ID) {
			return errors.Join(c.dragMotion(e.Time), c.scheduleAll())
		}
		s := c.seat.TouchSurface(e.ID)
		if s == nil {
			return nil
		}
		org, found := c.originOf(s)
		if !found {
			return nil
		}
		local := org.local(t.at)
		return c.seat.TouchMotion(e.Time, e.ID, local.X, local.Y)
	case backend.TouchUp:
		if c.touchDrag(e.ID) {
			c.seat.DropTouch(e.ID)
			err := c.drag.Drop()
			delete(c.touches, e.ID)
			return err
		}
		delete(c.touches, e.ID)
		return c.seat.TouchUp(e.Time, e.ID)
	case backend.TouchCancel:
		var err error
		if c.touchDragging() {
			err = c.drag.Cancel()
		}
		clear(c.touches)
		return errors.Join(err, c.seat.TouchCancel())
	case backend.TouchFrame:
		return c.seat.TouchFrame()
	case backend.Switch:
		if e.Kind != backend.SwitchLid || e.On == c.lidClosed {
			return nil
		}
		c.lidClosed = e.On
		err := c.applyOutputRules()
		c.applyLidPolicy()
		return err
	}
	return nil
}

func (c *Compositor) moveCursor(at time.Duration, next point) error {
	next, moved := c.constrainMotion(next)
	if !moved {
		return nil
	}
	c.cursor = next
	return errors.Join(c.pointerMoved(at), c.refreshConstraint())
}

func (c *Compositor) pointerMoved(at time.Duration) error {
	if c.interaction != nil {
		return errors.Join(c.dragMoved(), c.scheduleAll())
	}
	if c.pointerDrag() {
		return errors.Join(c.dragMotion(at), c.scheduleAll())
	}
	if f := c.seat.PointerFocus(); f != nil && c.seat.PointerGrabbed() {
		if org, ok := c.originOf(f); ok {
			local := org.local(c.cursor)
			return errors.Join(c.seat.PointerMotion(at, local.X, local.Y), c.cursorMoved())
		}
	}
	s, local, w := c.pointerTarget()
	var err error
	if s != c.seat.PointerFocus() {
		err = c.seat.SetPointerFocus(s, local.X, local.Y)
	} else if s != nil {
		err = c.seat.PointerMotion(at, local.X, local.Y)
	}
	if w != nil && c.grabClient() == nil && (c.active != w || c.seat.Focus() != w.top.Surface()) {
		err = errors.Join(err, c.focusWindow(w))
	}
	return errors.Join(err, c.cursorMoved())
}

func (c *Compositor) pointerTarget() (*surface.Surface, point, *window) {
	s, local, w := c.surfaceAt(c.cursor)
	if s != nil && c.outsideGrab(s) {
		return nil, point{}, nil
	}
	return s, local, w
}

func (c *Compositor) refreshPointer() error {
	if c.interaction != nil {
		return nil
	}
	if c.pointerDrag() {
		return c.dragMotion(backend.Now())
	}
	if c.seat.PointerGrabbed() {
		return nil
	}
	s, local, _ := c.pointerTarget()
	if s == c.seat.PointerFocus() {
		return nil
	}
	return errors.Join(c.seat.SetPointerFocus(s, local.X, local.Y), c.seat.PointerFrame())
}

func hitPopups(popups []placedPopup, p point) (*surface.Surface, point) {
	for _, pp := range slices.Backward(popups) {
		if s, local := hitTree(pp.popup.Surface(), flat(pp.origin), p); s != nil {
			return s, local
		}
	}
	return nil, point{}
}

func (c *Compositor) hitLayers(o *output.Output, levels []shell.Layer, p point) (*surface.Surface, point) {
	var hit *surface.Surface
	var at point
	for e := range c.layersAt(o, levels) {
		if s, local := hitTree(e.surface.Surface(), flat(e.bounds().Min), p); s != nil {
			hit, at = s, local
		}
	}
	return hit, at
}

func (c *Compositor) outputAt(p point) *output.Output {
	for _, o := range c.outputs {
		if p.pixel().In(o.Logical()) {
			return o
		}
	}
	return nil
}

func (c *Compositor) hitWindow(w *window, p point) (*surface.Surface, point) {
	if !w.visible {
		return nil, point{}
	}
	if s, local := hitPopups(c.popups(w), p); s != nil {
		return s, local
	}
	return hitTree(w.top.Surface(), c.windowOrigin(w), p)
}

func (c *Compositor) surfaceAt(p point) (*surface.Surface, point, *window) {
	if c.shell.Locked() {
		s, local := c.hitLock(p)
		return s, local, nil
	}
	if s, local := c.hitUnmanaged(p); s != nil {
		return s, local, nil
	}
	if s, local := c.hitInputPopups(p); s != nil {
		return s, local, nil
	}
	for _, e := range slices.Backward(c.layers) {
		if s, local := hitPopups(c.layerPopups(e), p); s != nil {
			return s, local, nil
		}
	}
	o := c.outputAt(p)
	if s, local := c.hitLayers(o, overlay, p); s != nil {
		return s, local, nil
	}
	for _, w := range slices.Backward(c.stack) {
		if w.layer == wm.LayerFullscreen {
			if s, local := c.hitWindow(w, p); s != nil {
				return s, local, w
			}
		}
	}
	if s, local := c.hitLayers(o, aboveWindows, p); s != nil {
		return s, local, nil
	}
	for _, w := range slices.Backward(c.stack) {
		if w.layer != wm.LayerFullscreen {
			if s, local := hitPopups(c.popups(w), p); s != nil {
				return s, local, w
			}
		}
	}
	for _, layer := range slices.Backward(windowLayers) {
		for _, w := range slices.Backward(c.stack) {
			if w.layer != layer || !w.visible {
				continue
			}
			if s, local := hitTree(w.top.Surface(), c.windowOrigin(w), p); s != nil {
				return s, local, w
			}
		}
	}
	if s, local := c.hitLayers(o, belowWindows, p); s != nil {
		return s, local, nil
	}
	return nil, point{}, nil
}

func hitTree(s *surface.Surface, org origin, p point) (*surface.Surface, point) {
	for member := range s.StackTopDown() {
		if member != s {
			if sub := member.Subsurface(); sub != nil {
				if hit, local := hitTree(member, org.child(sub.Position()), p); hit != nil {
					return hit, local
				}
			}
			continue
		}
		local := org.local(p)
		if s.Buffer() != nil && s.AcceptsInput(local.pixel()) {
			return s, local
		}
	}
	return nil, point{}
}

func (c *Compositor) originOf(target *surface.Surface) (origin, bool) {
	if org, ok := c.baseOrigin(target); ok {
		return org, true
	}
	for _, placed := range c.inputPopups() {
		if org, ok := treeOrigin(placed.popup.Surface, flat(placed.at.Min), target); ok {
			return org, true
		}
	}
	return origin{}, false
}

func (c *Compositor) baseOrigin(target *surface.Surface) (origin, bool) {
	for _, o := range c.outputs {
		if ls := c.lockSurfaceOn(o); ls != nil {
			if org, ok := treeOrigin(ls.Surface(), flat(o.Position()), target); ok {
				return org, true
			}
		}
	}
	for _, w := range c.unmanaged {
		if org, ok := treeOrigin(w.Surface(), c.unmanagedOrigin(w), target); ok {
			return org, true
		}
	}
	for _, w := range c.windows {
		if !w.visible {
			continue
		}
		if org, ok := treeOrigin(w.top.Surface(), c.windowOrigin(w), target); ok {
			return org, true
		}
		for _, pp := range c.popups(w) {
			if org, ok := treeOrigin(pp.popup.Surface(), flat(pp.origin), target); ok {
				return org, true
			}
		}
	}
	for _, e := range c.layers {
		if !e.shown() {
			continue
		}
		if org, ok := treeOrigin(e.surface.Surface(), flat(e.bounds().Min), target); ok {
			return org, true
		}
		for _, pp := range c.layerPopups(e) {
			if org, ok := treeOrigin(pp.popup.Surface(), flat(pp.origin), target); ok {
				return org, true
			}
		}
	}
	return origin{}, false
}

func treeOrigin(s *surface.Surface, org origin, target *surface.Surface) (origin, bool) {
	if s == target {
		return org, true
	}
	for member := range s.Stack() {
		if member != s && member.Subsurface() != nil {
			if found, ok := treeOrigin(member, org.child(member.Subsurface().Position()), target); ok {
				return found, true
			}
		}
	}
	return origin{}, false
}
