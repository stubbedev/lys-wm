package drm

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
)

type bufferMaker interface {
	Allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier, usage gpu.Usage) (*gpu.Buffer, error)
}

var errUndrawable = errors.New("the renderer draws into no buffer GBM allocated")

type bufferUse uint8

const (
	scannedOut bufferUse = iota
	drawnOnly
)

type drawable struct {
	target render.ReadTarget
	buf    gpu.DMABUF
	free   func() error
}

type memory interface {
	modifiers(format fourcc.Format) ([]fourcc.Modifier, error)
	drawable(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier, use bufferUse) (drawable, error)
	linear(size image.Point, format fourcc.Format) (*gpu.Buffer, error)
}

type gbmMemory struct {
	gbm bufferMaker
	gpu gpuRenderer
}

func (m gbmMemory) modifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return m.gpu.RenderableModifiers(format)
}

func (m gbmMemory) drawable(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier, use bufferUse) (drawable, error) {
	usage := gpu.UseRendering
	if use == scannedOut {
		usage |= gpu.UseScanout
	}
	bo, err := m.gbm.Allocate(size, format, modifiers, usage)
	if err != nil {
		return drawable{}, err
	}
	target, err := m.gpu.ImportTarget(bo.DMABUF)
	if err != nil {
		return drawable{}, errors.Join(fmt.Errorf("%w: a %v buffer with modifier %v: %w", errUndrawable, format, bo.Modifier, err), bo.Close())
	}
	return drawable{target: target, buf: bo.DMABUF, free: func() error { return errors.Join(target.Destroy(), bo.Close()) }}, nil
}

func (m gbmMemory) linear(size image.Point, format fourcc.Format) (*gpu.Buffer, error) {
	return m.gbm.Allocate(size, format, nil, gpu.UseLinear)
}

type vulkanMemory struct {
	r *vulkan.Renderer
}

func (m vulkanMemory) modifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return m.r.ExportModifiers(format)
}

func (m vulkanMemory) drawable(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier, _ bufferUse) (drawable, error) {
	target, exported, err := m.r.NewExportedTarget(size, format, modifiers)
	if err != nil {
		return drawable{}, err
	}
	kept, err := keepPlanes(exported)
	if err != nil {
		return drawable{}, errors.Join(err, target.Destroy())
	}
	return drawable{target: target, buf: kept.DMABUF, free: func() error { return errors.Join(target.Destroy(), kept.Close()) }}, nil
}

func (m vulkanMemory) linear(size image.Point, format fourcc.Format) (*gpu.Buffer, error) {
	b, err := m.r.Device().NewBuffer(size.X, size.Y, format, []fourcc.Modifier{fourcc.Linear})
	if err != nil {
		return nil, fmt.Errorf("vulkan: a linear %v %v buffer to copy into: %w", size, format, err)
	}
	defer b.Close()
	return keepPlanes(gpu.FromDmabuf(b.Dmabuf()))
}

func keepPlanes(d gpu.DMABUF) (*gpu.Buffer, error) {
	kept := gpu.NewBuffer(d, func() {})
	kept.Planes = make([]gpu.Plane, 0, len(d.Planes))
	for _, p := range d.Planes {
		fd, err := unix.FcntlInt(uintptr(p.FD), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("drm: keep plane fd %d of a %v %v buffer: %w", p.FD, d.Size, d.Format, err), kept.Close())
		}
		p.FD = fd
		kept.Planes = append(kept.Planes, p)
	}
	return kept, nil
}
