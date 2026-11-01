package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
)

type gpuRenderer interface {
	render.Renderer
	RenderableModifiers(format fourcc.Format) ([]fourcc.Modifier, error)
	ImportTarget(b gpu.DMABUF) (render.ReadTarget, error)
	ImportTexture(b gpu.DMABUF) (render.Texture, error)
	MainDevice() (uint64, error)
	NativeFences() bool
	SyncFile() (int, error)
	WaitFence(fd int) error
	Finish() error
}

type bufferMaker interface {
	Allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier, usage gpu.Usage) (*gpu.Buffer, error)
}

type gbmAllocator struct {
	dev       device
	path      string
	modifiers bool
	gbm       bufferMaker
	gpu       gpuRenderer
	closeGPU  func() error
}

type gpuStack struct {
	gbm      bufferMaker
	gpu      gpuRenderer
	closeGPU func() error
	// softwareOnly marks a stack that allocates only on a card node.
	softwareOnly bool
}

func openStackOnFile(files []func() (*kdrm.Device, error), open func(fd int) (gpuStack, error)) (gpuStack, error) {
	var errs []error
	for i, file := range files {
		f, err := file()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s, err := open(f.FD())
		if err != nil {
			errs = append(errs, fmt.Errorf("on %s: %w", f.Path(), err), f.Close())
			continue
		}
		if s.softwareOnly && i < len(files)-1 {
			errs = append(errs, s.closeGPU(), f.Close())
			continue
		}
		probe, err := s.gbm.Allocate(image.Pt(16, 16), scanoutFormat, nil, gpu.UseRendering|gpu.UseScanout)
		if err != nil {
			errs = append(errs, fmt.Errorf("on %s: %w", f.Path(), err), s.closeGPU(), f.Close())
			continue
		}
		if err := probe.Close(); err != nil {
			return gpuStack{}, errors.Join(err, s.closeGPU(), f.Close())
		}
		closeStack := s.closeGPU
		s.closeGPU = func() error { return errors.Join(closeStack(), f.Close()) }
		return s, nil
	}
	return gpuStack{}, errors.Join(errs...)
}

func newAllocator(dev *kdrm.Device, open func(fd int) (gpuStack, error)) (*gbmAllocator, error) {
	modifiers, err := dev.Cap(kdrm.CapAddFB2Modifiers)
	if err != nil {
		return nil, fmt.Errorf("drm: ADDFB2 modifier support of %s: %w", dev.Path(), err)
	}
	files, err := renderFiles(dev)
	if err != nil {
		return nil, err
	}
	s, err := openStackOnFile(files, open)
	if err != nil {
		return nil, fmt.Errorf("drm: rendering for %s: %w", dev.Path(), err)
	}
	return &gbmAllocator{dev: dev, path: dev.Path(), modifiers: modifiers == 1, gbm: s.gbm, gpu: s.gpu, closeGPU: s.closeGPU}, nil
}

func newGLESAllocator(dev *kdrm.Device, paths gpu.Paths) (*gbmAllocator, error) {
	return newAllocator(dev, func(fd int) (gpuStack, error) {
		stack, err := gles.OnGBM(fd, paths)
		if err != nil {
			return gpuStack{}, fmt.Errorf("render with GLES: %w", err)
		}
		return gpuStack{gbm: stack.GBM, gpu: stack.Renderer, closeGPU: stack.Close, softwareOnly: stack.SoftwareOnly()}, nil
	})
}

func newVulkanAllocator(dev *kdrm.Device, paths gpu.Paths) (*gbmAllocator, error) {
	number, err := dev.Number()
	if err != nil {
		return nil, fmt.Errorf("drm: the device number of %s: %w", dev.Path(), err)
	}
	return newAllocator(dev, func(fd int) (gpuStack, error) {
		r, err := vulkan.Open(number)
		if err != nil {
			return gpuStack{}, fmt.Errorf("render with Vulkan: %w", err)
		}
		lib, err := gpu.LoadGBM(paths)
		if err != nil {
			return gpuStack{}, errors.Join(fmt.Errorf("allocate with GBM: %w", err), r.Close())
		}
		bo, err := lib.Open(fd)
		if err != nil {
			return gpuStack{}, errors.Join(fmt.Errorf("allocate with GBM: %w", err), lib.Close(), r.Close())
		}
		closeGPU := func() error {
			err := r.Close()
			bo.Close()
			return errors.Join(err, lib.Close())
		}
		return gpuStack{gbm: bo, gpu: r, closeGPU: closeGPU}, nil
	})
}

func (a *gbmAllocator) renderer() render.Renderer {
	return a.gpu
}

func (a *gbmAllocator) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.gpu.RenderableModifiers(format)
}

func (a *gbmAllocator) allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (*scanout, error) {
	bo, err := a.gbm.Allocate(size, format, modifiers, gpu.UseRendering|gpu.UseScanout)
	if err != nil {
		return nil, err
	}
	target, err := a.gpu.ImportTarget(bo.DMABUF)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: import a %v buffer on %s: %w", bo.Modifier, a.path, err), bo.Close())
	}
	id, err := importFB(a.dev, bo.DMABUF, a.modifiers)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: %s: %w", a.path, err), target.Destroy(), bo.Close())
	}
	return &scanout{target: target, fb: id, release: func() error {
		return errors.Join(target.Destroy(), bo.Close())
	}}, nil
}

func importFB(dev device, d gpu.DMABUF, withModifiers bool) (uint32, error) {
	fb, err := framebuffer(d.Size, d.Format, d.Modifier, withModifiers)
	if err != nil {
		return 0, err
	}
	handles := make([]uint32, 0, len(d.Planes))
	closeHandles := func() error {
		errs := make([]error, 0, len(handles))
		for _, h := range handles {
			errs = append(errs, dev.CloseHandle(h))
		}
		return errors.Join(errs...)
	}
	for i, p := range d.Planes {
		h, err := dev.PrimeFDToHandle(p.FD)
		if err != nil {
			return 0, errors.Join(err, closeHandles())
		}
		if !slices.Contains(handles, h) {
			handles = append(handles, h)
		}
		fb.Handles[i], fb.Pitches[i], fb.Offsets[i] = h, p.Stride, p.Offset
	}
	id, err := dev.AddFB2(fb)
	if err != nil {
		return 0, errors.Join(err, closeHandles())
	}
	if err := closeHandles(); err != nil {
		return 0, errors.Join(err, dev.RemoveFB(id))
	}
	return id, nil
}

func framebuffer(size image.Point, format fourcc.Format, modifier fourcc.Modifier, withModifiers bool) (kdrm.FB, error) {
	fb := kdrm.FB{Width: uint32(size.X), Height: uint32(size.Y), Format: format} //nolint:gosec // scanout sizes fit
	switch {
	case !addable(modifier, withModifiers):
		return kdrm.FB{}, fmt.Errorf("the buffer has modifier %v but the device takes no ADDFB2 modifiers, so only an implicit or linear layout can be scanned out", modifier)
	case withModifiers && modifier != fourcc.Invalid:
		fb.Modifier, fb.WithModifier = modifier, true
	}
	return fb, nil
}

func addable(modifier fourcc.Modifier, withModifiers bool) bool {
	return withModifiers || modifier == fourcc.Invalid || modifier == fourcc.Linear
}

func (a *gbmAllocator) fence() (int, error) {
	if !a.gpu.NativeFences() {
		return -1, a.gpu.Finish()
	}
	return a.gpu.SyncFile()
}

func (a *gbmAllocator) close() error {
	return a.closeGPU()
}

func atomic(dev *kdrm.Device) error {
	for _, c := range []uint64{kdrm.ClientCapUniversal, kdrm.ClientCapAtomic} {
		if err := dev.SetClientCap(c, 1); err != nil {
			return fmt.Errorf("drm: %s needs atomic KMS: %w", dev.Path(), err)
		}
	}
	if err := dev.SetClientCap(kdrm.ClientCapCursorHotspot, 1); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
		return fmt.Errorf("drm: cursor hotspots on %s: %w", dev.Path(), err)
	}
	return nil
}

type Options struct {
	Paths   gpu.Paths
	Vendors edid.Vendors
	Report  func(error)
	Vulkan  bool
}

func primaryAllocator(dev *kdrm.Device, opts Options) (allocator, error) {
	if opts.Vulkan {
		return newVulkanAllocator(dev, opts.Paths)
	}
	return newGLESAllocator(dev, opts.Paths)
}

func New(l *loop.Loop, dev *kdrm.Device, opts Options) (*Backend, error) {
	if err := atomic(dev); err != nil {
		return nil, err
	}
	alloc, err := primaryAllocator(dev, opts)
	if err != nil {
		return nil, err
	}
	b, err := newBackend(l, dev, alloc, opts)
	if err != nil {
		return nil, errors.Join(err, alloc.close())
	}
	return b, nil
}
