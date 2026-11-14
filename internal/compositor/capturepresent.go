package compositor

import (
	"time"

	"github.com/stubbedev/lys-wm/internal/output"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/surface"
)

func (c *Compositor) captureOutput(w *window) *output.Output {
	if w.output != nil {
		return w.output
	}
	if len(c.outputs) > 0 {
		return c.outputs[0]
	}
	return nil
}

func (c *Compositor) presentationAt(o *output.Output, s *surface.Surface, at time.Duration) ([]*sw.WlOutput, time.Duration, uint64) {
	if o == nil {
		return nil, 0, 0
	}
	last := c.sceneOf(o).last
	refresh := outputRefresh(o.Backend(), last)
	seq := last.Seq
	if seq != 0 && refresh > 0 && !last.Variable && at > last.Time {
		seq += uint64((at - last.Time) / refresh) //nolint:gosec // at is after the last presentation
	}
	c.outputResources = o.AppendResources(c.outputResources[:0], s.Client())
	return c.outputResources, refresh, seq
}
