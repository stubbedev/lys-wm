package drm

import (
	"errors"
	"fmt"
	"image"
	"path/filepath"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
)

type copyAllocator struct {
	dev       device
	primary   render.Renderer
	loop      *loop.Loop
	report    func(error)
	modifiers bool
	linear    *gbmAllocator
	copies    bool
}

func (a *copyAllocator) fallBack(err error) bool {
	if a.linear == nil || a.copies {
		return false
	}
	a.copies = true
	a.report(fmt.Errorf("drm: a display-only card takes no linear buffer of the primary GPU, so each of its frames is copied on the CPU: %w", err))
	return true
}

func (a *copyAllocator) share(size image.Point, format fourcc.Format) (*scanout, error) {
	bo, err := a.linear.gbm.Allocate(size, format, []fourcc.Modifier{fourcc.Linear}, gpu.UseRendering)
	if err != nil {
		return nil, fmt.Errorf("drm: a linear %v buffer on the primary GPU: %w", size, err)
	}
	if bo.Modifier != fourcc.Linear {
		return nil, errors.Join(fmt.Errorf("drm: the primary GPU's linear buffer has modifier %v", bo.Modifier), bo.Close())
	}
	target, err := a.linear.gpu.ImportTarget(bo.DMABUF)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: render into a linear buffer on the primary GPU: %w", err), bo.Close())
	}
	fb, err := importFB(a.dev, bo.DMABUF, a.modifiers)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: scan out the primary GPU's linear buffer: %w", err), target.Destroy(), bo.Close())
	}
	return &scanout{target: target, fb: fb, release: func() error { return errors.Join(target.Destroy(), bo.Close()) }}, nil
}

func (a *copyAllocator) asyncReader() (render.AsyncReader, bool) {
	r, ok := a.primary.(render.AsyncReader)
	return r, ok && r.AsyncReads() && a.loop != nil
}

func (a *copyAllocator) renderer() render.Renderer {
	return a.primary
}

func (a *copyAllocator) named(how string) string {
	if r, ok := a.primary.(render.AsyncReader); ok && r.AsyncReads() {
		return how
	}
	return how + "; this renderer reads frames back synchronously, which holds up every output"
}

func (a *copyAllocator) renderable(fourcc.Format) ([]fourcc.Modifier, error) {
	return []fourcc.Modifier{fourcc.Linear}, nil
}

func (a *copyAllocator) allocate(size image.Point, format fourcc.Format, _ []fourcc.Modifier) (*scanout, error) {
	if a.linear != nil && !a.copies {
		s, err := a.share(size, format)
		if err == nil {
			return s, nil
		}
		a.fallBack(err)
	}
	async, isAsync := a.asyncReader()
	var target render.ReadTarget
	var err error
	if isAsync {
		target, err = async.NewAsyncTarget(size)
	} else {
		target, err = a.primary.NewTarget(size)
	}
	if err != nil {
		return nil, fmt.Errorf("drm: a %v target on the primary GPU: %w", size, err)
	}
	dumb, err := a.dev.CreateDumb(size)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: a %v scanout buffer: %w", size, err), target.Destroy())
	}
	fb, err := a.dev.AddFB2(kdrm.FB{Width: uint32(size.X), Height: uint32(size.Y), Format: format, Handles: [4]uint32{dumb.Handle}, Pitches: [4]uint32{dumb.Pitch}}) //nolint:gosec // scanout sizes fit
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: a %v framebuffer: %w", size, err), a.dev.DestroyDumb(dumb), target.Destroy())
	}
	release := func() error { return errors.Join(a.dev.DestroyDumb(dumb), target.Destroy()) }
	if t, ok := target.(render.AsyncReadTarget); ok && isAsync {
		c := &offLoopCopy{loop: a.loop, target: t, dumb: dumb, release: release}
		return &scanout{target: target, fb: fb, prepareLater: c.start, release: c.releaseWhenIdle}, nil
	}
	return &scanout{
		target:   target,
		fb:       fb,
		syncRead: true,
		prepare:  func() error { return target.Read(dumb.Pix, int(dumb.Pitch)) },
		release:  release,
	}, nil
}

type offLoopCopy struct {
	loop     *loop.Loop
	target   render.AsyncReadTarget
	dumb     kdrm.Dumb
	release  func() error
	copying  bool
	released bool
}

func (c *offLoopCopy) releaseWhenIdle() error {
	if c.copying {
		c.released = true
		return nil
	}
	return c.release()
}

func (c *offLoopCopy) post(f func() error) {
	if err := c.loop.Post(f); err != nil && !errors.Is(err, loop.ErrClosed) {
		panic(fmt.Sprintf("drm: hand a frame copy back to the loop: %v", err))
	}
}

func (c *offLoopCopy) finished(done func(error) error, err error) error {
	c.copying = false
	if c.released {
		return errors.Join(err, c.release())
	}
	return done(err)
}

func (c *offLoopCopy) start(done func(error) error) error {
	pending, err := c.target.ReadAsync()
	if err != nil {
		return done(fmt.Errorf("start the read of a frame: %w", err))
	}
	c.copying = true
	go func() {
		err := pending.Wait()
		c.post(func() error {
			if err != nil {
				return c.finished(done, errors.Join(err, pending.Release()))
			}
			pix, stride, err := pending.Map()
			if err != nil {
				return c.finished(done, errors.Join(err, pending.Release()))
			}
			go func() {
				size := c.target.Size()
				row, pitch := size.X*4, int(c.dumb.Pitch)
				for y := range size.Y {
					copy(c.dumb.Pix[y*pitch:y*pitch+row], pix[y*stride:])
				}
				c.post(func() error { return c.finished(done, pending.Release()) })
			}()
			return nil
		})
	}()
	return nil
}

func (a *copyAllocator) fence() (int, error) {
	if a.linear != nil {
		return a.linear.fence()
	}
	return -1, nil
}

func (a *copyAllocator) close() error {
	return nil
}

func NewSecondary(l *loop.Loop, dev *kdrm.Device, primary *Backend, opts Options, canRender bool) (*Backend, string, error) {
	if err := atomic(dev); err != nil {
		return nil, "", err
	}
	alloc, how := secondaryAllocator(dev, primary, stacksFor(opts), canRender)
	b, err := newBackend(l, dev, alloc, opts)
	if err != nil {
		return nil, "", errors.Join(err, alloc.close())
	}
	return b, how, nil
}

type namedStack struct {
	name string
	open func(dev *kdrm.Device) (*gbmAllocator, error)
}

func stacksFor(opts Options) []namedStack {
	glesStack := namedStack{"GLES", func(dev *kdrm.Device) (*gbmAllocator, error) { return newGLESAllocator(dev, opts.Paths) }}
	if !opts.Vulkan {
		return []namedStack{glesStack}
	}
	return []namedStack{{"Vulkan", func(dev *kdrm.Device) (*gbmAllocator, error) { return newVulkanAllocator(dev, opts.Paths) }}, glesStack}
}

func openLocal(dev *kdrm.Device, stacks []namedStack) (*gbmAllocator, string, error) {
	var errs []error
	for _, s := range stacks {
		a, err := s.open(dev)
		if err == nil {
			return a, s.name, nil
		}
		errs = append(errs, err)
	}
	return nil, "", errors.Join(errs...)
}

func secondaryAllocator(dev *kdrm.Device, primary *Backend, stacks []namedStack, canRender bool) (allocator, string) {
	cpu := &copyAllocator{dev: dev, primary: primary.Renderer()}
	name := filepath.Base(dev.Path())
	p, ok := primary.alloc.(*gbmAllocator)
	if !canRender && ok {
		cpu.linear = p
		return cpu, cpu.named("the primary GPU's linear buffers, since " + name + " has no render node, or a CPU copy where it refuses them")
	}
	if !canRender {
		return cpu, cpu.named("a CPU copy, since " + name + " has no render node")
	}
	if !ok {
		return cpu, cpu.named(fmt.Sprintf("a CPU copy, since the primary renders with %T, which shares no buffers", primary.alloc))
	}
	local, stack, err := openLocal(dev, stacks)
	if err != nil {
		return cpu, cpu.named(fmt.Sprintf("a CPU copy, since %s cannot render: %v", name, err))
	}
	a := &blitAllocator{primary: p, local: local}
	direct := a.probe()
	if direct == nil {
		return a, "a GPU blit with " + stack + " on " + name
	}
	if err := a.copyThroughVulkan(); err != nil {
		return cpu, cpu.named(fmt.Sprintf("a CPU copy, since the GPUs share no buffer: %v", errors.Join(direct, err, local.close())))
	}
	return a, "a GPU blit with " + stack + " on " + name + ", from a linear copy of each frame that Vulkan makes on the primary GPU"
}
