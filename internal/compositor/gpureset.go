package compositor

import (
	"errors"
	"fmt"
	"image"
	"time"

	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shm"
)

func (c *Compositor) checkGPU() error {
	lost := c.renderer.Lost()
	if lost == nil || !backend.GPULost(lost) {
		return lost
	}
	return c.recoverGPU(lost)
}

func (c *Compositor) recoverGPU(cause error) error {
	if !c.gpuLosses.Record(time.Unix(0, 0).UTC().Add(c.clock()), cause) {
		return fmt.Errorf("%w: the GPU was reset %d times within %v: %w", backend.ErrDeviceLost, c.gpuLosses.Count(), lgpu.LossWindow, c.gpuLosses.Err())
	}
	if err := c.renderer.Recover(); err != nil {
		return fmt.Errorf("%w: rebuild the renderer after a GPU reset: %w", backend.ErrDeviceLost, errors.Join(cause, err))
	}
	c.gpuResets++
	c.report(fmt.Errorf("compositor: the GPU was reset, %d time(s) within %v; every GPU resource was rebuilt: %w", c.gpuLosses.Count(), lgpu.LossWindow, cause))
	return errors.Join(c.rebuildGPU(), c.scheduleAll())
}

func (c *Compositor) GPUResets() uint64 {
	return c.gpuResets
}

func (c *Compositor) Reuploads() uint64 {
	return c.reuploads
}

func (c *Compositor) rebuildGPU() error {
	errs := []error{c.reupload(), c.dropDecor(), c.dropAllClosing(), c.dropLockSnapshots(), c.dropBlurs(), c.renewVirtualOutputs()}
	for _, sc := range c.scenes {
		sc.invalidate()
		errs = append(errs, sc.dropImage(), sc.dropWorking())
	}
	for _, w := range c.windows {
		errs = append(errs, c.dropJelly(w), dropSnapshot(&w.snapshot))
		w.frost = nil
	}
	for _, l := range c.layers {
		errs = append(errs, dropSnapshot(&l.snapshot))
		l.frost = nil
	}
	return errors.Join(errs...)
}

func dropSnapshot(t *render.TextureTarget) error {
	if *t == nil {
		return nil
	}
	err := (*t).Destroy()
	*t = nil
	return err
}

func (c *Compositor) reupload() error {
	var errs []error
	for s, t := range c.textures {
		u, ok := t.(uploaded)
		if !ok {
			continue
		}
		b, ok := s.Buffer().(*shm.Buffer)
		if !ok || b.Format() != u.format || b.Size() != u.size {
			errs = append(errs, c.dropTexture(s))
			continue
		}
		c.reuploads++
		c.uploadRects = append(c.uploadRects[:0], image.Rectangle{Max: u.size})
		errs = append(errs, b.Read(func(data []byte, stride int) error { return c.updateTexture(u, s, data, stride) }))
	}
	return errors.Join(errs...)
}
