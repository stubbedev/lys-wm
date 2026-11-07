package compositor

import (
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/output"
	shte "github.com/stubbedev/lys-wm/internal/protocol/server/hyprlandtoplevelexport"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	swft "github.com/stubbedev/lys-wm/internal/protocol/server/wlrforeigntoplevel"
	swsc "github.com/stubbedev/lys-wm/internal/protocol/server/wlrscreencopy"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shm"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	screencopyVersion     = 3
	toplevelExportVersion = 2
)

const screencopyDMABUFFormat = fourcc.XRGB8888

func (c *Compositor) addScreencopy(d *server.Display, offer *dmabufOffer) error {
	dmabufs := offer.offers(screencopyDMABUFFormat)
	if _, err := d.AddGlobal(swsc.ZwlrScreencopyManagerV1Interface, screencopyVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := swsc.NewZwlrScreencopyManagerV1(cl, id, version, screencopyManager{c, dmabufs})
		return err
	}); err != nil {
		return err
	}
	_, err := d.AddGlobal(shte.HyprlandToplevelExportManagerV1Interface, toplevelExportVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := shte.NewHyprlandToplevelExportManagerV1(cl, id, version, toplevelExports{c, dmabufs})
		return err
	})
	return err
}

type screencopyManager struct {
	c       *Compositor
	dmabufs bool
}

func (screencopyManager) Destroy(*swsc.ZwlrScreencopyManagerV1) error {
	return nil
}

func (m screencopyManager) CaptureOutput(_ *swsc.ZwlrScreencopyManagerV1, res *swsc.ZwlrScreencopyFrameV1, overlayCursor int32, wl *sw.WlOutput) (swsc.ZwlrScreencopyFrameV1Handler, error) {
	return m.frame(res, overlayCursor != 0, wl, image.Rectangle{})
}

func (m screencopyManager) CaptureOutputRegion(_ *swsc.ZwlrScreencopyManagerV1, res *swsc.ZwlrScreencopyFrameV1, overlayCursor int32, wl *sw.WlOutput, x, y, w, h int32) (swsc.ZwlrScreencopyFrameV1Handler, error) {
	r := image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
	if r.Empty() {
		r = image.Rect(int(x), int(y), int(x)+1, int(y)+1)
	}
	return m.frame(res, overlayCursor != 0, wl, r)
}

func (m screencopyManager) frame(res *swsc.ZwlrScreencopyFrameV1, cursor bool, wl *sw.WlOutput, logical image.Rectangle) (swsc.ZwlrScreencopyFrameV1Handler, error) {
	o := m.c.layout.Owner(wl)
	var source captureSource = goneSource{}
	if m.c.outputLive(o) {
		if logical != (image.Rectangle{}) {
			logical = logical.Sub(o.Position())
		}
		source = outputRegionSource{o, m.c.captureRegion(o, logical)}
	}
	f := &copyFrame{c: m.c, events: wlrFrame{res}, source: source, cursor: cursor}
	return screencopyCopy{f}, f.offer(m.dmabufs && res.Base().Version() >= 3)
}

type outputRegionSource struct {
	output *output.Output
	region image.Rectangle
}

func (src outputRegionSource) area(c *Compositor) (*output.Output, image.Rectangle, bool) {
	return src.output, src.region, c.outputLive(src.output)
}

func (src outputRegionSource) paint(c *Compositor, o *output.Output, area image.Rectangle, cursor, screencast bool) (render.Color, func(render.Pass) error) {
	return opaque, c.outputDraw(o, area, cursor, screencast)
}

type toplevelExports struct {
	c       *Compositor
	dmabufs bool
}

func (toplevelExports) Destroy(*shte.HyprlandToplevelExportManagerV1) error {
	return nil
}

func (m toplevelExports) CaptureToplevel(_ *shte.HyprlandToplevelExportManagerV1, res *shte.HyprlandToplevelExportFrameV1, overlayCursor int32, handle uint32) (shte.HyprlandToplevelExportFrameV1Handler, error) {
	return m.frame(res, overlayCursor != 0, m.c.windowByID(wm.WindowID(handle)))
}

func (m toplevelExports) CaptureToplevelWithWlrToplevelHandle(_ *shte.HyprlandToplevelExportManagerV1, res *shte.HyprlandToplevelExportFrameV1, overlayCursor int32, handle *swft.ZwlrForeignToplevelHandleV1) (shte.HyprlandToplevelExportFrameV1Handler, error) {
	var w *window
	if h, ok := handle.Handler().(wlrHandleRequests); ok {
		w = h.w
	}
	return m.frame(res, overlayCursor != 0, w)
}

func (m toplevelExports) frame(res *shte.HyprlandToplevelExportFrameV1, cursor bool, w *window) (shte.HyprlandToplevelExportFrameV1Handler, error) {
	var source captureSource = goneSource{}
	if w != nil && m.c.live(w) {
		source = windowSource{w}
	}
	f := &copyFrame{c: m.c, events: hyprlandFrame{res}, source: source, cursor: cursor}
	server.Track(res, &m.c.windowCopies, f)
	return toplevelCopy{f}, f.offer(m.dmabufs)
}

type copyEvents interface {
	wire.Resource
	SendBuffer(format sw.WlShmFormat, width, height, stride uint32) error
	SendLinuxDmabuf(format, width, height uint32) error
	SendBufferDone() error
	SendDamage(x, y, width, height uint32) error
	SendReady(tvSecHi, tvSecLo, tvNsec uint32) error
	SendFailed() error
	sendNoFlags() error
	dmabufEvents() bool
	alreadyUsed() error
	invalidBuffer(err error) error
}

type wlrFrame struct {
	*swsc.ZwlrScreencopyFrameV1
}

func (f wlrFrame) sendNoFlags() error { return f.SendFlags(0) }

func (f wlrFrame) dmabufEvents() bool { return f.Base().Version() >= 3 }

func (f wlrFrame) alreadyUsed() error {
	return f.ProtocolError(swsc.ZwlrScreencopyFrameV1ErrorAlreadyUsed, "the frame was already copied")
}

func (f wlrFrame) invalidBuffer(err error) error {
	return f.ProtocolError(swsc.ZwlrScreencopyFrameV1ErrorInvalidBuffer, "%v", err)
}

type hyprlandFrame struct {
	*shte.HyprlandToplevelExportFrameV1
}

func (f hyprlandFrame) sendNoFlags() error { return f.SendFlags(0) }

func (hyprlandFrame) dmabufEvents() bool { return true }

func (f hyprlandFrame) alreadyUsed() error {
	return f.ProtocolError(shte.HyprlandToplevelExportFrameV1ErrorAlreadyUsed, "the frame was already copied")
}

func (f hyprlandFrame) invalidBuffer(err error) error {
	return f.ProtocolError(shte.HyprlandToplevelExportFrameV1ErrorInvalidBuffer, "%v", err)
}

type copyFrame struct {
	c       *Compositor
	events  copyEvents
	source  captureSource
	size    image.Point
	cursor  bool
	dmabufs bool
	damage  region.Set
	oneshot bool
	frameLife
}

func (f *copyFrame) offer(dmabufs bool) error {
	server.OnDestroy(f.events, func() error {
		f.end(f.c)
		return nil
	})
	_, area, ok := f.source.area(f.c)
	if !ok || area.Empty() {
		f.finish()
		return f.events.SendFailed()
	}
	f.size, f.dmabufs = area.Size(), dmabufs
	w, h := uint32(f.size.X), uint32(f.size.Y) //nolint:gosec // capture sizes are positive
	if err := f.events.SendBuffer(sw.WlShmFormatXrgb8888, w, h, 4*w); err != nil {
		return err
	}
	if !f.events.dmabufEvents() {
		return nil
	}
	if dmabufs {
		if err := f.events.SendLinuxDmabuf(uint32(screencopyDMABUFFormat), w, h); err != nil {
			return err
		}
	}
	return f.events.SendBufferDone()
}

type screencopyCopy struct{ *copyFrame }

func (f screencopyCopy) Copy(_ *swsc.ZwlrScreencopyFrameV1, wl *sw.WlBuffer) error {
	return f.copy(wl, false)
}

func (f screencopyCopy) CopyWithDamage(_ *swsc.ZwlrScreencopyFrameV1, wl *sw.WlBuffer) error {
	return f.copy(wl, true)
}

func (screencopyCopy) Destroy(*swsc.ZwlrScreencopyFrameV1) error {
	return nil
}

type toplevelCopy struct{ *copyFrame }

func (f toplevelCopy) Copy(_ *shte.HyprlandToplevelExportFrameV1, wl *sw.WlBuffer, ignoreDamage int32) error {
	return f.copy(wl, ignoreDamage == 0)
}

func (toplevelCopy) Destroy(*shte.HyprlandToplevelExportFrameV1) error {
	return nil
}

func (f *copyFrame) copy(wl *sw.WlBuffer, waitForDamage bool) error {
	if f.state != frameIdle {
		return f.events.alreadyUsed()
	}
	o, area, ok := f.source.area(f.c)
	if !ok || area.Size() != f.size {
		f.finish()
		return f.events.SendFailed()
	}
	target, err := f.target(wl)
	if errors.Is(err, errUndrawable) {
		f.finish()
		return f.events.SendFailed()
	}
	if err != nil {
		return f.events.invalidBuffer(err)
	}
	clear, draw := f.source.paint(f.c, o, area, f.cursor, !f.oneshot)
	k := &capture{output: o, size: f.size, clear: clear, draw: draw, target: target}
	f.start(k)
	window, isWindow := f.source.(windowSource)
	if waitForDamage {
		k.waits = func(d *region.Set) bool {
			if !isWindow {
				addDamage(&f.damage, d, area)
			}
			return f.damage.Empty()
		}
	}
	k.done = func(at time.Duration, err error) error {
		if err != nil {
			if !errors.Is(err, errCaptureGone) {
				f.c.report(fmt.Errorf("compositor: copy a frame of %s: %w", o.Backend().Name(), err))
			}
			if !f.finish() {
				return nil
			}
			return f.events.SendFailed()
		}
		if !f.finish() {
			return nil
		}
		errs := []error{f.events.sendNoFlags()}
		for _, r := range f.damage.Rects() {
			errs = append(errs, f.events.SendDamage(uint32(r.Min.X), uint32(r.Min.Y), uint32(r.Dx()), uint32(r.Dy()))) //nolint:gosec // damage lies inside the capture
		}
		ts := wire.TimespecOf(at)
		errs = append(errs, f.events.SendReady(ts.SecHi, ts.SecLo, ts.Nsec))
		if isWindow {
			errs = append(errs, f.c.captured(window.window, at))
		}
		return errors.Join(errs...)
	}
	return f.c.queueCapture(k, waitForDamage)
}

func (f *copyFrame) target(wl *sw.WlBuffer) (captureTarget, error) {
	switch b := wl.Handler().(type) {
	case *shm.Buffer:
		return shmTarget(b, f.size, []render.Format{render.XRGB8888}, nil)
	case *dmabuf.Buffer:
		if !f.dmabufs {
			return nil, fmt.Errorf("%w: this frame offered no dmabufs", errConstraints)
		}
		return f.c.dmabufTarget(b, f.size)
	}
	return nil, fmt.Errorf("%w: neither a shared-memory nor a dmabuf buffer", errConstraints)
}

func (c *Compositor) noteWindowCopies(s *surface.Surface) error {
	var errs []error
	for _, f := range c.windowCopies {
		src, ok := f.source.(windowSource)
		if !ok || f.state != frameCapturing || !c.inWindow(src.window, s) {
			continue
		}
		f.damage.Add(image.Rectangle{Max: f.size})
		if o := f.pending.output; c.sceneOf(o).replayable() {
			errs = append(errs, c.wakeCaptures(o))
		} else {
			errs = append(errs, c.schedule(o))
		}
	}
	return errors.Join(errs...)
}
