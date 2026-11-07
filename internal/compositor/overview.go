package compositor

import (
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/output"
	swlr "github.com/stubbedev/lys-wm/internal/protocol/server/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type OverviewStyle struct {
	Blur bool
	Dim  float32
}

func DefaultOverviewStyle() OverviewStyle {
	return OverviewStyle{Blur: true, Dim: 0.35}
}

type overviewTarget interface{ isOverviewTarget() }

type (
	overviewWindow struct{ w *window }
	overviewSpace  struct{ t *overviewThumb }
	overviewPlus   struct{ v *overviewView }
)

func (overviewWindow) isOverviewTarget() {}
func (overviewSpace) isOverviewTarget()  {}
func (overviewPlus) isOverviewTarget()   {}

type overview struct {
	open     bool
	progress anim.Progress
	views    map[*output.Output]*overviewView
	selected overviewTarget
	press    *overviewPress
	carried  *overviewCarried
	scratch  frame
	region   [1]image.Rectangle
	targets  []overviewTarget
	rects    []image.Rectangle
	scope    wm.OverviewScope
	back     wm.OverviewScope
	app      string
	query    string
}

type measureKey struct {
	text string
	px   int
}

type overviewCarried struct {
	root *window
	at   image.Rectangle
}

type overviewView struct {
	redraw    *overviewThumb
	output    *output.Output
	workspace wm.WorkspaceID
	items     []*overviewItem
	order     []overviewEntry
	leaving   []overviewEntry
	left      wm.WorkspaceID
	spaces    []*overviewThumb
	groups    []overviewGroup
	filter    []render.TextureTarget
	plus      image.Rectangle
}

type overviewEntry struct {
	w     *window
	item  *overviewItem
	front bool
}

type overviewItem struct {
	root      *window
	minimized bool
	members   []*window
	bounds    image.Rectangle
	goal      image.Rectangle
	slot      anim.RectMotion
	fade      anim.Tween[float64]
	group     wm.WorkspaceID
	away      bool
	out       bool
	dimmed    bool
}

type thumbMember struct {
	w          *window
	rect       image.Rectangle
	fullscreen bool
}

type overviewThumb struct {
	o        *output.Output
	id       wm.WorkspaceID
	label    string
	rect     image.Rectangle
	members  []thumbMember
	surfaces []*surface.Surface
	target   render.TextureTarget
	version  uint64
	dirty    bool
	next     time.Duration
}

const thumbShare = 4

func (t *overviewThumb) due(size image.Point, now time.Duration) bool {
	if t.target == nil || t.target.Size() != size {
		return true
	}
	return t.dirty && now >= t.next
}

func (c *Compositor) overviewHolds() bool {
	return c.overview != nil && c.overview.open
}

func (c *Compositor) overviewAction(mode wm.OverviewMode, scope wm.OverviewScope) error {
	switch {
	case mode == wm.OverviewHide:
		return c.closeOverview()
	case !c.overviewHolds():
		return c.openOverview(scope)
	case mode == wm.OverviewShow || c.overview.scope != scope:
		return c.setOverviewScope(scope, c.focusedApp())
	}
	return c.closeOverview()
}

func (c *Compositor) focusedApp() string {
	id, ok := c.wm.Focused()
	if !ok {
		return ""
	}
	if w := c.windowByID(id); w != nil {
		return w.top.AppID()
	}
	return ""
}

func (c *Compositor) setOverviewScope(scope wm.OverviewScope, app string) error {
	ov := c.overview
	if scope == wm.OverviewApp && ov.scope != wm.OverviewApp {
		ov.back = ov.scope
	}
	ov.scope, ov.app = scope, app
	return errors.Join(c.syncOverview(c.clock()), c.scheduleAll())
}

func (c *Compositor) setOverviewQuery(q string) error {
	c.overview.query = q
	return errors.Join(c.syncOverview(c.clock()), c.scheduleAll())
}

func (c *Compositor) takeOverviewInput() error {
	if c.overview.open {
		return nil
	}
	c.overview.open = true
	var errs []error
	if c.interaction != nil {
		errs = append(errs, c.endDrag())
	}
	errs = append(errs, c.dismissGrabs(), c.seat.SetPointerFocus(nil, 0, 0), c.seat.PointerFrame())
	if len(c.touches) > 0 {
		maps.DeleteFunc(c.touches, func(id int32, _ touchAt) bool { return !c.touchGesture.holds(id) })
		errs = append(errs, c.seat.TouchCancel())
	}
	errs = append(errs, c.refocus())
	c.stateChanged()
	return errors.Join(errs...)
}

func (c *Compositor) canOpenOverview() bool {
	return !c.shell.Locked()
}

func (c *Compositor) openOverview(scope wm.OverviewScope) error {
	if c.overviewHolds() || !c.canOpenOverview() {
		return nil
	}
	now := c.clock()
	c.ensureOverview(scope)
	c.overview.progress.Set(1, now, c.animations.Curve(anim.Overview))
	return c.enterOverview(now)
}

func (c *Compositor) enterOverview(now time.Duration) error {
	return errors.Join(c.syncOverview(now), c.takeOverviewInput(), c.prepareFirstFrame(), c.scheduleAll())
}

func (c *Compositor) prepareFirstFrame() error {
	c.frameTime = c.aimedAny(c.clock())
	var errs []error
	for _, o := range c.outputs {
		v, ok := c.overview.views[o]
		if !ok || !c.outputLive(o) {
			continue
		}
		errs = append(errs, c.drawOverviewSelection(&c.overview.scratch, o, v, 1, 1))
		if !c.overview.progress.Done(c.frameTime) {
			continue
		}
		for _, t := range v.spaces {
			if t.id != v.workspace || t.target != nil || t.rect.Empty() {
				continue
			}
			if err := c.refreshThumb(o, v, t); err != nil {
				errs = append(errs, fmt.Errorf("the first thumbnail of workspace %d: %w", t.id, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) ensureOverview(scope wm.OverviewScope) {
	if c.overview == nil {
		c.overview = &overview{progress: anim.NewProgress(0), views: map[*output.Output]*overviewView{}}
	}
	if ov := c.overview; !ov.open {
		ov.scope, ov.back, ov.app, ov.query = scope, scope, c.focusedApp(), ""
	}
	if id, ok := c.wm.Focused(); ok && c.overview.selected == nil {
		if w := c.windowByID(id); w != nil {
			c.overview.selected = overviewWindow{w}
		}
	}
}

const overviewRubber = 0.12

func scrubbed(shown, delta float64) float64 {
	next := shown + delta
	switch {
	case next <= 0:
		return 0
	case next > 1:
		over := max(shown-1, 0)
		return 1 + anim.RubberBand(over, next-1-over, overviewRubber)
	}
	return next
}

func (c *Compositor) grabOverview() bool {
	ov := c.overview
	now := c.clock()
	if ov == nil || ov.progress.Done(now) {
		return false
	}
	ov.progress.Grab(now)
	return true
}

func (c *Compositor) releaseOverview(at time.Duration) error {
	ov := c.overview
	if ov == nil || !ov.progress.Tracking() {
		return nil
	}
	target := 0.0
	if ov.open {
		target = 1
	}
	ov.progress.Release(target, at, c.clock(), c.animations.Curve(anim.Overview))
	return c.scheduleAll()
}

type overviewDrive struct {
	c       *Compositor
	ov      *overview
	sign    float64
	wasOpen bool
}

func (c *Compositor) driveOverview(b binds.GestureBind, at time.Duration) (driven, error) {
	if !c.canOpenOverview() {
		return nil, nil
	}
	now := c.clock()
	d := &overviewDrive{c: c, sign: 1, wasOpen: c.overviewHolds()}
	if b.Direction == binds.SwipeVertical {
		d.sign = -1
	}
	c.ensureOverview(b.Scope)
	d.ov = c.overview
	d.ov.progress.Grab(now)
	d.ov.progress.Track(d.ov.progress.At(now), at)
	return d, c.enterOverview(now)
}

func (d *overviewDrive) live() bool {
	return d.c.overview == d.ov && d.ov.open && d.ov.progress.Tracking()
}

func (d *overviewDrive) moved(delta float64, at time.Duration) error {
	if !d.live() {
		return nil
	}
	d.ov.progress.Track(scrubbed(d.ov.progress.At(at), float64(d.sign*delta)), at)
	return d.c.scheduleAll()
}

func (d *overviewDrive) released(at time.Duration, cancelled bool) error {
	if !d.live() {
		return nil
	}
	c, ov := d.c, d.ov
	open := d.wasOpen
	if !cancelled {
		open = ov.progress.Projected(at) >= anim.CommitAt
	}
	curve := c.animations.Curve(anim.Overview)
	if open {
		ov.progress.Release(1, at, c.clock(), curve)
		return c.scheduleAll()
	}
	ov.progress.Release(0, at, c.clock(), curve)
	return c.returnOverviewInput()
}

func (c *Compositor) closeOverview() error {
	if !c.overviewHolds() {
		return nil
	}
	c.overview.progress.Set(0, c.clock(), c.animations.Curve(anim.Overview))
	return c.returnOverviewInput()
}

func (c *Compositor) returnOverviewInput() error {
	ov := c.overview
	ov.open = false
	c.abandonOverviewPress()
	c.stateChanged()
	return errors.Join(c.refocus(), c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) dropOverview() error {
	ov := c.overview
	if ov == nil {
		return nil
	}
	c.overview = nil
	var errs []error
	for _, o := range c.outputs {
		if sc := c.sceneOf(o); sc.blurs.mica != nil && c.overviewStyle.Blur {
			errs = append(errs, sc.blurs.mica.trim())
			sc.blurs.keepMica, c.keptBackdrop = true, true
		}
	}
	for _, v := range ov.views {
		errs = append(errs, c.dropThumbs(v.spaces), c.dropFilter(v))
	}
	return errors.Join(errs...)
}

func (c *Compositor) settleOverview(now time.Duration) error {
	ov := c.overview
	if ov == nil || ov.open || !ov.progress.Done(now) {
		return nil
	}
	return errors.Join(c.dropOverview(), c.scheduleAll())
}

func (c *Compositor) overviewLocked() error {
	if c.overview == nil || !c.shell.Locked() {
		return nil
	}
	err := c.dropOverview()
	c.stateChanged()
	return err
}

func (c *Compositor) releaseTarget(t render.TextureTarget) error {
	var errs []error
	for o, sc := range c.scenes {
		if sc.draws(t.Texture()) {
			sc.stale = true
			if c.outputLive(o) {
				errs = append(errs, c.schedule(o))
			}
		}
	}
	return errors.Join(append(errs, t.Destroy())...)
}

func (c *Compositor) dropFilter(v *overviewView) error {
	var errs []error
	for _, t := range v.filter {
		errs = append(errs, t.Destroy())
	}
	v.filter = nil
	return errors.Join(errs...)
}

func (c *Compositor) dropThumbs(thumbs []*overviewThumb) error {
	var errs []error
	for _, t := range thumbs {
		if t.target != nil {
			errs = append(errs, c.releaseTarget(t.target))
			t.target, t.version, t.dirty = nil, 0, true
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) overviewOutputGone(o *output.Output) error {
	if c.overview == nil {
		return nil
	}
	v, ok := c.overview.views[o]
	if !ok {
		return nil
	}
	delete(c.overview.views, o)
	return errors.Join(c.dropThumbs(v.spaces), c.dropFilter(v))
}

func (c *Compositor) dropOverviewTargets() error {
	if c.overview == nil {
		return nil
	}
	var errs []error
	for _, v := range c.overview.views {
		errs = append(errs, c.dropThumbs(v.spaces), c.dropFilter(v))
	}
	return errors.Join(errs...)
}

func (c *Compositor) overviewRoot(w *window, ws wm.WorkspaceID) *window {
	root := w
	for range c.windows {
		p := c.windowOf(root.top.parent())
		if p == nil {
			return root
		}
		if id, ok := c.wm.WorkspaceOf(p.id); !ok || id != ws || c.wm.Minimized(p.id) != c.wm.Minimized(w.id) {
			return root
		}
		root = p
	}
	return root
}

func (c *Compositor) syncOverview(now time.Duration) error {
	ov := c.overview
	if ov == nil {
		return nil
	}
	var errs []error
	for _, o := range c.outputs {
		errs = append(errs, c.syncOverviewView(ov, o, now))
	}
	ov.carried = nil
	for _, v := range ov.views {
		v.leaving = slices.DeleteFunc(v.leaving, func(e overviewEntry) bool { return !c.live(e.w) })
	}
	if !c.overviewTargetLive(ov.selected) {
		ov.selected = nil
	}
	if pr := ov.press; pr != nil && (pr.item != nil && !c.live(pr.item.root) || !c.overviewTargetLive(pr.target)) {
		c.abandonOverviewPress()
	}
	return errors.Join(errs...)
}

func (c *Compositor) overviewTargetLive(t overviewTarget) bool {
	switch t := t.(type) {
	case overviewWindow:
		it := c.overviewItemOf(t.w)
		return c.live(t.w) && it != nil && !it.out
	case overviewSpace:
		v := c.overview.views[t.t.o]
		return v != nil && slices.Contains(v.spaces, t.t)
	case overviewPlus:
		return c.overview.views[t.v.output] == t.v
	}
	return true
}

func (c *Compositor) overviewItemOf(w *window) *overviewItem {
	for _, v := range c.overview.views {
		for _, e := range v.order {
			if e.w == w {
				return e.item
			}
		}
	}
	return nil
}

func findItem(items []*overviewItem, root *window, minimized bool) *overviewItem {
	for _, it := range items {
		if it.root == root && it.minimized == minimized {
			return it
		}
	}
	return nil
}

func (c *Compositor) syncOverviewView(ov *overview, o *output.Output, now time.Duration) error {
	v, ok := ov.views[o]
	if !ok {
		v = &overviewView{output: o}
		ov.views[o] = v
	}
	name := o.Backend().Name()
	active, ok := c.wm.ActiveOn(name)
	if !ok {
		return nil
	}
	if active != v.workspace {
		v.leaving = nil
		if v.workspace != 0 && c.switches[o] != nil {
			v.leaving, v.left = v.order, v.workspace
			v.order = nil
		}
		v.workspace = active
	}
	shown := onWorkspace(c.wm.WorkspacePlacements(active, c.spacing()), active)
	places := c.overviewPlacements(ov, name, active, shown)
	old := v.items
	v.items = make([]*overviewItem, 0, len(places))
	entries := make([]overviewEntry, 0, len(places))
	for _, p := range places {
		w := c.windowByID(p.ID)
		if w == nil {
			continue
		}
		root := c.overviewRoot(w, p.Workspace)
		matches := c.overviewMatches(ov, w)
		it := findItem(v.items, root, p.Minimized)
		if it == nil {
			if it = findItem(old, root, p.Minimized); it == nil {
				if it = findItem(old, root, !p.Minimized); it != nil {
					it.minimized = p.Minimized
				}
			}
			if it == nil {
				it = &overviewItem{root: root, minimized: p.Minimized, fade: anim.NewTween(1.0, anim.Lerp)}
			}
			it.members, it.bounds = it.members[:0], p.Rect
			it.group, it.away, it.out = p.Workspace, p.Workspace != active, !matches
			it.dimmed = ov.scope == wm.OverviewApp && !it.away && !c.overviewOfApp(ov, w)
			v.items = append(v.items, it)
		}
		it.members = append(it.members, w)
		it.bounds = it.bounds.Union(p.Rect)
		it.out = it.out && !matches
		entries = append(entries, overviewEntry{w: w, item: it, front: p.Visible && !it.away})
	}
	v.order = v.order[:0]
	for _, layer := range [...]func(e overviewEntry) bool{
		func(e overviewEntry) bool { return e.item.out },
		func(e overviewEntry) bool { return !e.item.out && !e.front },
		func(e overviewEntry) bool { return !e.item.out && e.front },
	} {
		for _, e := range entries {
			if layer(e) {
				v.order = append(v.order, e)
			}
		}
	}
	spaces := workspacesOn(c.wm, name, len(v.spaces)+1)
	plan, groups := c.planOverviewView(ov, o, v, spaces)
	at := make([]int, len(groups))
	offsets := make([]int, len(groups))
	for i := 1; i < len(groups); i++ {
		offsets[i] = offsets[i-1] + groups[i-1].size
	}
	nm := 0
	for _, it := range v.items {
		goal, alpha := it.goal, 1.0
		switch {
		case it.out && it.dimmed:
			goal, alpha = it.bounds, overviewDimmed
		case it.out:
			alpha = 0
			if goal.Empty() {
				goal = it.bounds
			}
		case it.minimized:
			goal, nm = plan.Minimized[nm], nm+1
		default:
			g := groupIndex(groups, ov.scope, it.group)
			goal = plan.Windows[offsets[g]+at[g]]
			at[g]++
		}
		c.moveOverviewItem(ov, it, goal, now)
		if it.fade.Target() != alpha {
			it.fade.Set(alpha, now, c.animations.Curve(anim.Overview))
		}
	}
	v.groups = v.groups[:0]
	if ov.scope == wm.OverviewOutput {
		for i, g := range groups {
			v.groups = append(v.groups, overviewGroup{id: g.id, rect: plan.Groups[i], label: workspaceLabel(g.id)})
		}
	}
	v.plus = plan.Plus
	return c.syncThumbs(v, o, spaces, plan.Strip, shown)
}

const overviewDimmed = 0.3

type overviewGroup struct {
	id    wm.WorkspaceID
	rect  image.Rectangle
	label string
}

type groupPlan struct {
	id    wm.WorkspaceID
	size  int
	rects []image.Rectangle
}

func workspaceLabel(id wm.WorkspaceID) string {
	return "Workspace " + strconv.Itoa(int(id))
}

func groupIndex(groups []groupPlan, scope wm.OverviewScope, id wm.WorkspaceID) int {
	if scope != wm.OverviewOutput {
		return 0
	}
	return slices.IndexFunc(groups, func(g groupPlan) bool { return g.id == id })
}

func (c *Compositor) planOverviewView(ov *overview, o *output.Output, v *overviewView, spaces []wm.WorkspaceID) (wm.OverviewPlan, []groupPlan) {
	var groups []groupPlan
	var minimized []image.Rectangle
	if ov.scope != wm.OverviewOutput {
		groups = []groupPlan{{}}
	} else {
		for _, it := range v.items {
			if !it.out && !it.minimized && groupIndex(groups, ov.scope, it.group) < 0 {
				groups = append(groups, groupPlan{id: it.group})
			}
		}
		slices.SortStableFunc(groups, func(a, b groupPlan) int { return spaceOrder(spaces, a.id) - spaceOrder(spaces, b.id) })
	}
	for _, it := range v.items {
		switch {
		case it.out:
		case it.minimized:
			minimized = append(minimized, it.bounds)
		default:
			g := &groups[groupIndex(groups, ov.scope, it.group)]
			g.rects, g.size = append(g.rects, it.bounds), g.size+1
		}
	}
	rects := make([][]image.Rectangle, len(groups))
	for i, g := range groups {
		rects[i] = g.rects
	}
	label := 0
	if ov.scope == wm.OverviewOutput {
		label = overviewLabelHeight + overviewLabelGap
	}
	return wm.PlanOverviewGroups(o.Logical(), len(spaces), rects, label, minimized), groups
}

func (c *Compositor) overviewPlacements(ov *overview, name string, active wm.WorkspaceID, shown []wm.Placement) []wm.Placement {
	switch ov.scope {
	case wm.OverviewOutput:
		var places []wm.Placement
		for _, id := range workspacesOn(c.wm, name, 0) {
			if id == active {
				places = append(places, shown...)
				continue
			}
			places = append(places, onWorkspace(c.wm.WorkspacePlacements(id, c.spacing()), id)...)
		}
		return places
	case wm.OverviewApp:
		if name != c.wm.FocusedOutput() {
			return nil
		}
		places := slices.Clone(shown)
		for _, ws := range c.wm.Workspaces() {
			if ws.ID < 1 || ws.ID == active {
				continue
			}
			for _, p := range onWorkspace(c.wm.WorkspacePlacements(ws.ID, c.spacing()), ws.ID) {
				if w := c.windowByID(p.ID); w != nil && c.overviewOfApp(ov, w) {
					places = append(places, p)
				}
			}
		}
		return places
	}
	return shown
}

func (c *Compositor) overviewOfApp(ov *overview, w *window) bool {
	return ov.app != "" && w.top.AppID() == ov.app
}

func (c *Compositor) overviewMatches(ov *overview, w *window) bool {
	if ov.scope == wm.OverviewApp && !c.overviewOfApp(ov, w) {
		return false
	}
	return ov.query == "" || containsFold(w.top.Title(), ov.query) || containsFold(w.top.AppID(), ov.query)
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func workspacesOn(s wm.State, name string, room int) []wm.WorkspaceID {
	spaces := make([]wm.WorkspaceID, 0, room)
	for _, w := range s.Workspaces() {
		if w.Output == name {
			spaces = append(spaces, w.ID)
		}
	}
	return spaces
}

func (c *Compositor) moveOverviewItem(ov *overview, it *overviewItem, goal image.Rectangle, now time.Duration) {
	was := it.goal
	it.goal = goal
	curve := c.animations.Curve(anim.Overview)
	switch {
	case ov.press != nil && ov.press.dragging && ov.press.item == it:
	case was.Empty() && ov.carried != nil && ov.carried.root == it.root:
		it.slot = anim.NewRectMotion(ov.carried.at)
		it.slot.Set(goal, now, curve)
	case was.Empty():
		it.slot = anim.NewRectMotion(goal)
	case ov.progress.At(now) <= 0:
		it.slot.Jump(goal)
	case was != goal:
		it.slot.Set(goal, now, curve)
	}
}

func (c *Compositor) syncThumbs(v *overviewView, o *output.Output, spaces []wm.WorkspaceID, strip []image.Rectangle, shown []wm.Placement) error {
	old := v.spaces
	v.spaces = make([]*overviewThumb, 0, len(spaces))
	front := c.thumbMembers(shown)
	pinned := slices.DeleteFunc(slices.Clone(front), func(m thumbMember) bool { return !c.wm.Pinned(m.w.id) })
	for i, id := range spaces {
		var t *overviewThumb
		if at := slices.IndexFunc(old, func(x *overviewThumb) bool { return x.id == id }); at >= 0 {
			t = old[at]
			old = slices.Delete(slices.Clone(old), at, at+1)
		} else {
			t = &overviewThumb{o: o, id: id, label: workspaceLabel(id), dirty: true}
		}
		t.rect = strip[i]
		var members []thumbMember
		if id == v.workspace {
			members = front
		} else {
			members = append(c.thumbMembers(c.wm.WorkspacePlacements(id, c.spacing())), pinned...)
		}
		if !slices.Equal(t.members, members) {
			t.members, t.dirty = members, true
		}
		v.spaces = append(v.spaces, t)
	}
	return c.dropThumbs(old)
}

func (c *Compositor) thumbMembers(places []wm.Placement) []thumbMember {
	into := make([]thumbMember, 0, len(places))
	for _, p := range places {
		if w := c.windowByID(p.ID); w != nil && p.Visible && !p.Minimized {
			into = append(into, thumbMember{w, p.Rect, p.Layer == wm.LayerFullscreen})
		}
	}
	return into
}

func (c *Compositor) overviewForget(s *surface.Surface) {
	for _, v := range c.overview.views {
		for _, t := range v.spaces {
			if slices.Contains(t.surfaces, s) {
				t.surfaces = slices.DeleteFunc(t.surfaces, func(x *surface.Surface) bool { return x == s })
				t.dirty = true
			}
		}
	}
}

func (c *Compositor) backdropCommitted(root *surface.Surface) error {
	e := c.layerWithSurface(root)
	if e == nil || !slices.Contains(belowWindows, e.surface.Current().Layer) {
		return nil
	}
	return c.sceneOf(e.output).blurs.dropMica()
}

func (c *Compositor) overviewCommitted(root *surface.Surface) {
	e := c.layerWithSurface(root)
	for o, v := range c.overview.views {
		inThumbnails := e != nil && e.output == o && !isOverlay(e.surface)
		for _, t := range v.spaces {
			if inThumbnails || slices.ContainsFunc(t.members, func(m thumbMember) bool { return m.w.top.Surface() == root }) {
				t.dirty = true
			}
		}
	}
}

func (c *Compositor) overviewLayersChanged(o *output.Output) {
	if c.overview == nil {
		return
	}
	v, ok := c.overview.views[o]
	if !ok {
		return
	}
	for _, t := range v.spaces {
		t.dirty = true
	}
}

func (c *Compositor) overviewAnimating(o *output.Output, now time.Duration) bool {
	ov := c.overview
	if ov == nil {
		return false
	}
	if !ov.progress.Tracking() && !ov.progress.Done(now) {
		return true
	}
	v, ok := ov.views[o]
	if !ok {
		return false
	}
	return slices.ContainsFunc(v.items, func(it *overviewItem) bool { return !it.slot.Done(now) || !it.fade.Done(now) }) ||
		ov.progress.At(now) > 0 && slices.ContainsFunc(v.spaces, func(t *overviewThumb) bool { return t.dirty })
}

func placedIn(r, from, to image.Rectangle) image.Rectangle {
	if from.Empty() {
		return r
	}
	return mapRect(r, from, to)
}

func (it *overviewItem) home(now time.Duration) image.Rectangle {
	var r image.Rectangle
	for _, m := range it.members {
		r = r.Union(m.motion.At(now))
	}
	return r
}

func (it *overviewItem) frame(o *output.Output, p float64, now time.Duration) image.Rectangle {
	slot := it.slot.At(now)
	switch {
	case it.away:
		return slot
	case it.minimized:
		return anim.LerpRect(slot.Add(image.Pt(0, o.Logical().Max.Y-slot.Min.Y)), slot, p)
	}
	return anim.LerpRect(it.home(now), slot, p)
}

func (e overviewEntry) rect(o *output.Output, p float64, now time.Duration) image.Rectangle {
	f := e.item.frame(o, p, now)
	if e.item.minimized || e.item.away {
		return placedIn(e.w.tile, e.item.bounds, f)
	}
	return placedIn(e.w.motion.At(now), e.item.home(now), f)
}

func (c *Compositor) overviewRect(w *window, now time.Duration) (image.Rectangle, bool) {
	ov := c.overview
	if ov == nil {
		return image.Rectangle{}, false
	}
	for o, v := range ov.views {
		for _, e := range v.order {
			if e.w == w {
				return e.rect(o, ov.progress.At(now), now), true
			}
		}
	}
	return image.Rectangle{}, false
}

func (c *Compositor) overviewKeyboardTarget() *surface.Surface {
	var l *shell.LayerSurface
	if e := c.exclusiveLayer(); e != nil && e.Current().Layer == swlr.ZwlrLayerShellV1LayerOverlay {
		l = e
	} else if f := c.layerFocus; f != nil && f.Mapped() && f.Current().Layer == swlr.ZwlrLayerShellV1LayerOverlay {
		l = f
	}
	if l == nil {
		return nil
	}
	if top := c.grabTop(); top != nil && top.Client() == l.Surface().Client() {
		return top
	}
	return l.Surface()
}

func (c *Compositor) OverviewThumbRedraws() uint64 {
	return c.tallied.thumbs
}

func (c *Compositor) OverviewListed(o *output.Output) []wm.WindowID {
	if c.overview == nil {
		return nil
	}
	v, ok := c.overview.views[o]
	if !ok {
		return nil
	}
	var out []wm.WindowID
	for _, it := range v.items {
		if !it.out {
			out = append(out, it.root.id)
		}
	}
	return out
}

func (c *Compositor) OverviewSlots(o *output.Output) []image.Rectangle {
	if c.overview == nil {
		return nil
	}
	v, ok := c.overview.views[o]
	if !ok {
		return nil
	}
	out := make([]image.Rectangle, 0, len(v.items))
	for _, it := range v.items {
		out = append(out, it.goal)
	}
	return out
}

func (c *Compositor) OverviewThumbWindows(o *output.Output, id wm.WorkspaceID) []wm.WindowID {
	if c.overview == nil {
		return nil
	}
	v, ok := c.overview.views[o]
	if !ok {
		return nil
	}
	for _, t := range v.spaces {
		if t.id != id {
			continue
		}
		out := make([]wm.WindowID, 0, len(t.members))
		for _, m := range t.members {
			out = append(out, m.w.id)
		}
		return out
	}
	return nil
}

func (c *Compositor) CursorShape() string {
	return c.cursorShape()
}

func (c *Compositor) OverviewWindowRect(id wm.WindowID, now time.Duration) (image.Rectangle, bool) {
	w := c.windowByID(id)
	if w == nil {
		return image.Rectangle{}, false
	}
	return c.overviewRect(w, now)
}

func (c *Compositor) OverviewProgress(now time.Duration) float64 {
	if c.overview == nil {
		return 0
	}
	return c.overview.progress.At(now)
}

func (c *Compositor) Now() time.Duration {
	return c.clock()
}

func (c *Compositor) DirtyOverviewThumbs() {
	if c.overview == nil {
		return
	}
	for _, v := range c.overview.views {
		for _, t := range v.spaces {
			t.dirty = true
		}
	}
}

func (c *Compositor) ClosingCount() int {
	return len(c.closing)
}

func (c *Compositor) ClosingAt(now time.Duration) image.Point {
	if len(c.closing) == 0 {
		return image.Point{}
	}
	r := c.closing[0].motion.At(now)
	return r.Min.Add(r.Size().Div(2))
}

func (c *Compositor) OverviewThumbsStale() bool {
	if c.overview == nil {
		return false
	}
	for _, v := range c.overview.views {
		for _, t := range v.spaces {
			if t.dirty {
				return true
			}
		}
	}
	return false
}

func spaceOrder(spaces []wm.WorkspaceID, id wm.WorkspaceID) int {
	if i := slices.Index(spaces, id); i >= 0 {
		return i
	}
	return len(spaces)
}

func onWorkspace(places []wm.Placement, id wm.WorkspaceID) []wm.Placement {
	for i := range places {
		places[i].Workspace = id
	}
	return places
}
