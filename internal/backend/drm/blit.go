package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"
	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
)

type blitAllocator struct {
	primary *gbmAllocator
	local   *gbmAllocator
	vk      *lgpu.Device
}

func (a *blitAllocator) renderer() render.Renderer {
	return a.primary.stack.Renderer
}

func (a *blitAllocator) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.local.renderable(format)
}

type shared struct {
	target render.ReadTarget
	tex    render.Texture
	copy   func() error
	free   []func() error
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
	if a.vk != nil {
		return a.shareCopied(size, format)
	}
	s := &shared{}
	b, err := a.primary.stack.GBM.Allocate(size, format, []fourcc.Modifier{fourcc.Linear}, gpu.UseRendering)
	if err != nil {
		return nil, fmt.Errorf("a linear buffer on the primary GPU: %w", err)
	}
	s.free = append(s.free, b.Close)
	target, err := a.primary.stack.Renderer.ImportFramebuffer(b.DMABUF)
	if err != nil {
		return s.fail(fmt.Errorf("render into it on the primary GPU: %w", err))
	}
	s.target, s.free = target, append(s.free, target.Destroy)
	if err := a.importOnLocal(s, b.DMABUF); err != nil {
		return s.fail(err)
	}
	return s, nil
}

func (a *blitAllocator) shareCopied(size image.Point, format fourcc.Format) (*shared, error) {
	s := &shared{}
	lin, err := a.primary.stack.GBM.Allocate(size, format, nil, gpu.UseLinear)
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
	frame, err := a.primary.stack.GBM.Allocate(size, format, mods, gpu.UseRendering)
	if err != nil {
		return s.fail(fmt.Errorf("a frame on the primary GPU: %w", err))
	}
	s.free = append(s.free, frame.Close)
	target, err := a.primary.stack.Renderer.ImportFramebuffer(frame.DMABUF)
	if err != nil {
		return s.fail(fmt.Errorf("render into the frame on the primary GPU: %w", err))
	}
	s.target, s.free = target, append(s.free, target.Destroy)
	src, err := a.vk.ImportTarget(frame.Dmabuf())
	if err != nil {
		return s.fail(fmt.Errorf("copy from the frame with Vulkan: %w", err))
	}
	s.free = append(s.free, func() error { src.Close(); return nil })
	dst, err := a.vk.ImportLinear(lin.Dmabuf())
	if err != nil {
		return s.fail(fmt.Errorf("copy into the linear buffer with Vulkan: %w", err))
	}
	s.free = append(s.free, func() error { dst.Close(); return nil })
	if err := a.importOnLocal(s, lin.DMABUF); err != nil {
		return s.fail(err)
	}
	all := []image.Rectangle{{Max: size}}
	s.copy = func() error {
		if err := handOver(a.primaryDone, a.vk.SyncFiles(), a.vk.WaitSyncFile); err != nil {
			return err
		}
		return a.vk.CopyToLinear(dst, src, all)
	}
	return s, nil
}

func (a *blitAllocator) copyable(format fourcc.Format) ([]fourcc.Modifier, error) {
	rendered, err := a.primary.renderable(format)
	if err != nil {
		return nil, err
	}
	copied, err := a.vk.TargetModifiers(format)
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
	tex, err := a.local.stack.Renderer.ImportTexture(d)
	if err != nil {
		return fmt.Errorf("read it on %s: %w", a.local.dev.Path(), err)
	}
	s.tex, s.free = tex, append(s.free, tex.Destroy)
	return nil
}

func (a *blitAllocator) primaryDone(native bool) (int, error) {
	r := a.primary.stack.Renderer
	if native && r.NativeFences() {
		return r.SyncFile()
	}
	return -1, r.Finish()
}

func (a *blitAllocator) vkDone(native bool) (int, error) {
	if native && a.vk.SyncFiles() {
		return a.vk.SyncFile()
	}
	return -1, a.vk.Finish()
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

func (a *blitAllocator) copyThroughVulkan() error {
	main, err := a.primary.stack.Renderer.MainDevice()
	if err != nil {
		return err
	}
	d, err := lgpu.OpenNode(main)
	if err != nil {
		return fmt.Errorf("open the primary GPU with Vulkan: %w", err)
	}
	a.vk = d
	if err := a.probe(); err != nil {
		d.Close()
		a.vk = nil
		return err
	}
	return nil
}

func (a *blitAllocator) allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (*scanout, error) {
	out, err := a.local.allocate(size, format, modifiers)
	if err != nil {
		return nil, err
	}
	s, err := a.share(size, format)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: a %v frame shared with %s: %w", size, a.local.dev.Path(), err), out.free())
	}
	return &scanout{
		target:  s.target,
		fb:      out.fb,
		prepare: func() error { return a.blit(s, out.target, size) },
		free:    func() error { return errors.Join(s.release(), out.free()) },
	}, nil
}

func (a *blitAllocator) blit(s *shared, dst render.Target, size image.Point) error {
	local := a.local.stack.Renderer
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
	p.DrawTexture(s.tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, all, render.Normal, 1)
	return p.End()
}

func (a *blitAllocator) fence() (int, error) {
	return a.local.fence()
}

func (a *blitAllocator) close() error {
	if a.vk != nil {
		a.vk.Close()
		a.vk = nil
	}
	return a.local.close()
}
