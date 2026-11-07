package compositor

import (
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/output"
	seic "github.com/stubbedev/lys-wm/internal/protocol/server/extimagecopycapture"
	slch "github.com/stubbedev/lys-wm/internal/protocol/server/lyscapturehint"
	swsc "github.com/stubbedev/lys-wm/internal/protocol/server/wlrscreencopy"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const captureHintVersion = 1

func (c *Compositor) layerProps(e *layer) wm.LayerProps {
	if e.ruledGen != c.rulesGen || !e.ruled {
		e.props, e.ruled, e.ruledGen = wm.DecideLayer(c.layerRules, e.surface.Namespace()), true, c.rulesGen
	}
	return e.props
}

func (c *Compositor) blockedBy(s *surface.Surface, screencast bool) bool {
	for _, w := range c.windows {
		if c.decision(w).BlockOutFrom.Blocks(screencast) && c.inWindow(w, s) {
			return true
		}
	}
	for _, e := range c.layers {
		if !c.layerProps(e).BlockOutFrom.Blocks(screencast) {
			continue
		}
		if inTree(e.surface.Surface(), s) || slices.ContainsFunc(c.layerPopups(e), func(pp placedPopup) bool { return inTree(pp.popup.Surface(), s) }) {
			return true
		}
	}
	return false
}

func (c *Compositor) blocksAny(screencast bool) bool {
	return slices.ContainsFunc(c.windows, func(w *window) bool { return c.decision(w).BlockOutFrom.Blocks(screencast) }) ||
		slices.ContainsFunc(c.layers, func(e *layer) bool { return c.layerProps(e).BlockOutFrom.Blocks(screencast) })
}

func (c *Compositor) blockedOut(ops []drawOp, screencast bool) []drawOp {
	if !c.blocksAny(screencast) {
		return ops
	}
	out := c.maskedOps[:0]
	for _, op := range ops {
		if s := op.shows(); s != nil && c.blockedBy(s, screencast) {
			op = drawOp{fill: true, color: opaque, dst: op.dst, mask: op.mask, opaque: true, alpha: 1}
		}
		out = append(out, op)
	}
	c.maskedOps = out
	return out
}

func (c *Compositor) blockedRects(o *output.Output, into []image.Rectangle) []image.Rectangle {
	into = into[:0]
	for _, w := range c.windows {
		if w.visible && w.output == o && c.decision(w).BlockOutFrom.Blocks(true) {
			into = append(into, deviceRect(o, w.rect))
		}
	}
	for _, e := range c.layers {
		if e.shown() && e.output == o && c.layerProps(e).BlockOutFrom.Blocks(true) {
			into = append(into, deviceRect(o, e.bounds()))
		}
	}
	return into
}

func (c *Compositor) addCaptureHint(d *server.Display) error {
	_, err := d.AddGlobal(slch.LysCaptureHintManagerV1Interface, captureHintVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := slch.NewLysCaptureHintManagerV1(cl, id, version, captureHints{})
		return err
	})
	return err
}

type captureHints struct{}

func (captureHints) Destroy(*slch.LysCaptureHintManagerV1) error { return nil }

func (captureHints) OneshotSession(obj *slch.LysCaptureHintManagerV1, res *seic.ExtImageCopyCaptureSessionV1) error {
	s, ok := res.Handler().(*captureSession)
	switch {
	case !ok:
		return nil
	case s.captured || s.frame != nil && s.frame.state != frameIdle:
		return obj.ProtocolError(slch.LysCaptureHintManagerV1ErrorAlreadyCaptured, "session@%d already captured a frame", res.Base().ID())
	}
	s.oneshot = true
	return nil
}

func (captureHints) OneshotScreencopy(obj *slch.LysCaptureHintManagerV1, res *swsc.ZwlrScreencopyFrameV1) error {
	f, ok := res.Handler().(screencopyCopy)
	switch {
	case !ok:
		return nil
	case f.state != frameIdle:
		return obj.ProtocolError(slch.LysCaptureHintManagerV1ErrorAlreadyCaptured, "frame@%d was already copied", res.Base().ID())
	}
	f.oneshot = true
	return nil
}
