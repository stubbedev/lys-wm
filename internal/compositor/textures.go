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
	"github.com/stubbedev/lys-wm/internal/seat"
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
	size   image.Point
}

type importedTexture struct {
	tex  render.Texture
	yuv  gpu.YUVEncoding
	left func() error
}

type imported struct {
	tex    render.Texture
	buffer *dmabuf.Buffer
}

func (t uploaded) texture() render.Texture { return t.tex }

func (t uploaded) opaque() bool { return t.format.Opaque() }

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
	if c.lagging {
		c.lagging, c.layoutDirty = false, true
		if err := c.scheduleAll(); err != nil {
			return err
		}
	}
	_, drawable := c.textures[s]
	if s.Attached() {
		if err := c.attach(s); err != nil {
			return err
		}
	}
	_, stillDrawable := c.textures[s]
	root := s.Root()
	reach := c.footprintMoved(s, root) || drawable != stillDrawable
	err := errors.Join(c.noteCommit(s), c.updateCursorSessions())
	d := s.Damage()
	if c.overview != nil && (len(d) > 0 || reach) {
		c.overviewCommitted(root)
	}
	if c.keptBackdrop && c.overview == nil {
		err = errors.Join(err, c.backdropCommitted(root))
	}
	for _, o := range c.outputs {
		shows := showsTree(c.shown[o], root)
		if shows && len(d) > 0 {
			c.sceneOf(o).damaged(s, d)
			hintDamage(o, d)
		}
		if !shows && !reach {
			continue
		}
		if !reach && c.sceneOf(o).culled[s] {
			continue
		}
		if e := c.schedule(o); e != nil {
			err = errors.Join(err, e)
		}
	}
	if !c.drawnAnywhere(s) {
		err = errors.Join(err, c.throttleFrames(s))
	}
	return err
}

type placedRole interface {
	Placement() image.Rectangle
}

type footprint struct {
	bounds, placement image.Rectangle
}

func (c *Compositor) footprintMoved(s, root *surface.Surface) bool {
	role, placed := root.Role().(placedRole)
	if !placed {
		return true
	}
	now := footprint{root.TreeBounds(), role.Placement()}
	was, seen := c.footprints[root]
	if seen && was == now {
		return s.Offset() != (image.Point{})
	}
	c.footprints[root] = now
	return true
}

func showsTree(shown []*surface.Surface, root *surface.Surface) bool {
	for _, s := range shown {
		if s.Root() == root {
			return true
		}
	}
	return false
}

func (c *Compositor) attach(s *surface.Surface) error {
	if _, ok := s.Buffer().(*shm.Buffer); !ok {
		delete(c.cursorPixels, s)
	}
	switch b := s.Buffer().(type) {
	case nil:
		return errors.Join(c.keepSnapshot(s), c.dropTexture(s))
	case *shm.Buffer:
		if err := c.upload(s, b); err != nil {
			return err
		}
		return b.Release()
	case *dmabuf.Buffer:
		d := b.DMABUF()
		if d.Format.YUV() {
			d.YUV = c.colorrep.Of(s)
			if d.YUV.Coefficients == gpu.CoefficientsUnspecified {
				def := gpu.DefaultYUV(d.Size)
				d.YUV.Coefficients, d.YUV.Range = def.Coefficients, def.Range
			}
		}
		cached, ok := c.imported[b]
		if ok && cached.yuv == d.YUV {
			if t, shown := c.textures[s].(imported); shown && t.buffer == b {
				return nil
			}
		}
		if ok && cached.yuv != d.YUV {
			delete(c.imported, b)
			if err := c.destroyTexture(cached.tex); err != nil {
				return err
			}
			ok = false
		}
		tex := cached.tex
		if !ok {
			var err error
			if tex, err = c.importer.ImportTexture(d); err != nil {
				return fmt.Errorf("compositor: import the dmabuf of wl_surface@%d: %w", s.Resource().Base().ID(), err)
			}
			c.imported[b] = importedTexture{tex: tex, yuv: d.YUV, left: func() error { return c.leftScreen(b) }}
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
		c.textures[s] = uploaded{tex, render.ARGB8888, b.Size()}
		return b.Release()
	default:
		return fmt.Errorf("compositor: no upload path for buffers of type %T", b)
	}
}

const (
	textureRoundUp    = 128
	textureMostUnused = 4
)

func textureHolds(tex, size image.Point) bool {
	return size.In(image.Rectangle{Max: tex.Add(image.Pt(1, 1))}) && tex.X*tex.Y <= textureMostUnused*size.X*size.Y
}

func roomyTextureSize(size image.Point) image.Point {
	return image.Pt((size.X+textureRoundUp-1)/textureRoundUp*textureRoundUp, (size.Y+textureRoundUp-1)/textureRoundUp*textureRoundUp)
}

func (c *Compositor) upload(s *surface.Surface, b *shm.Buffer) error {
	full := image.Rectangle{Max: b.Size()}
	t, ok := c.textures[s].(uploaded)
	reusable := ok && t.format == b.Format() && textureHolds(t.tex.Size(), b.Size())
	damageOnly := reusable && t.size == b.Size()
	c.uploadRects = append(c.uploadRects[:0], full)
	if damageOnly {
		c.uploadRects = c.uploadRects[:0]
		for _, r := range s.BufferDamage() {
			if r = r.Intersect(full); !r.Empty() {
				c.uploadRects = append(c.uploadRects, r)
			}
		}
	}
	return b.Read(func(data []byte, stride int) error {
		if seat.IsCursorSurface(s) {
			c.keepCursorPixels(s, b.Format(), b.Size(), data, stride)
		}
		if reusable {
			t.size = b.Size()
			c.textures[s] = t
			return c.updateTexture(t, s, data, stride)
		}
		var tex render.Texture
		var err error
		if ok {
			if tex, err = c.renderer.NewBlankTexture(b.Format(), roomyTextureSize(b.Size())); err == nil {
				err = updateRects(tex, b.Size(), c.uploadRects, data, stride)
			}
		} else {
			tex, err = c.renderer.NewTexture(b.Format(), b.Size(), stride, data)
		}
		if err != nil {
			return fmt.Errorf("compositor: upload wl_surface@%d: %w", s.Resource().Base().ID(), err)
		}
		if err := c.dropTexture(s); err != nil {
			return errors.Join(err, tex.Destroy())
		}
		c.textures[s] = uploaded{tex, b.Format(), b.Size()}
		return nil
	})
}

func (c *Compositor) updateTexture(t uploaded, s *surface.Surface, data []byte, stride int) error {
	if err := updateRects(t.tex, t.size, c.uploadRects, data, stride); err != nil {
		return fmt.Errorf("compositor: upload wl_surface@%d: %w", s.Resource().Base().ID(), err)
	}
	return nil
}

func updateRects(tex render.Texture, size image.Point, rects []image.Rectangle, data []byte, stride int) error {
	room := tex.Size()
	for _, r := range rects {
		if err := tex.Update(r, stride, data[r.Min.Y*stride+r.Min.X*4:]); err != nil {
			return err
		}
		if r.Max.X == size.X && room.X > size.X {
			if err := tex.Update(image.Rect(size.X, r.Min.Y, size.X+1, r.Max.Y), stride, data[r.Min.Y*stride+(size.X-1)*4:]); err != nil {
				return err
			}
		}
		if r.Max.Y == size.Y && room.Y > size.Y {
			if err := tex.Update(image.Rect(r.Min.X, size.Y, r.Max.X, size.Y+1), stride, data[(size.Y-1)*stride+r.Min.X*4:]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Compositor) keepCursorPixels(s *surface.Surface, format render.Format, size image.Point, data []byte, stride int) {
	cp := c.cursorPixels[s]
	if cp == nil || cp.size != size {
		cp = &cursorPixels{pix: make([]byte, size.X*size.Y*4), size: size}
		c.cursorPixels[s] = cp
	}
	for _, r := range c.uploadRects {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			dst := cp.pix[(y*size.X+r.Min.X)*4 : (y*size.X+r.Max.X)*4]
			copy(dst, data[y*stride+r.Min.X*4:])
			format.DecodeRow(dst)
		}
	}
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
	errs := []error{c.scanoutBufferGone(b)}
	shown := false
	for s, t := range c.textures {
		if t, ok := t.(imported); ok && t.buffer == b {
			delete(c.textures, s)
			shown = true
		}
	}
	if cached, ok := c.imported[b]; ok {
		delete(c.imported, b)
		errs = append(errs, c.destroyTexture(cached.tex))
	}
	if t, ok := c.captureTargets[b]; ok {
		delete(c.captureTargets, b)
		errs = append(errs, t.Destroy())
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
	if c.overview != nil {
		c.overviewForget(s)
	}
	if err := c.seat.SurfaceDestroyed(s); err != nil {
		return err
	}
	if err := c.dropTexture(s); err != nil {
		return err
	}
	delete(c.footprints, s)
	delete(c.cursorPixels, s)
	delete(c.hidden.waiting, s)
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
