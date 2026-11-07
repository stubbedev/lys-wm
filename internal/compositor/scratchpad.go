package compositor

import (
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) trackScratchpads(now time.Duration) {
	curve := c.animations.Curve(anim.Scratchpad)
	for _, o := range c.outputs {
		name := o.Backend().Name()
		shown, prev := c.wm.ScratchOn(name), c.shownScratch[name]
		c.shownScratch[name] = shown
		if shown == prev {
			continue
		}
		if curve == anim.Instant {
			delete(c.scratchSlideOf, o)
			continue
		}
		slide := &workspaceSwitch{to: shown, style: c.animations.Style(anim.Scratchpad), pos: anim.NewProgress(0)}
		if shown == 0 {
			slide = &workspaceSwitch{from: prev, forward: true, style: slide.style, pos: anim.NewProgress(0)}
		}
		slide.pos.Set(slide.sign(), now, curve)
		c.scratchSlideOf[o] = slide
	}
}

func (c *Compositor) finishScratchSlides(o *output.Output, now time.Duration) {
	if s, ok := c.scratchSlideOf[o]; ok && s.pos.Done(now) {
		delete(c.scratchSlideOf, o)
	}
}

func (c *Compositor) scratchView(o *output.Output, w *window) (view, bool, bool) {
	s := c.scratchSlideOf[o]
	if s == nil || !w.laid || w.output != o {
		return identity, false, false
	}
	s.prepare(o.Logical(), c.frameTime)
	v, ok := s.viewOf(w.workspace)
	return v, ok, ok
}

func (c *Compositor) scratchShownOn(o *output.Output) bool {
	return c.wm.ScratchOn(o.Backend().Name()) != 0 || c.scratchSlideOf[o] != nil
}

func (c *Compositor) drawScratchpad(pass *frame, o *output.Output, shown []*surface.Surface) []*surface.Surface {
	if !c.scratchShownOn(o) {
		return shown
	}
	pass.FillRect(deviceRect(o, o.Logical()), c.dimAroundColor())
	for _, layer := range scratchLayers {
		for _, w := range c.stack {
			if w.scratch && w.layer == layer {
				shown = c.drawWindow(pass, o, w, shown)
				shown = c.drawWindowPopups(pass, o, w, shown)
			}
		}
	}
	return shown
}

var scratchLayers = []wm.PlacementLayer{wm.LayerTiled, wm.LayerFloating, wm.LayerMaximized, wm.LayerFullscreen}
