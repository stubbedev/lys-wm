package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shader"
)

const screenShaderPeriod = 3600

type screenShader struct {
	path     string
	shader   render.ScreenShader
	animated bool
	err      error
}

func (c *Compositor) loadShader(path string) *screenShader {
	s := &screenShader{path: path}
	src, err := os.ReadFile(path) //nolint:gosec // the user's own config names the file
	if err != nil {
		s.err = fmt.Errorf("read: %w", err)
		return s
	}
	program, err := shader.Compile(src)
	if err != nil {
		s.err = err
		return s
	}
	if s.shader, err = c.renderer.NewScreenShader(program); err != nil {
		s.err = err
		return s
	}
	s.animated = program.UsesTime()
	return s
}

func (c *Compositor) shaderOf(o *output.Output) *screenShader {
	path := c.configuredRule(o.Backend()).Shader
	if path == "" {
		return nil
	}
	s, ok := c.shaders[path]
	if !ok {
		s = c.loadShader(path)
		c.shaders[path] = s
		if s.err != nil {
			c.report(fmt.Errorf("compositor: screen shader %s stays off: %w", path, s.err))
		}
		c.stateChanged()
	}
	return s
}

func (c *Compositor) shading(o *output.Output) *screenShader {
	if c.shell.Locked() || c.hdr[o.Backend()] != nil {
		return nil
	}
	if s := c.shaderOf(o); s != nil && s.err == nil {
		return s
	}
	return nil
}

func (c *Compositor) shaderAnimates(o *output.Output) bool {
	s := c.shading(o)
	return s != nil && s.animated
}

func (c *Compositor) dropShaders() error {
	var errs []error
	for path, s := range c.shaders {
		if s.shader != nil {
			errs = append(errs, s.shader.Destroy())
		}
		delete(c.shaders, path)
	}
	return errors.Join(errs...)
}

func (c *Compositor) failShader(s *screenShader, err error) {
	c.report(fmt.Errorf("compositor: screen shader %s stays off: %w", s.path, err))
	s.err = err
	c.stateChanged()
}

func (sc *scene) useShader(s *screenShader) error {
	if sc.shader == s {
		return nil
	}
	sc.shader = s
	sc.invalidate()
	return sc.dropImage()
}

func (sc *scene) dropImage() error {
	if sc.image == nil {
		return nil
	}
	err := sc.image.Destroy()
	sc.image = nil
	return err
}

func (c *Compositor) sceneImage(o *output.Output, sc *scene, bounds image.Rectangle) error {
	if sc.image == nil || sc.image.Size() != bounds.Size() {
		if err := sc.dropImage(); err != nil {
			return err
		}
		t, err := c.renderer.NewTextureTarget(bounds.Size())
		if err != nil {
			return err
		}
		sc.image = t
		sc.invalidate()
		sc.diff(bounds)
	}
	if sc.zoom != nil {
		return c.zoomedImage(o, sc, bounds)
	}
	if sc.damage.Empty() {
		return nil
	}
	p, err := c.renderer.Begin(sc.image, render.Region(sc.damage.Rects()))
	if err != nil {
		return err
	}
	sc.cur.replay(p, colorOut{plans: &c.plans})
	return p.End()
}

func (c *Compositor) renderShaded(o *output.Output, sc *scene, s *screenShader, bounds image.Rectangle) error {
	if out, ok := o.Backend().(backend.Scanner); ok {
		if err := c.fill(o, nil, out); err != nil {
			return err
		}
	}
	if err := c.sceneImage(o, sc, bounds); err != nil {
		return err
	}
	f, err := c.beginFrame(o, sc)
	if err != nil {
		return err
	}
	if err := s.shader.Prepare(f.Target); err != nil {
		o.Backend().AbortFrame()
		c.failShader(s, err)
		sc.invalidate()
		return c.schedule(o)
	}
	whole := render.Region{bounds}
	p, err := f.Renderer.Begin(f.Target, whole)
	if err != nil {
		o.Backend().AbortFrame()
		return err
	}
	p.Clear(opaque)
	p.DrawScreenShader(s.shader, sc.image.Texture(), bounds, float32(math.Mod(c.frameTime.Seconds(), screenShaderPeriod)))
	if err := p.End(); err != nil {
		o.Backend().AbortFrame()
		if backend.Fatal(err) {
			return err
		}
		c.failShader(s, err)
		sc.invalidate()
		return c.schedule(o)
	}
	c.frames.Submit(o.Backend().Name())
	if err := c.present(o, sc, whole); err != nil {
		return err
	}
	sc.presented()
	return errors.Join(c.lockBlanked(o), c.runCaptures(o))
}
