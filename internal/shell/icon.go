package shell

import (
	"fmt"
	"image"
	"slices"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	sxti "github.com/stubbedev/lys-wm/internal/protocol/server/xdgtoplevelicon"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shm"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const iconVersion = 1

var IconSizes = []int32{16, 32, 64}

type IconImage struct {
	Scale int
	Image *image.RGBA
}

func (i IconImage) Pixels() int {
	return i.Image.Bounds().Dx()
}

type Icon struct {
	Serial uint64
	Name   string
	Images []IconImage
}

func (i *Icon) Best(px int) (IconImage, bool) {
	if i == nil || len(i.Images) == 0 {
		return IconImage{}, false
	}
	best := i.Images[0]
	for _, img := range i.Images[1:] {
		have, next := best.Pixels(), img.Pixels()
		if have < px && next > have || next >= px && next < have {
			best = img
		}
	}
	return best, true
}

func (sh *Shell) bindIcons(c *server.Client, id wire.ObjectID, version uint32) error {
	m, err := sxti.NewXdgToplevelIconManagerV1(c, id, version, iconManager{sh})
	if err != nil {
		return err
	}
	for _, size := range IconSizes {
		if err := m.SendIconSize(size); err != nil {
			return err
		}
	}
	return m.SendDone()
}

type iconManager struct{ sh *Shell }

func (iconManager) Destroy(*sxti.XdgToplevelIconManagerV1) error { return nil }

func (iconManager) CreateIcon(_ *sxti.XdgToplevelIconManagerV1, res *sxti.XdgToplevelIconV1) (sxti.XdgToplevelIconV1Handler, error) {
	b := &iconBuilder{resource: res}
	server.OnDestroy(res, func() error {
		b.destroyed = true
		return nil
	})
	return b, nil
}

func (m iconManager) SetIcon(_ *sxti.XdgToplevelIconManagerV1, top *sxdg.XdgToplevel, res *sxti.XdgToplevelIconV1) error {
	t, ok := top.Handler().(*Toplevel)
	if !ok {
		return fmt.Errorf("shell: xdg_toplevel@%d has handler %T", top.Base().ID(), top.Handler())
	}
	if res == nil {
		t.pendingIcon, t.iconSet = nil, true
		return nil
	}
	b, ok := res.Handler().(*iconBuilder)
	if !ok {
		return fmt.Errorf("shell: xdg_toplevel_icon_v1@%d has handler %T", res.Base().ID(), res.Handler())
	}
	icon, err := b.seal(&m.sh.iconSerial)
	if err != nil {
		return err
	}
	t.pendingIcon, t.iconSet = icon, true
	return nil
}

type iconBuffer struct {
	scale  int
	buffer *shm.Buffer
}

type iconBuilder struct {
	resource  *sxti.XdgToplevelIconV1
	name      string
	buffers   []iconBuffer
	sealed    *Icon
	assigned  bool
	destroyed bool
}

func (b *iconBuilder) Destroy(*sxti.XdgToplevelIconV1) error { return nil }

func (b *iconBuilder) mutable() error {
	if b.assigned {
		return b.resource.ProtocolError(sxti.XdgToplevelIconV1ErrorImmutable, "the icon was already assigned to a toplevel")
	}
	return nil
}

func (b *iconBuilder) SetName(_ *sxti.XdgToplevelIconV1, name string) error {
	if err := b.mutable(); err != nil {
		return err
	}
	b.name = name
	return nil
}

func (b *iconBuilder) AddBuffer(obj *sxti.XdgToplevelIconV1, res *sw.WlBuffer, scale int32) error {
	if err := b.mutable(); err != nil {
		return err
	}
	buf, ok := res.Handler().(*shm.Buffer)
	if !ok {
		return obj.ProtocolError(sxti.XdgToplevelIconV1ErrorInvalidBuffer, "wl_buffer@%d is not a wl_shm buffer", res.Base().ID())
	}
	if size := buf.Size(); size.X != size.Y {
		return obj.ProtocolError(sxti.XdgToplevelIconV1ErrorInvalidBuffer, "wl_buffer@%d is %v, not square", res.Base().ID(), size)
	}
	if scale < 1 {
		return obj.ProtocolError(sxti.XdgToplevelIconV1ErrorInvalidBuffer, "scale %d is below 1", scale)
	}
	server.OnDestroy(res, func() error {
		if b.destroyed {
			return nil
		}
		return obj.ProtocolError(sxti.XdgToplevelIconV1ErrorNoBuffer, "wl_buffer@%d was destroyed before its icon", res.Base().ID())
	})
	ib := iconBuffer{scale: int(scale), buffer: buf}
	b.buffers = slices.DeleteFunc(b.buffers, func(o iconBuffer) bool { return o.scale == ib.scale && o.buffer.Size() == buf.Size() })
	b.buffers = append(b.buffers, ib)
	return nil
}

func (b *iconBuilder) seal(serial *uint64) (*Icon, error) {
	if b.assigned {
		return b.sealed, nil
	}
	b.assigned = true
	if b.name == "" && len(b.buffers) == 0 {
		return nil, nil
	}
	*serial++
	icon := &Icon{Serial: *serial, Name: b.name, Images: make([]IconImage, 0, len(b.buffers))}
	for _, ib := range b.buffers {
		img, err := copyIcon(ib.buffer)
		if err != nil {
			return nil, fmt.Errorf("shell: read icon buffer: %w", err)
		}
		icon.Images = append(icon.Images, IconImage{Scale: ib.scale, Image: img})
	}
	b.sealed = icon
	return icon, nil
}

func copyIcon(b *shm.Buffer) (*image.RGBA, error) {
	size := b.Size()
	img := image.NewRGBA(image.Rectangle{Max: size})
	opaque := b.Format() == render.XRGB8888
	err := b.Read(func(data []byte, stride int) error {
		for y := range size.Y {
			src := data[y*stride : y*stride+4*size.X]
			dst := img.Pix[y*img.Stride : y*img.Stride+4*size.X]
			for x := 0; x < len(src); x += 4 {
				dst[x], dst[x+1], dst[x+2], dst[x+3] = src[x+2], src[x+1], src[x], src[x+3]
				if opaque {
					dst[x+3] = 0xff
				}
			}
		}
		return nil
	})
	return img, err
}
