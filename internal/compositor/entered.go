package compositor

import (
	"slices"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type entered struct {
	s       *surface.Surface
	outputs []*output.Output
}

type enteredSurfaces struct {
	list []*entered
	of   map[*surface.Surface]*entered
}

func (es *enteredSurfaces) lookup(s *surface.Surface) *entered {
	return es.of[s]
}

func (es *enteredSurfaces) get(s *surface.Surface) *entered {
	if e := es.of[s]; e != nil {
		return e
	}
	if es.of == nil {
		es.of = map[*surface.Surface]*entered{}
	}
	e := &entered{s: s}
	es.of[s] = e
	es.list = append(es.list, e)
	return e
}

func (es *enteredSurfaces) forget(s *surface.Surface) {
	if _, ok := es.of[s]; !ok {
		return
	}
	delete(es.of, s)
	es.list = slices.DeleteFunc(es.list, func(e *entered) bool { return e.s == s })
}

func (c *Compositor) presentsOn(s *surface.Surface, o *output.Output) bool {
	e := c.entered.lookup(s)
	if e == nil {
		return true
	}
	for _, x := range e.outputs {
		if c.drawing(x) {
			return x == o
		}
	}
	return true
}
