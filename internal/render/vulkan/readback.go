package vulkan

import (
	"errors"
	"fmt"
	"image"

	lgpu "github.com/stubbedev/lys-lib/gpu"

	"github.com/stubbedev/lys-wm/internal/render"
)

func (r *Renderer) AsyncReads() bool {
	return true
}

func (r *Renderer) NewAsyncTarget(size image.Point) (render.AsyncReadTarget, error) {
	return r.buildTarget(&Target{r: r, kind: offscreenTarget, size: size})
}

type pendingRead struct {
	t        *Target
	rb       *lgpu.Readback
	mapped   bool
	released bool
}

func (t *Target) ReadAsync() (render.PendingRead, error) {
	if t.reading != nil {
		return nil, fmt.Errorf("vulkan: a read of the %v target is still pending", t.Size())
	}
	if t.r.lost != nil {
		return render.NewLostRead(t.size), nil
	}
	if t.back == nil {
		size := t.Size()
		rb, err := t.r.d.NewReadback(size.X, size.Y)
		if err != nil {
			return nil, err
		}
		t.back = rb
	}
	if err := t.back.Copy(t.t); err != nil {
		return nil, fmt.Errorf("vulkan: copy the %v target into its readback: %w", t.Size(), err)
	}
	t.reading = &pendingRead{t: t, rb: t.back}
	return t.reading, nil
}

func (p *pendingRead) Wait() error {
	return p.rb.Wait()
}

func (p *pendingRead) Map() ([]byte, int, error) {
	switch {
	case p.mapped:
		return nil, 0, errors.New("vulkan: a pending read is already mapped")
	case p.t == nil:
		return nil, 0, errors.New("vulkan: the target of a pending read was destroyed")
	}
	pix, stride, err := p.rb.Pixels()
	if err != nil {
		return nil, 0, err
	}
	p.mapped = true
	return pix, stride, nil
}

func (p *pendingRead) Release() error {
	if p.released {
		return nil
	}
	p.released, p.mapped = true, false
	if p.t == nil {
		p.rb.Close()
		return nil
	}
	p.t.reading = nil
	p.t = nil
	return nil
}
