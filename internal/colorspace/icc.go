package colorspace

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	lgpu "github.com/stubbedev/lys-lib/gpu"
	"github.com/stubbedev/lys-lib/third_party/gomath"
)

var ErrICC = errors.New("colorspace: ICC profile")

const (
	iccHeader     = 128
	iccTagEntry   = 12
	curveTolerate = 1.0 / 255
)

func ParseICC(b []byte) (*Description, error) {
	tags, err := iccTags(b)
	if err != nil {
		return nil, err
	}
	var col [3][3]float64
	for i, name := range [3]string{"rXYZ", "gXYZ", "bXYZ"} {
		data, ok := tags[name]
		if !ok {
			return nil, fmt.Errorf("%w: no %s colorant, so no matrix-shaper model", ErrICC, name)
		}
		if col[i], err = iccXYZ(data); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrICC, name, err)
		}
	}
	var curves [3]lgpu.ColorFunc
	for i, name := range [3]string{"rTRC", "gTRC", "bTRC"} {
		data, ok := tags[name]
		if !ok {
			return nil, fmt.Errorf("%w: no %s tone curve, so no matrix-shaper model", ErrICC, name)
		}
		if curves[i], err = iccCurve(data); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrICC, name, err)
		}
	}
	for _, c := range curves[1:] {
		if !sameCurve(curves[0], c) {
			return nil, fmt.Errorf("%w: its channels have different tone curves", ErrICC)
		}
	}
	p := Primaries{R: chromaticity(col[0]), G: chromaticity(col[1]), B: chromaticity(col[2]), W: chromaticity([3]float64{col[0][0] + col[1][0] + col[2][0], col[0][1] + col[1][1] + col[2][1], col[0][2] + col[1][2] + col[2][2]})}
	d := withTarget(Description{TF: TF{Name: Curve, Curve: curves[0]}, Primaries: p, MinLum: 0.2, MaxLum: 80, RefLum: 80})
	if !d.Check() {
		return nil, fmt.Errorf("%w: its colorants span no colour space", ErrICC)
	}
	return d, nil
}

func iccTags(b []byte) (map[string][]byte, error) {
	if len(b) < iccHeader+4 || string(b[36:40]) != "acsp" {
		return nil, fmt.Errorf("%w: %d bytes without the profile signature", ErrICC, len(b))
	}
	if v := b[8]; v != 2 && v != 4 {
		return nil, fmt.Errorf("%w: version %d is neither 2 nor 4", ErrICC, v)
	}
	if class := string(b[12:16]); class != "mntr" && class != "spac" {
		return nil, fmt.Errorf("%w: class %q is neither Display nor ColorSpace", ErrICC, class)
	}
	if space := string(b[16:20]); space != "RGB " {
		return nil, fmt.Errorf("%w: colour space %q is not RGB", ErrICC, space)
	}
	if pcs := string(b[20:24]); pcs != "XYZ " && pcs != "Lab " {
		return nil, fmt.Errorf("%w: connection space %q is neither XYZ nor Lab", ErrICC, pcs)
	}
	n := int(binary.BigEndian.Uint32(b[iccHeader:]))
	if n < 0 || n > (len(b)-iccHeader-4)/iccTagEntry {
		return nil, fmt.Errorf("%w: a tag table of %d entries overruns %d bytes", ErrICC, n, len(b))
	}
	tags := make(map[string][]byte, n)
	for i := range n {
		e := b[iccHeader+4+i*iccTagEntry:]
		off, size := uint64(binary.BigEndian.Uint32(e[4:])), uint64(binary.BigEndian.Uint32(e[8:]))
		if off+size > uint64(len(b)) {
			return nil, fmt.Errorf("%w: tag %q overruns the profile", ErrICC, e[:4])
		}
		tags[string(e[:4])] = b[off : off+size]
	}
	return tags, nil
}

func s15(b []byte) float64 {
	return float64(int32(binary.BigEndian.Uint32(b))) / 65536 //nolint:gosec // s15Fixed16 is a signed 32-bit value
}

func iccXYZ(d []byte) ([3]float64, error) {
	if len(d) < 20 || string(d[:4]) != "XYZ " {
		return [3]float64{}, errors.New("not an XYZ type tag")
	}
	return [3]float64{s15(d[8:]), s15(d[12:]), s15(d[16:])}, nil
}

func chromaticity(c [3]float64) XY {
	s := c[0] + c[1] + c[2]
	if s == 0 {
		return XY{}
	}
	return XY{c[0] / s, c[1] / s}
}

func iccCurve(d []byte) (lgpu.ColorFunc, error) {
	if len(d) < 12 {
		return lgpu.ColorFunc{}, errors.New("a curve tag too short")
	}
	switch string(d[:4]) {
	case "curv":
		return iccTable(d)
	case "para":
		return iccParametric(d)
	}
	return lgpu.ColorFunc{}, fmt.Errorf("curve type %q", d[:4])
}

func iccTable(d []byte) (lgpu.ColorFunc, error) {
	n := int(binary.BigEndian.Uint32(d[8:]))
	switch {
	case n == 0:
		return lgpu.ColorFunc{Kind: lgpu.ColorLinear}, nil
	case n < 0 || 12+2*n > len(d):
		return lgpu.ColorFunc{}, fmt.Errorf("a table of %d entries overruns %d bytes", n, len(d))
	case n == 1:
		return curve(float64(binary.BigEndian.Uint16(d[12:]))/256, 1, 0, 0, 0, 0, 0), nil
	}
	table := make([]float64, n)
	for i := range table {
		table[i] = float64(binary.BigEndian.Uint16(d[12+2*i:])) / 65535
	}
	return fitTable(table)
}

func fitTable(table []float64) (lgpu.ColorFunc, error) {
	srgb := curve(2.4, 1/1.055, 0.055/1.055, 1/12.92, 0.04045, 0, 0)
	if fits(table, srgb) {
		return srgb, nil
	}
	var sum, weight float64
	last := float64(len(table) - 1)
	for i, v := range table {
		x := float64(i) / last
		if x > 0.05 && x < 0.95 && v > 0 {
			sum += float64(gomath.Log(v) * gomath.Log(x))
			weight += float64(gomath.Log(x) * gomath.Log(x))
		}
	}
	if weight > 0 {
		if g := curve(sum/weight, 1, 0, 0, 0, 0, 0); fits(table, g) {
			return g, nil
		}
	}
	return lgpu.ColorFunc{}, errors.New("a table neither the sRGB curve nor a gamma fits")
}

func fits(table []float64, f lgpu.ColorFunc) bool {
	t := lgpu.ColorTransform{Decode: f, Matrix: unitMatrix, Scale: 1}
	last := float32(len(table) - 1)
	for i, v := range table {
		x := float32(i) / last
		if math.Abs(float64(t.Apply([4]float32{x, x, x, 1})[0])-v) > curveTolerate {
			return false
		}
	}
	return true
}

func iccParametric(d []byte) (lgpu.ColorFunc, error) {
	kind := binary.BigEndian.Uint16(d[8:])
	counts := [...]int{1, 3, 4, 5, 7}
	if int(kind) >= len(counts) || 12+4*counts[kind] > len(d) {
		return lgpu.ColorFunc{}, fmt.Errorf("parametric curve type %d in %d bytes", kind, len(d))
	}
	p := make([]float64, 7)
	for i := range counts[kind] {
		p[i] = s15(d[12+4*i:])
	}
	g, a, b, c, dd, e, f := p[0], p[1], p[2], p[3], p[4], p[5], p[6]
	switch kind {
	case 0:
		return curve(g, 1, 0, 0, 0, 0, 0), nil
	case 1:
		return curve(g, a, b, 0, -b/a, 0, 0), nil
	case 2:
		return curve(g, a, b, 0, -b/a, c, c), nil
	case 3:
		return curve(g, a, b, c, dd, 0, 0), nil
	}
	return curve(g, a, b, c, dd, e, f), nil
}

func sameCurve(x, y lgpu.ColorFunc) bool {
	const close = 1e-4
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < close }
	return x.Kind == y.Kind && near(x.G, y.G) && near(x.A, y.A) && near(x.B, y.B) && near(x.C, y.C) && near(x.D, y.D) && near(x.E, y.E) && near(x.F, y.F)
}

func inverse(f lgpu.ColorFunc) lgpu.ColorFunc {
	if f.Kind != lgpu.ColorCurve {
		return f
	}
	g, a, b, c, d, e, ff := float64(f.G), float64(f.A), float64(f.B), float64(f.C), float64(f.D), float64(f.E), float64(f.F)
	ag := gomath.Pow(a, -g)
	inv := lgpu.ColorFunc{Kind: lgpu.ColorCurve, G: float32(1 / g), A: float32(ag), B: float32(-e * ag), E: float32(-b / a), D: float32(gomath.Pow(a*d+b, g) + e)}
	if c != 0 {
		inv.C, inv.F, inv.D = float32(1/c), float32(-ff/c), float32(c*d+ff)
	}
	return inv
}

func ProfileTarget(d *Description) *Target {
	return &Target{desc: d, encode: inverse(d.decode()), matrix: unitMatrix}
}

func (t *Target) EncodeRamp(n int) []float64 {
	return ramp(lgpu.ColorTransform{Matrix: unitMatrix, Scale: 1, Encode: t.encode}, n)
}

func (d *Description) DecodeRamp(n int) []float64 {
	return ramp(lgpu.ColorTransform{Decode: d.decode(), Matrix: unitMatrix, Scale: 1}, n)
}

func ramp(t lgpu.ColorTransform, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		x := float32(i) / float32(n-1)
		out[i] = float64(t.Apply([4]float32{x, x, x, 1})[0])
	}
	return out
}

func Conversion(from, to Primaries) ([9]float64, bool) {
	m, ok := conversion(from, to)
	return [9]float64(m), ok
}

func (t *Target) encodeOne(v float64) float64 {
	tr := lgpu.ColorTransform{Matrix: unitMatrix, Scale: 1, Encode: t.encode}
	return float64(tr.Apply([4]float32{float32(v), float32(v), float32(v), 1})[0])
}
