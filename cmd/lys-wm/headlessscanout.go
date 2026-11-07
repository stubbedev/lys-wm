package main

import (
	"errors"
	"fmt"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/gpu"
)

type scanoutRenderer interface {
	MainDevice() (uint64, error)
	DMABUFFormats() ([]gpu.FormatModifier, error)
}

func allowHeadlessScanout(b backend.Backend) error {
	h, ok := b.(*headless.Backend)
	if !ok {
		return errors.New("-headless-scanout needs the headless backend, whose planes are a fake")
	}
	r, ok := h.Renderer().(scanoutRenderer)
	if !ok {
		return errors.New("-headless-scanout needs a GPU renderer, which imports the dmabufs it scans out")
	}
	dev, err := r.MainDevice()
	if err != nil {
		return fmt.Errorf("-headless-scanout: the renderer's device: %w", err)
	}
	formats, err := r.DMABUFFormats()
	if err != nil {
		return fmt.Errorf("-headless-scanout: the renderer's dmabuf formats: %w", err)
	}
	target := gpu.ScanoutTarget{Device: dev, Formats: formats}
	for _, o := range h.Outputs() {
		o.AllowScanout(target)
	}
	return h.SetOverlayPlanes(backend.MaxOverlayPlanes, target)
}
