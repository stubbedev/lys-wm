package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"
	"github.com/stubbedev/lys-lib/fourcc"
	lgpu "github.com/stubbedev/lys-lib/gpu"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
)

var ErrNoVulkan = errors.New("drm: no Vulkan driver renders for the card")

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

type gpuAllocator struct {
	dev       device
	path      string
	modifiers bool
	mem       memory
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

func addFB2Modifiers(dev device) (bool, error) {
	modifiers, err := dev.Cap(kdrm.CapAddFB2Modifiers)
	if err != nil {
		return false, fmt.Errorf("drm: ADDFB2 modifier support of %s: %w", dev.Path(), err)
	}
	return modifiers == 1, nil
}

func newGLESAllocator(dev device, paths gpu.Paths) (*gpuAllocator, error) {
	modifiers, err := addFB2Modifiers(dev)
	if err != nil {
		return nil, err
	}
	files, err := renderFiles(dev)
	if err != nil {
		return nil, err
	}
	s, err := openStackOnFile(files, func(fd int) (gpuStack, error) {
		stack, err := gles.OnGBM(fd, paths)
		if err != nil {
			return gpuStack{}, fmt.Errorf("render with GLES: %w", err)
		}
		return gpuStack{gbm: stack.GBM, gpu: stack.Renderer, closeGPU: stack.Close, softwareOnly: stack.SoftwareOnly()}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("drm: rendering for %s: %w", dev.Path(), err)
	}
	return &gpuAllocator{dev: dev, path: dev.Path(), modifiers: modifiers, mem: gbmMemory{gbm: s.gbm, gpu: s.gpu}, gpu: s.gpu, closeGPU: s.closeGPU}, nil
}

func newVulkanAllocator(dev device, paths gpu.Paths) (*gpuAllocator, error) {
	modifiers, err := addFB2Modifiers(dev)
	if err != nil {
		return nil, err
	}
	number, err := dev.Number()
	if err != nil {
		return nil, fmt.Errorf("drm: the device number of %s: %w", dev.Path(), err)
	}
	files, err := renderFiles(dev)
	if err != nil {
		return nil, err
	}
	r, err := vulkan.Open(number)
	if err != nil {
		return nil, vulkanOpenError(dev.Path(), err)
	}
	s, err := openStackOnFile(files, func(fd int) (gpuStack, error) { return gbmOn(fd, paths) })
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: scanout buffers for %s: %w", dev.Path(), err), r.Close())
	}
	closeGPU := func() error { return errors.Join(r.Close(), s.closeGPU()) }
	a := &gpuAllocator{dev: dev, path: dev.Path(), modifiers: modifiers, mem: gbmMemory{gbm: s.gbm, gpu: r}, gpu: r, closeGPU: closeGPU}
	if err := a.probe(); err != nil {
		return nil, errors.Join(fmt.Errorf("drm: allocate scanout buffers for %s with Vulkan: %w", dev.Path(), err), closeGPU())
	}
	return a, nil
}

func gbmOn(fd int, paths gpu.Paths) (gpuStack, error) {
	lib, err := gpu.LoadGBM(paths)
	if err != nil {
		return gpuStack{}, err
	}
	g, err := lib.Open(fd)
	if err != nil {
		return gpuStack{}, errors.Join(err, lib.Close())
	}
	return gpuStack{gbm: g, closeGPU: func() error {
		g.Close()
		return lib.Close()
	}}, nil
}

func vulkanOpenError(path string, err error) error {
	if errors.Is(err, lgpu.ErrNoVulkan) || errors.Is(err, lgpu.ErrNoDevice) {
		return fmt.Errorf("%w %s: %w", ErrNoVulkan, path, err)
	}
	return fmt.Errorf("drm: render for %s with Vulkan: %w", path, err)
}

func (a *gpuAllocator) probe() error {
	mods, err := a.mem.modifiers(scanoutFormat)
	if err != nil {
		return err
	}
	d, err := a.mem.drawable(image.Pt(16, 16), scanoutFormat, mods, scannedOut)
	if err != nil {
		return err
	}
	id, err := importFB(a.dev, d.buf, a.modifiers)
	if err != nil {
		return errors.Join(fmt.Errorf("%s takes no framebuffer from a %v scanout buffer: %w", a.path, d.buf.Modifier, err), d.free())
	}
	return errors.Join(a.dev.RemoveFB(id), d.free())
}

func (a *gpuAllocator) renderer() render.Renderer {
	return a.gpu
}

func (a *gpuAllocator) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.mem.modifiers(format)
}

func (a *gpuAllocator) allocate(size image.Point, l layout) (*scanout, error) {
	d, err := a.mem.drawable(size, l.format, l.modifiers, scannedOut)
	if err != nil {
		return nil, fmt.Errorf("drm: a %v %v scanout buffer on %s: %w", size, l.format, a.path, err)
	}
	id, err := importFB(a.dev, d.buf, a.modifiers)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: %s: %w", a.path, err), d.free())
	}
	return &scanout{target: d.target, fb: id, modifier: d.buf.Modifier, fence: a.fence, release: d.free}, nil
}

func (a *gpuAllocator) linearTarget(size image.Point, format fourcc.Format) (drawable, error) {
	d, err := a.mem.drawable(size, format, []fourcc.Modifier{fourcc.Linear}, drawnOnly)
	if err != nil {
		return drawable{}, fmt.Errorf("a linear %v buffer on the primary GPU: %w", size, err)
	}
	if d.buf.Modifier != fourcc.Linear {
		return drawable{}, errors.Join(fmt.Errorf("the primary GPU's linear buffer has modifier %v", d.buf.Modifier), d.free())
	}
	return d, nil
}

func (a *gpuAllocator) rendersLinear() error {
	d, err := a.linearTarget(image.Pt(16, 16), scanoutFormat)
	if err != nil {
		return err
	}
	return d.free()
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

func (a *gpuAllocator) fence() (int, error) {
	if !a.gpu.NativeFences() {
		return -1, a.gpu.Finish()
	}
	return a.gpu.SyncFile()
}

func (a *gpuAllocator) close() error {
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

type Master uint8

const (
	HoldsMaster Master = iota
	WithoutMaster
)

type Options struct {
	Paths   gpu.Paths
	Vendors edid.Vendors
	Report  func(error)
	Vulkan  bool
	Master  Master
}

func primaryAllocator(dev device, opts Options) (allocator, error) {
	if opts.Vulkan {
		return newVulkanAllocator(dev, opts.Paths)
	}
	return newGLESAllocator(dev, opts.Paths)
}

func New(l *loop.Loop, dev *kdrm.Device, opts Options) (*Backend, error) {
	if err := atomic(dev); err != nil {
		return nil, err
	}
	own := device{kernel: dev}
	alloc, err := primaryAllocator(own, opts)
	if err != nil {
		return nil, err
	}
	b, err := newBackend(l, own, alloc, opts)
	if err != nil {
		return nil, errors.Join(err, alloc.close())
	}
	return b, nil
}
