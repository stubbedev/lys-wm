// Package colorrep implements wp_color_representation_manager_v1: a client
// says how its YCbCr surface encodes colour, which the compositor uses to
// decode it instead of guessing from the buffer size.
package colorrep

import (
	"maps"
	"slices"

	"github.com/stubbedev/lys-wm/internal/gpu"
	scr "github.com/stubbedev/lys-wm/internal/protocol/server/colorrepresentation"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

var Interface = scr.WpColorRepresentationManagerV1Interface

var coefficients = map[scr.WpColorRepresentationSurfaceV1Coefficients]gpu.Coefficients{
	scr.WpColorRepresentationSurfaceV1CoefficientsBt601:  gpu.BT601,
	scr.WpColorRepresentationSurfaceV1CoefficientsBt709:  gpu.BT709,
	scr.WpColorRepresentationSurfaceV1CoefficientsBt2020: gpu.BT2020,
}

var ranges = map[scr.WpColorRepresentationSurfaceV1Range]gpu.Range{
	scr.WpColorRepresentationSurfaceV1RangeFull:    gpu.FullRange,
	scr.WpColorRepresentationSurfaceV1RangeLimited: gpu.LimitedRange,
}

var chroma = map[scr.WpColorRepresentationSurfaceV1ChromaLocation]gpu.ChromaSiting{
	scr.WpColorRepresentationSurfaceV1ChromaLocationType0: {Horizontal: gpu.SitedAt0, Vertical: gpu.SitedAtHalf},
	scr.WpColorRepresentationSurfaceV1ChromaLocationType1: {Horizontal: gpu.SitedAtHalf, Vertical: gpu.SitedAtHalf},
	scr.WpColorRepresentationSurfaceV1ChromaLocationType2: {Horizontal: gpu.SitedAt0, Vertical: gpu.SitedAt0},
	scr.WpColorRepresentationSurfaceV1ChromaLocationType3: {Horizontal: gpu.SitedAtHalf, Vertical: gpu.SitedAt0},
	scr.WpColorRepresentationSurfaceV1ChromaLocationType4: {Horizontal: gpu.SitedAt0, Vertical: gpu.SitedAt1},
	scr.WpColorRepresentationSurfaceV1ChromaLocationType5: {Horizontal: gpu.SitedAtHalf, Vertical: gpu.SitedAt1},
}

type Manager struct {
	reps *surface.Latched[gpu.YUVEncoding]
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{reps: surface.NewLatched(gpu.YUVEncoding{})}
	if _, err := d.AddGlobal(Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Of(s *surface.Surface) gpu.YUVEncoding {
	return m.reps.Get(s)
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	res, err := scr.NewWpColorRepresentationManagerV1(c, id, v, requests{m})
	if err != nil {
		return err
	}
	if err := res.SendSupportedAlphaMode(scr.WpColorRepresentationSurfaceV1AlphaModePremultipliedElectrical); err != nil {
		return err
	}
	for _, c := range slices.Sorted(maps.Keys(coefficients)) {
		for _, r := range slices.Sorted(maps.Keys(ranges)) {
			if err := res.SendSupportedCoefficientsAndRanges(c, r); err != nil {
				return err
			}
		}
	}
	return res.SendDone()
}

type requests struct{ m *Manager }

func (requests) Destroy(*scr.WpColorRepresentationManagerV1) error { return nil }

func (r requests) GetSurface(obj *scr.WpColorRepresentationManagerV1, res *scr.WpColorRepresentationSurfaceV1, wl *sw.WlSurface) (scr.WpColorRepresentationSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	rs := &represented{res: res}
	check := &surface.Hooks{Commit: rs.check}
	s.AddHooks(check)
	v, ok := r.m.reps.Attach(s)
	if !ok {
		s.RemoveHooks(check)
		return nil, obj.ProtocolError(scr.WpColorRepresentationManagerV1ErrorSurfaceExists, "wl_surface@%d already has a colour representation", wl.Base().ID())
	}
	rs.v, rs.pending = v, r.m.reps.Get(s)
	server.OnDestroy(res, func() error {
		if !v.Gone() {
			s.RemoveHooks(check)
		}
		return v.Detach()
	})
	return rs, nil
}

type represented struct {
	res     *scr.WpColorRepresentationSurfaceV1
	v       *surface.LatchedValue[gpu.YUVEncoding]
	pending gpu.YUVEncoding
}

func (*represented) Destroy(*scr.WpColorRepresentationSurfaceV1) error { return nil }

func (rs *represented) inert(obj *scr.WpColorRepresentationSurfaceV1) error {
	return obj.ProtocolError(scr.WpColorRepresentationSurfaceV1ErrorInert, "the wl_surface was destroyed")
}

func (rs *represented) SetAlphaMode(obj *scr.WpColorRepresentationSurfaceV1, mode scr.WpColorRepresentationSurfaceV1AlphaMode) error {
	switch {
	case rs.v.Gone():
		return rs.inert(obj)
	case mode != scr.WpColorRepresentationSurfaceV1AlphaModePremultipliedElectrical:
		return obj.ProtocolError(scr.WpColorRepresentationSurfaceV1ErrorAlphaMode, "alpha mode %d is not supported", mode)
	}
	return nil
}

func (rs *represented) SetCoefficientsAndRange(obj *scr.WpColorRepresentationSurfaceV1, c scr.WpColorRepresentationSurfaceV1Coefficients, r scr.WpColorRepresentationSurfaceV1Range) error {
	if rs.v.Gone() {
		return rs.inert(obj)
	}
	gc, okc := coefficients[c]
	gr, okr := ranges[r]
	if !okc || !okr {
		return obj.ProtocolError(scr.WpColorRepresentationSurfaceV1ErrorCoefficients, "coefficients %d with range %d are not supported", c, r)
	}
	rs.pending.Coefficients, rs.pending.Range = gc, gr
	rs.v.Set(rs.pending)
	return nil
}

func (rs *represented) SetChromaLocation(obj *scr.WpColorRepresentationSurfaceV1, location scr.WpColorRepresentationSurfaceV1ChromaLocation) error {
	if rs.v.Gone() {
		return rs.inert(obj)
	}
	siting, ok := chroma[location]
	if !ok {
		return obj.ProtocolError(scr.WpColorRepresentationSurfaceV1ErrorChromaLocation, "chroma location %d", location)
	}
	rs.pending.Chroma = siting
	rs.v.Set(rs.pending)
	return nil
}

func (rs *represented) check(u *surface.Update) error {
	b, ok := u.Buffer().(interface{ DMABUF() gpu.DMABUF })
	yuv := ok && b.DMABUF().Format.YUV()
	if u.Buffer() == nil || yuv || rs.pending == (gpu.YUVEncoding{}) {
		return nil
	}
	return rs.res.ProtocolError(scr.WpColorRepresentationSurfaceV1ErrorPixelFormat, "YCbCr coefficients or chroma siting on an RGB buffer")
}
