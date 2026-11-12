package drm

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"slices"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
)

type attachable interface {
	attach(l *loop.Loop, modifiers bool)
}

var errUnshared = errors.New("the card scans out no linear buffer of the primary GPU")

type linearFirst struct {
	dev       device
	primary   *gpuAllocator
	modifiers bool
	own       allocator
	ownHow    string
}

func (a *linearFirst) attach(l *loop.Loop, modifiers bool) {
	a.modifiers = modifiers
	if own, ok := a.own.(attachable); ok {
		own.attach(l, modifiers)
	}
}

func (a *linearFirst) renderer() render.Renderer {
	return a.primary.gpu
}

func (a *linearFirst) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.own.renderable(format)
}

func (a *linearFirst) allocate(size image.Point, l layout) (*scanout, error) {
	if l.route == ownBuffer {
		return a.own.allocate(size, l)
	}
	d, err := a.primary.linearTarget(size, l.format)
	if err != nil {
		return nil, fmt.Errorf("drm: %w: %w", errUnshared, err)
	}
	fb, err := importFB(a.dev, d.buf, a.modifiers)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: %w: %w", errUnshared, err), d.free())
	}
	return &scanout{target: d.target, fb: fb, modifier: d.buf.Modifier, fence: a.primary.fence, release: d.free}, nil
}

func (a *linearFirst) fallBack(l layout, err error) (layout, string, bool) {
	if l.route == ownBuffer || !refused(err) && !errors.Is(err, errUnshared) {
		return l, "", false
	}
	l.route = ownBuffer
	return l, a.ownHow, true
}

func (a *linearFirst) close() error {
	return a.own.close()
}

type copyAllocator struct {
	dev     device
	primary render.Renderer
	loop    *loop.Loop
}

func (a *copyAllocator) attach(l *loop.Loop, _ bool) {
	a.loop = l
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

func (a *copyAllocator) allocate(size image.Point, l layout) (*scanout, error) {
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
	fb, err := a.dev.AddFB2(kdrm.FB{Width: uint32(size.X), Height: uint32(size.Y), Format: l.format, Handles: [4]uint32{dumb.Handle}, Pitches: [4]uint32{dumb.Pitch}}) //nolint:gosec // scanout sizes fit
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: a %v framebuffer: %w", size, err), a.dev.DestroyDumb(dumb), target.Destroy())
	}
	release := func() error { return errors.Join(a.dev.DestroyDumb(dumb), target.Destroy()) }
	if t, ok := target.(render.AsyncReadTarget); ok && isAsync {
		c := &offLoopCopy{loop: a.loop, target: t, dumb: dumb, stale: staleRows{height: size.Y}, release: release}
		return &scanout{target: target, fb: fb, fence: noFence, painted: c.stale.add, prepareLater: c.start, release: c.releaseWhenIdle}, nil
	}
	c := &syncCopy{target: target, dumb: dumb, stale: staleRows{height: size.Y}}
	return &scanout{
		target:   target,
		fb:       fb,
		fence:    noFence,
		syncRead: true,
		painted:  c.stale.add,
		prepare:  c.copy,
		release:  release,
	}, nil
}

type syncCopy struct {
	target render.ReadTarget
	dumb   kdrm.Dumb
	stale  staleRows
}

func (c *syncCopy) copy() error {
	for _, rows := range c.stale.spans {
		if err := c.target.ReadRows(c.dumb.Pix, int(c.dumb.Pitch), rows); err != nil {
			return err
		}
	}
	c.stale.reset()
	return nil
}

type offLoopCopy struct {
	loop     *loop.Loop
	target   render.AsyncReadTarget
	dumb     kdrm.Dumb
	stale    staleRows
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
			spans := c.stale.spans
			go func() {
				row, pitch := c.target.Size().X*4, int(c.dumb.Pitch)
				for _, s := range spans {
					for y := s.From; y < s.To; y++ {
						copy(c.dumb.Pix[y*pitch:y*pitch+row], pix[y*stride:])
					}
				}
				c.post(func() error {
					c.stale.reset()
					return c.finished(done, pending.Release())
				})
			}()
			return nil
		})
	}()
	return nil
}

func noFence() (int, error) {
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
	open func(dev *kdrm.Device) (*gpuAllocator, error)
}

func stacksFor(opts Options) []namedStack {
	glesStack := namedStack{"GLES", func(dev *kdrm.Device) (*gpuAllocator, error) { return newGLESAllocator(dev, opts.Paths) }}
	if !opts.Vulkan {
		return []namedStack{glesStack}
	}
	vulkanStack := namedStack{"Vulkan", func(dev *kdrm.Device) (*gpuAllocator, error) { return newVulkanAllocator(dev, opts.Paths) }}
	return []namedStack{vulkanStack, glesStack}
}

func openLocal(dev *kdrm.Device, stacks []namedStack) (*gpuAllocator, namedStack, error) {
	var errs []error
	for _, s := range stacks {
		a, err := s.open(dev)
		if err == nil {
			return a, s, nil
		}
		errs = append(errs, err)
	}
	return nil, namedStack{}, errors.Join(errs...)
}

func secondaryAllocator(dev *kdrm.Device, primary *Backend, stacks []namedStack, canRender bool) (allocator, string) {
	cpu := &copyAllocator{dev: dev, primary: primary.Renderer()}
	p, ok := primary.alloc.(*gpuAllocator)
	if !ok {
		return cpu, cpu.named(fmt.Sprintf("a CPU copy, since the primary renders with %T, which shares no buffers", primary.alloc))
	}
	own, how := ownBuffers(dev, p, cpu, stacks, canRender)
	return linearFirstOr(dev, p, own, how)
}

func ownBuffers(dev *kdrm.Device, p *gpuAllocator, cpu *copyAllocator, stacks []namedStack, canRender bool) (allocator, string) {
	name := filepath.Base(dev.Path())
	if !canRender {
		return cpu, cpu.named("a CPU copy, since " + name + " has no render node")
	}
	local, stack, err := openLocal(dev, stacks)
	if err != nil {
		return cpu, cpu.named(fmt.Sprintf("a CPU copy, since %s cannot render: %v", name, err))
	}
	a := &blitAllocator{primary: p, local: local}
	how := "a GPU blit with " + stack.name + " on " + name
	direct := a.probe()
	throughVulkan := direct != nil
	if throughVulkan {
		if err := a.copyThroughVulkan(); err != nil {
			return cpu, cpu.named(fmt.Sprintf("a CPU copy, since the GPUs share no buffer: %v", errors.Join(direct, err, local.close())))
		}
		how += ", from a linear copy of each frame that Vulkan makes on the primary GPU"
	}
	lazy, err := newLazyBlit(a, func() (*blitAllocator, error) {
		local, err := stack.open(dev)
		if err != nil {
			return nil, err
		}
		b := &blitAllocator{primary: p, local: local}
		if throughVulkan {
			if err := b.copyThroughVulkan(); err != nil {
				return nil, errors.Join(err, local.close())
			}
		}
		return b, nil
	})
	if err != nil {
		return cpu, cpu.named(fmt.Sprintf("a CPU copy, since %s gives no formats to render: %v", name, err))
	}
	return lazy, how + ", opened only while " + name + " drives an output"
}

func linearFirstOr(dev *kdrm.Device, primary *gpuAllocator, own allocator, ownHow string) (allocator, string) {
	if err := primary.rendersLinear(); err != nil {
		return own, fmt.Sprintf("%s; %v", ownHow, err)
	}
	return &linearFirst{dev: dev, primary: primary, own: own, ownHow: ownHow}, "the primary GPU's linear buffers, or, where " + filepath.Base(dev.Path()) + " refuses them, " + ownHow
}

type staleRows struct {
	spans  []render.Rows
	height int
}

func (s *staleRows) add(damage render.Region) {
	s.spans = addRows(s.spans, damage, s.height)
}

func (s *staleRows) reset() {
	s.spans = s.spans[:0]
}

func addRows(spans []render.Rows, damage render.Region, height int) []render.Rows {
	if len(damage) == 0 {
		return append(spans[:0], render.Rows{From: 0, To: height})
	}
	for _, r := range damage {
		if from, to := max(r.Min.Y, 0), min(r.Max.Y, height); from < to {
			spans = append(spans, render.Rows{From: from, To: to})
		}
	}
	slices.SortFunc(spans, func(a, b render.Rows) int { return cmp.Compare(a.From, b.From) })
	merged := spans[:0]
	for _, s := range spans {
		if n := len(merged); n > 0 && s.From <= merged[n-1].To {
			merged[n-1].To = max(merged[n-1].To, s.To)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}
