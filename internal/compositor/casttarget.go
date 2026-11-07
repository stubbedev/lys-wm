package compositor

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/output"
	seis "github.com/stubbedev/lys-wm/internal/protocol/server/extimagecapturesource"
	sldc "github.com/stubbedev/lys-wm/internal/protocol/server/lysdynamiccapture"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const dynamicCaptureVersion = 1

type castTarget struct {
	window *window
	output *output.Output
}

type dynamicSource struct{}

type noneSource struct{}

func (c *Compositor) addDynamicCapture(d *server.Display) error {
	_, err := d.AddGlobal(sldc.LysDynamicCaptureSourceManagerV1Interface, dynamicCaptureVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := sldc.NewLysDynamicCaptureSourceManagerV1(cl, id, version, dynamicSources{})
		return err
	})
	return err
}

type dynamicSources struct{}

func (dynamicSources) Destroy(*sldc.LysDynamicCaptureSourceManagerV1) error { return nil }

func (dynamicSources) CreateSource(_ *sldc.LysDynamicCaptureSourceManagerV1, _ *seis.ExtImageCaptureSourceV1) (seis.ExtImageCaptureSourceV1Handler, error) {
	return &sourceHandle{dynamicSource{}}, nil
}

func (c *Compositor) castSource() captureSource {
	switch t := c.cast; {
	case t.window != nil && c.live(t.window) && t.window.output != nil:
		return windowSource{t.window}
	case t.output != nil && c.outputLive(t.output):
		return outputSource{t.output}
	}
	return noneSource{}
}

func (c *Compositor) resolved(src captureSource) captureSource {
	if _, dynamic := src.(dynamicSource); dynamic {
		return c.castSource()
	}
	return src
}

func (dynamicSource) area(c *Compositor) (*output.Output, image.Rectangle, bool) {
	o, area, ok := c.castSource().area(c)
	if _, none := c.castSource().(noneSource); !none && ok && !area.Empty() {
		c.castSize = area.Size()
	}
	return o, area, ok
}

func (dynamicSource) paint(c *Compositor, o *output.Output, area image.Rectangle, cursor, screencast bool) (render.Color, func(render.Pass) error) {
	return c.castSource().paint(c, o, area, cursor, screencast)
}

func (noneSource) area(c *Compositor) (*output.Output, image.Rectangle, bool) {
	o := c.firstOutput()
	if o == nil {
		return nil, image.Rectangle{}, true
	}
	return o, image.Rectangle{Max: c.castSize}, true
}

func (noneSource) paint(*Compositor, *output.Output, image.Rectangle, bool, bool) (render.Color, func(render.Pass) error) {
	return opaque, func(render.Pass) error { return nil }
}

func (c *Compositor) castTargetAction(a wm.CastTargetAction) error {
	switch a.Kind {
	case wm.CastClear:
		return c.setCastTarget(castTarget{})
	case wm.CastOutput:
		o := c.focusedOutput()
		if a.Output != "" {
			o = c.outputMatching(wm.OutputMatch{Name: a.Output})
		}
		if o == nil {
			return fmt.Errorf("compositor: cast target: no output %q", a.Output)
		}
		return c.setCastTarget(castTarget{output: o})
	case wm.CastWindow:
		w := c.active
		if a.Window != 0 {
			w = c.windowByID(a.Window)
		}
		if w == nil {
			return fmt.Errorf("compositor: cast target: no window %d", a.Window)
		}
		return c.setCastTarget(castTarget{window: w})
	}
	return fmt.Errorf("compositor: unknown cast target %s", a.Kind)
}

func (c *Compositor) setCastTarget(t castTarget) error {
	if t == c.cast {
		return nil
	}
	dynamicSource{}.area(c)
	c.cast = t
	c.stateChanged()
	var errs []error
	for _, s := range c.captureSessions {
		if _, dynamic := s.source.(dynamicSource); !dynamic || s.stopped {
			continue
		}
		errs = append(errs, s.sync())
		o, area, ok := s.target()
		if !ok || area.Empty() {
			continue
		}
		s.damage.Add(image.Rectangle{Max: area.Size()})
		if o != nil {
			errs = append(errs, c.wakeCaptures(o))
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) castGone(w *window, o *output.Output) error {
	if w != nil && c.cast.window == w || o != nil && c.cast.output == o {
		return c.setCastTarget(castTarget{})
	}
	return nil
}

func (c *Compositor) ipcCastTarget() ipc.CastTarget {
	switch src := c.castSource().(type) {
	case windowSource:
		return ipc.CastTarget{Kind: ipc.CastWindow, Window: src.window.id}
	case outputSource:
		return ipc.CastTarget{Kind: ipc.CastOutput, Output: src.output.Backend().Name()}
	}
	return ipc.CastTarget{Kind: ipc.CastNone}
}

func (c *Compositor) CopiedBytes() uint64 {
	return c.copiedBytes.Load()
}
