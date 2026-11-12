package drm

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
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
