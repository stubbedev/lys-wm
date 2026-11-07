package compositor

import (
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
)

var (
	LockedColour    = render.Color{A: 0xff}
	AbandonedColour = render.Color{R: 0x60, A: 0xff}
)

func (c *Compositor) LockChanged() error {
	if !c.shell.LockConfirmed() {
		clear(c.blanked)
	}
	if err := c.fadeLock(); err != nil {
		return err
	}
	var errs []error
	if c.shell.Locked() {
		errs = append(errs, c.disableCapture(), c.dismissGrabs(), c.overviewLocked(), c.closeSwitcher())
	}
	return errors.Join(append(errs, c.refocus(), c.refreshPointer(), c.scheduleAll(), c.confirmLock())...)
}

func (c *Compositor) LockOutputSize(wl *sw.WlOutput) (image.Point, bool) {
	o := c.layout.Owner(wl)
	if !c.outputLive(o) {
		return image.Point{}, false
	}
	return o.LogicalSize(), true
}

func (c *Compositor) lockSurfaceOn(o *output.Output) *shell.LockSurface {
	for _, s := range c.shell.LockSurfaces() {
		if c.layout.Owner(s.Output()) == o {
			return s
		}
	}
	return nil
}

func (c *Compositor) lockTarget() *surface.Surface {
	surfaces := c.shell.LockSurfaces()
	if len(surfaces) == 0 {
		return nil
	}
	focused := c.wm.FocusedOutput()
	for _, s := range surfaces {
		if o := c.layout.Owner(s.Output()); o != nil && o.Backend().Name() == focused {
			return s.Surface()
		}
	}
	return surfaces[0].Surface()
}

func (c *Compositor) hitLock(p point) (*surface.Surface, point) {
	o := c.outputAt(p)
	if o == nil {
		return nil, point{}
	}
	s := c.lockSurfaceOn(o)
	if s == nil {
		return nil, point{}
	}
	return hitTree(s.Surface(), flat(o.Position()), p)
}

func (c *Compositor) drawLocked(pass *frame, o *output.Output, bounds image.Rectangle, shown []*surface.Surface, alpha float64) []*surface.Surface {
	colour := LockedColour
	if !c.shell.LockAlive() {
		colour = AbandonedColour
	}
	s := c.lockSurfaceOn(o)
	from := len(pass.ops)
	if s == nil || alpha >= 1 {
		pass.FillRect(bounds, colour)
	}
	if s != nil {
		shown = c.drawTree(pass, o, s.Surface(), flat(o.Position()), shown)
	}
	if alpha < 1 {
		pass.fade(from, float32(alpha))
	}
	return shown
}

func (c *Compositor) lockBlanked(o *output.Output) error {
	if !c.shell.Locked() || c.shell.LockConfirmed() || c.lockOpacity(c.frameTime) < 1 {
		return nil
	}
	c.blanked[o] = true
	return c.confirmLock()
}

func (c *Compositor) confirmLock() error {
	if !c.shell.Locked() || c.shell.LockConfirmed() {
		return nil
	}
	for _, o := range c.outputs {
		if o.Backend().State().Lit() && !c.blanked[o] {
			return nil
		}
	}
	return c.shell.ConfirmLock()
}

func (c *Compositor) fadeLock() error {
	locked := c.shell.Locked()
	if locked && !c.shell.LockAlive() {
		c.lockFade.Jump(1)
	}
	if locked == c.lockShown {
		return nil
	}
	c.lockShown = locked
	now := c.clock()
	if locked {
		c.lockFade.Set(1, now, c.animations.Curve(anim.FadeIn))
		return c.dropLockSnapshots()
	}
	if len(c.lockSnapshots) == 0 {
		c.lockFade.Jump(0)
		return nil
	}
	c.lockFade.Set(0, now, c.animations.Curve(anim.FadeOut))
	return nil
}

func (c *Compositor) LockReleasing() error {
	if err := c.dropLockSnapshots(); err != nil {
		return err
	}
	if c.animations.Curve(anim.FadeOut) == anim.Instant {
		return nil
	}
	for _, o := range c.outputs {
		if !c.outputLive(o) {
			continue
		}
		bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
		var f frame
		c.drawLocked(&f, o, bounds, nil, 1)
		tt, err := c.renderOffscreen(o, &f, bounds)
		if err != nil {
			return fmt.Errorf("compositor: snapshot the lock on %s: %w", o.Backend().Name(), err)
		}
		if tt != nil {
			c.lockSnapshots[o] = tt
		}
	}
	return nil
}

func (c *Compositor) lockOpacity(now time.Duration) float64 {
	return min(max(c.lockFade.At(now), 0), 1)
}

func (c *Compositor) lockFading(now time.Duration) bool {
	return !c.lockFade.Done(now)
}

func (c *Compositor) drawLockSnapshot(pass *frame, o *output.Output, bounds image.Rectangle, alpha float64) {
	tt := c.lockSnapshots[o]
	if tt == nil || alpha <= 0 {
		return
	}
	size := tt.Size()
	pass.DrawTexture(tt.Texture(), render.FRect{W: float64(size.X), H: float64(size.Y)}, bounds, render.Normal, float32(alpha))
}

func (c *Compositor) settleLockFade(now time.Duration) error {
	if c.shell.Locked() || c.lockFading(now) {
		return nil
	}
	return c.dropLockSnapshots()
}

func (c *Compositor) dropLockSnapshotOn(o *output.Output) error {
	tt, ok := c.lockSnapshots[o]
	if !ok {
		return nil
	}
	delete(c.lockSnapshots, o)
	return c.releaseTarget(tt)
}

func (c *Compositor) dropLockSnapshots() error {
	var errs []error
	for o := range c.lockSnapshots {
		errs = append(errs, c.dropLockSnapshotOn(o))
	}
	return errors.Join(errs...)
}
