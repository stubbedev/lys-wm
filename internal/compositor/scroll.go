package compositor

import (
	"fmt"
	"image"
	"math"
	"time"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func clipped(r image.Rectangle, p wm.Placement) image.Rectangle {
	if p.Clip.Empty() {
		return r
	}
	if in := r.Intersect(p.Clip); !in.Empty() {
		return in
	}
	return p.Clip
}

type viewDrive struct {
	c        *Compositor
	o        *output.Output
	tracking bool
	travel   float64
	shifted  int
}

func (c *Compositor) driveView() driven {
	o := c.outputAt(c.cursor)
	if o == nil {
		return nil
	}
	s, err := c.wm.FocusOutput(o.Backend().Name())
	if err != nil {
		c.report(fmt.Errorf("compositor: scroll the view of %s: %w", o.Backend().Name(), err))
		return nil
	}
	c.wm = s
	c.gesture.view = viewDrive{c: c, o: o, tracking: true}
	return &c.gesture.view
}

func (d *viewDrive) moved(by float64, _ time.Duration) error {
	if !d.tracking || d.c.shell.Locked() {
		return nil
	}
	if d.c.gesture.natural {
		by = -by
	}
	d.travel += by
	to := int(math.Round(d.travel * float64(d.c.usable[d.o].Dx())))
	dx := to - d.shifted
	d.shifted = to
	d.c.wm = d.c.wm.ScrollView(dx)
	d.c.stateChanged()
	return d.c.reconcile()
}

func (d *viewDrive) released(time.Duration, bool) error {
	if !d.tracking {
		return nil
	}
	d.tracking = false
	return d.c.update(d.c.wm.SettleView(d.c.spacing()))
}
