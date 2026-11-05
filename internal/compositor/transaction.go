package compositor

import (
	"errors"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	DefaultHoldBound = 100 * time.Millisecond
	dragHoldBound    = 17 * time.Millisecond
)

type member struct {
	w        *window
	c        *Compositor
	surface  *surface.Surface
	mark     answerMark
	watch    func(*surface.Update)
	barrier  *surface.Barrier
	unshown  *surface.Hooks
	answered bool
	reached  bool
}

type transaction struct {
	goal    wm.Arrangement
	source  *wm.Arranger
	members []*member
	began   time.Duration
}

func (t *transaction) complete() bool {
	return !slices.ContainsFunc(t.members, func(m *member) bool { return !m.reached || !m.w.top.synced() })
}

type HoldStats struct {
	Landed            int
	Expired           int
	Longest           time.Duration
	Last              time.Duration
	Armed             time.Duration
	MapToShown        int
	LongestMapToShown int
}

func (s *HoldStats) note(held time.Duration, expired bool) {
	s.Landed++
	s.Last, s.Longest = held, max(s.Longest, held)
	if expired {
		s.Expired++
	}
}

func (c *Compositor) opening(a wm.Arrangement) bool {
	for _, p := range a.Windows {
		if w := c.windowByID(p.ID); w != nil && !w.appeared && p.Visible && !p.Minimized {
			return true
		}
	}
	return false
}

func (c *Compositor) noteFirstShown(shown []*surface.Surface) {
	for _, w := range c.windows {
		if w.firstShown {
			continue
		}
		w.framesUnshown++
		if slices.Contains(shown, w.top.Surface()) {
			w.firstShown = true
			c.holds.MapToShown = w.framesUnshown
			c.holds.LongestMapToShown = max(c.holds.LongestMapToShown, w.framesUnshown)
		}
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
	return c.holdTimer != nil && !c.releasing && !c.shell.Locked()
}

func (c *Compositor) sendGoal(a wm.Arrangement) ([]*window, error) {
	focused, hasFocus := c.wm.Focused()
	awaited := c.awaited[:0]
	var err error
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
		err = errors.Join(err, c.settle(w, goal))
	}
	c.awaited = awaited
	return awaited, err
}

func (c *Compositor) freeArranger() *wm.Arranger {
	for i := range c.arrangers {
		x := &c.arrangers[i]
		if x != c.shownArranger && (c.txn == nil || x != c.txn.source) {
			return x
		}
	}
	panic("compositor: three arrangers, two in use")
}

func (c *Compositor) begin(x *wm.Arranger, awaited []*window) error {
	t := &c.txnStore
	t.goal, t.source, t.members, t.began = x.Arrangement, x, t.members[:0], backend.Now()
	var err error
	for _, w := range awaited {
		m := c.await(w)
		t.members = append(t.members, m)
		err = errors.Join(err, m.unthrottle())
	}
	c.txn = t
	c.holds.Armed = holdBound(c.interaction != nil, c.holdBound)
	return errors.Join(err, c.holdTimer.Arm(c.holds.Armed))
}

func (c *Compositor) await(w *window) *member {
	m := w.member
	if m == nil {
		m = &member{w: w, c: c}
		m.watch, m.barrier = m.committed, surface.NewBarrier(m.arrived)
		w.member = m
	}
	m.surface, m.mark, m.answered, m.reached = w.top.Surface(), w.top.mark(), false, false
	m.surface.Watch(m.watch)
	if !w.visible {
		m.unshown = &surface.Hooks{Applied: m.unthrottle}
		m.surface.AddHooks(m.unshown)
	}
	return m
}

func (m *member) committed(u *surface.Update) {
	if !m.answered && m.w.top.answers(u, m.mark) {
		m.answered = true
		u.Park(m.barrier)
	}
}

func (m *member) arrived() error {
	m.reached = true
	return m.c.landIfComplete()
}

func (m *member) finish() error {
	m.surface.Watch(nil)
	if m.unshown != nil {
		m.surface.RemoveHooks(m.unshown)
		m.unshown = nil
	}
	return m.barrier.Release()
}

func (m *member) unthrottle() error {
	if m.unshown == nil {
		return nil
	}
	return m.surface.FrameDone(wire.Millis(backend.Now()))
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
	var err error
	if c.interaction == nil {
		err = c.holdTimer.Disarm()
	}
	c.shownArranger = t.source
	now := c.clock()
	err = errors.Join(err, c.install(t.goal, now))
	c.refreshLooks(now)
	err = errors.Join(err, c.publish())
	c.releasing = true
	for _, m := range t.members {
		err = errors.Join(err, m.finish())
	}
	c.releasing = false
	return err
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
