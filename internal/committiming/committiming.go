// Package committiming implements wp_commit_timing_manager_v1: a content
// update can carry a target time, and it waits until the refresh that
// presents it would not show it before that time.
package committiming

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/loop"
	sct "github.com/stubbedev/lys-wm/internal/protocol/server/committiming"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

type Manager struct {
	loop     *loop.Loop
	nextFlip func(*surface.Surface) (time.Duration, bool)
	timers   []*timer
	waiting  []*timer
	armedAt  time.Duration
}

func (m *Manager) LastArmed() time.Duration {
	return m.armedAt
}

func New(d *server.Display, l *loop.Loop, nextFlip func(*surface.Surface) (time.Duration, bool)) (*Manager, error) {
	m := &Manager{loop: l, nextFlip: nextFlip}
	if _, err := d.AddGlobal(sct.WpCommitTimingManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Presented() error {
	m.waiting = m.waiting[:0]
	for _, t := range m.timers {
		if len(t.targets) > 0 {
			m.waiting = append(m.waiting, t)
		}
	}
	var errs error
	for _, t := range m.waiting {
		errs = errors.Join(errs, t.s.Retry())
	}
	clear(m.waiting)
	return errs
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sct.NewWpCommitTimingManagerV1(c, id, v, requests{m, c})
	return err
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*sct.WpCommitTimingManagerV1) error { return nil }

func (r requests) GetTimer(obj *sct.WpCommitTimingManagerV1, res *sct.WpCommitTimerV1, wl *sw.WlSurface) (sct.WpCommitTimerV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(r.m.timers, func(t *timer) bool { return t.s == s })
	if i >= 0 && r.m.timers[i].live {
		return nil, obj.ProtocolError(sct.WpCommitTimingManagerV1ErrorCommitTimerExists, "wl_surface@%d already has a commit timer", wl.Base().ID())
	}
	var t *timer
	if i >= 0 {
		t = r.m.timers[i]
	} else {
		t = &timer{m: r.m, s: s}
		if t.clock, err = r.m.loop.AddTimer(t.fire); err != nil {
			return nil, fmt.Errorf("committiming: a timer for wl_surface@%d: %w", wl.Base().ID(), err)
		}
		t.hooks = &surface.Hooks{Commit: t.commit, Destroyed: t.surfaceGone}
		s.AddHooks(t.hooks)
		r.m.timers = append(r.m.timers, t)
	}
	t.live = true
	server.OnDestroy(res, func() error {
		t.live = false
		return nil
	})
	return t, nil
}

type timer struct {
	m         *Manager
	s         *surface.Surface
	hooks     *surface.Hooks
	clock     *loop.Timer
	live      bool
	gone      bool
	pending   bool
	pendingAt time.Duration
	targets   []*commitTarget
	spare     []*commitTarget
	armedFor  time.Duration
}

type commitTarget struct {
	t       *timer
	at      time.Duration
	due     func() bool
	applied func() error
}

func (c *commitTarget) reached() bool {
	return c.t.due(c.at)
}

func (c *commitTarget) apply() error {
	t := c.t
	n := copy(t.targets, t.targets[1:])
	t.targets[n] = nil
	t.targets = t.targets[:n]
	t.spare = append(t.spare, c)
	return t.arm()
}

func (t *timer) newTarget(at time.Duration) *commitTarget {
	if n := len(t.spare); n > 0 {
		c := t.spare[n-1]
		t.spare[n-1] = nil
		t.spare = t.spare[:n-1]
		c.at = at
		return c
	}
	c := &commitTarget{t: t, at: at}
	c.due, c.applied = c.reached, c.apply
	return c
}

func (*timer) Destroy(*sct.WpCommitTimerV1) error { return nil }

func target(secHi, secLo, nsec uint32) time.Duration {
	sec := wire.Join(secHi, secLo)
	if sec > uint64(math.MaxInt64/int64(time.Second)) {
		return math.MaxInt64
	}
	return time.Duration(sec)*time.Second + time.Duration(nsec)
}

func (t *timer) SetTimestamp(obj *sct.WpCommitTimerV1, secHi, secLo, nsec uint32) error {
	switch {
	case t.gone:
		return obj.ProtocolError(sct.WpCommitTimerV1ErrorSurfaceDestroyed, "set_timestamp after the wl_surface was destroyed")
	case nsec >= uint32(time.Second):
		return obj.ProtocolError(sct.WpCommitTimerV1ErrorInvalidTimestamp, "tv_nsec %d is not below one second", nsec)
	case t.pending:
		return obj.ProtocolError(sct.WpCommitTimerV1ErrorTimestampExists, "the next commit already has a timestamp")
	}
	t.pending, t.pendingAt = true, target(secHi, secLo, nsec)
	return nil
}

func (t *timer) due(at time.Duration) bool {
	if t.m.loop.Now() >= at {
		return true
	}
	next, ok := t.m.nextFlip(t.s)
	return ok && next >= at
}

func (t *timer) commit(u *surface.Update) error {
	if !t.pending {
		return nil
	}
	t.pending = false
	c := t.newTarget(t.pendingAt)
	t.targets = append(t.targets, c)
	u.Gate(c.due)
	u.OnApply(c.applied)
	return t.arm()
}

func (t *timer) arm() error {
	now := t.m.loop.Now()
	var next time.Duration
	for _, c := range t.targets {
		if c.at > now && (next == 0 || c.at < next) {
			next = c.at
		}
	}
	if next == t.armedFor {
		return nil
	}
	t.armedFor = next
	if next == 0 {
		return t.clock.Disarm()
	}
	t.m.armedAt = next
	return t.clock.ArmFrame(next - now)
}

func (t *timer) fire() error {
	t.armedFor = 0
	return errors.Join(t.s.Retry(), t.arm())
}

func (t *timer) surfaceGone() error {
	t.gone = true
	t.targets = nil
	t.s.RemoveHooks(t.hooks)
	t.m.timers = slices.DeleteFunc(t.m.timers, func(x *timer) bool { return x == t })
	return t.clock.Remove()
}
