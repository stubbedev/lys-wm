package compositor

import (
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
)

const aimPhaseAge = time.Second

func (c *Compositor) aimed(o *output.Output, now time.Duration) time.Duration {
	last := c.sceneOf(o).last
	if last.Variable || last.Refresh <= 0 || last.Time == 0 || last.Time > now || now-last.Time > aimPhaseAge {
		return now
	}
	at, _ := backend.NextVblank(last.Time, now, last.Refresh)
	return at
}

func (c *Compositor) aimedAny(now time.Duration) time.Duration {
	soonest, found := now, false
	for _, o := range c.outputs {
		if !c.outputLive(o) {
			continue
		}
		if at := c.aimed(o, now); !found || at < soonest {
			soonest, found = at, true
		}
	}
	return soonest
}
