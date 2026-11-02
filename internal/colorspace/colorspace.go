// Package colorspace is the colour maths of HDR output and colour-managed
// surfaces: what a pixel encoding means, what an output's encoding is, and the
// transform from one to the other. It is pure: everything here is a function
// of its arguments, and the renderers evaluate the transforms it plans.
package colorspace

import (
	"math"

	lgpu "github.com/stubbedev/lys-lib/gpu"
	"github.com/stubbedev/lys-lib/third_party/gomath"
)

type XY struct {
	X, Y float64
}

type Primaries struct {
	R, G, B, W XY
}

var (
	d65 = XY{0.3127, 0.3290}

	SRGB        = Primaries{XY{0.64, 0.33}, XY{0.30, 0.60}, XY{0.15, 0.06}, d65}
	BT2020      = Primaries{XY{0.708, 0.292}, XY{0.170, 0.797}, XY{0.131, 0.046}, d65}
	DisplayP3   = Primaries{XY{0.680, 0.320}, XY{0.265, 0.690}, XY{0.150, 0.060}, d65}
	DCIP3       = Primaries{XY{0.680, 0.320}, XY{0.265, 0.690}, XY{0.150, 0.060}, XY{0.314, 0.351}}
	AdobeRGB    = Primaries{XY{0.64, 0.33}, XY{0.21, 0.71}, XY{0.15, 0.06}, d65}
	PALM        = Primaries{XY{0.67, 0.33}, XY{0.21, 0.71}, XY{0.14, 0.08}, XY{0.310, 0.316}}
	PAL         = Primaries{XY{0.64, 0.33}, XY{0.29, 0.60}, XY{0.15, 0.06}, d65}
	NTSC        = Primaries{XY{0.630, 0.340}, XY{0.310, 0.595}, XY{0.155, 0.070}, d65}
	GenericFilm = Primaries{XY{0.681, 0.319}, XY{0.243, 0.692}, XY{0.145, 0.049}, XY{0.310, 0.316}}
	CIE1931XYZ  = Primaries{XY{1, 0}, XY{0, 1}, XY{0, 0}, XY{1.0 / 3, 1.0 / 3}}
)

type matrix [9]float64

func (a matrix) mul(b matrix) matrix {
	var m matrix
	for r := range 3 {
		for c := range 3 {
			m[r*3+c] = float64(a[r*3]*b[c]) + float64(a[r*3+1]*b[3+c]) + float64(a[r*3+2]*b[6+c])
		}
	}
	return m
}

func (a matrix) apply(v [3]float64) [3]float64 {
	return [3]float64{
		float64(a[0]*v[0]) + float64(a[1]*v[1]) + float64(a[2]*v[2]),
		float64(a[3]*v[0]) + float64(a[4]*v[1]) + float64(a[5]*v[2]),
		float64(a[6]*v[0]) + float64(a[7]*v[1]) + float64(a[8]*v[2]),
	}
}

func (a matrix) invert() (matrix, bool) {
	det := float64(a[0]*(float64(a[4]*a[8])-float64(a[5]*a[7]))) - float64(a[1]*(float64(a[3]*a[8])-float64(a[5]*a[6]))) + float64(a[2]*(float64(a[3]*a[7])-float64(a[4]*a[6])))
	if math.Abs(det) < 1e-12 || math.IsNaN(det) {
		return matrix{}, false
	}
	k := 1 / det
	return matrix{
		float64(k * (float64(a[4]*a[8]) - float64(a[5]*a[7]))), float64(k * (float64(a[2]*a[7]) - float64(a[1]*a[8]))), float64(k * (float64(a[1]*a[5]) - float64(a[2]*a[4]))),
		float64(k * (float64(a[5]*a[6]) - float64(a[3]*a[8]))), float64(k * (float64(a[0]*a[8]) - float64(a[2]*a[6]))), float64(k * (float64(a[2]*a[3]) - float64(a[0]*a[5]))),
		float64(k * (float64(a[3]*a[7]) - float64(a[4]*a[6]))), float64(k * (float64(a[1]*a[6]) - float64(a[0]*a[7]))), float64(k * (float64(a[0]*a[4]) - float64(a[1]*a[3]))),
	}, true
}

func (c XY) xyz() [3]float64 {
	return [3]float64{c.X / c.Y, 1, (1 - c.X - c.Y) / c.Y}
}

func toXYZ(p Primaries) (matrix, bool) {
	col := func(c XY) [3]float64 { return [3]float64{c.X, c.Y, 1 - c.X - c.Y} }
	r, g, b := col(p.R), col(p.G), col(p.B)
	m := matrix{r[0], g[0], b[0], r[1], g[1], b[1], r[2], g[2], b[2]}
	inv, ok := m.invert()
	if !ok || p.W.Y <= 0 {
		return matrix{}, false
	}
	s := inv.apply(p.W.xyz())
	return m.mul(matrix{s[0], 0, 0, 0, s[1], 0, 0, 0, s[2]}), true
}

var bradford = matrix{0.8951, 0.2664, -0.1614, -0.7502, 1.7135, 0.0367, 0.0389, -0.0685, 1.0296}

func adaptation(from, to XY) matrix {
	if from == to {
		return matrix{1, 0, 0, 0, 1, 0, 0, 0, 1}
	}
	inv, _ := bradford.invert()
	a, b := bradford.apply(from.xyz()), bradford.apply(to.xyz())
	return inv.mul(matrix{b[0] / a[0], 0, 0, 0, b[1] / a[1], 0, 0, 0, b[2] / a[2]}).mul(bradford)
}

func conversion(from, to Primaries) (matrix, bool) {
	if from == to {
		return matrix{1, 0, 0, 0, 1, 0, 0, 0, 1}, true
	}
	src, ok := toXYZ(from)
	if !ok {
		return matrix{}, false
	}
	dst, ok := toXYZ(to)
	if !ok {
		return matrix{}, false
	}
	inv, ok := dst.invert()
	if !ok {
		return matrix{}, false
	}
	return inv.mul(adaptation(from.W, to.W)).mul(src), true
}

func (p Primaries) Valid() bool {
	_, ok := toXYZ(p)
	return ok
}

type TFName uint8

const (
	Gamma22 TFName = iota + 1
	Gamma28
	Compound24
	ExtSRGB
	BT1886
	ST240
	ST428
	ExtLinear
	PQ
	HLG
	Power
	Curve
)

type TF struct {
	Name     TFName
	Exponent float64
	Curve    lgpu.ColorFunc
}

type Description struct {
	TF              TF
	Primaries       Primaries
	MinLum          float64
	MaxLum          float64
	RefLum          float64
	TargetPrimaries Primaries
	TargetMin       float64
	TargetMax       float64
	MaxCLL          float64
	MaxFALL         float64
}

func Luminances(tf TFName) (minLum, maxLum, ref float64) {
	switch tf {
	case BT1886:
		return 0.01, 100, 100
	case PQ:
		return 0.005, 10000.005, 203
	case HLG:
		return 0.005, 1000, 203
	}
	return 0.2, 80, 80
}

var SDR = withTarget(Description{TF: TF{Name: Gamma22}, Primaries: SRGB, MinLum: 0.2, MaxLum: 80, RefLum: 80})

var SCRGB = withTargetVolume(Description{TF: TF{Name: ExtLinear}, Primaries: SRGB, MinLum: 0, MaxLum: 80, RefLum: 203}, BT2020, 0, 10000)

var WindowsBT2100 = withTarget(Description{TF: TF{Name: PQ}, Primaries: BT2020, MinLum: 0, MaxLum: 10000, RefLum: 203})

func withTarget(d Description) *Description {
	return withTargetVolume(d, d.Primaries, d.MinLum, d.MaxLum)
}

func withTargetVolume(d Description, p Primaries, minLum, maxLum float64) *Description {
	d.TargetPrimaries, d.TargetMin, d.TargetMax = p, minLum, maxLum
	return &d
}

func (d *Description) peak() float64 {
	nits := d.TargetMax
	if d.MaxCLL > 0 {
		nits = d.MaxCLL
	}
	return nits / d.RefLum
}

func (d *Description) sdrLike() bool {
	return d == SDR || d.Primaries == SRGB && (d.TF.Name == Gamma22 || d.TF.Name == Compound24) && d.MaxLum == d.RefLum
}

func curve(g, a, b, c, dd, e, f float64) lgpu.ColorFunc {
	return lgpu.ColorFunc{Kind: lgpu.ColorCurve, G: float32(g), A: float32(a), B: float32(b), C: float32(c), D: float32(dd), E: float32(e), F: float32(f)}
}

func (d *Description) decode() lgpu.ColorFunc {
	switch d.TF.Name {
	case Gamma22:
		return curve(2.2, 1, 0, 0, 0, 0, 0)
	case Gamma28:
		return curve(2.8, 1, 0, 0, 0, 0, 0)
	case Compound24, ExtSRGB:
		return curve(2.4, 1/1.055, 0.055/1.055, 1/12.92, 0.04045, 0, 0)
	case BT1886:
		return bt1886(d.MinLum, d.MaxLum)
	case ST240:
		return curve(1/0.45, 1/1.1115, 0.1115/1.1115, 1.0/4, 0.0913, 0, 0)
	case ST428:
		return curve(2.6, gomath.Pow(52.37/48, 1/2.6), 0, 0, 0, 0, 0)
	case PQ:
		return lgpu.ColorFunc{Kind: lgpu.ColorPQ}
	case HLG:
		return lgpu.ColorFunc{Kind: lgpu.ColorHLG}
	case Power:
		return curve(d.TF.Exponent, 1, 0, 0, 0, 0, 0)
	case Curve:
		return d.TF.Curve
	}
	return lgpu.ColorFunc{Kind: lgpu.ColorLinear}
}

func bt1886(lb, lw float64) lgpu.ColorFunc {
	const g = 2.4
	black, white := gomath.Pow(lb, 1/g), gomath.Pow(lw, 1/g)
	a, b := (white-black)/white, black/white
	s := 1 / (1 - gomath.Pow(b, g))
	k := gomath.Pow(s, 1/g)
	return curve(g, float64(a*k), float64(b*k), 0, 0, -float64(gomath.Pow(b, g)*s), 0)
}

func (d *Description) unit() float64 {
	if d.TF.Name == PQ {
		return 10000
	}
	return d.MaxLum - d.MinLum
}

type Target struct {
	desc   *Description
	encode lgpu.ColorFunc
	matrix [9]float32
}

var unitMatrix = [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1}

var SDRTarget = &Target{desc: SDR, encode: curve(1/2.2, 1, 0, 0, 0, 0, 0), matrix: unitMatrix}

type Output struct {
	SDRBrightness float64
	Peak          float64
	MinLum        float64
	MaxAverage    float64
	Panel         Primaries
}

func Working(o Output) *Target {
	d := withTargetVolume(Description{TF: TF{Name: ExtLinear}, Primaries: BT2020, MinLum: 0, MaxLum: o.SDRBrightness, RefLum: o.SDRBrightness}, BT2020, 0, o.Peak)
	return &Target{desc: d, encode: lgpu.ColorFunc{Kind: lgpu.ColorLinear}, matrix: unitMatrix}
}

func Signal(o Output, ctm [9]float32) *Target {
	d := withTargetVolume(Description{TF: TF{Name: PQ}, Primaries: BT2020, MinLum: 0, MaxLum: 10000, RefLum: o.SDRBrightness, MaxCLL: o.Peak, MaxFALL: o.MaxAverage}, o.Panel, o.MinLum, o.Peak)
	return &Target{desc: d, encode: lgpu.ColorFunc{Kind: lgpu.ColorPQ}, matrix: ctm}
}

func (t *Target) WithMatrix(ctm [9]float32) *Target {
	return &Target{desc: t.desc, encode: t.encode, matrix: ctm}
}

func (t *Target) Description() *Description {
	return t.desc
}

const kneeShare = 0.75

func Plan(src *Description, t *Target) *lgpu.ColorTransform {
	if src == nil {
		src = SDR
	}
	if src == t.desc || t == SDRTarget && src.sdrLike() {
		return nil
	}
	conv, ok := conversion(src.Primaries, t.desc.Primaries)
	if !ok {
		return nil
	}
	gain := src.unit() / (src.RefLum - src.MinLum)
	out := &lgpu.ColorTransform{
		Decode: src.decode(),
		Scale:  float32((t.desc.RefLum - t.desc.MinLum) / t.desc.unit()),
		Encode: t.encode,
	}
	for i, v := range conv {
		out.Matrix[i] = float32(float64(v * gain))
	}
	if t.matrix != unitMatrix {
		out.Matrix = mulMatrix32(t.matrix, out.Matrix)
	}
	if src.TF.Name == HLG {
		y := luma(src.Primaries)
		out.Luma = [3]float32{float32(y[0]), float32(y[1]), float32(y[2])}
		out.HLGGamma = float32(1.2 + float64(0.42*gomath.Log10(src.MaxLum/1000)))
	}
	if top, p := t.desc.peak(), src.peak(); p > top {
		knee := float64(kneeShare * top)
		if p := p - knee; p > 0 {
			d := top - knee
			out.Knee, out.Compress = float32(knee), float32((p-d)/float64(p*d))
		}
	}
	return out
}

func mulMatrix32(a, b [9]float32) [9]float32 {
	var m [9]float32
	for r := range 3 {
		for c := range 3 {
			m[r*3+c] = float32(a[r*3]*b[c]) + float32(a[r*3+1]*b[3+c]) + float32(a[r*3+2]*b[6+c])
		}
	}
	return m
}

func luma(p Primaries) [3]float64 {
	m, ok := toXYZ(p)
	if !ok {
		return [3]float64{0.2627, 0.678, 0.0593}
	}
	return [3]float64{m[3], m[4], m[5]}
}

func (d *Description) Check() bool {
	return d.Primaries.Valid() && d.TargetPrimaries.Valid() && d.RefLum > d.MinLum
}

func Passthrough(src *Description, t *Target) bool {
	if src == nil {
		src = SDR
	}
	if t == SDRTarget {
		return src.sdrLike()
	}
	if t.desc.TF.Name != src.TF.Name || t.matrix != unitMatrix {
		return false
	}
	return src.Primaries == t.desc.Primaries && src.RefLum == t.desc.RefLum && src.peak() <= t.desc.peak() && t.encode.Kind == lgpu.ColorPQ
}

type Plans struct {
	plans map[planKey]*lgpu.ColorTransform
}

type planKey struct {
	src *Description
	t   *Target
}

const maxPlans = 256

func (ps *Plans) Of(src *Description, t *Target) *lgpu.ColorTransform {
	k := planKey{src, t}
	if p, ok := ps.plans[k]; ok {
		return p
	}
	if ps.plans == nil || len(ps.plans) >= maxPlans {
		ps.plans = make(map[planKey]*lgpu.ColorTransform, 8)
	}
	p := Plan(src, t)
	ps.plans[k] = p
	return p
}
