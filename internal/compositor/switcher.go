package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type SwitcherStyle struct {
	OpenDelay time.Duration
}

func DefaultSwitcherStyle() SwitcherStyle {
	return SwitcherStyle{OpenDelay: 150 * time.Millisecond}
}

const (
	switcherGap       = 16
	switcherPad       = 16
	switcherMinHeight = 48
	switcherMaxHeight = 160
	switcherShare     = 6
)

type switcherThumb struct {
	w        *window
	rect     image.Rectangle
	target   render.TextureTarget
	version  uint64
	dirty    bool
	surfaces []*surface.Surface
}

type switcher struct {
	thumbs  []*switcherThumb
	at      int
	output  *output.Output
	showAt  time.Duration
	laidOut bool
	scratch frame
	region  [1]image.Rectangle
}

func (sw *switcher) selected() *window {
	return sw.thumbs[sw.at].w
}

func (c *Compositor) focusedOutput() *output.Output {
	name := c.wm.FocusedOutput()
	for _, o := range c.outputs {
		if o.Backend().Name() == name {
			return o
		}
	}
	return nil
}

func (c *Compositor) switcherAction(a wm.SwitcherAction) error {
	if sw := c.switcher; sw != nil {
		sw.at = ((sw.at+a.Step.Delta())%len(sw.thumbs) + len(sw.thumbs)) % len(sw.thumbs)
		c.stateChanged()
		return c.schedule(sw.output)
	}
	o := c.focusedOutput()
	if o == nil || c.overviewHolds() || c.shell.Locked() {
		return nil
	}
	list := c.mruWindows(a.Scope)
	if len(list) == 0 {
		return nil
	}
	if err := c.endWalk(); err != nil {
		return err
	}
	sw := &switcher{output: o, showAt: c.aimedAny(c.clock()) + c.switcherStyle.OpenDelay, thumbs: make([]*switcherThumb, len(list))}
	for i, w := range list {
		sw.thumbs[i] = &switcherThumb{w: w, dirty: true}
	}
	first := 0
	if list[0] == c.active {
		first = 1
	}
	if a.Step == wm.StepPrev {
		first = len(list) - 1
	}
	sw.at = first % len(list)
	c.switcher, c.mruPending = sw, nil
	c.stateChanged()
	mods := c.binds.Held(c.seat.DepressedModifiers())
	if mods == 0 {
		return c.commitSwitcher()
	}
	c.OnModifiersReleased(mods, c.commitSwitcher)
	return c.schedule(o)
}

func (c *Compositor) commitSwitcher() error {
	sw := c.switcher
	if sw == nil {
		return nil
	}
	w := sw.selected()
	err := c.closeSwitcher()
	if !c.live(w) {
		return err
	}
	return errors.Join(err, c.activate(w, wm.FocusOther))
}

func (c *Compositor) closeSwitcher() error {
	sw := c.switcher
	if sw == nil {
		return nil
	}
	c.switcher, c.modRelease = nil, nil
	var errs []error
	for _, t := range sw.thumbs {
		if t.target != nil {
			errs = append(errs, c.releaseTarget(t.target))
		}
	}
	c.stateChanged()
	if c.outputLive(sw.output) {
		errs = append(errs, c.schedule(sw.output))
	}
	return errors.Join(errs...)
}

func (c *Compositor) switcherForget(w *window) error {
	sw := c.switcher
	if sw == nil {
		return nil
	}
	i := slices.IndexFunc(sw.thumbs, func(t *switcherThumb) bool { return t.w == w })
	if i < 0 {
		return nil
	}
	t := sw.thumbs[i]
	sw.thumbs = slices.Delete(sw.thumbs, i, i+1)
	var err error
	if t.target != nil {
		err = c.releaseTarget(t.target)
	}
	if len(sw.thumbs) == 0 {
		return errors.Join(err, c.closeSwitcher())
	}
	if sw.at > i || sw.at == len(sw.thumbs) {
		sw.at--
	}
	sw.laidOut = false
	c.stateChanged()
	return errors.Join(err, c.schedule(sw.output))
}

func (c *Compositor) switcherOutputGone(o *output.Output) error {
	if c.switcher == nil || c.switcher.output != o {
		return nil
	}
	return c.closeSwitcher()
}

func (c *Compositor) switcherCommitted(root *surface.Surface) {
	sw := c.switcher
	if sw == nil {
		return
	}
	for _, t := range sw.thumbs {
		if t.w.top.Surface() == root || slices.Contains(t.surfaces, root) {
			t.dirty = true
		}
	}
}

func (c *Compositor) switcherShown(o *output.Output) bool {
	sw := c.switcher
	return sw != nil && sw.output == o && c.frameTime >= sw.showAt
}

func (c *Compositor) switcherWaiting(o *output.Output) bool {
	sw := c.switcher
	return sw != nil && sw.output == o && c.frameTime < sw.showAt
}

func (c *Compositor) switcherDirty(o *output.Output) bool {
	return c.switcherShown(o) && slices.ContainsFunc(c.switcher.thumbs, func(t *switcherThumb) bool { return t.dirty })
}

func (c *Compositor) layoutSwitcher(o *output.Output) {
	sw := c.switcher
	area := o.Logical()
	h := min(max(area.Dy()/switcherShare, switcherMinHeight), switcherMaxHeight)
	widths := make([]int, len(sw.thumbs))
	total := switcherGap * (len(sw.thumbs) - 1)
	for i, t := range sw.thumbs {
		size := t.w.rect.Size()
		w := h
		if size.Y > 0 {
			w = min(max(h*size.X/size.Y, h/2), 2*h)
		}
		widths[i] = w
		total += w
	}
	room := area.Dx() - 2*switcherPad
	if total > room {
		for i := range widths {
			widths[i] = widths[i] * room / total
		}
		h = h * room / total
		total = room
	}
	x := area.Min.X + (area.Dx()-total)/2
	y := area.Min.Y + (area.Dy()-h)/2
	for i, t := range sw.thumbs {
		r := image.Rect(x, y, x+widths[i], y+h)
		if r.Size() != t.rect.Size() {
			t.dirty = true
		}
		t.rect = r
		x += widths[i] + switcherGap
	}
	sw.laidOut = true
}

func (c *Compositor) redrawSwitcherThumbs(o *output.Output) error {
	if !c.switcherShown(o) {
		return nil
	}
	sw := c.switcher
	if !sw.laidOut {
		c.layoutSwitcher(o)
	}
	for _, t := range sw.thumbs {
		if !t.dirty || t.rect.Empty() {
			continue
		}
		if err := c.refreshSwitcherThumb(o, sw, t); err != nil {
			return fmt.Errorf("the switcher preview of window %d: %w", t.w.id, err)
		}
	}
	return nil
}

func (c *Compositor) refreshSwitcherThumb(o *output.Output, sw *switcher, t *switcherThumb) error {
	size := deviceRect(o, t.rect).Size()
	if t.target != nil && t.target.Size() != size {
		err := c.releaseTarget(t.target)
		t.target, t.version = nil, 0
		if err != nil {
			return err
		}
	}
	if t.target == nil {
		target, err := c.newSceneTarget(o, size)
		if err != nil {
			return err
		}
		t.target = target
	}
	f := &sw.scratch
	f.ops = f.ops[:0]
	t.surfaces = c.drawClippedTree(f, o, t.w.top.Surface(), c.windowOrigin(t.w), t.surfaces[:0], c.windowMask(o, t.w), 1)
	f.remap(0, deviceRect(o, t.w.rect), image.Rectangle{Max: size})
	sw.region[0] = image.Rectangle{Max: size}
	pass, err := c.renderer.Begin(t.target, sw.region[:])
	if err != nil {
		return err
	}
	pass.Clear(render.Color{})
	f.replayFrom(pass, image.Point{}, c.sceneOut(o))
	if err := pass.End(); err != nil {
		return err
	}
	c.jellyGen++
	t.version, t.dirty = c.jellyGen, false
	c.switcherRedraws++
	return nil
}

func (c *Compositor) drawSwitcher(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	if !c.switcherShown(o) {
		return shown, nil
	}
	sw := c.switcher
	if !sw.laidOut {
		c.layoutSwitcher(o)
	}
	c.switcherFrames++
	strip := sw.thumbs[0].rect
	for _, t := range sw.thumbs[1:] {
		strip = strip.Union(t.rect)
	}
	radius := deviceLength(o, overviewRadius)
	c.drawOverviewGlass(pass, o, deviceRect(o, strip.Inset(-switcherPad)), deviceLength(o, overviewRadius+switcherPad), 1)
	for _, t := range sw.thumbs {
		dst := deviceRect(o, t.rect)
		shown = append(shown, t.surfaces...)
		if t.version == 0 {
			pass.fillMasked(dst, c.decor.Tab.Inactive.Background, render.Mask{Keep: render.RoundRect{Rect: dst, Radius: radius}})
			continue
		}
		drawn := t.target.Size()
		pass.ops = append(pass.ops, drawOp{
			tex: t.target.Texture(), src: render.FRect{W: float64(drawn.X), H: float64(drawn.Y)}, dst: dst, alpha: 1, version: t.version,
			mask: render.Mask{Keep: render.RoundRect{Rect: dst, Radius: radius}}, depicts: t.w.top.Surface(),
		})
	}
	sel := sw.thumbs[sw.at]
	c.drawOverviewRing(pass, o, sel.rect, radius, 1)
	dev := deviceRect(o, sel.rect)
	return shown, c.drawOverviewLabel(pass, o, sel.w.top.Title(), sel.w.top.Icon(), dev.Max.Y+o.Scale().ToDevice(overviewLabelGap), dev, 1)
}

func (c *Compositor) switcherKey(keysym uint32) (bool, error) {
	if c.switcher == nil || keysym != keysymEscape {
		return false, nil
	}
	return true, c.closeSwitcher()
}

func (c *Compositor) ipcSwitcher() *ipc.Switcher {
	sw := c.switcher
	if sw == nil {
		return nil
	}
	w := sw.selected()
	return &ipc.Switcher{Selected: w.id, Title: w.top.Title()}
}

func (c *Compositor) SwitcherFrames() uint64 {
	return c.switcherFrames
}

func (c *Compositor) SwitcherThumbRedraws() uint64 {
	return c.switcherRedraws
}

func (c *Compositor) SwitcherOpen() bool {
	return c.switcher != nil
}

func (c *Compositor) dropSwitcherTargets() error {
	if c.switcher == nil {
		return nil
	}
	var errs []error
	for _, t := range c.switcher.thumbs {
		if t.target != nil {
			errs = append(errs, c.releaseTarget(t.target))
			t.target, t.version, t.dirty = nil, 0, true
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) SwitcherWindows() []wm.WindowID {
	if c.switcher == nil {
		return nil
	}
	out := make([]wm.WindowID, len(c.switcher.thumbs))
	for i, t := range c.switcher.thumbs {
		out[i] = t.w.id
	}
	return out
}

func (c *Compositor) SwitcherPreview(id wm.WindowID) (image.Rectangle, bool) {
	if c.switcher == nil {
		return image.Rectangle{}, false
	}
	for _, t := range c.switcher.thumbs {
		if t.w.id == id {
			return t.rect, true
		}
	}
	return image.Rectangle{}, false
}
