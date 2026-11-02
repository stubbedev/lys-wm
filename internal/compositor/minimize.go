package compositor

import (
	"errors"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type iconHint struct {
	surface *surface.Surface
	rect    image.Rectangle
}

type genie struct {
	window   *window
	from, to image.Rectangle
	progress anim.Tween[float64]
	fades    bool
}

func (c *Compositor) family(w *window) []*window {
	root := w
	for root.top.modal() {
		p := c.windowOf(root.top.parent())
		if p == nil {
			break
		}
		root = p
	}
	out := []*window{root}
	for i := 0; i < len(out); i++ {
		for _, x := range c.windows {
			if x.top.parent() != nil && x.top.parent() == out[i].top && !slices.Contains(out, x) {
				out = append(out, x)
			}
		}
	}
	return out
}

func (c *Compositor) minimize(w *window) error {
	s := c.wm
	var err error
	for _, x := range c.family(w) {
		if s, err = s.Minimize(x.id, x.tile); err != nil {
			return err
		}
		if c.interaction != nil && c.interaction.w == x {
			c.interaction = nil
			c.hidePreview(c.clock())
		}
	}
	var dismissed error
	if top := c.grabTop(); top != nil && top.Client() == w.top.Surface().Client() {
		dismissed = c.shell.DismissGrab()
	}
	return errors.Join(dismissed, c.update(s, nil))
}

func (c *Compositor) restore(w *window) error {
	c.layerFocus = nil
	s := c.wm
	var err error
	members := c.family(w)
	for _, x := range members {
		if x != w {
			if s, err = s.Restore(x.id, c.settings.Autotile); err != nil {
				return err
			}
		}
	}
	if s, err = s.Restore(w.id, c.settings.Autotile); err != nil {
		return err
	}
	return c.update(s, nil)
}

func (c *Compositor) activate(w *window) error {
	if c.wm.Minimized(w.id) {
		return c.restore(w)
	}
	return c.focusWindow(w)
}

func nearestEdge(area, r image.Rectangle) int {
	c := r.Min.Add(r.Size().Div(2))
	gaps := [4]int{c.X - area.Min.X, area.Max.X - c.X, c.Y - area.Min.Y, area.Max.Y - c.Y}
	best := 0
	for i, g := range gaps {
		if g < gaps[best] {
			best = i
		}
	}
	return best
}

const genieEdgeShare = 8

func edgeSpan(lo, hi, span, areaLo, areaHi int) (int, int) {
	half := max(span/genieEdgeShare/2, 1)
	mid := (max(lo, areaLo) + min(hi, areaHi)) / 2
	return mid - half, mid + half
}

func edgeTarget(area, r image.Rectangle) image.Rectangle {
	x0, x1 := edgeSpan(r.Min.X, r.Max.X, r.Dx(), area.Min.X, area.Max.X)
	y0, y1 := edgeSpan(r.Min.Y, r.Max.Y, r.Dy(), area.Min.Y, area.Max.Y)
	return [4]image.Rectangle{
		image.Rect(area.Min.X, y0, area.Min.X+1, y1),
		image.Rect(area.Max.X-1, y0, area.Max.X, y1),
		image.Rect(x0, area.Min.Y, x1, area.Min.Y+1),
		image.Rect(x0, area.Max.Y-1, x1, area.Max.Y),
	}[nearestEdge(area, r)]
}

func (c *Compositor) iconOrigin(s *surface.Surface) (image.Point, bool) {
	for _, e := range c.layers {
		if at, ok := treeOrigin(e.surface.Surface(), flat(e.bounds().Min), s); ok && e.placed {
			return at.at().pixel(), true
		}
	}
	for _, x := range c.windows {
		if at, ok := treeOrigin(x.top.Surface(), c.windowOrigin(x), s); ok && x.visible {
			return at.at().pixel(), true
		}
	}
	return image.Point{}, false
}

func (c *Compositor) genieTarget(w *window, r image.Rectangle) image.Rectangle {
	if w.icon.surface != nil {
		if at, ok := c.iconOrigin(w.icon.surface); ok {
			if icon := w.icon.rect.Add(at); w.output != nil && icon.Overlaps(w.output.Logical()) {
				return icon
			}
		}
	}
	if w.output == nil {
		return r
	}
	return edgeTarget(w.output.Logical(), r)
}

func (c *Compositor) genieLeaving(w *window) *closing {
	if i := slices.IndexFunc(c.closing, func(cl *closing) bool { return cl.genie != nil && cl.genie.window == w }); i >= 0 {
		return c.closing[i]
	}
	return nil
}

func (c *Compositor) startMinimize(w *window, now time.Duration) error {
	curve := c.animations.Curve(anim.WindowsMinimize)
	if curve == anim.Instant {
		return nil
	}
	at := w.motion.At(now)
	progress := anim.NewTween(0.0, anim.Lerp)
	to := image.Rectangle{}
	if w.genie != nil {
		progress = anim.NewTween(w.genie.progress.At(now), anim.Lerp)
		at, to = w.genie.from, w.genie.to
		w.genie = nil
	}
	tt, err := c.snapshot(w)
	if err != nil || tt == nil {
		return err
	}
	if to.Empty() {
		to = c.genieTarget(w, at)
	}
	progress.Set(1, now, curve)
	c.closing = append(c.closing, &closing{
		snapshot: tt, output: w.output, level: windowLevel,
		motion: anim.NewTween(at, anim.LerpRect), fade: anim.NewTween(1.0, anim.Lerp),
		genie: &genie{window: w, from: at, to: to, progress: progress, fades: c.minimizeFades()},
	})
	return nil
}

func (c *Compositor) startRestore(w *window, goal image.Rectangle, now time.Duration) error {
	curve := c.animations.Curve(anim.WindowsMinimize)
	var errs error
	progress := anim.NewTween(1.0, anim.Lerp)
	if cl := c.genieLeaving(w); cl != nil {
		progress = anim.NewTween(cl.genie.progress.At(now), anim.Lerp)
		c.closing = slices.DeleteFunc(c.closing, func(x *closing) bool { return x == cl })
		errs = c.dropClosing(cl)
	}
	if curve == anim.Instant {
		return errs
	}
	if o := c.outputAtRect(goal); o != nil {
		w.output = o
	}
	progress.Set(0, now, curve)
	w.genie = &genie{window: w, from: goal, to: c.genieTarget(w, goal), progress: progress, fades: c.minimizeFades()}
	return errs
}

func (c *Compositor) minimizeFades() bool {
	return c.animations.Style(anim.WindowsMinimize).Kind == anim.FadeStyle
}

func (c *Compositor) drawGenie(pass *frame, o *output.Output, g *genie, tex render.Texture, version uint64) {
	from, to := deviceRect(o, g.from), deviceRect(o, g.to)
	if g.fades {
		size := tex.Size()
		alpha := float32(min(max(1-g.progress.At(c.frameTime), 0), 1))
		pass.ops = append(pass.ops, drawOp{tex: tex, src: render.FRect{W: float64(size.X), H: float64(size.Y)}, dst: from, alpha: alpha, version: version})
		return
	}
	strips, alongX := anim.GenieLayout(c.genieStrips[:0], from, to, g.progress.At(c.frameTime))
	c.genieStrips = strips
	size := tex.Size()
	for _, s := range strips {
		if s.Rect.Empty() {
			continue
		}
		x, y, w, h := s.Source(size, alongX)
		pass.ops = append(pass.ops, drawOp{tex: tex, src: render.FRect{X: x, Y: y, W: w, H: h}, dst: s.Rect, alpha: 1, version: version})
	}
}

func (c *Compositor) drawWindowGenie(pass *frame, o *output.Output, w *window, from int) error {
	if w.genie == nil {
		return nil
	}
	tile := deviceRect(o, w.tile)
	if tile.Empty() {
		return nil
	}
	if err := c.renderJelly(pass, o, w, tile, from); err != nil {
		return err
	}
	c.drawGenie(pass, o, w.genie, w.jelly.Texture(), w.jellyVersion)
	return nil
}

func (c *Compositor) settleGenies(now time.Duration) {
	for _, w := range c.windows {
		if w.genie != nil && w.genie.progress.Done(now) {
			w.genie = nil
		}
	}
}
