package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/output"
	spt "github.com/stubbedev/lys-wm/internal/protocol/server/presentationtime"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type scannedOut struct {
	outputs  int
	released *server.Client
	then     []func() error
}

var scanoutFormats = []fourcc.Format{fourcc.XRGB8888, fourcc.XBGR8888, fourcc.XRGB2101010, fourcc.XBGR2101010}

type filling struct {
	surface *surface.Surface
	out     backend.Scanner
}

func (c *Compositor) scanout(o *output.Output, sc *scene, bounds image.Rectangle) (bool, error) {
	op, alone := fills(sc.cur.ops[:sc.cur.cursorFrom], bounds)
	c.showContentType(o, op.surface, alone)
	out, ok := o.Backend().(backend.Scanner)
	if !ok || c.adopting != nil {
		return false, nil
	}
	if alone && !colorspace.Passthrough(op.space, c.scanoutTarget(o)) {
		op, alone = drawOp{}, false
	}
	if err := c.fill(o, op.surface, out); err != nil || !alone || sc.cur.cursorFrom != len(sc.cur.ops) {
		return false, err
	}
	t, ok := c.textures[op.surface].(imported)
	if !ok {
		return false, nil
	}
	b := t.buffer
	d := b.DMABUF()
	d.Format = d.Format.Opaque()
	if d.Size != bounds.Size() || op.src != (render.FRect{W: float64(d.Size.X), H: float64(d.Size.Y)}) || !op.covers() || !slices.Contains(scanoutFormats, d.Format) {
		return false, nil
	}
	shown, err := out.Scanout(b, d, c.imported[b].left, c.tears(op.surface))
	if err != nil || !shown {
		return false, err
	}
	sc.zeroCopy[0] = op.surface
	held, ok := c.scannedOut[b]
	if !ok {
		held = c.takeScannedOut()
		c.scannedOut[b] = held
	}
	held.outputs++
	return true, nil
}

func (c *Compositor) showContentType(o *output.Output, s *surface.Surface, alone bool) {
	ct, ok := o.Backend().(backend.ContentTyped)
	if !ok {
		return
	}
	t := wm.ContentNone
	if alone {
		t = c.contentTypes.Of(s)
	}
	ct.SetContentType(t)
}

func (c *Compositor) takeScannedOut() *scannedOut {
	if n := len(c.scannedFree); n > 0 {
		held := c.scannedFree[n-1]
		c.scannedFree = c.scannedFree[:n-1]
		return held
	}
	return &scannedOut{}
}

func (c *Compositor) scanoutTarget(o *output.Output) *colorspace.Target {
	if hc := c.hdr[o.Backend()]; hc != nil {
		return hc.signal
	}
	return colorspace.SDRTarget
}

func fills(ops []drawOp, bounds image.Rectangle) (drawOp, bool) {
	if len(ops) != 1 {
		return drawOp{}, false
	}
	op := ops[0]
	if op.surface == nil || op.dst != bounds || op.transform != render.Normal || op.alpha != 1 {
		return drawOp{}, false
	}
	return op, true
}

func (c *Compositor) fill(o *output.Output, s *surface.Surface, out backend.Scanner) error {
	prev := c.filled[o].surface
	if prev == s {
		return nil
	}
	if s == nil {
		delete(c.filled, o)
	} else {
		c.filled[o] = filling{surface: s, out: out}
	}
	if c.dmabuf == nil {
		return nil
	}
	var err error
	for _, changed := range []*surface.Surface{prev, s} {
		if changed != nil {
			err = errors.Join(err, c.dmabuf.ScanoutChanged(changed))
		}
	}
	return err
}

func (c *Compositor) SurfaceScanout(s *surface.Surface) gpu.ScanoutTarget {
	for _, o := range c.outputs {
		if f, ok := c.filled[o]; ok && f.surface == s {
			t := f.out.ScanoutFormats()
			t.Formats = slices.DeleteFunc(slices.Clone(t.Formats), func(f gpu.FormatModifier) bool { return !slices.Contains(scanoutFormats, f.Format) })
			return t
		}
	}
	return gpu.ScanoutTarget{}
}

func (c *Compositor) leftScreen(b *dmabuf.Buffer) error {
	held, ok := c.scannedOut[b]
	if !ok {
		return nil
	}
	if held.outputs--; held.outputs > 0 {
		return nil
	}
	delete(c.scannedOut, b)
	var err error
	if held.released != nil {
		err = b.Release()
	}
	for i, f := range held.then {
		err = errors.Join(err, f())
		held.then[i] = nil
	}
	*held = scannedOut{then: held.then[:0]}
	c.scannedFree = append(c.scannedFree, held)
	return err
}

func (c *Compositor) OffScreen(b *dmabuf.Buffer, f func() error) error {
	held, ok := c.scannedOut[b]
	if !ok {
		return f()
	}
	held.then = append(held.then, f)
	return nil
}

func (c *Compositor) releaseBuffer(b *dmabuf.Buffer, client *server.Client) error {
	if held, ok := c.scannedOut[b]; ok {
		held.released = client
		return nil
	}
	return b.Release()
}

func (c *Compositor) scanoutBufferGone(b *dmabuf.Buffer) error {
	if held, ok := c.scannedOut[b]; ok {
		held.released = nil
	}
	var errs []error
	for _, o := range c.outputs {
		if out, ok := o.Backend().(backend.Scanner); ok {
			errs = append(errs, out.ForgetScanout(b))
		}
	}
	return errors.Join(errs...)
}

const overlayMinPixels = 256 * 256

func overlayCandidate(ops []drawOp, cursorFrom int, bounds image.Rectangle) int {
	for i := cursorFrom - 1; i >= 0; i-- {
		if op := &ops[i]; op.overlayable(bounds) && !overlapped(ops[i+1:], op.dst) {
			return i
		}
	}
	return -1
}

func overlapped(above []drawOp, r image.Rectangle) bool {
	for i := range above {
		if above[i].dst.Overlaps(r) {
			return true
		}
	}
	return false
}

func (op *drawOp) overlayable(bounds image.Rectangle) bool {
	return op.surface != nil && op.covers() && op.transform == render.Normal && op.alpha == 1 && op.whole.Empty() &&
		op.dst != bounds && op.dst.In(bounds) && op.dst.Dx()*op.dst.Dy() >= overlayMinPixels &&
		op.src == render.FRect{W: float64(op.dst.Dx()), H: float64(op.dst.Dy())}
}

type overlayShown struct {
	buffer *dmabuf.Buffer
	dst    image.Rectangle
}

func (c *Compositor) assignOverlay(o *output.Output, sc *scene, bounds image.Rectangle, shading bool) error {
	out, ok := o.Backend().(backend.OverlayPlanes)
	if !ok {
		return nil
	}
	var want overlayShown
	var op *drawOp
	if !shading && sc.zoom == nil && c.adopting == nil && c.hdr[o.Backend()] == nil {
		if i := overlayCandidate(sc.cur.ops, sc.cur.cursorFrom, bounds); i >= 0 {
			op = &sc.cur.ops[i]
			if t, isImported := c.textures[op.surface].(imported); isImported {
				d := t.buffer.DMABUF()
				d.Format = d.Format.Opaque()
				if out.TestOverlay(t.buffer, d, op.dst) {
					want = overlayShown{t.buffer, op.dst}
				}
			}
		}
	}
	if want.buffer != nil {
		op.overlay, sc.zeroCopy[1] = true, op.surface
	}
	if want == c.overlays[o] {
		return nil
	}
	if want.buffer == nil {
		delete(c.overlays, o)
		out.ClearOverlay()
		return nil
	}
	if c.overlays == nil {
		c.overlays = map[*output.Output]overlayShown{}
	}
	d := want.buffer.DMABUF()
	d.Format = d.Format.Opaque()
	if err := out.SetOverlay(want.buffer, d, want.dst, c.imported[want.buffer].left); err != nil {
		op.overlay, sc.zeroCopy[1] = false, nil
		return err
	}
	held, ok := c.scannedOut[want.buffer]
	if !ok {
		held = c.takeScannedOut()
		c.scannedOut[want.buffer] = held
	}
	held.outputs++
	c.overlays[o] = want
	return nil
}

func (c *Compositor) flipOverlay(o *output.Output, sc *scene) (bool, error) {
	shown, ok := c.overlays[o]
	out, planes := o.Backend().(backend.OverlayPlanes)
	if !ok || !planes || !sc.drawn || sc.targetStale {
		return false, nil
	}
	for _, r := range sc.damage.Rects() {
		if !r.In(shown.dst) {
			return false, nil
		}
	}
	return out.FlipOverlay()
}

func (sc *scene) feedbackKind(s *surface.Surface, kind spt.WpPresentationFeedbackKind, idle bool) spt.WpPresentationFeedbackKind {
	if !idle && (sc.zeroCopy[0] == s || sc.zeroCopy[1] == s) {
		kind |= spt.WpPresentationFeedbackKindZeroCopy
	}
	return kind
}
