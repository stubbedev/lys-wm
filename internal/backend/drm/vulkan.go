package drm

import (
	"errors"
	"fmt"
	"image"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
)

type vulkanAllocator struct {
	dev       *kdrm.Device
	modifiers bool
	r         *vulkan.Renderer
	gbmLib    *gpu.GBMLibrary
	gbm       *gpu.GBM
}

func newVulkanAllocator(dev *kdrm.Device, paths gpu.Paths) (*vulkanAllocator, error) {
	modifiers, err := dev.Cap(kdrm.CapAddFB2Modifiers)
	if err != nil {
		return nil, fmt.Errorf("drm: ADDFB2 modifier support of %s: %w", dev.Path(), err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(dev.FD(), &st); err != nil {
		return nil, fmt.Errorf("drm: stat %s: %w", dev.Path(), err)
	}
	r, err := vulkan.Open(st.Rdev)
	if err != nil {
		return nil, fmt.Errorf("drm: Vulkan on %s: %w", dev.Path(), err)
	}
	lib, err := gpu.LoadGBM(paths)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: GBM for %s: %w", dev.Path(), err), r.Close())
	}
	bo, err := lib.Open(dev.FD())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: GBM on %s: %w", dev.Path(), err), lib.Close(), r.Close())
	}
	return &vulkanAllocator{dev: dev, modifiers: modifiers == 1, r: r, gbmLib: lib, gbm: bo}, nil
}

func (a *vulkanAllocator) renderer() render.Renderer {
	return a.r
}

func (a *vulkanAllocator) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.r.RenderableModifiers(format)
}

func (a *vulkanAllocator) allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (*scanout, error) {
	bo, err := a.gbm.Allocate(size, format, modifiers, gpu.UseRendering|gpu.UseScanout)
	if err != nil {
		return nil, err
	}
	t, err := a.r.ImportTarget(bo.DMABUF)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: import a %v buffer into Vulkan on %s: %w", bo.Modifier, a.dev.Path(), err), bo.Close())
	}
	id, closeHandles, err := importFB(a.dev, bo.DMABUF, a.modifiers)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: %s: %w", a.dev.Path(), err), t.Destroy(), bo.Close())
	}
	return &scanout{target: t, fb: id, release: func() error {
		return errors.Join(closeHandles(), t.Destroy(), bo.Close())
	}}, nil
}

func (a *vulkanAllocator) fence() (int, error) {
	if !a.r.NativeFences() {
		return -1, a.r.Finish()
	}
	return a.r.SyncFile()
}

func (a *vulkanAllocator) close() error {
	err := a.r.Close()
	a.gbm.Close()
	return errors.Join(err, a.gbmLib.Close())
}
