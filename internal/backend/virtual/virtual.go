// Package virtual is an output that exists only in memory: whatever backend
// runs the session, it renders offscreen with the session's renderer, paced by
// its own frame clock at its mode's refresh rate, and never presents.
package virtual

import (
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/stubbedev/lys-lib/edid"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
)

const (
	MaxEdge       = 16384
	MaxRefreshMHz = 1000000
)

type Handler interface {
	Frame(o backend.Output) error
	Presented(o backend.Output, p backend.Presentation) error
}

type Output struct {
	name       string
	mode       backend.Mode
	state      backend.OutputState
	renderer   render.Renderer
	handler    Handler
	target     render.ReadTarget
	drawn      bool
	clock      *backend.FrameClock
	timer      *loop.Timer
	armed      bool
	pending    bool
	inFrame    bool
	closed     bool
	empties    int
	lastVblank time.Duration
	nextVblank time.Duration
	seq        uint64
	nextSeq    uint64
	frames     uint64
}

var _ backend.Output = (*Output)(nil)

func New(l *loop.Loop, r render.Renderer, h Handler, name string, m backend.Mode) (*Output, error) {
	if err := m.Valid(); err != nil {
		return nil, fmt.Errorf("virtual: %s: %w", name, err)
	}
	if m.Size.X > MaxEdge || m.Size.Y > MaxEdge || m.RefreshMHz > MaxRefreshMHz {
		return nil, fmt.Errorf("virtual: %s: mode %dx%d@%dmHz is beyond %dx%d at %d Hz", name, m.Size.X, m.Size.Y, m.RefreshMHz, MaxEdge, MaxEdge, MaxRefreshMHz/1000)
	}
	m.Preferred = true
	o := &Output{name: name, mode: m, state: backend.OutputState{Mode: m}, renderer: r, handler: h}
	o.clock = backend.NewFrameClock(l.Idle, o.frameReady, o.runFrame, nil)
	var err error
	if o.timer, err = l.AddTimer(o.vblank); err != nil {
		return nil, fmt.Errorf("virtual: %s: frame timer: %w", name, err)
	}
	return o, nil
}

func (o *Output) Close() error {
	if o.closed {
		return nil
	}
	o.closed = true
	o.clock.Cancel()
	return errors.Join(o.timer.Remove(), o.freeTarget())
}

func (o *Output) Renew() error {
	if o.target == nil {
		return nil
	}
	if err := o.freeTarget(); err != nil {
		return err
	}
	return o.allocate(o.state.Mode.Size)
}

func (o *Output) Frames() uint64 {
	return o.frames
}

func (o *Output) Name() string {
	return o.name
}

func (o *Output) Description() string {
	return "Virtual output " + o.name
}

func (*Output) Identity() edid.Info {
	return edid.Info{}
}

func (*Output) PhysicalSize() image.Point {
	return image.Point{}
}

func (o *Output) Modes() []backend.Mode {
	return []backend.Mode{o.mode}
}

func (o *Output) State() backend.OutputState {
	return o.state
}

func (o *Output) Commit(s backend.OutputState) error {
	switch {
	case o.closed:
		return fmt.Errorf("virtual: commit to removed output %s", o.name)
	case !s.Enabled():
		o.clock.Cancel()
		o.pending = false
		o.state = s
		return o.freeTarget()
	case s.Mode.Size != o.mode.Size || s.Mode.RefreshMHz != o.mode.RefreshMHz:
		return fmt.Errorf("virtual: %s has only the mode %dx%d@%dmHz, not %dx%d@%dmHz", o.name, o.mode.Size.X, o.mode.Size.Y, o.mode.RefreshMHz, s.Mode.Size.X, s.Mode.Size.Y, s.Mode.RefreshMHz)
	case s.Depth != backend.Depth8:
		return fmt.Errorf("virtual: %s: %w", o.name, backend.ErrDepthUnsupported)
	case s.HDR.Active:
		return fmt.Errorf("virtual: %s: %w", o.name, backend.ErrHDRUnsupported)
	}
	if o.target == nil {
		if err := o.allocate(s.Mode.Size); err != nil {
			return err
		}
	}
	if !s.Lit() {
		o.clock.Cancel()
		o.pending = false
	}
	o.state = s
	return nil
}

func (o *Output) allocate(size image.Point) error {
	t, err := o.renderer.NewTarget(size)
	if err != nil {
		return fmt.Errorf("virtual: %s: offscreen target of %v: %w", o.name, size, err)
	}
	o.target, o.drawn = t, false
	return nil
}

func (o *Output) freeTarget() error {
	t := o.target
	o.target, o.drawn = nil, false
	if t == nil {
		return nil
	}
	if err := t.Destroy(); err != nil {
		return fmt.Errorf("virtual: %s: free the offscreen target: %w", o.name, err)
	}
	return nil
}

func (o *Output) ScheduleFrame() error {
	if !o.state.Lit() || o.closed {
		return nil
	}
	return o.clock.Request()
}

func (o *Output) frameReady() bool {
	return !o.pending && o.empties < backend.EmptyFramesPerRefresh && !o.inFrame && !o.closed && o.state.Lit()
}

func (o *Output) runFrame() error {
	o.frames++
	if err := o.handler.Frame(o); err != nil {
		return err
	}
	if !o.pending {
		o.empties++
	}
	return o.arm()
}

func (o *Output) BeginFrame() (backend.Frame, error) {
	switch {
	case o.closed:
		return backend.Frame{}, fmt.Errorf("virtual: frame on removed output %s", o.name)
	case o.target == nil:
		return backend.Frame{}, fmt.Errorf("virtual: frame on disabled output %s", o.name)
	case o.inFrame:
		return backend.Frame{}, backend.Broken("virtual: %s already has a frame in progress", o.name)
	}
	o.inFrame = true
	age := 0
	if o.drawn {
		age = 1
	}
	return backend.Frame{Renderer: o.renderer, Target: o.target, Age: age}, nil
}

func (o *Output) AbortFrame() {
	o.inFrame = false
}

func (o *Output) Present(render.Region) error {
	if !o.inFrame {
		return backend.Broken("virtual: present on %s without BeginFrame", o.name)
	}
	o.inFrame, o.drawn, o.pending = false, true, true
	return o.arm()
}

func (*Output) CursorPlane() (backend.CursorPlane, error) {
	return nil, backend.ErrNoCursorPlanes
}

func (o *Output) arm() error {
	if o.armed {
		return nil
	}
	now := backend.Now()
	at, ticks := backend.NextVblank(o.lastVblank, now, o.state.Mode.Interval())
	o.nextVblank, o.nextSeq = at, o.seq+ticks
	if err := o.timer.Arm(at - now); err != nil {
		return fmt.Errorf("virtual: arm the frame timer of %s: %w", o.name, err)
	}
	o.armed = true
	return nil
}

func (o *Output) vblank() error {
	o.armed = false
	o.lastVblank, o.seq = o.nextVblank, o.nextSeq
	idle := o.empties > 0
	o.empties = 0
	p := backend.Presentation{Time: o.lastVblank, Refresh: o.state.Mode.Interval(), Seq: o.seq}
	switch {
	case o.closed:
		return nil
	case o.pending:
		o.pending = false
		if err := o.handler.Presented(o, p); err != nil {
			return err
		}
	case idle:
		p.Idle = true
		if err := o.handler.Presented(o, p); err != nil {
			return err
		}
	}
	return o.clock.Kick()
}
