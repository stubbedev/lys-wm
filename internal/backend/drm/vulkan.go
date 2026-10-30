package drm

import (
	"errors"
	"fmt"
	"image"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
)

type vulkanAllocator struct {
	dev       *kdrm.Device
	modifiers bool
	r         *vulkan.Renderer
}

func newVulkanAllocator(dev *kdrm.Device) (*vulkanAllocator, error) {
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
	return &vulkanAllocator{dev: dev, modifiers: modifiers == 1, r: r}, nil
}

func (a *vulkanAllocator) renderer() render.Renderer {
	return a.r
}

func (a *vulkanAllocator) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.r.RenderableModifiers(format)
}

func (a *vulkanAllocator) allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (*scanout, error) {
	t, err := a.r.NewScanoutTarget(size, format, modifiers)
	if err != nil {
		return nil, err
	}
	id, remove, err := importFB(a.dev, t.Dmabuf(), a.modifiers)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: %s: %w", a.dev.Path(), err), t.Destroy())
	}
	return &scanout{target: t, fb: id, free: func() error {
		return errors.Join(remove(), t.Destroy())
	}}, nil
}

func (a *vulkanAllocator) fence() (int, error) {
	if !a.r.NativeFences() {
		return -1, a.r.Finish()
	}
	return a.r.SyncFile()
}

func (a *vulkanAllocator) close() error {
	return a.r.Close()
}
