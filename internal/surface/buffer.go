package surface

import (
	"fmt"
	"image"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
)

type Buffer interface {
	Size() image.Point
	Lock()
	Unlock() error
}

type Backing struct {
	resource *sw.WlBuffer
	release  func() error
	locks    int
	orphaned bool
}

func NewBacking(res *sw.WlBuffer, release func() error) *Backing {
	b := &Backing{resource: res, release: release}
	server.OnDestroy(res, b.orphan)
	return b
}

func (b *Backing) Lock() {
	b.locks++
}

func (b *Backing) Unlock() error {
	if b.locks == 0 {
		return fmt.Errorf("surface: unlock wl_buffer@%d, which no surface state holds", b.resource.Base().ID())
	}
	b.locks--
	return b.settle()
}

func (b *Backing) orphan() error {
	b.orphaned = true
	return b.settle()
}

func (b *Backing) settle() error {
	if b.locks > 0 || !b.orphaned {
		return nil
	}
	return b.release()
}

func unlock(b Buffer) error {
	if b == nil {
		return nil
	}
	return b.Unlock()
}
