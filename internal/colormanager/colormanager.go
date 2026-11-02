// Package colormanager implements wp_color_manager_v1: clients learn what each
// output shows and what each surface would best hold, describe an encoding by
// parameters or an ICC profile, and tag surfaces with it. What the tags mean for
// a frame is colorspace's and the compositor's.
package colormanager

import (
	"errors"
	"fmt"
	"math"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/output"
	scm "github.com/stubbedev/lys-wm/internal/protocol/server/colormanagement"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const (
	version    = 3
	maxICCSize = 32 << 20
	million    = 1000000
	tfPrecise  = 10000
	lumPrecise = 10000
)

var Interface = scm.WpColorManagerV1Interface

type View interface {
	Owner(res *sw.WlOutput) *output.Output
	OutputDescription(o *output.Output) *colorspace.Description
	PreferredDescription(s *surface.Surface) *colorspace.Description
}

var features = []scm.WpColorManagerV1Feature{
	scm.WpColorManagerV1FeatureIccV2V4,
	scm.WpColorManagerV1FeatureParametric,
	scm.WpColorManagerV1FeatureSetPrimaries,
	scm.WpColorManagerV1FeatureSetTfPower,
	scm.WpColorManagerV1FeatureSetLuminances,
	scm.WpColorManagerV1FeatureSetMasteringDisplayPrimaries,
	scm.WpColorManagerV1FeatureExtendedTargetVolume,
	scm.WpColorManagerV1FeatureWindowsScrgb,
	scm.WpColorManagerV1FeatureWindowsBt2100,
}

type namedTF struct {
	wire scm.WpColorManagerV1TransferFunction
	name colorspace.TFName
}

var transferFunctions = []namedTF{
	{scm.WpColorManagerV1TransferFunctionBt1886, colorspace.BT1886},
	{scm.WpColorManagerV1TransferFunctionGamma22, colorspace.Gamma22},
	{scm.WpColorManagerV1TransferFunctionGamma28, colorspace.Gamma28},
	{scm.WpColorManagerV1TransferFunctionSt240, colorspace.ST240},
	{scm.WpColorManagerV1TransferFunctionExtLinear, colorspace.ExtLinear},
	{scm.WpColorManagerV1TransferFunctionSrgb, colorspace.Compound24},
	{scm.WpColorManagerV1TransferFunctionExtSrgb, colorspace.ExtSRGB},
	{scm.WpColorManagerV1TransferFunctionSt2084Pq, colorspace.PQ},
	{scm.WpColorManagerV1TransferFunctionSt428, colorspace.ST428},
	{scm.WpColorManagerV1TransferFunctionHlg, colorspace.HLG},
	{scm.WpColorManagerV1TransferFunctionCompoundPower24, colorspace.Compound24},
}

type namedPrimaries struct {
	wire scm.WpColorManagerV1Primaries
	p    colorspace.Primaries
}

var primaries = []namedPrimaries{
	{scm.WpColorManagerV1PrimariesSrgb, colorspace.SRGB},
	{scm.WpColorManagerV1PrimariesPalM, colorspace.PALM},
	{scm.WpColorManagerV1PrimariesPal, colorspace.PAL},
	{scm.WpColorManagerV1PrimariesNtsc, colorspace.NTSC},
	{scm.WpColorManagerV1PrimariesGenericFilm, colorspace.GenericFilm},
	{scm.WpColorManagerV1PrimariesBt2020, colorspace.BT2020},
	{scm.WpColorManagerV1PrimariesCie1931Xyz, colorspace.CIE1931XYZ},
	{scm.WpColorManagerV1PrimariesDciP3, colorspace.DCIP3},
	{scm.WpColorManagerV1PrimariesDisplayP3, colorspace.DisplayP3},
	{scm.WpColorManagerV1PrimariesAdobeRgb, colorspace.AdobeRGB},
}

type Manager struct {
	d         *server.Display
	view      View
	global    *server.Global
	surfaces  *surface.Latched[*colorspace.Description]
	feedback  map[*feedback]struct{}
	outputs   map[*outputColor]struct{}
	identity  map[*colorspace.Description]uint64
	identNext uint64
}

func New(d *server.Display, view View) *Manager {
	return &Manager{
		d: d, view: view,
		surfaces: surface.NewLatched[*colorspace.Description](nil),
		feedback: map[*feedback]struct{}{},
		outputs:  map[*outputColor]struct{}{},
		identity: map[*colorspace.Description]uint64{},
	}
}

func (m *Manager) Advertise(on bool) error {
	if on == (m.global != nil) {
		return nil
	}
	if !on {
		g := m.global
		m.global = nil
		return g.Remove()
	}
	var err error
	m.global, err = m.d.AddGlobal(Interface, version, m.bind)
	return err
}

func (m *Manager) Of(s *surface.Surface) *colorspace.Description {
	return m.surfaces.Get(s)
}

func (m *Manager) nextIdentity() uint64 {
	m.identNext++
	return m.identNext
}

func (m *Manager) identityOf(d *colorspace.Description) uint64 {
	if id, ok := m.identity[d]; ok {
		return id
	}
	id := m.nextIdentity()
	m.identity[d] = id
	return id
}

func (m *Manager) OutputChanged(o *output.Output) error {
	var errs []error
	for k := range m.outputs {
		if k.o == o {
			errs = append(errs, k.res.SendImageDescriptionChanged())
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) OutputRemoved(o *output.Output) {
	for k := range m.outputs {
		if k.o == o {
			k.o = nil
		}
	}
}

func (m *Manager) PreferredChanged(s *surface.Surface) error {
	if len(m.feedback) == 0 {
		return nil
	}
	var errs []error
	for f := range m.feedback {
		if f.s == s {
			errs = append(errs, f.update())
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) AllPreferredChanged() error {
	var errs []error
	for f := range m.feedback {
		errs = append(errs, f.update())
	}
	return errors.Join(errs...)
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	res, err := scm.NewWpColorManagerV1(c, id, v, requests{m})
	if err != nil {
		return err
	}
	errs := []error{res.SendSupportedIntent(scm.WpColorManagerV1RenderIntentPerceptual)}
	for _, f := range features {
		if f == scm.WpColorManagerV1FeatureWindowsBt2100 && v < 3 {
			continue
		}
		errs = append(errs, res.SendSupportedFeature(f))
	}
	for _, tf := range transferFunctions {
		if tf.wire == scm.WpColorManagerV1TransferFunctionCompoundPower24 && v < 2 {
			continue
		}
		errs = append(errs, res.SendSupportedTfNamed(tf.wire))
	}
	for _, p := range primaries {
		errs = append(errs, res.SendSupportedPrimariesNamed(p.wire))
	}
	errs = append(errs, res.SendDone())
	return errors.Join(errs...)
}

type requests struct{ m *Manager }

func (requests) Destroy(*scm.WpColorManagerV1) error { return nil }

func (r requests) GetOutput(_ *scm.WpColorManagerV1, res *scm.WpColorManagementOutputV1, wl *sw.WlOutput) (scm.WpColorManagementOutputV1Handler, error) {
	k := &outputColor{m: r.m, res: res, o: r.m.view.Owner(wl)}
	r.m.outputs[k] = struct{}{}
	server.OnDestroy(res, func() error {
		delete(r.m.outputs, k)
		return nil
	})
	return k, nil
}

func (r requests) GetSurface(obj *scm.WpColorManagerV1, res *scm.WpColorManagementSurfaceV1, wl *sw.WlSurface) (scm.WpColorManagementSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	v, ok := r.m.surfaces.Attach(s)
	if !ok {
		return nil, obj.ProtocolError(scm.WpColorManagerV1ErrorSurfaceExists, "wl_surface@%d already has a color management surface", wl.Base().ID())
	}
	server.OnDestroy(res, v.Detach)
	return &tagged{v: v}, nil
}

func (r requests) GetSurfaceFeedback(_ *scm.WpColorManagerV1, res *scm.WpColorManagementSurfaceFeedbackV1, wl *sw.WlSurface) (scm.WpColorManagementSurfaceFeedbackV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	f := &feedback{m: r.m, res: res, s: s, last: r.m.view.PreferredDescription(s)}
	hooks := &surface.Hooks{Destroyed: func() error {
		f.s = nil
		delete(r.m.feedback, f)
		return nil
	}}
	s.AddHooks(hooks)
	r.m.feedback[f] = struct{}{}
	server.OnDestroy(res, func() error {
		if f.s != nil {
			f.s.RemoveHooks(hooks)
		}
		delete(r.m.feedback, f)
		return nil
	})
	return f, nil
}

func (r requests) CreateIccCreator(*scm.WpColorManagerV1, *scm.WpImageDescriptionCreatorIccV1) (scm.WpImageDescriptionCreatorIccV1Handler, error) {
	return &iccCreator{m: r.m}, nil
}

func (r requests) CreateParametricCreator(*scm.WpColorManagerV1, *scm.WpImageDescriptionCreatorParamsV1) (scm.WpImageDescriptionCreatorParamsV1Handler, error) {
	return &paramsCreator{m: r.m}, nil
}

func (r requests) CreateWindowsScrgb(_ *scm.WpColorManagerV1, res *scm.WpImageDescriptionV1) (scm.WpImageDescriptionV1Handler, error) {
	return r.m.ready(res, colorspace.SCRGB, r.m.identityOf(colorspace.SCRGB), false)
}

func (r requests) CreateWindowsBt2100(_ *scm.WpColorManagerV1, res *scm.WpImageDescriptionV1) (scm.WpImageDescriptionV1Handler, error) {
	return r.m.ready(res, colorspace.WindowsBT2100, r.m.identityOf(colorspace.WindowsBT2100), false)
}

func (r requests) GetImageDescription(_ *scm.WpColorManagerV1, res *scm.WpImageDescriptionV1, _ *scm.WpImageDescriptionReferenceV1) (scm.WpImageDescriptionV1Handler, error) {
	return r.m.failed(res, scm.WpImageDescriptionV1CauseUnsupported, "lys-wm hands out no image description references")
}

type imageDescription struct {
	d           *colorspace.Description
	information bool
}

func (m *Manager) ready(res *scm.WpImageDescriptionV1, d *colorspace.Description, id uint64, information bool) (scm.WpImageDescriptionV1Handler, error) {
	var err error
	if res.Base().Version() >= 2 {
		err = res.SendReady2(uint32(id>>32), uint32(id)) //nolint:gosec // the two halves of a 64-bit identity
	} else {
		err = res.SendReady(uint32(id)) //nolint:gosec // version 1 carries the low bits only
	}
	return &imageDescription{d: d, information: information}, err
}

func (m *Manager) failed(res *scm.WpImageDescriptionV1, cause scm.WpImageDescriptionV1Cause, msg string) (scm.WpImageDescriptionV1Handler, error) {
	return &imageDescription{}, res.SendFailed(cause, msg)
}

func (*imageDescription) Destroy(*scm.WpImageDescriptionV1) error { return nil }

func (i *imageDescription) GetInformation(obj *scm.WpImageDescriptionV1, res *scm.WpImageDescriptionInfoV1) error {
	switch {
	case i.d == nil:
		return obj.ProtocolError(scm.WpImageDescriptionV1ErrorNotReady, "the image description is not ready")
	case !i.information:
		return obj.ProtocolError(scm.WpImageDescriptionV1ErrorNoInformation, "this image description allows no get_information")
	}
	return sendInformation(res, i.d)
}

func scaled(c colorspace.XY) (int32, int32) {
	return int32(math.Round(c.X * million)), int32(math.Round(c.Y * million))
}

func sendInformation(res *scm.WpImageDescriptionInfoV1, d *colorspace.Description) error {
	rx, ry := scaled(d.Primaries.R)
	gx, gy := scaled(d.Primaries.G)
	bx, by := scaled(d.Primaries.B)
	wx, wy := scaled(d.Primaries.W)
	errs := []error{res.SendPrimaries(rx, ry, gx, gy, bx, by, wx, wy)}
	for _, p := range primaries {
		if p.p == d.Primaries {
			errs = append(errs, res.SendPrimariesNamed(p.wire))
			break
		}
	}
	switch d.TF.Name {
	case colorspace.Power:
		errs = append(errs, res.SendTfPower(uint32(math.Round(d.TF.Exponent*tfPrecise))))
	default:
		for _, tf := range transferFunctions {
			if tf.name == d.TF.Name {
				errs = append(errs, res.SendTfNamed(tf.wire))
				break
			}
		}
	}
	errs = append(errs, res.SendLuminances(uint32(math.Round(d.MinLum*lumPrecise)), uint32(math.Round(d.MaxLum)), uint32(math.Round(d.RefLum))))
	rx, ry = scaled(d.TargetPrimaries.R)
	gx, gy = scaled(d.TargetPrimaries.G)
	bx, by = scaled(d.TargetPrimaries.B)
	wx, wy = scaled(d.TargetPrimaries.W)
	errs = append(errs, res.SendTargetPrimaries(rx, ry, gx, gy, bx, by, wx, wy), res.SendTargetLuminance(uint32(math.Round(d.TargetMin*lumPrecise)), uint32(math.Round(d.TargetMax))))
	if d.MaxCLL > 0 {
		errs = append(errs, res.SendTargetMaxCll(uint32(math.Round(d.MaxCLL))))
	}
	if d.MaxFALL > 0 {
		errs = append(errs, res.SendTargetMaxFall(uint32(math.Round(d.MaxFALL))))
	}
	return errors.Join(append(errs, res.SendDone())...)
}

type outputColor struct {
	m   *Manager
	res *scm.WpColorManagementOutputV1
	o   *output.Output
}

func (*outputColor) Destroy(*scm.WpColorManagementOutputV1) error { return nil }

func (k *outputColor) GetImageDescription(_ *scm.WpColorManagementOutputV1, res *scm.WpImageDescriptionV1) (scm.WpImageDescriptionV1Handler, error) {
	if k.o == nil {
		return k.m.failed(res, scm.WpImageDescriptionV1CauseNoOutput, "the output is gone")
	}
	d := k.m.view.OutputDescription(k.o)
	return k.m.ready(res, d, k.m.identityOf(d), true)
}

type tagged struct {
	v *surface.LatchedValue[*colorspace.Description]
}

func (*tagged) Destroy(*scm.WpColorManagementSurfaceV1) error { return nil }

func (t *tagged) inert(obj *scm.WpColorManagementSurfaceV1) error {
	return obj.ProtocolError(scm.WpColorManagementSurfaceV1ErrorInert, "the wl_surface was destroyed")
}

func (t *tagged) SetImageDescription(obj *scm.WpColorManagementSurfaceV1, res *scm.WpImageDescriptionV1, intent scm.WpColorManagerV1RenderIntent) error {
	if t.v.Gone() {
		return t.inert(obj)
	}
	i, ok := res.Handler().(*imageDescription)
	if !ok || i.d == nil {
		return obj.ProtocolError(scm.WpColorManagementSurfaceV1ErrorImageDescription, "the image description is not ready")
	}
	if intent != scm.WpColorManagerV1RenderIntentPerceptual {
		return obj.ProtocolError(scm.WpColorManagementSurfaceV1ErrorRenderIntent, "render intent %d is not advertised", intent)
	}
	t.v.Set(i.d)
	return nil
}

func (t *tagged) UnsetImageDescription(obj *scm.WpColorManagementSurfaceV1) error {
	if t.v.Gone() {
		return t.inert(obj)
	}
	t.v.Set(nil)
	return nil
}

type feedback struct {
	m    *Manager
	res  *scm.WpColorManagementSurfaceFeedbackV1
	s    *surface.Surface
	last *colorspace.Description
}

func (*feedback) Destroy(*scm.WpColorManagementSurfaceFeedbackV1) error { return nil }

func (f *feedback) update() error {
	if f.s == nil {
		return nil
	}
	d := f.m.view.PreferredDescription(f.s)
	if d == f.last {
		return nil
	}
	f.last = d
	id := f.m.identityOf(d)
	if f.res.Base().Version() >= 2 {
		return f.res.SendPreferredChanged2(uint32(id>>32), uint32(id)) //nolint:gosec // the two halves of a 64-bit identity
	}
	return f.res.SendPreferredChanged(uint32(id)) //nolint:gosec // version 1 carries the low bits only
}

func (f *feedback) preferred(obj *scm.WpColorManagementSurfaceFeedbackV1, res *scm.WpImageDescriptionV1) (scm.WpImageDescriptionV1Handler, error) {
	if f.s == nil {
		return nil, obj.ProtocolError(scm.WpColorManagementSurfaceFeedbackV1ErrorInert, "the wl_surface was destroyed")
	}
	d := f.m.view.PreferredDescription(f.s)
	return f.m.ready(res, d, f.m.identityOf(d), true)
}

func (f *feedback) GetPreferred(obj *scm.WpColorManagementSurfaceFeedbackV1, res *scm.WpImageDescriptionV1) (scm.WpImageDescriptionV1Handler, error) {
	return f.preferred(obj, res)
}

func (f *feedback) GetPreferredParametric(obj *scm.WpColorManagementSurfaceFeedbackV1, res *scm.WpImageDescriptionV1) (scm.WpImageDescriptionV1Handler, error) {
	return f.preferred(obj, res)
}

type iccCreator struct {
	m    *Manager
	data []byte
	err  error
	set  bool
}

func (c *iccCreator) SetIccFile(obj *scm.WpImageDescriptionCreatorIccV1, fd int, offset, length uint32) error {
	return errors.Join(c.read(obj, fd, offset, length), unix.Close(fd))
}

func (c *iccCreator) read(obj *scm.WpImageDescriptionCreatorIccV1, fd int, offset, length uint32) error {
	if c.set {
		return obj.ProtocolError(scm.WpImageDescriptionCreatorIccV1ErrorAlreadySet, "the ICC file is already set")
	}
	c.set = true
	if length == 0 || length > maxICCSize {
		return obj.ProtocolError(scm.WpImageDescriptionCreatorIccV1ErrorBadSize, "%d bytes is not 1 to %d", length, maxICCSize)
	}
	if _, err := unix.Seek(fd, 0, unix.SEEK_CUR); err != nil {
		return obj.ProtocolError(scm.WpImageDescriptionCreatorIccV1ErrorBadFD, "the fd is not seekable: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return obj.ProtocolError(scm.WpImageDescriptionCreatorIccV1ErrorBadFD, "the fd cannot be examined: %v", err)
	}
	if int64(offset)+int64(length) > st.Size {
		return obj.ProtocolError(scm.WpImageDescriptionCreatorIccV1ErrorOutOfFile, "%d bytes at %d overrun a file of %d", length, offset, st.Size)
	}
	data := make([]byte, length)
	for n := 0; n < len(data); {
		k, err := unix.Pread(fd, data[n:], int64(offset)+int64(n))
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EBADF) || errors.Is(err, unix.EISDIR) || errors.Is(err, unix.EINVAL):
			return obj.ProtocolError(scm.WpImageDescriptionCreatorIccV1ErrorBadFD, "the fd is not readable: %v", err)
		case err != nil:
			c.err = fmt.Errorf("read the ICC file: %w", err)
			return nil
		case k == 0:
			c.err = errors.New("the ICC file shrank while it was read")
			return nil
		}
		n += k
	}
	c.data = data
	return nil
}

func (c *iccCreator) Create(obj *scm.WpImageDescriptionCreatorIccV1, res *scm.WpImageDescriptionV1) (scm.WpImageDescriptionV1Handler, error) {
	switch {
	case !c.set:
		return nil, obj.ProtocolError(scm.WpImageDescriptionCreatorIccV1ErrorIncompleteSet, "no ICC file was set")
	case c.err != nil:
		return c.m.failed(res, scm.WpImageDescriptionV1CauseOperatingSystem, c.err.Error())
	}
	d, err := colorspace.ParseICC(c.data)
	if err != nil {
		return c.m.failed(res, scm.WpImageDescriptionV1CauseUnsupported, err.Error())
	}
	return c.m.ready(res, d, c.m.nextIdentity(), false)
}

type paramsCreator struct {
	m               *Manager
	tf              colorspace.TF
	hasTF           bool
	primaries       colorspace.Primaries
	hasPrimaries    bool
	minLum          float64
	maxLum          float64
	refLum          float64
	hasLuminances   bool
	target          colorspace.Primaries
	hasTarget       bool
	targetMin       float64
	targetMax       float64
	hasTargetLum    bool
	maxCLL, maxFALL float64
	hasCLL, hasFALL bool
}

func alreadySet(obj *scm.WpImageDescriptionCreatorParamsV1, what string) error {
	return obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorAlreadySet, "%s is already set", what)
}

func (c *paramsCreator) SetTfNamed(obj *scm.WpImageDescriptionCreatorParamsV1, tf scm.WpColorManagerV1TransferFunction) error {
	if c.hasTF {
		return alreadySet(obj, "the transfer function")
	}
	for _, t := range transferFunctions {
		if t.wire == tf {
			c.tf, c.hasTF = colorspace.TF{Name: t.name}, true
			return nil
		}
	}
	return obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorInvalidTf, "transfer function %d is not supported", tf)
}

func (c *paramsCreator) SetTfPower(obj *scm.WpImageDescriptionCreatorParamsV1, eexp uint32) error {
	if c.hasTF {
		return alreadySet(obj, "the transfer function")
	}
	if eexp < 1*tfPrecise || eexp > 10*tfPrecise {
		return obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorInvalidTf, "exponent %d/10000 is not 1 to 10", eexp)
	}
	c.tf, c.hasTF = colorspace.TF{Name: colorspace.Power, Exponent: float64(eexp) / tfPrecise}, true
	return nil
}

func (c *paramsCreator) SetPrimariesNamed(obj *scm.WpImageDescriptionCreatorParamsV1, p scm.WpColorManagerV1Primaries) error {
	if c.hasPrimaries {
		return alreadySet(obj, "the primaries")
	}
	for _, n := range primaries {
		if n.wire == p {
			c.primaries, c.hasPrimaries = n.p, true
			return nil
		}
	}
	return obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorInvalidPrimariesNamed, "primaries %d are not supported", p)
}

func chromaticities(rX, rY, gX, gY, bX, bY, wX, wY int32) colorspace.Primaries {
	xy := func(x, y int32) colorspace.XY { return colorspace.XY{X: float64(x) / million, Y: float64(y) / million} }
	return colorspace.Primaries{R: xy(rX, rY), G: xy(gX, gY), B: xy(bX, bY), W: xy(wX, wY)}
}

func (c *paramsCreator) SetPrimaries(obj *scm.WpImageDescriptionCreatorParamsV1, rX, rY, gX, gY, bX, bY, wX, wY int32) error {
	if c.hasPrimaries {
		return alreadySet(obj, "the primaries")
	}
	c.primaries, c.hasPrimaries = chromaticities(rX, rY, gX, gY, bX, bY, wX, wY), true
	return nil
}

func (c *paramsCreator) SetLuminances(obj *scm.WpImageDescriptionCreatorParamsV1, minLum, maxLum, referenceLum uint32) error {
	if c.hasLuminances {
		return alreadySet(obj, "the luminances")
	}
	low := float64(minLum) / lumPrecise
	if float64(maxLum) <= low || float64(referenceLum) <= low {
		return obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorInvalidLuminance, "max %d and reference %d are not above min %v", maxLum, referenceLum, low)
	}
	c.minLum, c.maxLum, c.refLum, c.hasLuminances = low, float64(maxLum), float64(referenceLum), true
	return nil
}

func (c *paramsCreator) SetMasteringDisplayPrimaries(obj *scm.WpImageDescriptionCreatorParamsV1, rX, rY, gX, gY, bX, bY, wX, wY int32) error {
	if c.hasTarget {
		return alreadySet(obj, "the mastering primaries")
	}
	c.target, c.hasTarget = chromaticities(rX, rY, gX, gY, bX, bY, wX, wY), true
	return nil
}

func (c *paramsCreator) SetMasteringLuminance(obj *scm.WpImageDescriptionCreatorParamsV1, minLum, maxLum uint32) error {
	if c.hasTargetLum {
		return alreadySet(obj, "the mastering luminance")
	}
	low := float64(minLum) / lumPrecise
	if float64(maxLum) <= low {
		return obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorInvalidLuminance, "max %d is not above min %v", maxLum, low)
	}
	c.targetMin, c.targetMax, c.hasTargetLum = low, float64(maxLum), true
	return nil
}

func (c *paramsCreator) SetMaxCll(obj *scm.WpImageDescriptionCreatorParamsV1, v uint32) error {
	if c.hasCLL {
		return alreadySet(obj, "the maximum content light level")
	}
	c.maxCLL, c.hasCLL = float64(v), true
	return nil
}

func (c *paramsCreator) SetMaxFall(obj *scm.WpImageDescriptionCreatorParamsV1, v uint32) error {
	if c.hasFALL {
		return alreadySet(obj, "the maximum frame-average light level")
	}
	c.maxFALL, c.hasFALL = float64(v), true
	return nil
}

func (c *paramsCreator) Create(obj *scm.WpImageDescriptionCreatorParamsV1, res *scm.WpImageDescriptionV1) (scm.WpImageDescriptionV1Handler, error) {
	if !c.hasTF || !c.hasPrimaries {
		return nil, obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorIncompleteSet, "the transfer function and primaries are required")
	}
	if c.hasCLL && c.hasFALL && c.maxFALL > c.maxCLL {
		return nil, obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorInvalidLuminance, "max_fall %v is above max_cll %v", c.maxFALL, c.maxCLL)
	}
	d := c.description()
	if res.Base().Version() < 2 && (c.hasCLL && (c.maxCLL <= d.TargetMin || c.maxCLL > d.TargetMax) || c.hasFALL && (c.maxFALL <= d.TargetMin || c.maxFALL > d.TargetMax)) {
		return nil, obj.ProtocolError(scm.WpImageDescriptionCreatorParamsV1ErrorInvalidLuminance, "max_cll %v or max_fall %v is outside the mastering range %v to %v", c.maxCLL, c.maxFALL, d.TargetMin, d.TargetMax)
	}
	if !d.Check() {
		return c.m.failed(res, scm.WpImageDescriptionV1CauseUnsupported, "the primaries span no colour space")
	}
	return c.m.ready(res, d, c.m.nextIdentity(), false)
}

func (c *paramsCreator) description() *colorspace.Description {
	d := &colorspace.Description{TF: c.tf, Primaries: c.primaries, TargetPrimaries: c.primaries, MaxCLL: c.maxCLL, MaxFALL: c.maxFALL}
	d.MinLum, d.MaxLum, d.RefLum = colorspace.Luminances(c.tf.Name)
	if c.hasLuminances {
		d.MinLum, d.MaxLum, d.RefLum = c.minLum, c.maxLum, c.refLum
	}
	if c.tf.Name == colorspace.PQ {
		d.MaxLum = d.MinLum + 10000
	}
	d.TargetMin, d.TargetMax = d.MinLum, d.MaxLum
	if c.hasTarget {
		d.TargetPrimaries = c.target
	}
	if c.hasTargetLum {
		d.TargetMin, d.TargetMax = c.targetMin, c.targetMax
	}
	return d
}
