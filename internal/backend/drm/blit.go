package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"
	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
)

type blitAllocator struct {
	primary      *gpuAllocator
	local        *gpuAllocator
	vk           *lgpu.Device
	primaryVK    *vulkan.Renderer
	closeVK      func()
	copierLost   error
	localLosses  lgpu.Losses
	copierLosses lgpu.Losses
}

func (a *blitAllocator) copier() *lgpu.Device {
	if a.primaryVK != nil {
		return a.primaryVK.Device()
	}
	return a.vk
}

func (a *blitAllocator) copies() bool {
	return a.vk != nil || a.primaryVK != nil
}

func (a *blitAllocator) onCopier(err error) error {
	if a.primaryVK == nil && a.copierLost == nil && backend.GPULost(err) {
		a.copierLost = err
	}
	return err
}

func (a *blitAllocator) renderer() render.Renderer {
	return a.primary.gpu
}

func (a *blitAllocator) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.local.renderable(format)
}

type shared struct {
	target render.ReadTarget
	tex    render.Texture
	copy   func() error
	free   []func() error
	lin    *gpu.Buffer
	frame  gpu.DMABUF
	src    *lgpu.Target
	dst    *lgpu.Linear
	on     *lgpu.Device
}

func (a *blitAllocator) importCopy(s *shared) error {
	d := a.copier()
	s.src, s.dst, s.on = nil, nil, nil
	src, err := d.ImportTarget(s.frame.Dmabuf())
	if err != nil {
		return fmt.Errorf("copy from the frame with Vulkan: %w", a.onCopier(err))
	}
	dst, err := d.ImportLinear(s.lin.Dmabuf())
	if err != nil {
		src.Close()
		return fmt.Errorf("copy into the linear buffer with Vulkan: %w", a.onCopier(err))
	}
	s.src, s.dst, s.on = src, dst, d
	return nil
}

func (s *shared) dropCopy() {
	if s.on == nil {
		return
	}
	s.src.Close()
	s.dst.Close()
	s.src, s.dst, s.on = nil, nil, nil
}

func (s *shared) release() error {
	errs := make([]error, 0, len(s.free))
	for _, free := range slices.Backward(s.free) {
		errs = append(errs, free())
	}
	s.free = nil
	return errors.Join(errs...)
}

func (s *shared) fail(err error) (*shared, error) {
	return nil, errors.Join(err, s.release())
}

func (a *blitAllocator) share(size image.Point, format fourcc.Format) (*shared, error) {
	if a.copies() {
		return a.shareCopied(size, format)
	}
	d, err := a.primary.linearTarget(size, format)
	if err != nil {
		return nil, err
	}
	s := &shared{target: d.target, free: []func() error{d.free}}
	if err := a.importOnLocal(s, d.buf); err != nil {
		return s.fail(err)
	}
	return s, nil
}

func (a *blitAllocator) shareCopied(size image.Point, format fourcc.Format) (*shared, error) {
	s := &shared{}
	lin, err := a.primary.mem.linear(size, format)
	if err != nil {
		return nil, fmt.Errorf("a linear buffer on the primary GPU: %w", err)
	}
	s.free = append(s.free, lin.Close)
	if lin.Modifier != fourcc.Linear {
		return s.fail(fmt.Errorf("the primary GPU's linear buffer has modifier %v", lin.Modifier))
	}
	mods, err := a.copyable(format)
	if err != nil {
		return s.fail(err)
	}
	frame, err := a.primary.mem.drawable(size, format, mods, drawnOnly)
	if err != nil {
		return s.fail(fmt.Errorf("a frame on the primary GPU: %w", err))
	}
	s.target, s.free = frame.target, append(s.free, frame.free)
	s.lin, s.frame = lin, frame.buf
	s.free = append(s.free, func() error { s.dropCopy(); return nil })
	if err := a.importCopy(s); err != nil {
		return s.fail(err)
	}
	if err := a.importOnLocal(s, lin.DMABUF); err != nil {
		return s.fail(err)
	}
	all := []image.Rectangle{{Max: size}}
	s.copy = func() error {
		if s.on != a.copier() {
			if err := a.importCopy(s); err != nil {
				return err
			}
		}
		if a.primaryVK != nil {
			return a.primaryVK.CopyToLinear(s.dst, s.src, all)
		}
		if err := handOver(a.primaryDone, a.vk.SyncFiles(), a.vk.WaitSyncFile); err != nil {
			return a.onCopier(err)
		}
		return a.onCopier(a.vk.CopyToLinear(s.dst, s.src, all))
	}
	return s, nil
}

func (a *blitAllocator) copyable(format fourcc.Format) ([]fourcc.Modifier, error) {
	rendered, err := a.primary.renderable(format)
	if err != nil {
		return nil, err
	}
	copied, err := a.copier().TargetModifiers(format)
	if err != nil {
		return nil, fmt.Errorf("the primary GPU's Vulkan device: %w", err)
	}
	both := slices.DeleteFunc(slices.Clone(rendered), func(m fourcc.Modifier) bool { return !slices.Contains(copied, m) })
	if len(both) == 0 {
		return nil, fmt.Errorf("the primary GPU renders %v in modifiers %v and Vulkan copies from %v, none of them shared", format, rendered, copied)
	}
	return both, nil
}

func (a *blitAllocator) importOnLocal(s *shared, d gpu.DMABUF) error {
	tex, err := a.local.gpu.ImportTexture(d)
	if err != nil {
		return fmt.Errorf("read it on %s: %w", a.local.path, err)
	}
	s.tex, s.free = tex, append(s.free, tex.Destroy)
	return nil
}

func (a *blitAllocator) primaryDone(native bool) (int, error) {
	r := a.primary.gpu
	if native && r.NativeFences() {
		return r.SyncFile()
	}
	return -1, r.Finish()
}

func (a *blitAllocator) vkDone(native bool) (int, error) {
	if r := a.primaryVK; r != nil {
		if native && r.NativeFences() {
			return r.SyncFile()
		}
		return -1, r.Finish()
	}
	if native && a.vk.SyncFiles() {
		fd, err := a.vk.SyncFile()
		return fd, a.onCopier(err)
	}
	return -1, a.onCopier(a.vk.Finish())
}

func handOver(done func(native bool) (int, error), native bool, wait func(fd int) error) error {
	fd, err := done(native)
	if err != nil || fd < 0 {
		return err
	}
	return wait(fd)
}

func (a *blitAllocator) probe() error {
	s, err := a.share(image.Pt(16, 16), scanoutFormat)
	if err != nil {
		return err
	}
	return s.release()
}

func (a *blitAllocator) openCopier() error {
	if r, ok := a.primary.gpu.(*vulkan.Renderer); ok {
		a.primaryVK, a.closeVK = r, func() {}
		return nil
	}
	main, err := a.primary.gpu.MainDevice()
	if err != nil {
		return err
	}
	d, err := lgpu.OpenNode(main)
	if err != nil {
		return fmt.Errorf("open the primary GPU with Vulkan: %w", err)
	}
	a.vk, a.closeVK = d, d.Close
	return nil
}

func (a *blitAllocator) copyThroughVulkan() error {
	if err := a.openCopier(); err != nil {
		return err
	}
	if err := a.probe(); err != nil {
		a.closeCopier()
		return err
	}
	return nil
}

func (a *blitAllocator) closeCopier() {
	if !a.copies() {
		return
	}
	a.closeVK()
	a.vk, a.primaryVK, a.closeVK = nil, nil, nil
}

func (a *blitAllocator) allocate(size image.Point, l layout) (*scanout, error) {
	out, err := a.local.allocate(size, l)
	if err != nil {
		return nil, err
	}
	s, err := a.share(size, l.format)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: a %v frame shared with %s: %w", size, a.local.path, err), out.free(a.local.dev))
	}
	return &scanout{
		target:   s.target,
		fb:       out.fb,
		modifier: out.modifier,
		fence:    a.local.fence,
		prepare:  func() error { return a.blit(s, out.target, size) },
		release:  func() error { return errors.Join(s.release(), out.release()) },
	}, nil
}

func (a *blitAllocator) blit(s *shared, dst render.Target, size image.Point) error {
	err := a.blitOnce(s, dst, size)
	if rerr := a.recoverDevices(); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}

func (a *blitAllocator) recoverDevices() error {
	var errs []error
	if lost := a.local.gpu.Lost(); lost != nil {
		if !a.localLosses.Record(time.Now().UTC(), lost) {
			return fmt.Errorf("%w: the GPU of %s was reset %d times within %v: %w", backend.ErrDeviceLost, a.local.path, a.localLosses.Count(), lgpu.LossWindow, a.localLosses.Err())
		}
		if err := a.local.gpu.Recover(); err != nil {
			return fmt.Errorf("%w: rebuild the renderer of %s after a GPU reset: %w", backend.ErrDeviceLost, a.local.path, errors.Join(lost, err))
		}
		errs = append(errs, fmt.Errorf("drm: the GPU of %s was reset and its renderer rebuilt: %w", a.local.path, lost))
	}
	if lost := a.copierLost; lost != nil {
		a.copierLost = nil
		if !a.copierLosses.Record(time.Now().UTC(), lost) {
			return fmt.Errorf("%w: the primary GPU's Vulkan copier was lost %d times within %v: %w", backend.ErrDeviceLost, a.copierLosses.Count(), lgpu.LossWindow, a.copierLosses.Err())
		}
		a.closeCopier()
		if err := a.openCopier(); err != nil {
			return fmt.Errorf("%w: reopen the primary GPU's Vulkan copier after a device loss: %w", backend.ErrDeviceLost, errors.Join(lost, err))
		}
		errs = append(errs, fmt.Errorf("drm: the primary GPU's Vulkan copier was lost and reopened: %w", lost))
	}
	return errors.Join(errs...)
}

func (a *blitAllocator) blitOnce(s *shared, dst render.Target, size image.Point) error {
	local := a.local.gpu
	if s.copy == nil {
		if err := handOver(a.primaryDone, local.NativeFences(), local.WaitFence); err != nil {
			return err
		}
	} else {
		if err := s.copy(); err != nil {
			return err
		}
		if err := handOver(a.vkDone, local.NativeFences(), local.WaitFence); err != nil {
			return err
		}
	}
	all := image.Rectangle{Max: size}
	p, err := local.Begin(dst, render.Region{all})
	if err != nil {
		return err
	}
	p.DrawTexture(s.tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, all, render.Normal, 1, render.Mask{})
	return p.End()
}

func (a *blitAllocator) close() error {
	a.closeCopier()
	return a.local.close()
}
