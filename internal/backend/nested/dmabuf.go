package nested

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	ld "github.com/stubbedev/lys-wm/internal/protocol/client/linuxdmabuf"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wlclient"
	"github.com/stubbedev/lys-wm/internal/wlclient/feedback"
)

const (
	dmabufVersion    = 4
	dmabufMinVersion = 3
	dmabufBuffers    = 2
	dmabufFormat     = fourcc.XRGB8888
	dmabufHostName   = "zwp_linux_dmabuf_v1"
)

type sharedBuffer struct {
	target render.Target
	dmabuf gpu.DMABUF
	free   func() error
}

type gpuRenderer interface {
	render.Renderer
	Finish() error
}

type dmabufStack interface {
	renderer() gpuRenderer
	modifiers(host feedback.Feedback) ([]fourcc.Modifier, error)
	allocate(size image.Point, modifiers []fourcc.Modifier) (sharedBuffer, error)
	close() error
}

type openStack func(main uint64) (dmabufStack, error)

type dmabufChain struct {
	linux     *ld.ZwpLinuxDmabufV1
	stack     dmabufStack
	modifiers []fourcc.Modifier
}

func hostFormats(host *wlclient.Display, globals registry, reg *cw.WlRegistry) (*ld.ZwpLinuxDmabufV1, feedback.Feedback, error) {
	g, ok := globals[dmabufHostName]
	if !ok || g.version < dmabufMinVersion {
		return nil, feedback.Feedback{}, fmt.Errorf("nested: rendering on a GPU needs the host's %s version %d", dmabufHostName, dmabufMinVersion)
	}
	events := &dmabufEvents{}
	linux, err := ld.NewZwpLinuxDmabufV1(host, min(g.version, dmabufVersion), events)
	if err != nil {
		return nil, feedback.Feedback{}, err
	}
	if err := reg.Bind(g.name, linux); err != nil {
		return nil, feedback.Feedback{}, err
	}
	if g.version >= dmabufVersion {
		fb, err := feedback.Default(host, linux)
		if err != nil {
			return nil, feedback.Feedback{}, errors.Join(fmt.Errorf("nested: %w", err), linux.Destroy())
		}
		return linux, fb, nil
	}
	if err := host.Roundtrip(); err != nil {
		return nil, feedback.Feedback{}, errors.Join(fmt.Errorf("nested: list the host's dmabuf modifiers: %w", err), linux.Destroy())
	}
	return linux, feedback.Feedback{Tranches: []feedback.Tranche{{Formats: events.formats}}}, nil
}

func newDMABUFChain(host *wlclient.Display, globals registry, reg *cw.WlRegistry, open openStack) (*dmabufChain, error) {
	linux, fb, err := hostFormats(host, globals, reg)
	if err != nil {
		return nil, err
	}
	stack, err := open(fb.Main)
	if err != nil {
		return nil, errors.Join(err, linux.Destroy())
	}
	modifiers, err := stack.modifiers(fb)
	if err != nil {
		return nil, errors.Join(err, stack.close(), linux.Destroy())
	}
	slices.Sort(modifiers)
	return &dmabufChain{linux: linux, stack: stack, modifiers: modifiers}, nil
}

func hostImports(host feedback.Feedback, rendered []fourcc.Modifier) []fourcc.Modifier {
	return slices.DeleteFunc(slices.Clone(rendered), func(m fourcc.Modifier) bool {
		return !host.Imports(gpu.FormatModifier{Format: dmabufFormat, Modifier: m})
	})
}

type glesStack struct {
	fd    int
	stack *gles.Stack
}

func openGLES(paths gpu.Paths) openStack {
	return func(main uint64) (dmabufStack, error) {
		if main == 0 {
			return nil, fmt.Errorf("nested: the GLES renderer needs the device the host names through %s version %d", dmabufHostName, dmabufVersion)
		}
		node, err := feedback.RenderNode(main)
		if err != nil {
			return nil, fmt.Errorf("nested: the host's renderer: %w", err)
		}
		fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("nested: open %s: %w", node, err)
		}
		stack, err := gles.OnGBM(fd, paths)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("nested: GLES on %s: %w", node, err), unix.Close(fd))
		}
		return &glesStack{fd: fd, stack: stack}, nil
	}
}

func (s *glesStack) renderer() gpuRenderer {
	return s.stack.Renderer
}

func (s *glesStack) modifiers(host feedback.Feedback) ([]fourcc.Modifier, error) {
	rendered, err := s.stack.Renderer.RenderableModifiers(dmabufFormat)
	if err != nil {
		return nil, err
	}
	shared := hostImports(host, rendered)
	if len(shared) == 0 && !host.Imports(gpu.FormatModifier{Format: dmabufFormat, Modifier: fourcc.Invalid}) {
		return nil, fmt.Errorf("nested: the host and %s share no %v modifier", s.stack.GBM.Backend(), dmabufFormat)
	}
	return shared, nil
}

func (s *glesStack) allocate(size image.Point, modifiers []fourcc.Modifier) (sharedBuffer, error) {
	bo, err := s.stack.GBM.Allocate(size, dmabufFormat, modifiers, gpu.UseRendering)
	if err != nil {
		return sharedBuffer{}, err
	}
	target, err := s.stack.Renderer.ImportFramebuffer(bo.DMABUF)
	if err != nil {
		return sharedBuffer{}, errors.Join(err, bo.Close())
	}
	return sharedBuffer{target: target, dmabuf: bo.DMABUF, free: func() error { return errors.Join(target.Destroy(), bo.Close()) }}, nil
}

func (s *glesStack) close() error {
	return errors.Join(s.stack.Close(), unix.Close(s.fd))
}

type vulkanStack struct {
	r *vulkan.Renderer
}

func openVulkan(main uint64) (dmabufStack, error) {
	var (
		r   *vulkan.Renderer
		err error
	)
	if main != 0 {
		r, err = vulkan.Open(main)
	} else {
		r, err = vulkan.OpenDefault()
	}
	if err != nil {
		return nil, fmt.Errorf("nested: %w", err)
	}
	return &vulkanStack{r: r}, nil
}

func (s *vulkanStack) renderer() gpuRenderer {
	return s.r
}

func (s *vulkanStack) modifiers(host feedback.Feedback) ([]fourcc.Modifier, error) {
	if !s.r.Device().Exports() {
		return nil, fmt.Errorf("nested: %s shares no dmabufs with the host", s.r.Device().Name())
	}
	rendered, err := s.r.ExportModifiers(dmabufFormat)
	if err != nil {
		return nil, fmt.Errorf("nested: %s: %w", s.r.Device().Name(), err)
	}
	shared := hostImports(host, rendered)
	if len(shared) == 0 {
		return nil, fmt.Errorf("nested: the host imports %v in modifiers %v, %s renders it in %v, none of them shared", dmabufFormat, host.Modifiers(dmabufFormat), s.r.Device().Name(), rendered)
	}
	return shared, nil
}

func (s *vulkanStack) allocate(size image.Point, modifiers []fourcc.Modifier) (sharedBuffer, error) {
	target, d, err := s.r.NewExportedTarget(size, dmabufFormat, modifiers)
	if err != nil {
		return sharedBuffer{}, err
	}
	return sharedBuffer{target: target, dmabuf: d, free: target.Destroy}, nil
}

func (s *vulkanStack) close() error {
	return s.r.Close()
}

func (c *dmabufChain) renderer() render.Renderer {
	return c.stack.renderer()
}

func (c *dmabufChain) finish() error {
	return c.stack.renderer().Finish()
}

func (c *dmabufChain) allocate(o *Output, size image.Point) ([]*buffer, func() error, error) {
	var shared []sharedBuffer
	free := func() error {
		errs := make([]error, 0, len(shared))
		for _, s := range shared {
			errs = append(errs, s.free())
		}
		return errors.Join(errs...)
	}
	buffers := make([]*buffer, dmabufBuffers)
	for i := range buffers {
		s, err := c.stack.allocate(size, c.modifiers)
		if err != nil {
			return nil, nil, errors.Join(err, free())
		}
		shared = append(shared, s)
		buf := &buffer{target: s.target}
		if buf.wl, err = c.hostBuffer(o, buf, s.dmabuf); err != nil {
			return nil, nil, errors.Join(err, free())
		}
		buffers[i] = buf
	}
	return buffers, free, nil
}

func (c *dmabufChain) hostBuffer(o *Output, buf *buffer, d gpu.DMABUF) (*cw.WlBuffer, error) {
	params, err := c.linux.CreateParams(paramsEvents{})
	if err != nil {
		return nil, err
	}
	hi, lo := wire.Halves(uint64(d.Modifier))
	for i, p := range d.Planes {
		if err := params.Add(p.FD, uint32(i), p.Offset, p.Stride, hi, lo); err != nil {
			return nil, err
		}
	}
	wl, err := params.CreateImmed(bufferEvents{o, buf}, int32(d.Size.X), int32(d.Size.Y), uint32(d.Format), 0) //nolint:gosec // buffer sizes fit the protocol
	if err != nil {
		return nil, err
	}
	return wl, params.Destroy()
}

func (c *dmabufChain) close() error {
	return errors.Join(c.stack.close(), c.linux.Destroy())
}

type dmabufEvents struct {
	formats []gpu.FormatModifier
}

func (*dmabufEvents) Format(*ld.ZwpLinuxDmabufV1, uint32) error { return nil }

func (e *dmabufEvents) Modifier(_ *ld.ZwpLinuxDmabufV1, format, hi, lo uint32) error {
	e.formats = append(e.formats, gpu.FormatModifier{Format: fourcc.Format(format), Modifier: fourcc.Modifier(uint64(hi)<<32 | uint64(lo))})
	return nil
}

type paramsEvents struct{}

func (paramsEvents) Created(*ld.ZwpLinuxBufferParamsV1, *cw.WlBuffer) (cw.WlBufferHandler, error) {
	return nil, errors.New("nested: the host announced a buffer lys-wm created with create_immed")
}

func (paramsEvents) Failed(*ld.ZwpLinuxBufferParamsV1) error {
	return errors.New("nested: the host rejected a dmabuf buffer")
}
