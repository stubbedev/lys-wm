package compositor

import (
	"errors"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	DefaultHoldBound = 100 * time.Millisecond
	dragHoldBound    = 17 * time.Millisecond
)

type member struct {
	w       *window
	surface *surface.Surface
	hooks   *surface.Hooks
	barrier *surface.Barrier
	reached bool
}

type transaction struct {
	goal    wm.Arrangement
	members []*member
	began   time.Duration
}

func (t *transaction) complete() bool {
	return !slices.ContainsFunc(t.members, func(m *member) bool { return !m.reached || !m.w.top.synced() })
}

type HoldStats struct {
	Landed  int
	Expired int
	Longest time.Duration
	Last    time.Duration
}

func (s *HoldStats) note(held time.Duration, expired bool) {
	s.Landed++
	s.Last, s.Longest = held, max(s.Longest, held)
	if expired {
		s.Expired++
	}
}

func (c *Compositor) Holding() bool {
	return c.txn != nil
}

func (c *Compositor) Holds() HoldStats {
	return c.holds
}

func holdBound(dragging bool, configured time.Duration) time.Duration {
	if dragging {
		return min(dragHoldBound, configured)
	}
	return configured
}

func stillStalled(stalled, dragging, stale bool) bool {
	return stalled && (dragging || stale)
}

func sameArrangement(a, b wm.Arrangement) bool {
	return slices.Equal(a.Windows, b.Windows) && slices.EqualFunc(a.TabBars, b.TabBars, func(x, y wm.TabBar) bool {
		return x.Rect == y.Rect && x.Active == y.Active && slices.Equal(x.Tabs, y.Tabs)
	})
}

func (c *Compositor) awaits(w *window, p wm.Placement, wanted want) bool {
	w.stalled = stillStalled(w.stalled, c.interaction != nil, w.top.stale())
	return !w.stalled && p.Visible && !p.Minimized && c.outputAtRect(p.Rect) != nil && w.top.resizes(wanted)
}

func (c *Compositor) holdable() bool {
	return c.holdTimer != nil && !c.shell.Locked()
}

func (c *Compositor) sendGoal(a wm.Arrangement) ([]*window, error) {
	focused, hasFocus := c.wm.Focused()
	awaited := c.awaited[:0]
	var errs []error
	for _, p := range a.Windows {
		w := c.windowByID(p.ID)
		if w == nil {
			return awaited, errUnknownWindow(p.ID)
		}
		w.goal = c.content(p.Rect, p.Layer)
		goal := c.wanted(w.goal, p.Layer, hasFocus && focused == w.id)
		goal.minimized = p.Minimized
		if c.interaction != nil && w.top.stale() && w.top.resizes(goal) {
			c.lagging = true
			continue
		}
		if c.awaits(w, p, goal) {
			awaited = append(awaited, w)
		}
		errs = append(errs, c.settle(w, goal))
	}
	c.awaited = awaited
	return awaited, errors.Join(errs...)
}

func (c *Compositor) begin(a wm.Arrangement, awaited []*window) error {
	t := &transaction{goal: a, members: make([]*member, len(awaited)), began: backend.Now()}
	for i, w := range awaited {
		t.members[i] = c.await(w)
	}
	c.txn = t
	return c.holdTimer.Arm(holdBound(c.interaction != nil, c.holdBound))
}

func (c *Compositor) await(w *window) *member {
	m := &member{w: w, surface: w.top.Surface()}
	answers := w.top.answer()
	m.hooks = &surface.Hooks{Commit: func(u *surface.Update) error {
		if m.barrier != nil || !answers(u) {
			return nil
		}
		m.barrier = u.Barrier(func() error {
			m.reached = true
			return c.landIfComplete()
		})
		return nil
	}}
	m.surface.AddHooks(m.hooks)
	return m
}

func (c *Compositor) landIfComplete() error {
	if c.txn == nil || !c.txn.complete() {
		return nil
	}
	return c.recovered(c.land(false))
}

func (c *Compositor) holdExpired() error {
	if c.txn == nil {
		return nil
	}
	for _, m := range c.txn.members {
		if !m.reached || !m.w.top.synced() {
			m.w.stalled = true
		}
	}
	return c.recovered(c.land(true))
}

func (c *Compositor) recovered(err error) error {
	if err == nil || backend.Fatal(err) {
		return err
	}
	c.report(err)
	return nil
}

func (c *Compositor) landInFlight() error {
	if c.txn == nil {
		return nil
	}
	return c.land(false)
}

func (c *Compositor) land(expired bool) error {
	t := c.txn
	c.txn = nil
	c.holds.note(backend.Now()-t.began, expired)
	errs := []error{c.holdTimer.Disarm(), c.install(t.goal, c.clock()), c.publish()}
	for _, m := range t.members {
		m.surface.RemoveHooks(m.hooks)
		if m.barrier != nil {
			errs = append(errs, m.barrier.Release())
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) stretchStale(pass *frame, o *output.Output, from int, w *window) {
	if !w.top.stale() {
		return
	}
	shown := w.top.shownSize()
	if shown == w.rect.Size() || shown.X <= 0 || shown.Y <= 0 {
		return
	}
	pass.stretch(from, deviceRect(o, image.Rectangle{Min: w.rect.Min, Max: w.rect.Min.Add(shown)}), deviceRect(o, w.rect))
}
