package compositor

import (
	"errors"
	"fmt"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
)

const DefaultInheritTimeout = 10 * time.Second

func showsInherited(o *output.Output) bool {
	in, ok := o.Backend().(backend.Inheriting)
	return ok && in.Inherited()
}

func (c *Compositor) keepInherited(o *output.Output) error {
	if !showsInherited(o) {
		return nil
	}
	t, err := c.loop.AddTimer(func() error { return c.inheritTimedOut(o) })
	if err != nil {
		return fmt.Errorf("compositor: time the frame %s inherited: %w", o.Backend().Name(), err)
	}
	if err := t.Arm(c.inheritTimeout); err != nil {
		return errors.Join(fmt.Errorf("compositor: time the frame %s inherited: %w", o.Backend().Name(), err), t.Remove())
	}
	c.inherited[o] = t
	return nil
}

func (c *Compositor) holdsInherited(o *output.Output, content bool) (bool, error) {
	if _, ok := c.inherited[o]; !ok {
		return false, nil
	}
	if !content && showsInherited(o) {
		return true, nil
	}
	return false, c.endInherited(o)
}

func (c *Compositor) inheritTimedOut(o *output.Output) error {
	if _, ok := c.inherited[o]; !ok {
		return nil
	}
	c.report(fmt.Errorf("compositor: no client drew on %s within %v, so lys-wm draws over the frame it inherited", o.Backend().Name(), c.inheritTimeout))
	return errors.Join(c.endInherited(o), c.schedule(o))
}

func (c *Compositor) endInherited(o *output.Output) error {
	t, ok := c.inherited[o]
	if !ok {
		return nil
	}
	delete(c.inherited, o)
	return t.Remove()
}
