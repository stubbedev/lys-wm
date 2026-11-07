package compositor

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/edid"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/colormanager"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

const (
	defaultSDRBrightness = 203
	unknownPeak          = 400
)

type hdrColor struct {
	output  colorspace.Output
	working *colorspace.Target
	signal  *colorspace.Target
}

type colorOut struct {
	plans   *colorspace.Plans
	to      *colorspace.Target
	working *colorspace.Description
}

func (to colorOut) source(op *drawOp) *colorspace.Description {
	if to.working != nil && op.tex != nil && op.tex.Working() {
		return to.working
	}
	return op.space
}

func (to colorOut) plan(src *colorspace.Description) *render.ColorTransform {
	if to.to == nil {
		if src == nil {
			return nil
		}
		return to.plans.Of(src, colorspace.SDRTarget)
	}
	return to.plans.Of(src, to.to)
}

func (c *Compositor) sceneOut(o *output.Output) colorOut {
	hc := c.hdr[o.Backend()]
	if hc == nil {
		return colorOut{plans: &c.plans}
	}
	return colorOut{plans: &c.plans, to: hc.working, working: hc.working.Description()}
}

func (c *Compositor) captureOut(o *output.Output) colorOut {
	hc := c.hdr[o.Backend()]
	if hc == nil {
		return colorOut{plans: &c.plans}
	}
	return colorOut{plans: &c.plans, to: colorspace.SDRTarget, working: hc.working.Description()}
}

func (c *Compositor) newSceneTarget(o *output.Output, size image.Point) (render.TextureTarget, error) {
	if c.hdr[o.Backend()] != nil {
		return c.renderer.NewWorkingTarget(size)
	}
	return c.renderer.NewTextureTarget(size)
}

func (c *Compositor) surfaceSpace(s *surface.Surface) *colorspace.Description {
	if c.colorManager == nil {
		return nil
	}
	return c.colorManager.Of(s)
}

func (c *Compositor) paintFrame(o *output.Output, sc *scene, f backend.Frame, bounds image.Rectangle) (render.Region, error) {
	hc := c.hdr[o.Backend()]
	if hc == nil {
		repaint := render.Region(sc.repaintFor(f, bounds).Rects())
		c.tallied.repaintPx += uint64(repaint.Area()) //nolint:gosec // an area is not negative
		p, err := f.Renderer.Begin(f.Target, repaint)
		if err != nil {
			return nil, err
		}
		sc.cur.replayOnPrimary(p, colorOut{plans: &c.plans})
		return repaint, p.End()
	}
	if err := c.ensureWorking(o, sc, bounds); err != nil {
		return nil, err
	}
	repaint := render.Region(sc.repaintFor(f, bounds).Rects())
	c.tallied.repaintPx += uint64(repaint.Area()) //nolint:gosec // an area is not negative
	p, err := f.Renderer.Begin(sc.working, render.Region(sc.damage.Rects()))
	if err != nil {
		return nil, err
	}
	sc.cur.replay(p, c.sceneOut(o))
	if err := p.End(); err != nil {
		return nil, err
	}
	if p, err = f.Renderer.Begin(f.Target, repaint); err != nil {
		return nil, err
	}
	size := bounds.Size()
	p.SetColor(c.plans.Of(hc.working.Description(), hc.signal))
	p.DrawTexture(sc.working.Texture(), render.FRect{W: float64(size.X), H: float64(size.Y)}, bounds, render.Normal, 1, render.Mask{})
	return repaint, p.End()
}

func (c *Compositor) ensureWorking(o *output.Output, sc *scene, bounds image.Rectangle) error {
	if sc.working != nil && sc.working.Size() == bounds.Size() {
		return nil
	}
	if err := sc.dropWorking(); err != nil {
		return err
	}
	t, err := c.newSceneTarget(o, bounds.Size())
	if err != nil {
		return err
	}
	sc.working = t
	sc.damage.Reset()
	sc.damage.Add(bounds)
	return nil
}

func (sc *scene) dropWorking() error {
	if sc.working == nil {
		return nil
	}
	t := sc.working
	sc.working = nil
	return t.Destroy()
}

func matrix32(m backend.Matrix) [9]float32 {
	var out [9]float32
	for i, v := range m {
		out[i] = float32(v)
	}
	return out
}

func chromaticity(c edid.Chromaticity) colorspace.XY {
	return colorspace.XY{X: c.X, Y: c.Y}
}

func (c *Compositor) hdrOutput(b backend.Output, rule OutputRule, caps edid.HDR) (colorspace.Output, backend.HDRMetadata) {
	peak := rule.HDRPeak
	if peak == 0 {
		peak = caps.MaxLuminance
	}
	if peak == 0 {
		peak = unknownPeak
		c.report(fmt.Errorf("compositor: output %s: its EDID has no peak luminance, so it is taken as %d cd/m²; set hdr_peak if the display says otherwise", b.Name(), unknownPeak))
	}
	sdr := rule.SDRBrightness
	if sdr == 0 {
		sdr = defaultSDRBrightness
	}
	if sdr > peak {
		c.report(fmt.Errorf("compositor: output %s: sdr_brightness %v is above its peak of %v cd/m², so SDR white shows at the peak", b.Name(), sdr, peak))
		sdr = peak
	}
	average := caps.MaxAverageLuminance
	if average == 0 {
		average = peak
	}
	panel := colorspace.Primaries{R: chromaticity(caps.Red), G: chromaticity(caps.Green), B: chromaticity(caps.Blue), W: chromaticity(caps.White)}
	if !panel.Valid() {
		panel = colorspace.BT2020
	}
	co := colorspace.Output{SDRBrightness: sdr, Peak: peak, MinLum: caps.MinLuminance, MaxAverage: average, Panel: panel}
	return co, backend.HDRMetadata{
		Active: true, Red: caps.Red, Green: caps.Green, Blue: caps.Blue, White: caps.White,
		MaxLuminance: peak, MinLuminance: caps.MinLuminance, MaxCLL: peak, MaxFALL: average,
	}
}

func (c *Compositor) hdrAsked(b backend.Output, rule OutputRule) (colorspace.Output, backend.HDRMetadata, error) {
	d, ok := b.(backend.HDRDisplay)
	if !ok {
		return colorspace.Output{}, backend.HDRMetadata{}, fmt.Errorf("%w: this backend has no HDR signal", backend.ErrHDRUnsupported)
	}
	caps, err := d.HDRCapabilities()
	if err != nil {
		return colorspace.Output{}, backend.HDRMetadata{}, err
	}
	probe, err := c.renderer.NewWorkingTarget(image.Pt(1, 1))
	if err != nil {
		return colorspace.Output{}, backend.HDRMetadata{}, fmt.Errorf("%w: %w", backend.ErrHDRUnsupported, err)
	}
	if err := probe.Destroy(); err != nil {
		return colorspace.Output{}, backend.HDRMetadata{}, err
	}
	co, meta := c.hdrOutput(b, rule, caps)
	return co, meta, nil
}

func (c *Compositor) setOutputColor(b backend.Output, co *colorspace.Output) (bool, error) {
	prev := c.hdr[b]
	if prev == nil && co == nil || prev != nil && co != nil && prev.output == *co {
		return false, nil
	}
	ctm := backend.Identity
	if m, ok := c.ctms[b]; ok {
		ctm = m
	}
	if co == nil {
		delete(c.hdr, b)
	} else {
		c.hdr[b] = &hdrColor{output: *co, working: colorspace.Working(*co), signal: colorspace.Signal(*co, matrix32(ctm))}
	}
	o := c.output(b)
	if o == nil {
		return false, nil
	}
	return true, c.outputColorChanged(o, b, prev != nil, co != nil, ctm)
}

func (c *Compositor) outputColorChanged(o *output.Output, b backend.Output, wasHDR, isHDR bool, ctm backend.Matrix) error {
	c.recolorCursor(o)
	sc := c.sceneOf(o)
	sc.invalidate()
	errs := []error{sc.dropWorking(), c.dropBlurs(), c.dropClosingOn(o), c.dropLockSnapshotOn(o)}
	if wasHDR != isHDR {
		errs = append(errs, c.crtcColor(o, b, isHDR, ctm))
	}
	if c.colorManager != nil {
		errs = append(errs, c.colorManager.OutputChanged(o), c.colorManager.AllPreferredChanged())
	}
	return errors.Join(append(errs, c.schedule(o))...)
}

func (c *Compositor) crtcColor(o *output.Output, b backend.Output, isHDR bool, ctm backend.Matrix) error {
	var errs []error
	if isHDR {
		errs = append(errs, c.gamma.Removed(o))
		if g, ok := b.(backend.Gamma); ok && g.GammaSize() > 0 {
			errs = append(errs, g.SetGamma(nil))
		}
		ctm = backend.Identity
	}
	if ct, ok := b.(backend.ColorTransform); ok && ct.HasColorTransform() {
		errs = append(errs, ct.SetColorTransform(ctm))
	}
	return errors.Join(errs...)
}

func (c *Compositor) ApplyColorMatrix(o *output.Output, m backend.Matrix) error {
	b := o.Backend()
	if m == backend.Identity {
		delete(c.ctms, b)
	} else {
		c.ctms[b] = m
	}
	hc := c.hdr[b]
	if hc == nil {
		ct, ok := b.(backend.ColorTransform)
		switch {
		case ok && ct.HasColorTransform():
			return ct.SetColorTransform(m)
		case m == backend.Identity:
			return nil
		}
		return fmt.Errorf("%s has no colour transform", b.Name())
	}
	hc.signal = hc.signal.WithMatrix(matrix32(m))
	c.recolorCursor(o)
	c.sceneOf(o).invalidate()
	return c.schedule(o)
}

func (c *Compositor) GammaBlocked(o *output.Output) bool {
	return c.hdr[o.Backend()] != nil
}

func (c *Compositor) OutputDescription(o *output.Output) *colorspace.Description {
	if hc := c.hdr[o.Backend()]; hc != nil {
		return hc.signal.Description()
	}
	return colorspace.SDR
}

func (c *Compositor) PreferredDescription(s *surface.Surface) *colorspace.Description {
	for _, e := range c.entered {
		if e.s == s && len(e.outputs) > 0 {
			return c.OutputDescription(e.outputs[0])
		}
	}
	if len(c.outputs) > 0 {
		return c.OutputDescription(c.outputs[0])
	}
	return colorspace.SDR
}

func (c *Compositor) syncColorManagement() error {
	wanted := c.colorManagement
	if !wanted {
		wanted = c.outputRules.Fallback.HDR != HDROff
		for _, r := range c.outputRules.Named {
			wanted = wanted || r.HDR != HDROff
		}
	}
	if !wanted && c.colorManager == nil {
		return nil
	}
	if c.colorManager == nil {
		c.colorManager = colormanager.New(c.display, c)
	}
	return c.colorManager.Advertise(wanted)
}
