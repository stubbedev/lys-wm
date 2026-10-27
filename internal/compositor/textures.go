package compositor

import (
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"

	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shm"
	"github.com/stubbedev/lys-wm/internal/singlepixel"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/syncobj"
)

type texture interface {
	texture() render.Texture
	opaque() bool
}

type uploaded struct {
	tex    render.Texture
	format render.Format
}

type imported struct {
	tex    render.Texture
	buffer *dmabuf.Buffer
}

func (t uploaded) texture() render.Texture { return t.tex }

func (t uploaded) opaque() bool { return t.format == render.XRGB8888 }

func (t imported) texture() render.Texture { return t.tex }

func (t imported) opaque() bool { return !t.buffer.DMABUF().Format.HasAlpha() }

type dmabufRenderer interface {
	dmabuf.Importer
	ImportTexture(b gpu.DMABUF) (render.Texture, error)
}

type syncRenderer interface {
	syncobj.Fencer
	RenderNode() (string, error)
	NativeFences() bool
}

func (c *Compositor) Committed(s *surface.Surface) error {
	c.cursorCommitted(s)
	if d := s.Damage(); !d.Empty() {
		for _, sc := range c.scenes {
			sc.damaged(s, d)
		}
	}
	if s.Attached() {
		if err := c.attach(s); err != nil {
			return err
		}
	}
	return errors.Join(c.noteCommit(s), c.updateCursorSessions(), c.scheduleAll())
}

func (c *Compositor) attach(s *surface.Surface) error {
	switch b := s.Buffer().(type) {
	case nil:
		return c.dropTexture(s)
	case *shm.Buffer:
		if err := c.upload(s, b); err != nil {
			return err
		}
		return b.Release()
	case *dmabuf.Buffer:
		if t, ok := c.textures[s].(imported); ok && t.buffer == b {
			return nil
		}
		tex, ok := c.imported[b]
		if !ok {
			var err error
			d := b.DMABUF()
			if d.Format.YUV() {
				d.YUV = gpu.DefaultYUV(d.Size)
			}
			if tex, err = c.importer.ImportTexture(d); err != nil {
				return fmt.Errorf("compositor: import the dmabuf of wl_surface@%d: %w", s.Resource().Base().ID(), err)
			}
			c.imported[b] = tex
		}
		if err := c.dropTexture(s); err != nil {
			return err
		}
		c.textures[s] = imported{tex, b}
		return nil
	case *singlepixel.Buffer:
		tex, err := c.renderer.NewTexture(render.ARGB8888, b.Size(), 4, b.Pixel())
		if err != nil {
			return fmt.Errorf("compositor: upload single-pixel wl_surface@%d: %w", s.Resource().Base().ID(), err)
		}
		if err := c.dropTexture(s); err != nil {
			return errors.Join(err, tex.Destroy())
		}
		c.textures[s] = uploaded{tex, render.ARGB8888}
		return b.Release()
	default:
		return fmt.Errorf("compositor: no upload path for buffers of type %T", b)
	}
}

func (c *Compositor) upload(s *surface.Surface, b *shm.Buffer) error {
	full := image.Rectangle{Max: b.Size()}
	t, ok := c.textures[s].(uploaded)
	update := ok && t.format == b.Format() && t.tex.Size() == b.Size()
	c.stagedRects = append(c.stagedRects[:0], full)
	if update {
		c.stagedRects = c.stagedRects[:0]
		for _, r := range s.BufferDamage().Rects() {
			if r = r.Intersect(full); !r.Empty() {
				c.stagedRects = append(c.stagedRects, r)
			}
		}
	}
	if err := c.stage(b); err != nil {
		return err
	}
	if !update {
		tex, err := c.renderer.NewTexture(b.Format(), b.Size(), full.Dx()*4, c.staged)
		if err != nil {
			return fmt.Errorf("compositor: upload wl_surface@%d: %w", s.Resource().Base().ID(), err)
		}
		if err := c.dropTexture(s); err != nil {
			return errors.Join(err, tex.Destroy())
		}
		c.textures[s] = uploaded{tex, b.Format()}
		return nil
	}
	off := 0
	for _, r := range c.stagedRects {
		if err := t.tex.Update(r, r.Dx()*4, c.staged[off:]); err != nil {
			return fmt.Errorf("compositor: upload wl_surface@%d: %w", s.Resource().Base().ID(), err)
		}
		off += r.Dx() * r.Dy() * 4
	}
	return nil
}

func (c *Compositor) stage(b *shm.Buffer) error {
	n := 0
	for _, r := range c.stagedRects {
		n += r.Dx() * r.Dy() * 4
	}
	if cap(c.staged) < n {
		c.staged = make([]byte, n)
	}
	c.staged = c.staged[:n]
	return b.Read(func(data []byte, stride int) error {
		off := 0
		for _, r := range c.stagedRects {
			row := r.Dx() * 4
			for y := r.Min.Y; y < r.Max.Y; y++ {
				at := y*stride + r.Min.X*4
				copy(c.staged[off:off+row], data[at:at+row])
				off += row
			}
		}
		return nil
	})
}

func (c *Compositor) dropTexture(s *surface.Surface) error {
	t, ok := c.textures[s]
	if !ok {
		return nil
	}
	delete(c.textures, s)
	if t, ok := t.(imported); ok {
		return c.releaseBuffer(t.buffer, s.Client())
	}
	if err := c.destroyTexture(t.texture()); err != nil {
		return fmt.Errorf("compositor: destroy the texture of wl_surface@%d: %w", s.Resource().Base().ID(), err)
	}
	return nil
}

func (c *Compositor) BufferDestroyed(b *dmabuf.Buffer) error {
	c.scanoutBufferGone(b)
	shown := false
	for s, t := range c.textures {
		if t, ok := t.(imported); ok && t.buffer == b {
			delete(c.textures, s)
			shown = true
		}
	}
	var errs []error
	if tex, ok := c.imported[b]; ok {
		delete(c.imported, b)
		errs = append(errs, c.destroyTexture(tex))
	}
	if shown {
		errs = append(errs, c.scheduleAll())
	}
	return errors.Join(errs...)
}

func (c *Compositor) opaque(s *surface.Surface) bool {
	t, ok := c.textures[s]
	return ok && t.opaque() || s.Opaque().Covers(image.Rectangle{Max: s.Size()})
}

func (c *Compositor) textureOf(s *surface.Surface) render.Texture {
	if t, ok := c.textures[s]; ok {
		return t.texture()
	}
	return nil
}

func (c *Compositor) Destroyed(s *surface.Surface) error {
	if err := c.seat.SurfaceDestroyed(s); err != nil {
		return err
	}
	if err := c.dropTexture(s); err != nil {
		return err
	}
	for _, sc := range c.scenes {
		sc.forget(s)
	}
	for o, shown := range c.shown {
		c.shown[o] = slices.DeleteFunc(shown, func(x *surface.Surface) bool { return x == s })
	}
	c.entered = slices.DeleteFunc(c.entered, func(e *entered) bool { return e.s == s })
	maps.DeleteFunc(c.filled, func(_ *output.Output, f filling) bool { return f.surface == s })
	return nil
}

func (c *Compositor) destroyTexture(t render.Texture) error {
	var errs []error
	for o, sc := range c.scenes {
		if sc.stale || !sc.draws(t) {
			continue
		}
		sc.stale = true
		if c.outputLive(o) {
			errs = append(errs, c.schedule(o))
		}
	}
	return errors.Join(append(errs, t.Destroy())...)
}
