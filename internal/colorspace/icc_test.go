package colorspace

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

type iccTag struct {
	name string
	data []byte
}

func s15bytes(v float64) []byte {
	return binary.BigEndian.AppendUint32(nil, uint32(int32(math.Round(v*65536))))
}

func xyzTag(x, y, z float64) []byte {
	b := append([]byte("XYZ "), 0, 0, 0, 0)
	b = append(b, s15bytes(x)...)
	b = append(b, s15bytes(y)...)
	return append(b, s15bytes(z)...)
}

func gammaTag(g float64) []byte {
	b := append([]byte("curv"), 0, 0, 0, 0, 0, 0, 0, 1)
	return binary.BigEndian.AppendUint16(b, uint16(math.Round(g*256)))
}

func tableTag(f func(float64) float64) []byte {
	const n = 1024
	b := append([]byte("curv"), 0, 0, 0, 0)
	b = binary.BigEndian.AppendUint32(b, n)
	for i := range n {
		b = binary.BigEndian.AppendUint16(b, uint16(math.Round(f(float64(i)/(n-1))*65535)))
	}
	return b
}

func paraTag(kind uint16, params ...float64) []byte {
	b := append([]byte("para"), 0, 0, 0, 0)
	b = binary.BigEndian.AppendUint16(b, kind)
	b = append(b, 0, 0)
	for _, p := range params {
		b = append(b, s15bytes(p)...)
	}
	return b
}

func profile(version byte, class, space string, tags ...iccTag) []byte {
	head := make([]byte, iccHeader)
	head[8] = version
	copy(head[12:], class)
	copy(head[16:], space)
	copy(head[20:], "XYZ ")
	copy(head[36:], "acsp")
	table := binary.BigEndian.AppendUint32(nil, uint32(len(tags)))
	off := iccHeader + 4 + len(tags)*iccTagEntry
	var body []byte
	for _, t := range tags {
		table = append(table, t.name...)
		table = binary.BigEndian.AppendUint32(table, uint32(off+len(body)))
		table = binary.BigEndian.AppendUint32(table, uint32(len(t.data)))
		body = append(body, t.data...)
	}
	return append(append(head, table...), body...)
}

func srgbColorants() []iccTag {
	return []iccTag{
		{"rXYZ", xyzTag(0.4360, 0.2225, 0.0139)},
		{"gXYZ", xyzTag(0.3851, 0.7169, 0.0971)},
		{"bXYZ", xyzTag(0.1431, 0.0606, 0.7139)},
	}
}

func withCurves(c []byte) []iccTag {
	return append(srgbColorants(), iccTag{"rTRC", c}, iccTag{"gTRC", c}, iccTag{"bTRC", c})
}

func TestICCMatrixShaperReadsItsPrimariesAndCurve(t *testing.T) {
	d, err := ParseICC(profile(4, "mntr", "RGB ", withCurves(paraTag(3, 2.4, 1/1.055, 0.055/1.055, 1/12.92, 0.04045))...))
	if err != nil {
		t.Fatal(err)
	}
	near(t, "white x (D50)", d.Primaries.W.X, 0.3457, 0.001)
	plan := Plan(d, Working(hdrOutput()))
	if plan == nil {
		t.Fatal("no plan")
	}
	white := through(plan, 1, 1, 1)
	for _, v := range white[:3] {
		near(t, "an ICC white adapts to the output's", float64(v), 1, 2e-3)
	}
	sdr := through(Plan(nil, Working(hdrOutput())), 0, 1, 0)
	icc := through(plan, 0, 1, 0)
	for i := range 3 {
		near(t, "ICC sRGB green matches sRGB green", float64(icc[i]), float64(sdr[i]), 0.02)
	}
}

func TestICCTableCurves(t *testing.T) {
	srgb := func(x float64) float64 {
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	for name, table := range map[string]func(float64) float64{
		"sRGB":      srgb,
		"gamma 1.8": func(x float64) float64 { return math.Pow(x, 1.8) },
	} {
		d, err := ParseICC(profile(2, "spac", "RGB ", withCurves(tableTag(table))...))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		plan := Plan(d, Working(hdrOutput()))
		grey := through(plan, 0.5, 0.5, 0.5)
		near(t, name, float64(grey[1]), table(0.5), 0.01)
	}
}

func TestICCGammaAndIdentityCurves(t *testing.T) {
	d, err := ParseICC(profile(2, "mntr", "RGB ", withCurves(gammaTag(2.2))...))
	if err != nil {
		t.Fatal(err)
	}
	near(t, "gamma 2.2", float64(d.TF.Curve.G), 2.2, 0.01)
	if _, err := ParseICC(profile(2, "mntr", "RGB ", withCurves(append([]byte("curv"), make([]byte, 8)...))...)); err != nil {
		t.Fatal(err)
	}
}

func TestICCRefusesWhatItCannotShow(t *testing.T) {
	mixed := append(srgbColorants(), iccTag{"rTRC", gammaTag(2.2)}, iccTag{"gTRC", gammaTag(1.8)}, iccTag{"bTRC", gammaTag(2.2)})
	s := tableTag(func(x float64) float64 { return x * x * (3 - 2*x) })
	for name, b := range map[string][]byte{
		"not a profile":        []byte("hello"),
		"version 5":            profile(5, "mntr", "RGB ", withCurves(gammaTag(2.2))...),
		"a printer":            profile(2, "prtr", "RGB ", withCurves(gammaTag(2.2))...),
		"CMYK":                 profile(2, "mntr", "CMYK", withCurves(gammaTag(2.2))...),
		"LUT only":             profile(2, "mntr", "RGB ", iccTag{"A2B0", []byte("mft2")}),
		"curves that differ":   profile(2, "mntr", "RGB ", mixed...),
		"a table nothing fits": profile(2, "mntr", "RGB ", withCurves(s)...),
		"colorants in a line":  profile(2, "mntr", "RGB ", iccTag{"rXYZ", xyzTag(0.1, 0.1, 0.1)}, iccTag{"gXYZ", xyzTag(0.2, 0.2, 0.2)}, iccTag{"bXYZ", xyzTag(0.3, 0.3, 0.3)}, iccTag{"rTRC", gammaTag(2.2)}, iccTag{"gTRC", gammaTag(2.2)}, iccTag{"bTRC", gammaTag(2.2)}),
	} {
		if _, err := ParseICC(b); !errors.Is(err, ErrICC) {
			t.Errorf("%s: %v, want ErrICC", name, err)
		}
	}
}

func TestICCTagsThatOverrunAreRefused(t *testing.T) {
	b := profile(2, "mntr", "RGB ", withCurves(gammaTag(2.2))...)
	binary.BigEndian.PutUint32(b[iccHeader+4+4:], uint32(len(b)))
	if _, err := ParseICC(b); !errors.Is(err, ErrICC) {
		t.Fatalf("%v", err)
	}
	binary.BigEndian.PutUint32(b[iccHeader:], 1<<30)
	if _, err := ParseICC(b); !errors.Is(err, ErrICC) {
		t.Fatalf("%v", err)
	}
}
