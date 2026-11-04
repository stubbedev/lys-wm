package harness

import (
	"encoding/binary"
	"errors"
	"image"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/loop"
	ccm "github.com/stubbedev/lys-wm/internal/protocol/client/colormanagement"
	cwsc "github.com/stubbedev/lys-wm/internal/protocol/client/wlrscreencopy"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type managerEvents struct {
	intents   []ccm.WpColorManagerV1RenderIntent
	features  []ccm.WpColorManagerV1Feature
	tfs       []ccm.WpColorManagerV1TransferFunction
	primaries []ccm.WpColorManagerV1Primaries
	done      bool
}

func (e *managerEvents) SupportedIntent(_ *ccm.WpColorManagerV1, v ccm.WpColorManagerV1RenderIntent) error {
	e.intents = append(e.intents, v)
	return nil
}

func (e *managerEvents) SupportedFeature(_ *ccm.WpColorManagerV1, v ccm.WpColorManagerV1Feature) error {
	e.features = append(e.features, v)
	return nil
}

func (e *managerEvents) SupportedTfNamed(_ *ccm.WpColorManagerV1, v ccm.WpColorManagerV1TransferFunction) error {
	e.tfs = append(e.tfs, v)
	return nil
}

func (e *managerEvents) SupportedPrimariesNamed(_ *ccm.WpColorManagerV1, v ccm.WpColorManagerV1Primaries) error {
	e.primaries = append(e.primaries, v)
	return nil
}

func (e *managerEvents) Done(*ccm.WpColorManagerV1) error {
	e.done = true
	return nil
}

func colorManager(t *testing.T, h *Harness) (*testclient.Client, *ccm.WpColorManagerV1, *managerEvents) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	m, ev := bindColorManagerEvents(t, c)
	return c, m, ev
}

func bindColorManager(t *testing.T, c *testclient.Client) *ccm.WpColorManagerV1 {
	t.Helper()
	m, _ := bindColorManagerEvents(t, c)
	return m
}

func bindColorManagerEvents(t *testing.T, c *testclient.Client) (*ccm.WpColorManagerV1, *managerEvents) {
	t.Helper()
	ev := &managerEvents{}
	m, err := ccm.NewWpColorManagerV1(c.Display, 3, ev)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	c.DispatchUntil("the supported colour features", func() bool { return ev.done })
	return m, ev
}

type descEvents struct {
	ready  bool
	id     uint64
	failed bool
	cause  ccm.WpImageDescriptionV1Cause
	msg    string
}

func (e *descEvents) Failed(_ *ccm.WpImageDescriptionV1, cause ccm.WpImageDescriptionV1Cause, msg string) error {
	e.failed, e.cause, e.msg = true, cause, msg
	return nil
}

func (e *descEvents) Ready(_ *ccm.WpImageDescriptionV1, id uint32) error {
	e.ready, e.id = true, uint64(id)
	return nil
}

func (e *descEvents) Ready2(_ *ccm.WpImageDescriptionV1, hi, lo uint32) error {
	e.ready, e.id = true, uint64(hi)<<32|uint64(lo)
	return nil
}

func hdr10Description(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1, cll uint32) *ccm.WpImageDescriptionV1 {
	t.Helper()
	p, err := m.CreateParametricCreator()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(p.SetTfNamed(ccm.WpColorManagerV1TransferFunctionSt2084Pq), p.SetPrimariesNamed(ccm.WpColorManagerV1PrimariesBt2020), p.SetMaxCll(cll)); err != nil {
		t.Fatal(err)
	}
	ev := &descEvents{}
	d, err := p.Create(ev)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the HDR10 description", func() bool { return ev.ready || ev.failed })
	if ev.failed {
		t.Fatalf("the HDR10 description failed: %s", ev.msg)
	}
	return d
}

type infoEvents struct {
	tfNamed     []ccm.WpColorManagerV1TransferFunction
	named       []ccm.WpColorManagerV1Primaries
	luminances  [3]uint32
	target      [2]uint32
	targetChrom [8]int32
	primaries   [8]int32
	cll, fall   uint32
	done        bool
}

func (e *infoEvents) Done(*ccm.WpImageDescriptionInfoV1) error {
	e.done = true
	return nil
}

func (e *infoEvents) IccFile(_ *ccm.WpImageDescriptionInfoV1, fd int, _ uint32) error {
	return unix.Close(fd)
}

func (e *infoEvents) Primaries(_ *ccm.WpImageDescriptionInfoV1, a, b, c, d, f, g, h, i int32) error {
	e.primaries = [8]int32{a, b, c, d, f, g, h, i}
	return nil
}

func (e *infoEvents) PrimariesNamed(_ *ccm.WpImageDescriptionInfoV1, v ccm.WpColorManagerV1Primaries) error {
	e.named = append(e.named, v)
	return nil
}
func (e *infoEvents) TfPower(*ccm.WpImageDescriptionInfoV1, uint32) error { return nil }
func (e *infoEvents) TfNamed(_ *ccm.WpImageDescriptionInfoV1, v ccm.WpColorManagerV1TransferFunction) error {
	e.tfNamed = append(e.tfNamed, v)
	return nil
}

func (e *infoEvents) Luminances(_ *ccm.WpImageDescriptionInfoV1, a, b, c uint32) error {
	e.luminances = [3]uint32{a, b, c}
	return nil
}

func (e *infoEvents) TargetPrimaries(_ *ccm.WpImageDescriptionInfoV1, a, b, c, d, f, g, h, i int32) error {
	e.targetChrom = [8]int32{a, b, c, d, f, g, h, i}
	return nil
}

func (e *infoEvents) TargetLuminance(_ *ccm.WpImageDescriptionInfoV1, a, b uint32) error {
	e.target = [2]uint32{a, b}
	return nil
}

func (e *infoEvents) TargetMaxCll(_ *ccm.WpImageDescriptionInfoV1, v uint32) error {
	e.cll = v
	return nil
}

func (e *infoEvents) TargetMaxFall(_ *ccm.WpImageDescriptionInfoV1, v uint32) error {
	e.fall = v
	return nil
}

type outputColorEvents struct{ changed int }

func (e *outputColorEvents) ImageDescriptionChanged(*ccm.WpColorManagementOutputV1) error {
	e.changed++
	return nil
}

func outputInfo(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1) *infoEvents {
	t.Helper()
	o, err := m.GetOutput(&outputColorEvents{}, c.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	ev := &descEvents{}
	d, err := o.GetImageDescription(ev)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the output's description", func() bool { return ev.ready || ev.failed })
	if ev.failed {
		t.Fatalf("no output description: %s", ev.msg)
	}
	info := &infoEvents{}
	if _, err := d.GetInformation(info); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the description's information", func() bool { return info.done })
	return info
}

func hdrConfig(rule compositor.OutputRule) func(*compositor.Config) {
	rule.HDR = true
	return func(cfg *compositor.Config) { cfg.Outputs = compositor.OutputRules{Fallback: rule} }
}

func TestColorManagementIsOfferedOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	if testclient.Connect(t, h.Socket()).HasGlobal(ccm.WpColorManagerV1Interface.Name) {
		t.Fatal("wp_color_manager_v1 is offered with HDR off and no color_management")
	}
	for name, configure := range map[string]func(*compositor.Config){
		"color_management": func(cfg *compositor.Config) { cfg.ColorManagement = true },
		"an output in HDR": hdrConfig(compositor.OutputRule{}),
	} {
		h := StartWith(t, configure, small)
		if !testclient.Connect(t, h.Socket()).HasGlobal(ccm.WpColorManagerV1Interface.Name) {
			t.Errorf("%s does not offer wp_color_manager_v1", name)
		}
	}
}

func TestColorManagementAdvertisesWhatItHonours(t *testing.T) {
	t.Parallel()
	h := StartWith(t, hdrConfig(compositor.OutputRule{}), small)
	_, _, ev := colorManager(t, h)
	if !slices.Equal(ev.intents, []ccm.WpColorManagerV1RenderIntent{ccm.WpColorManagerV1RenderIntentPerceptual}) {
		t.Errorf("intents %v, want perceptual only", ev.intents)
	}
	for _, f := range []ccm.WpColorManagerV1Feature{ccm.WpColorManagerV1FeatureParametric, ccm.WpColorManagerV1FeatureIccV2V4, ccm.WpColorManagerV1FeatureWindowsScrgb, ccm.WpColorManagerV1FeatureWindowsBt2100} {
		if !slices.Contains(ev.features, f) {
			t.Errorf("feature %d is missing from %v", f, ev.features)
		}
	}
	for _, tf := range []ccm.WpColorManagerV1TransferFunction{ccm.WpColorManagerV1TransferFunctionSt2084Pq, ccm.WpColorManagerV1TransferFunctionHlg, ccm.WpColorManagerV1TransferFunctionGamma22, ccm.WpColorManagerV1TransferFunctionExtLinear} {
		if !slices.Contains(ev.tfs, tf) {
			t.Errorf("transfer function %d is missing from %v", tf, ev.tfs)
		}
	}
	if slices.Contains(ev.tfs, ccm.WpColorManagerV1TransferFunctionLog100) {
		t.Error("log_100 is advertised, which the renderers do not evaluate")
	}
	if !slices.Contains(ev.primaries, ccm.WpColorManagerV1PrimariesBt2020) {
		t.Errorf("BT.2020 is missing from %v", ev.primaries)
	}
}

func TestAnOutputDescribesItsSignal(t *testing.T) {
	t.Parallel()
	sdr := StartWith(t, func(cfg *compositor.Config) { cfg.ColorManagement = true }, small)
	c, m, _ := colorManager(t, sdr)
	info := outputInfo(t, c, m)
	if !slices.Contains(info.tfNamed, ccm.WpColorManagerV1TransferFunctionGamma22) || !slices.Contains(info.named, ccm.WpColorManagerV1PrimariesSrgb) {
		t.Errorf("an SDR output describes %+v", info)
	}
	if info.luminances != [3]uint32{2000, 80, 80} {
		t.Errorf("SDR luminances %v", info.luminances)
	}

	hdr := StartWith(t, hdrConfig(compositor.OutputRule{SDRBrightness: 250}), small)
	c, m, _ = colorManager(t, hdr)
	info = outputInfo(t, c, m)
	if !slices.Contains(info.tfNamed, ccm.WpColorManagerV1TransferFunctionSt2084Pq) || !slices.Contains(info.named, ccm.WpColorManagerV1PrimariesBt2020) {
		t.Errorf("an HDR output describes %+v", info)
	}
	if info.luminances[2] != 250 || info.target[1] != 1000 || info.cll != 1000 || info.fall != 400 {
		t.Errorf("an HDR output's volume %+v", info)
	}
	if info.targetChrom[0] != 680000 {
		t.Errorf("the target primaries are not the panel's: %v", info.targetChrom)
	}
}

func TestColorManagementProtocolErrors(t *testing.T) {
	t.Parallel()
	h := StartWith(t, hdrConfig(compositor.OutputRule{}), small)
	for name, cse := range map[string]struct {
		do   func(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1)
		code uint32
	}{
		"a second surface object": {func(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1) {
			s := c.Surface()
			if _, err := m.GetSurface(s); err != nil {
				t.Fatal(err)
			}
			if _, err := m.GetSurface(s); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpColorManagerV1ErrorSurfaceExists)},
		"an unready description": {func(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1) {
			s := c.Surface()
			cs, err := m.GetSurface(s)
			if err != nil {
				t.Fatal(err)
			}
			p, _ := m.CreateParametricCreator()
			_ = p.SetTfNamed(ccm.WpColorManagerV1TransferFunctionGamma22)
			_ = p.SetPrimaries(100000, 100000, 200000, 200000, 300000, 300000, 312700, 329000)
			ev := &descEvents{}
			d, _ := p.Create(ev)
			c.DispatchUntil("the description to fail", func() bool { return ev.failed })
			if err := cs.SetImageDescription(d, ccm.WpColorManagerV1RenderIntentPerceptual); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpColorManagementSurfaceV1ErrorImageDescription)},
		"an unadvertised intent": {func(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1) {
			s := c.Surface()
			cs, err := m.GetSurface(s)
			if err != nil {
				t.Fatal(err)
			}
			if err := cs.SetImageDescription(hdr10Description(t, c, m, 1000), ccm.WpColorManagerV1RenderIntentAbsolute); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpColorManagementSurfaceV1ErrorRenderIntent)},
		"an unset requirement": {func(t *testing.T, _ *testclient.Client, m *ccm.WpColorManagerV1) {
			p, _ := m.CreateParametricCreator()
			if _, err := p.Create(&descEvents{}); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpImageDescriptionCreatorParamsV1ErrorIncompleteSet)},
		"a transfer function twice": {func(t *testing.T, _ *testclient.Client, m *ccm.WpColorManagerV1) {
			p, _ := m.CreateParametricCreator()
			_ = p.SetTfNamed(ccm.WpColorManagerV1TransferFunctionGamma22)
			_ = p.SetTfNamed(ccm.WpColorManagerV1TransferFunctionGamma22)
		}, uint32(ccm.WpImageDescriptionCreatorParamsV1ErrorAlreadySet)},
		"an unadvertised transfer function": {func(t *testing.T, _ *testclient.Client, m *ccm.WpColorManagerV1) {
			p, _ := m.CreateParametricCreator()
			_ = p.SetTfNamed(ccm.WpColorManagerV1TransferFunctionLog100)
		}, uint32(ccm.WpImageDescriptionCreatorParamsV1ErrorInvalidTf)},
		"an exponent out of range": {func(t *testing.T, _ *testclient.Client, m *ccm.WpColorManagerV1) {
			p, _ := m.CreateParametricCreator()
			_ = p.SetTfPower(5000)
		}, uint32(ccm.WpImageDescriptionCreatorParamsV1ErrorInvalidTf)},
		"luminances upside down": {func(t *testing.T, _ *testclient.Client, m *ccm.WpColorManagerV1) {
			p, _ := m.CreateParametricCreator()
			_ = p.SetLuminances(1000000, 50, 80)
		}, uint32(ccm.WpImageDescriptionCreatorParamsV1ErrorInvalidLuminance)},
		"fall above cll": {func(t *testing.T, _ *testclient.Client, m *ccm.WpColorManagerV1) {
			p, _ := m.CreateParametricCreator()
			_ = p.SetTfNamed(ccm.WpColorManagerV1TransferFunctionSt2084Pq)
			_ = p.SetPrimariesNamed(ccm.WpColorManagerV1PrimariesBt2020)
			_ = p.SetMaxCll(100)
			_ = p.SetMaxFall(200)
			if _, err := p.Create(&descEvents{}); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpImageDescriptionCreatorParamsV1ErrorInvalidLuminance)},
		"information a client's description lacks": {func(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1) {
			if _, err := hdr10Description(t, c, m, 1000).GetInformation(&infoEvents{}); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpImageDescriptionV1ErrorNoInformation)},
		"an ICC file of no bytes": {func(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1) {
			fd := memfd(t, []byte("x"))
			p, _ := m.CreateIccCreator()
			if err := p.SetIccFile(fd, 0, 0); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpImageDescriptionCreatorIccV1ErrorBadSize)},
		"an ICC file past its end": {func(t *testing.T, c *testclient.Client, m *ccm.WpColorManagerV1) {
			fd := memfd(t, []byte("x"))
			p, _ := m.CreateIccCreator()
			if err := p.SetIccFile(fd, 0, 2); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpImageDescriptionCreatorIccV1ErrorOutOfFile)},
		"an ICC creator with no file": {func(t *testing.T, _ *testclient.Client, m *ccm.WpColorManagerV1) {
			p, _ := m.CreateIccCreator()
			if _, err := p.Create(&descEvents{}); err != nil {
				t.Fatal(err)
			}
		}, uint32(ccm.WpImageDescriptionCreatorIccV1ErrorIncompleteSet)},
	} {
		t.Run(name, func(t *testing.T) {
			c, m, _ := colorManager(t, h)
			cse.do(t, c, m)
			if err := c.Display.Flush(); err != nil {
				t.Fatal(err)
			}
			if e := c.ExpectProtocolError(cse.code); e == nil {
				t.Fatal("no protocol error")
			}
		})
	}
}

func memfd(t *testing.T, data []byte) int {
	t.Helper()
	fd, err := unix.MemfdCreate("icc", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Write(fd, data); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

func TestAnICCProfileBecomesADescriptionOrAFailure(t *testing.T) {
	t.Parallel()
	h := StartWith(t, hdrConfig(compositor.OutputRule{}), small)
	c, m, _ := colorManager(t, h)
	create := func(data []byte) *descEvents {
		t.Helper()
		p, err := m.CreateIccCreator()
		if err != nil {
			t.Fatal(err)
		}
		if err := p.SetIccFile(memfd(t, data), 0, uint32(len(data))); err != nil {
			t.Fatal(err)
		}
		ev := &descEvents{}
		if _, err := p.Create(ev); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the ICC description", func() bool { return ev.ready || ev.failed })
		return ev
	}
	if ev := create(testICCProfile()); !ev.ready {
		t.Fatalf("a matrix-shaper profile failed: %s", ev.msg)
	}
	if ev := create([]byte("not a profile, not at all, not even close")); !ev.failed || ev.cause != ccm.WpImageDescriptionV1CauseUnsupported {
		t.Fatalf("garbage as a profile: %+v", ev)
	}
}

type preferredEvents struct {
	ids []uint64
}

func (e *preferredEvents) PreferredChanged(_ *ccm.WpColorManagementSurfaceFeedbackV1, id uint32) error {
	e.ids = append(e.ids, uint64(id))
	return nil
}

func (e *preferredEvents) PreferredChanged2(_ *ccm.WpColorManagementSurfaceFeedbackV1, hi, lo uint32) error {
	e.ids = append(e.ids, uint64(hi)<<32|uint64(lo))
	return nil
}

func TestASurfaceHearsWhatItsOutputPrefers(t *testing.T) {
	t.Parallel()
	var cfg compositor.Config
	h := StartWith(t, func(c *compositor.Config) {
		c.ColorManagement = true
		cfg = *c
	}, small)
	c, m, _ := colorManager(t, h)
	w := c.Window("mover", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	ev := &preferredEvents{}
	f, err := m.GetSurfaceFeedback(ev, w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	preferred := func() (uint64, *infoEvents) {
		t.Helper()
		de := &descEvents{}
		d, err := f.GetPreferred(de)
		if err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the preferred description", func() bool { return de.ready })
		info := &infoEvents{}
		if _, err := d.GetInformation(info); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("its information", func() bool { return info.done })
		return de.id, info
	}
	sdrID, info := preferred()
	if !slices.Contains(info.tfNamed, ccm.WpColorManagerV1TransferFunctionGamma22) {
		t.Fatalf("a window on an SDR output prefers %+v", info)
	}
	cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: true}}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
	c.DispatchUntil("preferred_changed", func() bool { return len(ev.ids) > 0 })
	if ev.ids[0] == sdrID {
		t.Fatalf("the HDR identity %d is the SDR one's", ev.ids[0])
	}
	hdrID, info := preferred()
	if hdrID != ev.ids[0] {
		t.Fatalf("get_preferred is %d, preferred_changed said %d", hdrID, ev.ids[0])
	}
	if !slices.Contains(info.tfNamed, ccm.WpColorManagerV1TransferFunctionSt2084Pq) {
		t.Fatalf("a window on an HDR output prefers %+v", info)
	}
}

func pqByte(nits float64) byte {
	const m1, m2, c1, c2, c3 = 0.1593017578125, 78.84375, 0.8359375, 18.8515625, 18.6875
	y := math.Pow(nits/10000, m1)
	return byte(math.Round(math.Pow((c1+c2*y)/(1+c3*y), m2) * 255))
}

func within(got [3]byte, want [3]byte, tolerance int) bool {
	for i := range got {
		if d := int(got[i]) - int(want[i]); d > tolerance || d < -tolerance {
			return false
		}
	}
	return true
}

func grey(v byte) [3]byte { return [3]byte{v, v, v} }

func showingAbout(x, y int, want [3]byte) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		return within(pixel(snapshot(b.Outputs()[0]), x, y), want, 2)
	}
}

func fullWindowConfig(rule compositor.OutputRule) func(*compositor.Config) {
	rule.HDR = true
	return func(cfg *compositor.Config) {
		cfg.Outputs = compositor.OutputRules{Fallback: rule}
		cfg.WM.Gaps = wm.Gaps{}
	}
}

func tagWith(m *ccm.WpColorManagerV1, d *ccm.WpImageDescriptionV1) func(*testclient.Window) error {
	return func(w *testclient.Window) error {
		cs, err := m.GetSurface(w.Surface)
		if err != nil {
			return err
		}
		return cs.SetImageDescription(d, ccm.WpColorManagerV1RenderIntentPerceptual)
	}
}

func TestSDRWhiteShowsAtTheConfiguredBrightness(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		for _, nits := range []float64{0, 100, 300} {
			h := start(t, fullWindowConfig(compositor.OutputRule{SDRBrightness: nits}), small)
			c := testclient.Connect(t, h.Socket())
			w := c.Window("sdr", testclient.Fill(0xff, 0xff, 0xff))
			c.DispatchUntil("the window to map", w.Mapped)
			want := nits
			if want == 0 {
				want = 203
			}
			h.Eventually("SDR white at the configured PQ level", showingAbout(30, 30, grey(pqByte(want))))
		}
	})
}

func TestTaggedPQAnchorsToTheOutputsSDRWhite(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, fullWindowConfig(compositor.OutputRule{SDRBrightness: 100}), small)
		c, m, _ := colorManager(t, h)
		level := pqByte(203)
		w := c.WindowWith("tagged", testclient.Fill(level, level, level), tagWith(m, hdr10Description(t, c, m, 800)))
		c.DispatchUntil("the window to map", w.Mapped)
		h.Eventually("the content's reference white at the output's", showingAbout(30, 30, grey(pqByte(100))))
	})
}

func TestScreencopyOfAnHDROutputIsSDR(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, fullWindowConfig(compositor.OutputRule{}), small)
		c, m, _ := colorManager(t, h)
		w := c.Window("sdr", testclient.Fill(0xff, 0x40, 0x00))
		c.DispatchUntil("the window to map", w.Mapped)
		h.Eventually("the window on the PQ signal", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return pixel(snapshot(b.Outputs()[0]), 60, 40)[1] > 0x40
		})
		settle(t, h)
		mgr, err := cwsc.NewZwlrScreencopyManagerV1(c.Display, 3)
		if err != nil {
			t.Fatal(err)
		}
		c.Bind(mgr)
		f, _, pix := screenshot(t, c, mgr, image.Rectangle{}, false)
		c.DispatchUntil("the capture", func() bool { return f.ready || f.failed })
		if f.failed {
			t.Fatal("the capture failed")
		}
		if got := rgbAt(pix, int(f.stride), 60, 40); got != [3]byte{0x00, 0x40, 0xff} {
			t.Fatalf("a capture of an SDR window on an HDR output is %v, want its own pixels", got)
		}
		level := pqByte(1000)
		hdr := c.WindowWith("hdr", testclient.Fill(level, level, level), tagWith(m, hdr10Description(t, c, m, 1000)))
		c.DispatchUntil("the HDR window to map", hdr.Mapped)
		var sdrThere [3]byte
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			sdrThere = pixel(snapshot(b.Outputs()[0]), 120, 50)
			return nil
		})
		h.Eventually("the HDR window in the layout, past the hold for its neighbour", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return pixel(snapshot(b.Outputs()[0]), 120, 50) != sdrThere
		})
		settle(t, h)
		g, _, pix := screenshot(t, c, mgr, image.Rectangle{}, false)
		c.DispatchUntil("the second capture", func() bool { return g.ready || g.failed })
		if g.failed {
			t.Fatal("the second capture failed")
		}
		if got := rgbAt(pix, int(g.stride), 120, 50); got[0] < 0xf0 || got != grey(got[0]) {
			t.Fatalf("a capture of a 1000 cd/m² window is %v, want tone-mapped white", got)
		}
	})
}

func TestHDRContentTonemapsOnAnSDROutput(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			cfg.ColorManagement = true
			cfg.WM.Gaps = wm.Gaps{}
		}, small)
		c, m, _ := colorManager(t, h)
		sdr := c.Window("sdr", testclient.Fill(0x80, 0x80, 0x80))
		c.DispatchUntil("the SDR window to map", sdr.Mapped)
		level := pqByte(4000)
		w := c.WindowWith("hdr", testclient.Fill(level, level, level), tagWith(m, hdr10Description(t, c, m, 4000)))
		c.DispatchUntil("the window to map", w.Mapped)
		h.Eventually("the HDR window at SDR white", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return within(pixel(snapshot(b.Outputs()[0]), 120, 50), grey(0xff), 2)
		})
		h.Eventually("the SDR window beside it untouched", showing(40, 50, grey(0x80)))
		if err := errors.Join(w.Destroy(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestADisplayWithoutHDRStaysSDRAndSaysSo(t *testing.T) {
	t.Parallel()
	var r reports
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Report = r.report
		cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: true}}
	}, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.RefuseHDR()
		o, err := b.AddOutput(small)
		if err != nil {
			return err
		}
		if got := o.State(); got.HDR.Active || got.Depth != backend.Depth8 || !got.Lit() {
			t.Errorf("a display without HDR is at %+v", got)
		}
		return nil
	})
	errs := r.take()
	if len(errs) != 1 || !errors.Is(errs[0], backend.ErrHDRUnsupported) {
		t.Fatalf("reports %v, want the one display without HDR", errs)
	}
}

func TestARendererWithoutAWorkingSpaceStaysSDRAndSaysSo(t *testing.T) {
	t.Parallel()
	var r reports
	open := func(l *loop.Loop) (*headless.Backend, error) {
		b := headless.New(l)
		b.RefuseWorkingSpace()
		return b, nil
	}
	h := start(t, setup{open: open, traced: true, configure: func(cfg *compositor.Config) {
		cfg.Report = r.report
		cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: true}}
		cfg.WM.Gaps = wm.Gaps{}
	}}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("sdr", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("SDR white", showing(30, 30, grey(0xff)))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if got := b.Outputs()[0].State(); got.HDR.Active || got.Depth != backend.Depth8 {
			t.Errorf("state %+v", got)
		}
		return nil
	})
	errs := r.take()
	if len(errs) != 1 || !errors.Is(errs[0], backend.ErrHDRUnsupported) {
		t.Fatalf("reports %v, want the one output staying SDR", errs)
	}
}

func TestOffHDROutputsKeepEightBitSDRState(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if got := b.Outputs()[0].State(); got.HDR != (backend.HDRMetadata{}) || got.Depth != backend.Depth8 {
			t.Errorf("default state %+v", got)
		}
		return nil
	})
}

func TestAnHDROutputIsTenBitAndCarriesThePanelsMetadata(t *testing.T) {
	t.Parallel()
	h := StartWith(t, hdrConfig(compositor.OutputRule{HDRPeak: 600}), small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		got := b.Outputs()[0].State()
		if !got.HDR.Active || got.Depth != backend.Depth10 || got.HDR.MaxLuminance != 600 || got.HDR.Red.X != 0.68 {
			t.Errorf("state %+v", got)
		}
		return nil
	})
}

func TestBlurShadowsAndCornersCompositeInTheWorkingSpace(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		half := float32(0.5)
		h := start(t, func(cfg *compositor.Config) {
			fullWindowConfig(compositor.OutputRule{})(cfg)
			cfg.Decor.Rounding = 6
			cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 4, Passes: 2}
			cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^glass$`), Mode: wm.RuleFloat, Size: image.Pt(60, 30), Opacity: &half}}
		}, small)
		c := testclient.Connect(t, h.Socket())
		bg := c.Window("background", testclient.Fill(0xff, 0xff, 0xff))
		c.DispatchUntil("the background to map", bg.Mapped)
		glass := c.Window("glass", testclient.Fill(0, 0, 0))
		c.DispatchUntil("the glass to map", glass.Mapped)
		h.Eventually("half black over blurred white, halved in linear light", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return within(pixel(snapshot(b.Outputs()[0]), 60, 40), grey(pqByte(203.0/2)), 4)
		})
		settle(t, h)
		mgr, err := cwsc.NewZwlrScreencopyManagerV1(c.Display, 3)
		if err != nil {
			t.Fatal(err)
		}
		c.Bind(mgr)
		f, _, pix := screenshot(t, c, mgr, image.Rectangle{}, false)
		c.DispatchUntil("the capture", func() bool { return f.ready || f.failed })
		if f.failed {
			t.Fatal("the capture failed")
		}
		if got := rgbAt(pix, int(f.stride), 60, 40); got[0] < 0x70 || got[0] > 0xd0 {
			t.Fatalf("the blur in a capture is %v", got)
		}
	})
}

func TestTheCursorDrawsInTheFrameOnAnHDROutput(t *testing.T) {
	h := StartWithCursorPlanes(t, image.Pt(64, 64), hdrConfig(compositor.OutputRule{}), small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if b.Outputs()[0].Cursor().Visible() {
			t.Error("an HDR output shows the cursor on a plane that would show it as PQ")
		}
		return nil
	})
}

func TestGammaIsRefusedAndTheColorMatrixAppliesInTheEncodeOnHDR(t *testing.T) {
	t.Parallel()
	h := StartWith(t, fullWindowConfig(compositor.OutputRule{}), small)
	g, _, ev := gammaControl(t, h)
	g.DispatchUntil("gamma refused", func() bool { return ev.failed })
	c, m, _ := ctmControl(t, h)
	w := c.Window("white", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("white", showingAbout(30, 30, grey(pqByte(203))))
	setCTM(t, c, m, 0, backend.Matrix{0.5, 0, 0, 0, 1, 0, 0, 0, 1})
	if err := errors.Join(m.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("red's light halved in the encode", showingAbout(30, 30, [3]byte{pqByte(203.0 / 2), pqByte(203), pqByte(203)}))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if got := b.Outputs()[0].ColorTransform(); got != backend.Identity && got != (backend.Matrix{}) {
			t.Errorf("the CRTC's matrix is %v on an HDR output", got)
		}
		return nil
	})
}

func TestReloadTurnsHDROnAndOff(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		var cfg compositor.Config
		h := start(t, func(c *compositor.Config) {
			c.WM.Gaps = wm.Gaps{}
			cfg = *c
		}, small)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("sdr", testclient.Fill(0xff, 0xff, 0xff))
		c.DispatchUntil("the window to map", w.Mapped)
		h.Eventually("SDR white", showing(30, 30, grey(0xff)))
		cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: true}}
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
		h.Eventually("white on the PQ signal", showingAbout(30, 30, grey(pqByte(203))))
		cfg.Outputs = compositor.OutputRules{}
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
		h.Eventually("SDR white again", showing(30, 30, grey(0xff)))
	})
}

func testICCProfile() []byte {
	head := make([]byte, 128)
	head[8] = 4
	copy(head[12:], "mntr")
	copy(head[16:], "RGB ")
	copy(head[20:], "XYZ ")
	copy(head[36:], "acsp")
	s15 := func(v float64) []byte { return binary.BigEndian.AppendUint32(nil, uint32(int32(math.Round(v*65536)))) }
	xyz := func(x, y, z float64) []byte {
		return slices.Concat([]byte("XYZ \x00\x00\x00\x00"), s15(x), s15(y), s15(z))
	}
	gamma := binary.BigEndian.AppendUint16([]byte("curv\x00\x00\x00\x00\x00\x00\x00\x01"), 563)
	tags := []struct {
		name string
		data []byte
	}{{"rXYZ", xyz(0.4360, 0.2225, 0.0139)}, {"gXYZ", xyz(0.3851, 0.7169, 0.0971)}, {"bXYZ", xyz(0.1431, 0.0606, 0.7139)}, {"rTRC", gamma}, {"gTRC", gamma}, {"bTRC", gamma}}
	table := binary.BigEndian.AppendUint32(nil, uint32(len(tags)))
	off := 128 + 4 + 12*len(tags)
	var body []byte
	for _, t := range tags {
		table = append(table, t.name...)
		table = binary.BigEndian.AppendUint32(table, uint32(off+len(body)))
		table = binary.BigEndian.AppendUint32(table, uint32(len(t.data)))
		body = append(body, t.data...)
	}
	return slices.Concat(head, table, body)
}

func TestAScreenShaderStaysOffOnAnHDROutputAndSaysSo(t *testing.T) {
	t.Parallel()
	var r reports
	path := t.TempDir() + "/invert.frag"
	if err := os.WriteFile(path, []byte("precision mediump float;\nvarying vec2 v_texcoord;\nuniform sampler2D tex;\nvoid main() { gl_FragColor = vec4(1.0) - texture2D(tex, v_texcoord); }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Report = r.report
		cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: true, Shader: path}}
		cfg.WM.Gaps = wm.Gaps{}
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("sdr", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("white on the PQ signal, unshaded", showingAbout(30, 30, grey(pqByte(203))))
	if errs := r.take(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "stays off while it is in HDR") {
		t.Fatalf("reports %v", errs)
	}
}
