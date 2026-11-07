package compositor

import (
	"errors"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const DefaultGestureDistance = 400

type Gestures struct {
	Binds    []binds.GestureBind
	Distance float64
	Natural  bool
	Devices  map[string]bool
}

func DefaultGestures() Gestures {
	return Gestures{Binds: DefaultGestureBinds(anim.Style{Kind: anim.Slide}), Distance: DefaultGestureDistance, Natural: true}
}

func DefaultGestureBinds(workspaces anim.Style) []binds.GestureBind {
	if workspaces.Kind == anim.SlideVert {
		return []binds.GestureBind{{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeVertical, Drive: binds.DriveWorkspaces}}
	}
	return []binds.GestureBind{
		{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeHorizontal, Drive: binds.DriveWorkspaces},
		{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeVertical, Drive: binds.DriveOverview},
	}
}

func (g Gestures) natural(d backend.Device) bool {
	if v, ok := g.Devices[d.Name()]; ok {
		return v
	}
	return g.Natural
}

type driven interface {
	moved(by float64, at time.Duration) error
	released(at time.Duration, cancelled bool) error
}

type gestureInput struct {
	recognizer binds.Recognizer
	kind       backend.Gesture
	natural    bool
	binds      []binds.GestureBind
	drive      driven
	workspaces workspaceDrive
	held       *workspaceSwitch
	heldOn     *output.Output
	overview   bool
}

func (c *Compositor) gestureBegin(d backend.Device, e backend.GestureBegin) error {
	g := &c.gesture
	ended := c.endDrive()
	g.kind, g.natural = e.Gesture, c.gestures.natural(d)
	g.binds = binds.HeldFor(g.binds[:0], c.gestures.Binds, c.binds.Held(c.seat.DepressedModifiers()))
	step := g.recognizer.Begin(g.binds, e.Gesture, e.Fingers, !c.shell.Locked() && c.inhibiting == nil)
	if g.recognizer.Drives(binds.DriveWorkspaces) {
		if o := c.outputAt(c.cursor); o != nil {
			now := c.clock()
			c.finishSwitches(o, now)
			if sw, ok := c.switches[o]; ok {
				sw.pos.Grab(now)
				g.held, g.heldOn = sw, o
				g.recognizer.Swallow()
				return ended
			}
		}
	}
	if g.recognizer.Drives(binds.DriveOverview) && c.grabOverview() {
		g.overview = true
		g.recognizer.Swallow()
		return ended
	}
	if step.Forward {
		return errors.Join(ended, c.seat.GestureBegin(e.Time, e.Gesture, e.Fingers))
	}
	return errors.Join(ended, c.gestureStep(step, e.Time))
}

func (c *Compositor) gestureStep(step binds.Step, at time.Duration) error {
	var cancelled, released, claimed error
	if step.CancelClient {
		cancelled = c.seat.GestureEnd(at, c.gesture.kind, true)
	}
	if step.Passed {
		released = c.releaseHeld(at)
	}
	if step.Claimed {
		claimed = c.claimGesture(step.Bind, at)
	}
	if step.Fire {
		c.run(step.Bind.Action, nil, wm.FocusOther)
	}
	return errors.Join(cancelled, released, claimed)
}

func (c *Compositor) claimGesture(b binds.GestureBind, at time.Duration) error {
	g := &c.gesture
	switch {
	case b.Drive == binds.DriveWorkspaces && !c.overviewHolds():
		g.drive = c.driveWorkspaces(at)
		return nil
	case b.Drive == binds.DriveZoom:
		g.drive = c.driveZoom()
		return c.releaseHeld(at)
	case b.Drive == binds.DriveOverview:
		g.overview = false
		err := c.releaseHeld(at)
		drive, opened := c.driveOverview(b, at)
		g.drive = drive
		return errors.Join(err, opened)
	}
	return c.releaseHeld(at)
}

func (c *Compositor) releaseHeld(at time.Duration) error {
	g := &c.gesture
	sw, o := g.held, g.heldOn
	g.held, g.heldOn = nil, nil
	if g.overview {
		g.overview = false
		return c.releaseOverview(at)
	}
	if sw == nil || c.switches[o] != sw || !sw.pos.Tracking() {
		return nil
	}
	active, ok := c.wm.ActiveOn(o.Backend().Name())
	if !ok {
		return nil
	}
	sw.pos.Release(sw.restOn(active), at, c.clock(), c.animations.Curve(anim.Workspaces))
	return c.schedule(o)
}

func (c *Compositor) swipeUpdate(e *backend.SwipeUpdate) error {
	step := c.gesture.recognizer.Swipe(e.UnaccelDX, e.UnaccelDY)
	if step.Forward {
		return c.seat.SwipeUpdate(e.Time, e.DX, e.DY)
	}
	err := c.gestureStep(step, e.Time)
	if c.gesture.drive == nil || step.Travel == 0 {
		return err
	}
	return errors.Join(err, c.gesture.drive.moved(step.Travel/c.gestures.Distance, e.Time))
}

func (c *Compositor) pinchUpdate(e *backend.PinchUpdate) error {
	step := c.gesture.recognizer.Pinch(e.Scale)
	if step.Forward {
		return c.seat.PinchUpdate(e.Time, e.DX, e.DY, e.Scale, e.Rotation)
	}
	err := c.gestureStep(step, e.Time)
	if c.gesture.drive == nil {
		return err
	}
	return errors.Join(err, c.gesture.drive.moved(e.Scale, e.Time))
}

func (c *Compositor) gestureEnd(e backend.GestureEnd) error {
	g := &c.gesture
	step := g.recognizer.End(e.Cancelled)
	var ended, released error
	if step.Forward {
		ended = c.seat.GestureEnd(e.Time, e.Gesture, e.Cancelled)
	} else {
		ended = c.gestureStep(step, e.Time)
	}
	if g.drive != nil {
		released = g.drive.released(e.Time, e.Cancelled)
		g.drive = nil
	}
	return errors.Join(ended, released, c.releaseHeld(e.Time))
}

func (c *Compositor) endDrive() error {
	g := &c.gesture
	now := c.clock()
	err := c.releaseHeld(now)
	if g.drive == nil {
		return err
	}
	drive := g.drive
	g.drive = nil
	return errors.Join(err, drive.released(now, true))
}

type workspaceDrive struct {
	c  *Compositor
	o  *output.Output
	sw *workspaceSwitch
}

func (c *Compositor) driveWorkspaces(at time.Duration) driven {
	g := &c.gesture
	sw, o := g.held, g.heldOn
	g.held, g.heldOn = nil, nil
	if o == nil {
		if o = c.outputAt(c.cursor); o == nil {
			return nil
		}
	}
	if sw == nil || c.switches[o] != sw || !sw.pos.Tracking() {
		active, ok := c.wm.ActiveOn(o.Backend().Name())
		if !ok {
			return nil
		}
		sw = &workspaceSwitch{from: active, forward: true, style: c.animations.Style(anim.Workspaces), pos: anim.NewProgress(0)}
		sw.pos.Grab(c.clock())
		sw.pos.Track(0, at)
		c.switches[o] = sw
	}
	g.workspaces = workspaceDrive{c: c, o: o, sw: sw}
	return &g.workspaces
}

func (d *workspaceDrive) live() bool {
	return d.c.switches[d.o] == d.sw && d.sw.pos.Tracking()
}

func (d *workspaceDrive) moved(delta float64, at time.Duration) error {
	if !d.live() || d.c.shell.Locked() {
		return nil
	}
	if d.c.gesture.natural {
		delta = -delta
	}
	sw, name := d.sw, d.o.Backend().Name()
	step := d.c.wm.SwipeWorkspace(name, sw.from, sw.pos.At(0), delta)
	if step.Shift != 0 {
		sw.pos.Shift(step.Shift)
		sw.from = step.From
	}
	if step.Shift != 0 || step.To == 0 || sw.to == 0 || (sw.to > sw.from) != (step.At >= 0) {
		sw.to = step.To
	}
	sw.forward = step.At >= 0
	sw.pos.Track(step.At, at)
	var err error
	if active, ok := d.c.wm.ActiveOn(name); !ok || (active != sw.from && active != sw.to) {
		err = d.c.showWorkspace(name, sw.from)
	}
	return errors.Join(err, d.c.schedule(d.o))
}

func (d *workspaceDrive) released(at time.Duration, cancelled bool) error {
	if !d.live() {
		return nil
	}
	c, sw, name := d.c, d.sw, d.o.Backend().Name()
	now := c.clock()
	curve := c.animations.Curve(anim.Workspaces)
	active, ok := c.wm.ActiveOn(name)
	if !ok {
		return nil
	}
	target, rest := sw.restOn(active), active
	if !cancelled {
		target, rest = wm.WorkspaceSwipe{From: sw.from, To: sw.to}.Rest(sw.pos.Projected(at))
	}
	sw.pos.Release(target, at, now, curve)
	if rest == active {
		return c.schedule(d.o)
	}
	return c.showWorkspace(name, rest)
}

func (c *Compositor) showWorkspace(name string, id wm.WorkspaceID) error {
	c.shownWorkspace[name] = id
	s, err := c.wm.FocusOutput(name)
	if err != nil {
		return err
	}
	return c.update(s.SwitchWorkspace(id))
}
