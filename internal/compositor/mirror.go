package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type mirror struct {
	out       backend.Output
	source    wm.OutputMatch
	transform render.Transform
	draws     uint64
}

func (m *mirror) of(o *output.Output) bool {
	b := o.Backend()
	return m.source.Matches(b.Name(), b.Description())
}

func (c *Compositor) mirrorOf(b backend.Output) *mirror {
	if i := slices.IndexFunc(c.mirrors, func(m *mirror) bool { return m.out == b }); i >= 0 {
		return c.mirrors[i]
	}
	return nil
}

func (c *Compositor) mirrored(o *output.Output) bool {
	return slices.ContainsFunc(c.mirrors, func(m *mirror) bool { return m.of(o) })
}

func (c *Compositor) mirrorSource(m *mirror) *output.Output {
	if i := slices.IndexFunc(c.outputs, m.of); i >= 0 {
		return c.outputs[i]
	}
	return nil
}

func (c *Compositor) addMirror(b backend.Output, rule OutputRule) error {
	m := &mirror{out: b, source: *rule.Mirror, transform: rule.Transform}
	c.mirrors = append(c.mirrors, m)
	return errors.Join(c.mirrorChanged(m), c.outputConfig.Changed())
}

func (c *Compositor) dropMirror(m *mirror) error {
	c.mirrors = slices.DeleteFunc(c.mirrors, func(x *mirror) bool { return x == m })
	var err error
	if src := c.mirrorSource(m); src != nil {
		err = c.sourceChanged(src)
	}
	return err
}

func (c *Compositor) mirrorChanged(m *mirror) error {
	src := c.mirrorSource(m)
	if src == nil {
		c.report(fmt.Errorf("compositor: output %s mirrors an output that is not there; it stays black until one matches", m.out.Name()))
		return m.out.ScheduleFrame()
	}
	return c.sourceChanged(src)
}

func (c *Compositor) sourceChanged(src *output.Output) error {
	c.sceneOf(src).invalidate()
	return c.schedule(src)
}

func (c *Compositor) scheduleMirrorsOf(o *output.Output) error {
	var errs []error
	for _, m := range c.mirrors {
		if m.of(o) {
			errs = append(errs, m.out.ScheduleFrame())
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) mirrorFrame(m *mirror) error {
	return c.letterbox(m.out, func(p render.Pass, size image.Point) {
		src := c.mirrorSource(m)
		if src == nil {
			return
		}
		if sc := c.sceneOf(src); sc.composed != nil {
			from := sc.composed.Size()
			upright := m.transform.Size(size)
			p.DrawTexture(sc.composed.Texture(), render.FRect{W: float64(from.X), H: float64(from.Y)}, m.transform.Rect(letterboxed(from, upright), upright), m.transform, 1, render.Mask{})
			m.draws++
		}
	})
}

func (c *Compositor) letterbox(b backend.Output, draw func(p render.Pass, size image.Point)) error {
	if err := letterboxFrame(b, draw); err != nil {
		return c.dropFrame(b, err, b.ScheduleFrame)
	}
	c.drew(b)
	return nil
}

func letterboxFrame(out backend.Output, draw func(p render.Pass, size image.Point)) error {
	f, err := out.BeginFrame()
	if err != nil {
		return err
	}
	size := out.State().Mode.Size
	whole := image.Rectangle{Max: size}
	p, err := f.Renderer.Begin(f.Target, render.Region{whole})
	if err != nil {
		out.AbortFrame()
		return err
	}
	p.Clear(letterbox)
	draw(p, size)
	if err := p.End(); err != nil {
		out.AbortFrame()
		return err
	}
	return out.Present(render.Region{whole})
}

func letterboxed(content, into image.Point) image.Rectangle {
	w, h := into.X, content.Y*into.X/content.X
	if h > into.Y {
		w, h = content.X*into.Y/content.Y, into.Y
	}
	at := image.Pt((into.X-w)/2, (into.Y-h)/2)
	return image.Rectangle{Min: at, Max: at.Add(image.Pt(w, h))}
}

func (c *Compositor) MirrorDraws(name string) (uint64, bool) {
	if i := slices.IndexFunc(c.mirrors, func(m *mirror) bool { return m.out.Name() == name }); i >= 0 {
		return c.mirrors[i].draws, true
	}
	return 0, false
}

func (c *Compositor) applyMirrorRules() error {
	var errs []error
	for _, m := range slices.Clone(c.mirrors) {
		b := m.out
		rule := c.outputRule(b)
		if rule.Mirror != nil && !rule.Disabled {
			if _, _, err := c.commitOutput(b, rule); err != nil {
				errs = append(errs, err)
			}
			if m.source != *rule.Mirror || m.transform != rule.Transform {
				m.source, m.transform = *rule.Mirror, rule.Transform
				errs = append(errs, c.mirrorChanged(m))
			}
			continue
		}
		errs = append(errs, c.dropMirror(m))
		if _, _, err := c.commitOutput(b, rule); err != nil {
			errs = append(errs, err)
		}
		errs = append(errs, c.place(b, rule))
	}
	return errors.Join(errs...)
}

func (c *Compositor) mirrorName(m *mirror) string {
	if src := c.mirrorSource(m); src != nil {
		return src.Backend().Name()
	}
	if m.source.Name != "" {
		return m.source.Name
	}
	return "desc:" + m.source.Description
}
