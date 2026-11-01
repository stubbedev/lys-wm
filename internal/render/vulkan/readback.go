package vulkan

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/fourcc"
	lgpu "github.com/stubbedev/lys-lib/gpu"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
)

type readback struct {
	d      *lgpu.Device
	buffer *lgpu.Buffer
	linear *lgpu.Linear
	fd     int
	stride int
	pix    []byte
}

func newReadback(d *lgpu.Device, size image.Point) (*readback, error) {
	b, err := d.NewBuffer(size.X, size.Y, fourcc.ARGB8888, []fourcc.Modifier{fourcc.Linear})
	if err != nil {
		return nil, fmt.Errorf("vulkan: a linear %v buffer to read back into: %w", size, err)
	}
	rb := &readback{d: d, buffer: b}
	m := b.Dmabuf()
	if len(m.Planes) != 1 {
		rb.close()
		return nil, fmt.Errorf("vulkan: a linear %v buffer of %d planes", size, len(m.Planes))
	}
	p := m.Planes[0]
	rb.fd, rb.stride = p.FD, int(p.Stride)
	if rb.linear, err = d.ImportLinear(m); err != nil {
		rb.close()
		return nil, fmt.Errorf("vulkan: copy into the linear %v buffer: %w", size, err)
	}
	mapped, err := unix.Mmap(rb.fd, int64(p.Offset), rb.stride*size.Y, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		rb.close()
		return nil, fmt.Errorf("vulkan: map the linear %v buffer: %w", size, err)
	}
	rb.pix = mapped
	return rb, nil
}

func (rb *readback) close() {
	if rb.pix != nil {
		if err := unix.Munmap(rb.pix); err != nil {
			panic(fmt.Sprintf("vulkan: unmap a readback buffer: %v", err))
		}
		rb.pix = nil
	}
	if rb.linear != nil {
		rb.linear.Close()
	}
	rb.buffer.Close()
}

func (r *Renderer) AsyncReads() bool {
	if !r.asyncProbed {
		r.asyncProbed = true
		r.asyncReads = r.probeAsyncReads()
	}
	return r.asyncReads
}

func (r *Renderer) probeAsyncReads() bool {
	if !r.d.Exports() || !r.d.SyncFiles() {
		return false
	}
	rb, err := newReadback(r.d, image.Pt(1, 1))
	if err != nil {
		return false
	}
	rb.close()
	return true
}

func (r *Renderer) NewAsyncTarget(size image.Point) (render.AsyncReadTarget, error) {
	if !r.AsyncReads() {
		return nil, fmt.Errorf("%w: %s reads targets back only synchronously", gpu.ErrUnsupported, r.d.Name())
	}
	t, err := r.d.NewOffscreen(size.X, size.Y)
	if err != nil {
		return nil, err
	}
	return &Target{r: r, t: t}, nil
}

type pendingRead struct {
	t      *Target
	rb     *readback
	fence  int
	mapped bool
}

func (t *Target) ReadAsync() (render.PendingRead, error) {
	if t.reading != nil {
		return nil, fmt.Errorf("vulkan: a read of the %v target is still pending", t.Size())
	}
	if !t.r.AsyncReads() {
		return nil, fmt.Errorf("%w: %s reads targets back only synchronously", gpu.ErrUnsupported, t.r.d.Name())
	}
	if t.back == nil {
		rb, err := newReadback(t.r.d, t.Size())
		if err != nil {
			return nil, err
		}
		t.back = rb
	}
	if err := t.r.d.CopyToLinear(t.back.linear, t.t, []image.Rectangle{{Max: t.Size()}}); err != nil {
		return nil, fmt.Errorf("vulkan: copy the %v target into its readback buffer: %w", t.Size(), err)
	}
	fence, err := t.r.d.SyncFile()
	if err != nil {
		return nil, fmt.Errorf("vulkan: fence a read of the %v target: %w", t.Size(), err)
	}
	t.reading = &pendingRead{t: t, rb: t.back, fence: fence}
	return t.reading, nil
}

func (p *pendingRead) Wait() error {
	if p.fence < 0 {
		return nil
	}
	fds := []unix.PollFd{{Fd: int32(p.fence), Events: unix.POLLIN}} //nolint:gosec // file descriptors fit int32
	for {
		_, err := unix.Poll(fds, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("vulkan: wait for a pending read: %w", err)
		}
		return nil
	}
}

func (p *pendingRead) Map() ([]byte, int, error) {
	switch {
	case p.mapped:
		return nil, 0, errors.New("vulkan: a pending read is already mapped")
	case p.t == nil:
		return nil, 0, errors.New("vulkan: the target of a pending read was destroyed")
	}
	if err := gpu.CPUAccess(p.rb.fd, gpu.SyncRead); err != nil {
		return nil, 0, err
	}
	p.mapped = true
	return p.rb.pix, p.rb.stride, nil
}

func (p *pendingRead) Release() error {
	var errs []error
	if p.mapped && p.t != nil {
		errs = append(errs, gpu.CPUAccess(p.rb.fd, gpu.SyncRead|gpu.SyncEnd))
	}
	p.mapped = false
	if p.fence >= 0 {
		errs = append(errs, unix.Close(p.fence))
		p.fence = -1
	}
	if p.t != nil {
		p.t.reading = nil
		p.t = nil
	}
	return errors.Join(errs...)
}
