package drm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/stubbedev/lys-wm/internal/backend"
	kdrm "github.com/stubbedev/lys-wm/internal/drm"
)

var (
	_ backend.Gamma          = (*Output)(nil)
	_ backend.ColorTransform = (*Output)(nil)
)

type colorProp uint8

const (
	gammaLUT colorProp = iota
	colorMatrix
	colorProps
)

var colorPropNames = [colorProps]string{gammaLUT: "GAMMA_LUT", colorMatrix: "CTM"}

func (p colorProp) String() string { return colorPropNames[p] }

type colorBlob struct {
	current uint32
	next    uint32
	dirty   bool
}

type colorBlobs [colorProps]colorBlob

func (c *colorBlobs) dirty() bool {
	for _, b := range c {
		if b.dirty {
			return true
		}
	}
	return false
}

func (o *Output) GammaSize() int {
	if _, ok := o.crtc.props[gammaLUT.String()]; !ok {
		return 0
	}
	return int(o.crtc.props["GAMMA_LUT_SIZE"].Value) //nolint:gosec // a LUT size is a small count
}

func lut(ramps []uint16) []byte {
	n := len(ramps) / 3
	b := make([]byte, 0, 8*n)
	for i := range n {
		for _, v := range []uint16{ramps[i], ramps[n+i], ramps[2*n+i], 0} {
			b = binary.NativeEndian.AppendUint16(b, v)
		}
	}
	return b
}

func (o *Output) SetGamma(ramps []uint16) error {
	if size := o.GammaSize(); ramps != nil && len(ramps) != 3*size {
		return fmt.Errorf("drm: %s takes 3 ramps of %d gamma entries, not %d entries", o.Name(), size, len(ramps))
	}
	if ramps == nil {
		return o.setColor(gammaLUT, nil)
	}
	return o.setColor(gammaLUT, lut(ramps))
}

func (o *Output) HasColorTransform() bool {
	_, ok := o.crtc.props[colorMatrix.String()]
	return ok
}

func ctm(m backend.Matrix) []byte {
	b := make([]byte, 0, 8*len(m))
	for _, v := range m {
		b = binary.NativeEndian.AppendUint64(b, s3132(v))
	}
	return b
}

func s3132(v float64) uint64 {
	const sign = 1 << 63
	magnitude := uint64(math.Round(math.Abs(v) * (1 << 32)))
	if math.Signbit(v) && magnitude != 0 {
		return magnitude | sign
	}
	return magnitude
}

func (o *Output) SetColorTransform(m backend.Matrix) error {
	if !o.HasColorTransform() {
		return fmt.Errorf("drm: the CRTC of %s has no CTM property", o.Name())
	}
	if m == backend.Identity {
		return o.setColor(colorMatrix, nil)
	}
	return o.setColor(colorMatrix, ctm(m))
}

func (o *Output) setColor(p colorProp, data []byte) error {
	var blob uint32
	if data != nil {
		var err error
		if blob, err = o.b.dev.CreateBlob(data); err != nil {
			return fmt.Errorf("drm: the %s of %s: %w", p, o.Name(), err)
		}
	}
	if o.lit && !o.off && !o.b.paused {
		r := &o.b.req
		r.Reset()
		o.crtc.set(r, p.String(), uint64(blob))
		if err := o.test(kdrm.AtomicNonblock); err != nil {
			return errors.Join(fmt.Errorf("drm: %s refuses the %s: %w", o.Name(), p, err), o.destroyBlob(blob))
		}
	}
	c := &o.color[p]
	err := o.dropPending(c)
	c.next, c.dirty = blob, true
	return errors.Join(err, o.flushPlanes())
}

func (o *Output) destroyBlob(blob uint32) error {
	if blob == 0 {
		return nil
	}
	return o.b.dev.DestroyBlob(blob)
}

func (o *Output) dropPending(c *colorBlob) error {
	if !c.dirty {
		return nil
	}
	c.dirty = false
	return o.destroyBlob(c.next)
}

func (o *Output) addColor() {
	for p := range colorProps {
		c := o.color[p]
		switch {
		case c.dirty:
			o.crtc.set(&o.b.req, p.String(), uint64(c.next))
		case o.modeset:
			o.crtc.set(&o.b.req, p.String(), uint64(c.current))
		}
	}
}

func (o *Output) colorSent() error {
	var errs []error
	for p := range colorProps {
		c := &o.color[p]
		if !c.dirty {
			continue
		}
		old := c.current
		c.current, c.dirty = c.next, false
		errs = append(errs, o.destroyBlob(old))
	}
	return errors.Join(errs...)
}

func (o *Output) releaseColor() error {
	var errs []error
	for p := range colorProps {
		c := &o.color[p]
		errs = append(errs, o.dropPending(c), o.destroyBlob(c.current))
	}
	o.color = colorBlobs{}
	return errors.Join(errs...)
}
