package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/output"
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

func (c *Compositor) showFrameHints(o *output.Output, sc *scene, bounds image.Rectangle, plain bool) (drawOp, bool) {
	op, alone := drawOp{}, false
	if plain {
		op, alone = fills(sc.cur.ops[:sc.cur.cursorFrom], bounds)
	}
	c.showContentType(o, op.surface, alone)
	if err := c.showAdaptiveSync(o, op.surface, alone); err != nil {
		c.report(fmt.Errorf("compositor: announce the adaptive sync of %s: %w", o.Backend().Name(), err))
	}
	if ft, ok := o.Backend().(backend.FrameTearing); ok {
		ft.TearFrame(alone && c.tears(op.surface))
	}
	c.showHDR(o, op.surface, alone)
	if ff, ok := o.Backend().(backend.FrameFilling); ok {
		ff.FillFrame(alone)
	}
	return op, alone
}

func (c *Compositor) scanout(o *output.Output, sc *scene, op drawOp, alone bool) (bool, error) {
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
	if !op.covers() || !slices.Contains(scanoutFormats, d.Format) {
		return false, nil
	}
	shown, err := out.Scanout(b, d, backend.Placement{Src: op.src, Dst: op.dst}, c.imported[b].left, c.tears(op.surface))
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
		t = c.contentType(s)
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
	if p := c.shaderProfile(o); p != nil {
		return p.target
	}
	return colorspace.SDRTarget
}

func fills(ops []drawOp, bounds image.Rectangle) (drawOp, bool) {
	switch {
	case len(ops) == 1 && ops[0].dst == bounds && ops[0].plain():
		return ops[0], true
	case len(ops) == 2 && ops[0] == letterboxFill(bounds) && ops[1].dst.In(bounds) && ops[1].plain():
		return ops[1], true
	}
	return drawOp{}, false
}

func (op *drawOp) plain() bool {
	return op.surface != nil && op.transform == render.Normal && op.alpha == 1
}

func letterboxFill(bounds image.Rectangle) drawOp {
	return drawOp{fill: true, color: letterbox, dst: bounds, opaque: true}
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
	if k, ok := c.candidates[s]; ok && k.offered {
		return k.out.OverlayTarget()
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
