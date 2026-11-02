package colorspace

import (
	"math"
	"testing"

	lgpu "github.com/stubbedev/lys-lib/gpu"
)

func hdrOutput() Output {
	return Output{SDRBrightness: 203, Peak: 1000, MinLum: 0.005, MaxAverage: 400, Panel: DisplayP3}
}

func near(t *testing.T, what string, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Errorf("%s = %v, want %v within %v", what, got, want, tolerance)
	}
}

func through(tr *lgpu.ColorTransform, r, g, b float32) [4]float32 {
	return tr.Apply([4]float32{r, g, b, 1})
}

func TestConversionMatchesTheStandardMatrix(t *testing.T) {
	m, ok := conversion(SRGB, BT2020)
	if !ok {
		t.Fatal("no conversion")
	}
	for i, want := range [9]float64{0.6274, 0.3293, 0.0433, 0.0691, 0.9195, 0.0114, 0.0164, 0.0880, 0.8956} {
		near(t, "BT.709 to BT.2020", m[i], want, 0.001)
	}
	for r := range 3 {
		near(t, "white stays white", m[r*3]+m[r*3+1]+m[r*3+2], 1, 1e-6)
	}
}

func TestConversionAdaptsTheWhitePoint(t *testing.T) {
	m, ok := conversion(DCIP3, SRGB)
	if !ok {
		t.Fatal("no conversion")
	}
	for r := range 3 {
		near(t, "DCI white shows as D65 white", m[r*3]+m[r*3+1]+m[r*3+2], 1, 1e-6)
	}
}

func TestAnUndescribedSurfaceNeedsNoTransformOnSDR(t *testing.T) {
	for name, d := range map[string]*Description{
		"none":  nil,
		"SDR":   SDR,
		"gamma": withTarget(Description{TF: TF{Name: Gamma22}, Primaries: SRGB, MinLum: 0.2, MaxLum: 80, RefLum: 80}),
		"sRGB":  withTarget(Description{TF: TF{Name: Compound24}, Primaries: SRGB, MinLum: 0.2, MaxLum: 80, RefLum: 80}),
	} {
		if p := Plan(d, SDRTarget); p != nil {
			t.Errorf("%s: %+v", name, p)
		}
		if !Passthrough(d, SDRTarget) {
			t.Errorf("%s does not scan out on SDR", name)
		}
	}
}

func TestSDRWhiteLandsAtTheConfiguredBrightness(t *testing.T) {
	for _, nits := range []float64{100, 203, 300} {
		o := hdrOutput()
		o.SDRBrightness = nits
		work, sig := Working(o), Signal(o, unitMatrix)
		white := through(Plan(nil, work), 1, 1, 1)
		near(t, "SDR white in the working space", float64(white[0]), 1, 1e-4)
		pq := through(Plan(work.Description(), sig), white[0], white[1], white[2])
		back := lgpu.ColorTransform{Decode: lgpu.ColorFunc{Kind: lgpu.ColorPQ}, Matrix: unitMatrix, Scale: 10000}
		near(t, "SDR white on the signal", float64(back.Apply([4]float32{pq[0], pq[0], pq[0], 1})[0]), nits, nits/100)
	}
}

func TestSDRBlackStaysBlackOnHDR(t *testing.T) {
	work := Working(hdrOutput())
	if got := through(Plan(nil, work), 0, 0, 0); got != [4]float32{0, 0, 0, 1} {
		t.Fatalf("SDR black is %v in the working space", got)
	}
}

func TestSDRGreenConvertsIntoBT2020(t *testing.T) {
	got := through(Plan(nil, Working(hdrOutput())), 0, 1, 0)
	near(t, "red", float64(got[0]), 0.3293, 0.001)
	near(t, "green", float64(got[1]), 0.9195, 0.001)
	near(t, "blue", float64(got[2]), 0.0880, 0.001)
}

func pqOf(nits float64) float32 {
	tr := lgpu.ColorTransform{Matrix: unitMatrix, Scale: float32(nits / 10000), Encode: lgpu.ColorFunc{Kind: lgpu.ColorPQ}}
	return tr.Apply([4]float32{1, 1, 1, 1})[0]
}

func hdr10(cll float64) *Description {
	return withTargetCLL(Description{TF: TF{Name: PQ}, Primaries: BT2020, MinLum: 0.005, MaxLum: 10000.005, RefLum: 203}, cll)
}

func withTargetCLL(d Description, cll float64) *Description {
	d.MaxCLL = cll
	return withTarget(d)
}

func TestHDRContentKeepsItsLevelsBelowTheOutputsPeak(t *testing.T) {
	work := Working(hdrOutput())
	plan := Plan(hdr10(1000), work)
	if plan.Compress != 0 {
		t.Fatalf("content that fits compresses: %+v", plan)
	}
	for _, nits := range []float64{50, 203, 800, 1000} {
		s := pqOf(nits)
		near(t, "working light", float64(through(plan, s, s, s)[0]), nits/203, nits/203/50)
	}
}

func TestHDRContentCompressesIntoTheOutputsPeak(t *testing.T) {
	o := hdrOutput()
	o.Peak = 400
	plan := Plan(hdr10(4000), Working(o))
	if plan.Compress <= 0 {
		t.Fatal("no compression")
	}
	s := pqOf(203)
	near(t, "reference white stays", float64(through(plan, s, s, s)[0]), 1, 0.02)
	last := 0.0
	for _, nits := range []float64{300, 500, 1000, 2000, 4000} {
		s := pqOf(nits)
		v := float64(through(plan, s, s, s)[0])
		if v <= last || v > 400.0/203*1.001 {
			t.Fatalf("%v cd/m² is %v, after %v, with the peak at %v", nits, v, last, 400.0/203)
		}
		last = v
	}
	near(t, "the content peak reaches the panel peak", last, 400.0/203, 0.02)
}

func TestHDRContentTonemapsToSDR(t *testing.T) {
	plan := Plan(hdr10(4000), SDRTarget)
	if plan.Encode.Kind != lgpu.ColorCurve || plan.Compress <= 0 {
		t.Fatalf("%+v", plan)
	}
	level := func(nits float64) float64 {
		s := pqOf(nits)
		return float64(through(plan, s, s, s)[0])
	}
	near(t, "mid grey", level(203.0/4), math.Pow(0.25, 1/2.2), 0.01)
	if white := level(203); white < 0.85 || white > 1 {
		t.Fatalf("reference white is %v on SDR", white)
	}
	if peak := level(4000); peak < 0.99 || peak > 1.001 {
		t.Fatalf("the peak is %v on SDR", peak)
	}
	if level(0) != 0 {
		t.Fatal("black is lifted")
	}
}

func TestBT2020ClipsIntoSDRWithoutNaN(t *testing.T) {
	plan := Plan(hdr10(1000), SDRTarget)
	s := pqOf(203)
	got := through(plan, 0, s, 0)
	for _, v := range got {
		if math.IsNaN(float64(v)) {
			t.Fatalf("%v", got)
		}
	}
}

func TestHLGSystemGammaFollowsThePeak(t *testing.T) {
	d := withTarget(Description{TF: TF{Name: HLG}, Primaries: BT2020, MinLum: 0.005, MaxLum: 1000, RefLum: 203})
	plan := Plan(d, Working(hdrOutput()))
	near(t, "gamma", float64(plan.HLGGamma), 1.2, 1e-4)
	near(t, "luma", float64(plan.Luma[1]), 0.678, 0.001)
	near(t, "peak", float64(through(plan, 1, 1, 1)[0]), 1000.0/203, 0.02)
}

func TestWindowsSCRGBAnchorsItsReferenceWhite(t *testing.T) {
	plan := Plan(SCRGB, Working(hdrOutput()))
	near(t, "2.5375 is SDR white", float64(through(plan, 2.5375, 2.5375, 2.5375)[0]), 1, 1e-3)
	got := through(plan, -0.1, 1, 1)
	if math.IsNaN(float64(got[0])) {
		t.Fatalf("%v", got)
	}
}

func TestTransferFunctionsDecode(t *testing.T) {
	for name, c := range map[string]struct {
		d        *Description
		in, want float32
	}{
		"sRGB low":  {withTarget(Description{TF: TF{Name: Compound24}, Primaries: SRGB, MaxLum: 80, RefLum: 80}), 0.04, 0.04 / 12.92},
		"sRGB high": {withTarget(Description{TF: TF{Name: Compound24}, Primaries: SRGB, MaxLum: 80, RefLum: 80}), 0.5, 0.21404},
		"gamma 2.8": {withTarget(Description{TF: TF{Name: Gamma28}, Primaries: SRGB, MaxLum: 80, RefLum: 80}), 0.5, 0.14359},
		"power 3":   {withTarget(Description{TF: TF{Name: Power, Exponent: 3}, Primaries: SRGB, MaxLum: 80, RefLum: 80}), 0.5, 0.125},
		"ST 240":    {withTarget(Description{TF: TF{Name: ST240}, Primaries: SRGB, MaxLum: 80, RefLum: 80}), 0.05, 0.0125},
		"ST 428":    {withTarget(Description{TF: TF{Name: ST428}, Primaries: SRGB, MaxLum: 80, RefLum: 80}), 1, 52.37 / 48},
		"linear":    {withTarget(Description{TF: TF{Name: ExtLinear}, Primaries: SRGB, MaxLum: 80, RefLum: 80}), 0.3, 0.3},
		"BT.1886 0": {withTarget(Description{TF: TF{Name: BT1886}, Primaries: SRGB, MinLum: 0.01, MaxLum: 100, RefLum: 100}), 0, 0},
		"BT.1886 1": {withTarget(Description{TF: TF{Name: BT1886}, Primaries: SRGB, MinLum: 0.01, MaxLum: 100, RefLum: 100}), 1, 1},
	} {
		plan := lgpu.ColorTransform{Decode: c.d.decode(), Matrix: unitMatrix, Scale: 1}
		near(t, name, float64(through(&plan, c.in, c.in, c.in)[0]), float64(c.want), 2e-3)
	}
}

func TestPassthroughOnlyWhatMatchesTheSignal(t *testing.T) {
	o := hdrOutput()
	sig := Signal(o, unitMatrix)
	match := hdr10(1000)
	if !Passthrough(match, sig) {
		t.Error("HDR10 that fits does not scan out")
	}
	bright := o
	bright.SDRBrightness = 300
	for name, c := range map[string]struct {
		src *Description
		t   *Target
	}{
		"no description":    {nil, sig},
		"SDR":               {SDR, sig},
		"too bright":        {hdr10(2000), sig},
		"undeclared":        {withTarget(Description{TF: TF{Name: PQ}, Primaries: BT2020, MinLum: 0.005, MaxLum: 10000.005, RefLum: 203}), sig},
		"another white":     {match, Signal(bright, unitMatrix)},
		"narrow primaries":  {withTargetCLL(Description{TF: TF{Name: PQ}, Primaries: SRGB, MinLum: 0.005, MaxLum: 10000.005, RefLum: 203}, 1000), sig},
		"a colour matrix":   {match, Signal(o, [9]float32{0.5, 0, 0, 0, 1, 0, 0, 0, 1})},
		"HDR on SDR":        {match, SDRTarget},
		"wide SDR on SDR":   {withTarget(Description{TF: TF{Name: Gamma22}, Primaries: BT2020, MaxLum: 80, RefLum: 80}), SDRTarget},
		"linear SDR on SDR": {withTarget(Description{TF: TF{Name: ExtLinear}, Primaries: SRGB, MaxLum: 80, RefLum: 80}), SDRTarget},
	} {
		if Passthrough(c.src, c.t) {
			t.Errorf("%s scans out", name)
		}
	}
}

func TestTheColorMatrixAppliesInTheWorkingSpaceOnHDR(t *testing.T) {
	o := hdrOutput()
	dim := [9]float32{0.5, 0, 0, 0, 1, 0, 0, 0, 1}
	got := through(Plan(Working(o).Description(), Signal(o, dim)), 1, 1, 1)
	if got[0] >= got[1] {
		t.Fatalf("red %v is not below green %v", got[0], got[1])
	}
	near(t, "red's light is halved", float64(pqDecodeForTest(got[0])), 0.5*203, 2)
}

func pqDecodeForTest(s float32) float32 {
	back := lgpu.ColorTransform{Decode: lgpu.ColorFunc{Kind: lgpu.ColorPQ}, Matrix: unitMatrix, Scale: 10000}
	return back.Apply([4]float32{s, s, s, 1})[0]
}

func TestPlansRemembersAndStaysBounded(t *testing.T) {
	var ps Plans
	work := Working(hdrOutput())
	a := ps.Of(nil, work)
	if b := ps.Of(nil, work); a != b || a == nil {
		t.Fatalf("%p, %p", a, b)
	}
	if ps.Of(nil, SDRTarget) != nil {
		t.Fatal("SDR on SDR has a plan")
	}
	for range 3 * maxPlans {
		d := *SDR
		d.RefLum = 40
		ps.Of(&d, work)
	}
	if len(ps.plans) > maxPlans {
		t.Fatalf("%d plans", len(ps.plans))
	}
}

func TestPrimariesThatSpanNothingAreInvalid(t *testing.T) {
	line := Primaries{XY{0.1, 0.1}, XY{0.2, 0.2}, XY{0.3, 0.3}, d65}
	if line.Valid() {
		t.Fatal("three primaries on a line")
	}
	if !BT2020.Valid() || !CIE1931XYZ.Valid() {
		t.Fatal("a named space")
	}
}
