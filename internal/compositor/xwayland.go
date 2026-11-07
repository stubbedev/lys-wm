package compositor

import (
	"errors"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/lys-lib/third_party/gomath"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xwayland"
)

type xToplevel struct {
	*xwayland.Window
	c *Compositor
}

func (xToplevel) Popups() []*shell.Popup {
	return nil
}

func (t xToplevel) Geometry() image.Rectangle {
	r := t.c.fromX(t.Rect())
	return image.Rectangle{Max: r.Size()}
}

func (t xToplevel) AppID() string {
	return t.Class().Class
}

func (xToplevel) Tag() string {
	return ""
}

func (xToplevel) Icon() *shell.Icon {
	return nil
}

func (t xToplevel) pid() int32 {
	return t.Pid()
}

func (t xToplevel) parent() toplevel {
	if p := t.Parent(); p != nil {
		return xToplevel{p, t.c}
	}
	return nil
}

func (t xToplevel) modal() bool {
	return t.Modal()
}

func (t xToplevel) prefersFloating() bool {
	return t.PrefersFloating()
}

func (t xToplevel) wantsFullscreen() bool {
	return t.Fullscreen()
}

func (t xToplevel) sizeLimits() (minimum, maximum image.Point) {
	h, k := t.SizeHints(), output.Unit
	if o := t.c.xOutput(t.Rect().Min, (*output.Output).XRect); o != nil {
		k = o.Scale()
	}
	return k.LogicalPoint(h.Min), k.LogicalPoint(h.Max)
}

func (xToplevel) scaled() bool {
	return false
}

func (t xToplevel) state(w want) xwayland.State {
	r := t.c.toX(w.rect)
	if w.layer == wm.LayerFloating {
		h := t.SizeHints()
		r.Max = r.Min.Add(wm.Limits{Min: h.Min, Max: h.Max}.Fit(r.Size()))
	}
	return xwayland.State{Rect: r, Activated: w.activated, Fullscreen: w.layer == wm.LayerFullscreen, Minimized: w.minimized}
}

func (t xToplevel) settled(w want) bool {
	return t.Applied() == t.state(w)
}

func (t xToplevel) apply(w want) error {
	return t.Apply(t.state(w))
}

func (t xToplevel) resizes(w want) bool {
	size := t.state(w).Rect.Size()
	return size != t.Rect().Size() && size != t.Surface().Size()
}

func (t xToplevel) mark() answerMark {
	return answerMark{size: t.Applied().Rect.Size()}
}

func (xToplevel) answers(u *surface.Update, m answerMark) bool {
	b := u.Buffer()
	return b != nil && b.Size() == m.size
}

func (t xToplevel) synced() bool {
	return t.Synced()
}

func (t xToplevel) stale() bool {
	size := t.Applied().Rect.Size()
	return size != (image.Point{}) && t.Surface().Size() != size
}

func (t xToplevel) shownSize() image.Point {
	at := t.Rect().Min
	return t.c.fromX(image.Rectangle{Min: at, Max: at.Add(t.Surface().Size())}).Size()
}

func (t xToplevel) overflows(frame image.Point) bool {
	shown := t.shownSize()
	return (shown.X > frame.X || shown.Y > frame.Y) && !t.stale()
}

func (c *Compositor) ScaleXwayland(clients func(*server.Client) bool) error {
	c.xwaylandClients = clients
	return errors.Join(c.layout.Physical(clients), c.reconcile())
}

func (c *Compositor) xOrigin(at image.Point) origin {
	o := c.xOutput(at, (*output.Output).XRect)
	if o == nil {
		return flat(at)
	}
	return origin{base: o.Position(), px: at.Sub(o.XPosition()), k: o.Scale()}
}

func (c *Compositor) xOutput(p image.Point, area func(*output.Output) image.Rectangle) *output.Output {
	if c.xwaylandClients == nil {
		return nil
	}
	var best *output.Output
	nearest := math.Inf(1)
	for _, o := range c.outputs {
		r := area(o)
		q := image.Pt(min(max(p.X, r.Min.X), r.Max.X-1), min(max(p.Y, r.Min.Y), r.Max.Y-1))
		if d := gomath.Hypot(float64(q.X-p.X), float64(q.Y-p.Y)); d < nearest {
			best, nearest = o, d
		}
	}
	return best
}

func (c *Compositor) toX(r image.Rectangle) image.Rectangle {
	o := c.xOutput(r.Min, (*output.Output).Logical)
	if o == nil {
		return r
	}
	k := o.Scale()
	at := o.XPosition().Add(k.DevicePoint(r.Min.Sub(o.Position())))
	return image.Rectangle{Min: at, Max: at.Add(k.DevicePoint(r.Size()))}
}

func (c *Compositor) fromX(r image.Rectangle) image.Rectangle {
	o := c.xOutput(r.Min, (*output.Output).XRect)
	if o == nil {
		return r
	}
	k := o.Scale()
	at := o.Position().Add(k.LogicalPoint(r.Min.Sub(o.XPosition())))
	return image.Rectangle{Min: at, Max: at.Add(k.LogicalPoint(r.Size()))}
}

func (c *Compositor) unmanagedOrigin(w *xwayland.Window) origin {
	return c.xOrigin(w.Rect().Min)
}

func (t xToplevel) close() error {
	return t.Close()
}

func (t xToplevel) ping() error {
	return t.Ping(shell.PingTimeout)
}

func (c *Compositor) XWindowMapped(w *xwayland.Window) error {
	if w.Override() {
		c.unmanaged = append(c.unmanaged, w)
		return errors.Join(c.refreshPointer(), c.scheduleAll())
	}
	return c.mapToplevel(xToplevel{w, c})
}

func (c *Compositor) XWindowUnmapped(w *xwayland.Window) error {
	if i := slices.Index(c.unmanaged, w); i >= 0 {
		c.unmanaged = slices.Delete(c.unmanaged, i, i+1)
		return errors.Join(c.refreshPointer(), c.scheduleAll())
	}
	return c.unmapToplevel(xToplevel{w, c})
}

func (c *Compositor) XWindowChanged(w *xwayland.Window) error {
	c.stateChanged()
	var err error
	if win := c.windowOf(xToplevel{w, c}); win != nil && w.Urgent() {
		err = c.markUrgent(win)
	}
	return errors.Join(err, c.refreshLimits(xToplevel{w, c}), c.redecide(xToplevel{w, c}), c.refreshPointer(), c.scheduleAll(), c.syncForeign())
}

func (c *Compositor) XWindowResponsive(w *xwayland.Window, responsive bool) error {
	if win := c.windowOf(xToplevel{w, c}); win != nil {
		win.hung = !responsive
		c.stateChanged()
	}
	return nil
}

func (c *Compositor) XWindowSynced(*xwayland.Window) error {
	return c.landIfComplete()
}

func (c *Compositor) XWindowRequest(w *xwayland.Window, r xwayland.Request) error {
	win := c.windowOf(xToplevel{w, c})
	if win == nil {
		return nil
	}
	switch r := r.(type) {
	case xwayland.Activate:
		return c.markUrgent(win)
	case xwayland.SetFullscreen:
		return c.update(c.wm.SetFullscreen(win.id, r.On))
	case xwayland.Minimize:
		return c.minimize(win)
	case xwayland.Configure:
		if !c.wm.Floats(win.id) {
			return nil
		}
		rect := c.fromX(r.Rect)
		return c.update(c.wm.SetFloatingRect(win.id, rect.Inset(-c.decor.BorderWidth)))
	}
	return nil
}

func (c *Compositor) drawUnmanaged(pass *frame, o *output.Output, shown []*surface.Surface) []*surface.Surface {
	for _, w := range c.unmanaged {
		if c.fromX(w.Rect()).Overlaps(o.Logical()) {
			shown = c.drawTree(pass, o, w.Surface(), c.unmanagedOrigin(w), shown)
		}
	}
	return shown
}

func (c *Compositor) hitUnmanaged(p point) (*surface.Surface, point) {
	for _, w := range slices.Backward(c.unmanaged) {
		if s, local := hitTree(w.Surface(), c.unmanagedOrigin(w), p); s != nil {
			return s, local
		}
	}
	return nil, point{}
}

func (c *Compositor) WorkArea() image.Rectangle {
	name := c.wm.FocusedOutput()
	for _, o := range c.wm.Outputs() {
		if o.Name == name {
			return c.toX(o.Area)
		}
	}
	return image.Rectangle{}
}

func (c *Compositor) WatchWorkArea(f func(image.Rectangle)) {
	c.workAreaWatch, c.workArea = f, c.WorkArea()
}

func (c *Compositor) publishWorkArea() {
	if c.workAreaWatch == nil {
		return
	}
	if r := c.WorkArea(); r != c.workArea {
		c.workArea = r
		c.workAreaWatch(r)
	}
}
