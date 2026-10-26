package compositor

import (
	"errors"
	"image"
	"math"
	"slices"

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
	r, _ := t.c.fromX(t.Rect())
	return image.Rectangle{Max: r.Size()}
}

func (t xToplevel) AppID() string {
	return t.Class().Class
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

func (xToplevel) sizeLimits() (minimum, maximum image.Point) {
	return image.Point{}, image.Point{}
}

func (xToplevel) scaled() bool {
	return false
}

func (t xToplevel) state(w want) xwayland.State {
	r := t.c.toX(w.rect)
	if w.layer == wm.LayerFloating {
		h := t.SizeHints()
		r.Max = r.Min.Add(clampSize(r.Size(), h.Min, h.Max))
	}
	return xwayland.State{Rect: r, Activated: w.activated, Fullscreen: w.layer == wm.LayerFullscreen}
}

func (t xToplevel) settled(w want) bool {
	return t.Applied() == t.state(w)
}

func (t xToplevel) apply(w want) error {
	return t.Apply(t.state(w))
}

func (c *Compositor) ScaleXwayland(clients func(*server.Client) bool) error {
	c.xwaylandClients = clients
	return errors.Join(c.layout.Physical(clients), c.reconcile())
}

func (c *Compositor) xScale(at image.Point) output.Scale {
	if o := c.xOutput(at, (*output.Output).Logical); o != nil {
		return o.Scale()
	}
	return output.Unit
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
		if d := math.Hypot(float64(q.X-p.X), float64(q.Y-p.Y)); d < nearest {
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

func (c *Compositor) fromX(r image.Rectangle) (image.Rectangle, output.Scale) {
	o := c.xOutput(r.Min, (*output.Output).XRect)
	if o == nil {
		return r, output.Unit
	}
	k := o.Scale()
	at := o.Position().Add(k.LogicalPoint(r.Min.Sub(o.XPosition())))
	return image.Rectangle{Min: at, Max: at.Add(k.LogicalPoint(r.Size()))}, k
}

func (c *Compositor) unmanagedOrigin(w *xwayland.Window) origin {
	r, k := c.fromX(w.Rect())
	return origin{at: r.Min, k: k}
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
	t := xToplevel{w, c}
	if err := c.mapToplevel(t); err != nil {
		return err
	}
	if !w.Fullscreen() {
		return nil
	}
	return c.update(c.wm.SetFullscreen(c.windowOf(t).id, true))
}

func (c *Compositor) XWindowUnmapped(w *xwayland.Window) error {
	if i := slices.Index(c.unmanaged, w); i >= 0 {
		c.unmanaged = slices.Delete(c.unmanaged, i, i+1)
		return errors.Join(c.refreshPointer(), c.scheduleAll())
	}
	return c.unmapToplevel(xToplevel{w, c})
}

func (c *Compositor) XWindowChanged(w *xwayland.Window) error {
	var err error
	if win := c.windowOf(xToplevel{w, c}); win != nil && w.Urgent() {
		err = c.markUrgent(win)
	}
	return errors.Join(err, c.refreshPointer(), c.scheduleAll(), c.syncForeign())
}

func (c *Compositor) XWindowResponsive(w *xwayland.Window, responsive bool) error {
	if win := c.windowOf(xToplevel{w, c}); win != nil {
		win.hung = !responsive
	}
	return nil
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
	case xwayland.Configure:
		if !c.wm.Floats(win.id) {
			return nil
		}
		rect, _ := c.fromX(r.Rect)
		return c.update(c.wm.SetFloatingRect(win.id, rect.Inset(-c.decor.BorderWidth)))
	}
	return nil
}

func (c *Compositor) drawUnmanaged(pass *frame, o *output.Output, shown []*surface.Surface) []*surface.Surface {
	for _, w := range c.unmanaged {
		if r, _ := c.fromX(w.Rect()); r.Overlaps(o.Logical()) {
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
