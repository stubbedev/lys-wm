// Package singlepixel implements wp_single_pixel_buffer_manager_v1: one-pixel
// buffers of a solid premultiplied color.
package singlepixel

import (
	"image"

	sspb "github.com/stubbedev/lys-wm/internal/protocol/server/singlepixelbuffer"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

func New(d *server.Display) error {
	d.OnPrepareHandover(emit)
	_, err := d.AddGlobal(sspb.WpSinglePixelBufferManagerV1Interface, version, bind)
	return err
}

func bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sspb.NewWpSinglePixelBufferManagerV1(c, id, v, manager{})
	return err
}

type manager struct{}

func (manager) Destroy(*sspb.WpSinglePixelBufferManagerV1) error {
	return nil
}

func channel(v uint32) byte {
	return byte((uint64(v)*0xff + 0x7fffffff) / 0xffffffff) //nolint:gosec // at most 0xff for any uint32
}

func (manager) CreateU32RgbaBuffer(_ *sspb.WpSinglePixelBufferManagerV1, res *sw.WlBuffer, r, g, b, a uint32) (sw.WlBufferHandler, error) {
	return &Buffer{resource: res, pixel: [4]byte{channel(b), channel(g), channel(r), channel(a)}}, nil
}

type Buffer struct {
	resource *sw.WlBuffer
	pixel    [4]byte
}

func (b *Buffer) Size() image.Point {
	return image.Pt(1, 1)
}

func (b *Buffer) Pixel() []byte {
	return b.pixel[:]
}

func (b *Buffer) Destroy(*sw.WlBuffer) error {
	return nil
}

func (b *Buffer) Lock() {}

func (b *Buffer) Unlock() error {
	return nil
}

func (b *Buffer) Release() error {
	return b.resource.SendRelease()
}

func emit(c *server.Client, st *server.Stream) error {
	for _, r := range c.Objects() {
		res, ok := r.(*sw.WlBuffer)
		if !ok {
			continue
		}
		b, ok := res.Handler().(*Buffer)
		if !ok {
			continue
		}
		manager, err := st.Global(sspb.WpSinglePixelBufferManagerV1Interface)
		if err != nil {
			return err
		}
		e, err := st.Request(manager, sspb.WpSinglePixelBufferManagerV1Interface, "create_u32_rgba_buffer")
		if err != nil {
			return err
		}
		e.Object(res.Base().ID())
		e.Uint(uint32(b.pixel[2]) * 0x01010101)
		e.Uint(uint32(b.pixel[1]) * 0x01010101)
		e.Uint(uint32(b.pixel[0]) * 0x01010101)
		e.Uint(uint32(b.pixel[3]) * 0x01010101)
		if err := e.End(); err != nil {
			return err
		}
		st.Emitted(res)
	}
	return nil
}

func (b *Buffer) ResourceID() (wire.ObjectID, bool) {
	return b.resource.Base().ID(), !b.resource.Base().Removed()
}
