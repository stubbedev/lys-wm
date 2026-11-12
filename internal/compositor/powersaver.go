package compositor

import "github.com/stubbedev/lys-wm/internal/backend"

const DefaultPowerSaverRate = 60

func (c *Compositor) SetPowerSaver(on bool) {
	c.powerSaver = on
	c.capFrameRates()
}

func (c *Compositor) capFrameRates() {
	for _, o := range c.outputs {
		c.capFrameRate(o.Backend())
	}
	for _, b := range c.unplaced() {
		c.capFrameRate(b)
	}
}

func (c *Compositor) capFrameRate(b backend.Output) {
	fc, ok := b.(backend.FrameRateCap)
	if !ok {
		return
	}
	hz := 0
	if c.powerSaver {
		hz = c.powerSaverRate
	}
	fc.CapFrameRate(hz)
}
