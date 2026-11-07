package compositor

import (
	"errors"
	"image"

	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) contentOf(s *surface.Surface) (wm.ContentType, wm.ContentSource) {
	if w := c.windowWithSurface(s); w != nil {
		if t := c.decision(w).Content; t != nil {
			return *t, wm.ContentFromRule
		}
	}
	return c.contentTypes.Of(s), wm.ContentFromClient
}

func (c *Compositor) contentType(s *surface.Surface) wm.ContentType {
	t, _ := c.contentOf(s)
	return t
}

func (c *Compositor) restate(w *window) error {
	now := *c.decision(w)
	if now == w.stated {
		return nil
	}
	w.stated = now
	s := w.top.Surface()
	if s == nil {
		return nil
	}
	whole := []image.Rectangle{{Max: s.Size()}}
	var err error
	for _, o := range c.outputs {
		if !showsTree(c.shown[o], s) {
			continue
		}
		c.sceneOf(o).damaged(s, whole)
		err = errors.Join(err, c.schedule(o))
	}
	return err
}
