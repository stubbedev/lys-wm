package compositor

import (
	"errors"
	"fmt"
	"math"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/output"
)

type profile struct {
	desc   *colorspace.Description
	target *colorspace.Target
	kms    bool
	luts   crtcLUTs
	ramps  []uint16
}

type crtcLUTs struct {
	gamma   backend.Gamma
	degamma backend.Degamma
}

var errNoConversion = errors.New("its primaries convert from sRGB to nothing")

type CursorColorBypass interface {
	CursorSkipsCRTCColor() bool
}

func (c *Compositor) applyProfile(b backend.Output, rule OutputRule) error {
	prev := c.profiles[b]
	if rule.ICC == nil {
		if prev == nil {
			return nil
		}
		delete(c.profiles, b)
		return c.profileChanged(b, prev, nil)
	}
	if prev != nil && prev.desc == rule.ICC {
		return nil
	}
	luts, kms := profilesInKMS(b)
	p := &profile{desc: rule.ICC, target: colorspace.ProfileTarget(rule.ICC), kms: kms, luts: luts}
	if prev != nil {
		p.ramps = prev.ramps
	}
	c.profiles[b] = p
	return c.profileChanged(b, prev, p)
}

func profilesInKMS(b backend.Output) (crtcLUTs, bool) {
	g, okG := b.(backend.Gamma)
	d, okD := b.(backend.Degamma)
	ct, okC := b.(backend.ColorTransform)
	ok := okG && okD && okC && g.GammaSize() > 0 && d.DegammaSize() > 0 && ct.HasColorTransform()
	return crtcLUTs{gamma: g, degamma: d}, ok
}

func (c *Compositor) profileChanged(b backend.Output, prev, next *profile) error {
	var errs []error
	switch {
	case next != nil && next.kms:
		errs = append(errs, c.programProfile(b, next))
	case prev != nil && prev.kms:
		errs = append(errs, prev.luts.degamma.SetDegamma(nil), c.applySDRMatrix(b), prev.luts.gamma.SetGamma(prev.ramps))
	}
	if o := c.output(b); o != nil {
		c.sceneOf(o).invalidate()
		c.recolorCursor(o)
		if c.colorManager != nil {
			errs = append(errs, c.colorManager.OutputChanged(o))
		}
		errs = append(errs, c.schedule(o))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("compositor: the ICC profile of %s: %w", b.Name(), err)
	}
	return nil
}

func (c *Compositor) programProfile(b backend.Output, p *profile) error {
	d, g := p.luts.degamma, p.luts.gamma
	return errors.Join(d.SetDegamma(ramps(colorspace.SDR.DecodeRamp(d.DegammaSize()))), c.applySDRMatrix(b), g.SetGamma(composeGamma(p.target.EncodeRamp(g.GammaSize()), p.ramps)))
}

func (c *Compositor) applySDRMatrix(b backend.Output) error {
	m := backend.Identity
	if user, ok := c.ctms[b]; ok {
		m = user
	}
	if p := c.profiles[b]; p != nil && p.kms {
		conv, ok := colorspace.Conversion(colorspace.SRGB, p.desc.Primaries)
		if !ok {
			return errNoConversion
		}
		m = mulMatrix(m, backend.Matrix(conv))
	}
	ct, ok := b.(backend.ColorTransform)
	switch {
	case ok && ct.HasColorTransform():
		return ct.SetColorTransform(m)
	case m == backend.Identity:
		return nil
	}
	return fmt.Errorf("%s has no colour transform", b.Name())
}

func mulMatrix(a, b backend.Matrix) backend.Matrix {
	var m backend.Matrix
	for r := range 3 {
		for k := range 3 {
			m[r*3+k] = a[r*3]*b[k] + a[r*3+1]*b[3+k] + a[r*3+2]*b[6+k]
		}
	}
	return m
}

func ramps(values []float64) []uint16 {
	out := make([]uint16, 3*len(values))
	for i, v := range values {
		code := uint16(math.Round(min(max(v, 0), 1) * math.MaxUint16))
		out[i], out[len(values)+i], out[2*len(values)+i] = code, code, code
	}
	return out
}

func composeGamma(encode []float64, client []uint16) []uint16 {
	n := len(encode)
	out := ramps(encode)
	if client == nil {
		return out
	}
	for ch := range 3 {
		curve := client[ch*n : (ch+1)*n]
		for i, v := range encode {
			out[ch*n+i] = sample(curve, v)
		}
	}
	return out
}

func sample(curve []uint16, x float64) uint16 {
	pos := min(max(x, 0), 1) * float64(len(curve)-1)
	lo := int(pos)
	if lo >= len(curve)-1 {
		return curve[len(curve)-1]
	}
	f := pos - float64(lo)
	return uint16(math.Round(float64(curve[lo])*(1-f) + float64(curve[lo+1])*f))
}

type profileGamma struct {
	c *Compositor
	b backend.Output
	g backend.Gamma
}

func (g profileGamma) GammaSize() int {
	return g.g.GammaSize()
}

func (g profileGamma) SetGamma(r []uint16) error {
	p := g.c.profiles[g.b]
	if p == nil || !p.kms {
		return g.g.SetGamma(r)
	}
	p.ramps = r
	return g.g.SetGamma(composeGamma(p.target.EncodeRamp(g.GammaSize()), r))
}

func (c *Compositor) OutputGamma(o *output.Output) (backend.Gamma, bool) {
	g, ok := o.Backend().(backend.Gamma)
	if !ok {
		return nil, false
	}
	return profileGamma{c: c, b: o.Backend(), g: g}, true
}

func (c *Compositor) shaderProfile(o *output.Output) *profile {
	if p := c.profiles[o.Backend()]; p != nil && !p.kms {
		return p
	}
	return nil
}

func (c *Compositor) cursorProfile(o *output.Output) *profile {
	p := c.profiles[o.Backend()]
	if p == nil {
		return nil
	}
	if bypass, ok := o.Backend().(CursorColorBypass); !p.kms || ok && bypass.CursorSkipsCRTCColor() {
		return p
	}
	return nil
}

func (c *Compositor) sdrOut(o *output.Output) colorOut {
	if p := c.shaderProfile(o); p != nil {
		return colorOut{plans: &c.plans, to: p.target}
	}
	return colorOut{plans: &c.plans}
}
