package compositor

import (
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/third_party/gomath"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const DefaultHotCornerPressure = 80

type HotCorner struct {
	Action    wm.Action
	Modifiers binds.ModifierSet
	Mods      uint32
}

type HotCornerSet [CornerCount]HotCorner

type HotCorners struct {
	Corners    HotCornerSet
	Pressure   float64
	Fullscreen bool
}

func DefaultHotCorners() HotCorners {
	return HotCorners{Pressure: DefaultHotCornerPressure}
}

func (s HotCornerSet) Resolve(r binds.Resolver) (HotCornerSet, error) {
	for k := range CornerCount {
		mask, err := s[k].Modifiers.Mask(r)
		if err != nil {
			return HotCornerSet{}, fmt.Errorf("the %s hot corner's modifiers: %w", k, err)
		}
		s[k].Mods = mask
	}
	return s, nil
}

func (rs OutputRules) ResolveHotCorners(r binds.Resolver) (OutputRules, error) {
	out := OutputRules{Named: make([]OutputRule, len(rs.Named))}
	var err error
	for i, rule := range rs.Named {
		if out.Named[i], err = rule.resolveHotCorners(r); err != nil {
			return OutputRules{}, err
		}
	}
	if out.Fallback, err = rs.Fallback.resolveHotCorners(r); err != nil {
		return OutputRules{}, err
	}
	return out, nil
}

func (r OutputRule) resolveHotCorners(res binds.Resolver) (OutputRule, error) {
	if r.HotCorners == nil {
		return r, nil
	}
	set, err := r.HotCorners.Resolve(res)
	if err != nil {
		return OutputRule{}, fmt.Errorf("compositor: output %+v: %w", r.Match, err)
	}
	r.HotCorners = &set
	return r, nil
}

type hotCornerState struct {
	quiet  image.Rectangle
	on     *output.Output
	corner Corner
	pushed float64
	fired  bool
	scans  uint64
}

func (h *hotCornerState) leave() {
	h.on, h.pushed, h.fired = nil, 0, false
}

func (h *hotCornerState) forget() {
	h.leave()
	h.quiet = image.Rectangle{}
}

func (c *Compositor) HotCornerScans() uint64 {
	return c.hot.scans
}

func (c *Compositor) hotCornersOn(o *output.Output) HotCornerSet {
	if set := c.configuredRule(o.Backend()).HotCorners; set != nil {
		return *set
	}
	return c.hotCorners.Corners
}

func (c *Compositor) pushHotCorner(from, want point, e *backend.PointerMotion) {
	h := &c.hot
	at := c.cursor.pixel()
	if at.In(h.quiet) {
		h.leave()
		return
	}
	h.scans++
	o := c.outputAt(c.cursor)
	if o == nil {
		h.forget()
		return
	}
	r, set, shared := o.Logical(), c.hotCornersOn(o), c.sharedEdges(o)
	h.quiet = r
	for k := range CornerCount {
		if set[k].Action != nil && k.free(shared) {
			h.quiet = r.Inset(1)
			break
		}
	}
	k, ok := cornerAt(r, at)
	if !ok || set[k].Action == nil || !k.free(shared) || !c.hotCornerArmed(set[k]) {
		h.leave()
		return
	}
	entered := from.pixel() != at
	if entered || h.on != o || h.corner != k {
		h.on, h.corner, h.pushed, h.fired = o, k, 0, false
	}
	if entered || h.fired {
		return
	}
	h.pushed += gomath.Hypot(unaccelerated(want.X-c.cursor.X, e.DX, e.UnaccelDX), unaccelerated(want.Y-c.cursor.Y, e.DY, e.UnaccelDY))
	if h.pushed < c.hotCorners.Pressure {
		return
	}
	h.fired = true
	c.run(set[k].Action, nil, wm.FocusOther)
}

func unaccelerated(away, delta, raw float64) float64 {
	if delta == 0 {
		return 0
	}
	return away * raw / delta
}

func (c *Compositor) hotCornerArmed(k HotCorner) bool {
	if c.shell.Locked() || c.constraint != nil {
		return false
	}
	if id, ok := c.wm.Focused(); ok && !c.hotCorners.Fullscreen && c.wm.Fullscreen(id) {
		return false
	}
	return k.Mods == 0 || c.binds.Held(c.seat.DepressedModifiers()) == k.Mods
}
