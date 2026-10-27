package harness

import (
	"errors"
	"image"
	"slices"
	"testing"

	ccr "github.com/stubbedev/lys-wm/internal/protocol/client/colorrepresentation"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type colorRepEvents struct {
	alpha []ccr.WpColorRepresentationSurfaceV1AlphaMode
	pairs [][2]uint32
	done  bool
}

func (e *colorRepEvents) SupportedAlphaMode(_ *ccr.WpColorRepresentationManagerV1, m ccr.WpColorRepresentationSurfaceV1AlphaMode) error {
	e.alpha = append(e.alpha, m)
	return nil
}

func (e *colorRepEvents) SupportedCoefficientsAndRanges(_ *ccr.WpColorRepresentationManagerV1, c ccr.WpColorRepresentationSurfaceV1Coefficients, r ccr.WpColorRepresentationSurfaceV1Range) error {
	e.pairs = append(e.pairs, [2]uint32{uint32(c), uint32(r)})
	return nil
}

func (e *colorRepEvents) Done(*ccr.WpColorRepresentationManagerV1) error {
	e.done = true
	return nil
}

func colorRep(t *testing.T, h *Harness) (*testclient.Client, *ccr.WpColorRepresentationManagerV1, *colorRepEvents) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	ev := &colorRepEvents{}
	m, err := ccr.NewWpColorRepresentationManagerV1(c.Display, 1, ev)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	c.DispatchUntil("the supported representations", func() bool { return ev.done })
	return c, m, ev
}

func TestColorRepresentationAdvertisesWhatTheImportHonours(t *testing.T) {
	h := Start(t, small)
	_, _, ev := colorRep(t, h)
	if !slices.Equal(ev.alpha, []ccr.WpColorRepresentationSurfaceV1AlphaMode{ccr.WpColorRepresentationSurfaceV1AlphaModePremultipliedElectrical}) {
		t.Fatalf("alpha modes %v, want premultiplied electrical only", ev.alpha)
	}
	var want [][2]uint32
	for _, c := range []ccr.WpColorRepresentationSurfaceV1Coefficients{ccr.WpColorRepresentationSurfaceV1CoefficientsBt709, ccr.WpColorRepresentationSurfaceV1CoefficientsBt601, ccr.WpColorRepresentationSurfaceV1CoefficientsBt2020} {
		for _, r := range []ccr.WpColorRepresentationSurfaceV1Range{ccr.WpColorRepresentationSurfaceV1RangeFull, ccr.WpColorRepresentationSurfaceV1RangeLimited} {
			want = append(want, [2]uint32{uint32(c), uint32(r)})
		}
	}
	if !slices.Equal(ev.pairs, want) {
		t.Fatalf("coefficients and ranges %v, want %v", ev.pairs, want)
	}
}

func TestColorRepresentationProtocolErrors(t *testing.T) {
	h := Start(t, small)
	for name, c := range map[string]struct {
		do   func(t *testing.T, c *testclient.Client, m *ccr.WpColorRepresentationManagerV1)
		code ccr.WpColorRepresentationSurfaceV1Error
	}{
		"a second object": {func(t *testing.T, c *testclient.Client, m *ccr.WpColorRepresentationManagerV1) {
			s := c.Surface()
			_, err := m.GetSurface(s)
			_, err2 := m.GetSurface(s)
			if err := errors.Join(err, err2); err != nil {
				t.Fatal(err)
			}
		}, ccr.WpColorRepresentationSurfaceV1Error(ccr.WpColorRepresentationManagerV1ErrorSurfaceExists)},
		"straight alpha": {func(t *testing.T, c *testclient.Client, m *ccr.WpColorRepresentationManagerV1) {
			r, err := m.GetSurface(c.Surface())
			if err := errors.Join(err, r.SetAlphaMode(ccr.WpColorRepresentationSurfaceV1AlphaModeStraight)); err != nil {
				t.Fatal(err)
			}
		}, ccr.WpColorRepresentationSurfaceV1ErrorAlphaMode},
		"unsupported coefficients": {func(t *testing.T, c *testclient.Client, m *ccr.WpColorRepresentationManagerV1) {
			r, err := m.GetSurface(c.Surface())
			if err := errors.Join(err, r.SetCoefficientsAndRange(ccr.WpColorRepresentationSurfaceV1CoefficientsIctcp, ccr.WpColorRepresentationSurfaceV1RangeFull)); err != nil {
				t.Fatal(err)
			}
		}, ccr.WpColorRepresentationSurfaceV1ErrorCoefficients},
		"coefficients on an RGB buffer": {func(t *testing.T, c *testclient.Client, m *ccr.WpColorRepresentationManagerV1) {
			s := c.Surface()
			r, err := m.GetSurface(s)
			if err := errors.Join(err, r.SetCoefficientsAndRange(ccr.WpColorRepresentationSurfaceV1CoefficientsBt709, ccr.WpColorRepresentationSurfaceV1RangeLimited),
				s.Attach(c.Buffer(image.Pt(4, 4), 0, quadrants), 0, 0), s.Commit()); err != nil {
				t.Fatal(err)
			}
		}, ccr.WpColorRepresentationSurfaceV1ErrorPixelFormat},
	} {
		t.Run(name, func(t *testing.T) {
			client, m, _ := colorRep(t, h)
			c.do(t, client, m)
			client.ExpectProtocolError(uint32(c.code))
		})
	}
}
