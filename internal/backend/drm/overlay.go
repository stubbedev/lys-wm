package drm

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
)

const (
	testedOverlays    = 16
	propColorEncoding = "COLOR_ENCODING"
	propColorRange    = "COLOR_RANGE"
	nvidiaDriver      = "nvidia"
)

var (
	colorEncodings = map[gpu.Coefficients]string{gpu.BT601: "ITU-R BT.601 YCbCr", gpu.BT709: "ITU-R BT.709 YCbCr", gpu.BT2020: "ITU-R BT.2020 YCbCr"}
	colorRanges    = map[gpu.Range]string{gpu.LimitedRange: "YCbCr limited range", gpu.FullRange: "YCbCr full range"}
)

type planeYUV struct {
	encoding, rng uint64
	set           bool
}

type overlayUse struct {
	client  *clientFB
	test    overlayTest
	release func() error
}

type overlayTest struct {
	plane uint32
	fb    uint32
	at    backend.Placement
	yuv   planeYUV
}

type overlaySet [backend.MaxOverlayPlanes]overlayTest

type overlayUses [backend.MaxOverlayPlanes]*overlayUse

func (u *overlayUses) set() overlaySet {
	var s overlaySet
	for i, use := range u {
		if use != nil {
			s[i] = use.test
		}
	}
	return s
}

func (s *overlaySet) holds(fb uint32) bool {
	return slices.ContainsFunc(s[:], func(t overlayTest) bool { return t.fb == fb })
}

type overlayFlip struct {
	err     error
	changed bool
}

func (o *Output) overlaysUsable() bool {
	return len(o.overlays) > 0 && o.enabled && o.lit && !o.off && !o.b.paused && o.kms == kmsFlip && !o.flipping && o.drawing == nil && o.front != nil
}

func (o *Output) OverlayPlanes() int {
	return len(o.overlays)
}

func (o *Output) OverlayTarget() gpu.ScanoutTarget {
	var formats []gpu.FormatModifier
	for _, p := range o.overlays {
		for _, f := range p.clients {
			if !slices.Contains(formats, f) {
				formats = append(formats, f)
			}
		}
	}
	slices.SortFunc(formats, func(a, b gpu.FormatModifier) int {
		return cmp.Or(cmp.Compare(a.Format, b.Format), cmp.Compare(a.Modifier, b.Modifier))
	})
	return gpu.ScanoutTarget{Device: o.b.number, Formats: formats}
}

func (o *Output) layerTest(i int, l backend.Overlay) (overlayTest, *clientFB, bool) {
	p := o.overlays[i]
	if !l.At.Valid(l.Buffer.Size, modeOf(o.mode).Size) || !slices.Contains(p.clients, gpu.FormatModifier{Format: l.Buffer.Format, Modifier: l.Buffer.Modifier}) {
		return overlayTest{}, nil, false
	}
	yuv, err := o.overlayYUV(p, l.Buffer)
	if err != nil {
		return overlayTest{}, nil, false
	}
	cf, err := o.b.clientFB(l.Key, l.Buffer)
	if err != nil {
		return overlayTest{}, nil, false
	}
	return overlayTest{plane: p.id, fb: cf.fb, at: l.At, yuv: yuv}, cf, true
}

func (o *Output) TestOverlays(layers []backend.Overlay) uint {
	if !o.overlaysUsable() {
		return 0
	}
	spent := false
	var set overlaySet
	var taken []int
	for i, l := range layers[:min(len(layers), len(o.overlays))] {
		t, _, ok := o.layerTest(i, l)
		if !ok {
			continue
		}
		passed, seen := o.overlayTested[t]
		if !seen {
			if spent {
				continue
			}
			spent = true
			var alone overlaySet
			alone[i] = t
			passed = o.testOverlays(alone)
			o.rememberOverlay(t, passed)
		}
		if passed {
			set[i] = t
			taken = append(taken, i)
		}
	}
	for len(taken) > 1 {
		passed, seen := o.overlaySets[set]
		if !seen && !spent {
			spent = true
			passed = o.testOverlays(set)
			o.rememberOverlaySet(set, passed)
			seen = true
		}
		if seen && passed {
			break
		}
		last := taken[len(taken)-1]
		set[last], taken = overlayTest{}, taken[:len(taken)-1]
	}
	var mask uint
	for _, i := range taken {
		mask |= 1 << i
	}
	return mask
}

func (o *Output) testOverlays(set overlaySet) bool {
	o.b.req.Reset()
	for i, t := range set {
		if t.fb != 0 {
			o.placeOverlay(o.overlays[i], t)
		}
	}
	return o.test(planeFlags) == nil
}

func (o *Output) rememberOverlay(t overlayTest, ok bool) {
	if o.overlayTested == nil || len(o.overlayTested) >= testedOverlays {
		o.overlayTested = map[overlayTest]bool{}
	}
	o.overlayTested[t] = ok
}

func (o *Output) rememberOverlaySet(s overlaySet, ok bool) {
	if o.overlaySets == nil || len(o.overlaySets) >= testedOverlays {
		o.overlaySets = map[overlaySet]bool{}
	}
	o.overlaySets[s] = ok
}

func (o *Output) overlayYUV(p *plane, d gpu.DMABUF) (planeYUV, error) {
	if !d.Format.YUV() {
		return planeYUV{}, nil
	}
	y := d.YUV
	if y.Coefficients == gpu.CoefficientsUnspecified {
		y = gpu.DefaultYUV(d.Size)
		if o.b.driver == nvidiaDriver {
			y.Range = gpu.FullRange
		}
	}
	encoding, err := p.enum(o.b.dev, propColorEncoding, colorEncodings[y.Coefficients])
	if err != nil {
		return planeYUV{}, err
	}
	rng, err := p.enum(o.b.dev, propColorRange, colorRanges[y.Range])
	if err != nil {
		return planeYUV{}, err
	}
	return planeYUV{encoding: encoding, rng: rng, set: true}, nil
}

func (p *plane) enum(dev device, name, value string) (uint64, error) {
	prop, ok := p.props[name]
	if !ok {
		return 0, fmt.Errorf("drm: plane %d has no %s property", p.id, name)
	}
	info, err := dev.PropertyInfo(prop.ID)
	if err != nil {
		return 0, fmt.Errorf("drm: the %s property of plane %d: %w", name, p.id, err)
	}
	v, ok := info.Enums[value]
	if !ok {
		return 0, fmt.Errorf("drm: the %s property of plane %d has no %q", name, p.id, value)
	}
	return v, nil
}

func (o *Output) placeOverlay(p *plane, t overlayTest) {
	r := &o.b.req
	p.set(r, "FB_ID", uint64(t.fb))
	p.set(r, "CRTC_ID", uint64(o.crtc.id))
	p.setRects(r, t.at.Src, t.at.Dst)
	if t.yuv.set {
		p.set(r, propColorEncoding, t.yuv.encoding)
		p.set(r, propColorRange, t.yuv.rng)
	}
}

func (o *Output) SetOverlays(layers []backend.Overlay) error {
	if len(layers) > len(o.overlays) {
		return fmt.Errorf("drm: %d overlays on %s, which has %d overlay planes", len(layers), o.Name(), len(o.overlays))
	}
	var next overlayUses
	for i, l := range layers {
		if l.Key == nil {
			continue
		}
		t, cf, ok := o.layerTest(i, l)
		if !ok {
			return fmt.Errorf("drm: overlay plane %d of %s cannot show a %v %v buffer at %+v", o.overlays[i].id, o.Name(), l.Buffer.Format, l.Buffer.Modifier, l.At)
		}
		next[i] = &overlayUse{client: cf, test: t, release: l.Release}
	}
	o.overlayNext = next
	return nil
}

func (o *Output) addOverlay() {
	r := &o.b.req
	for i, p := range o.overlays {
		next := o.overlayNext[i]
		switch {
		case next == o.overlayShown[i]:
		case next != nil:
			o.placeOverlay(p, next.test)
		default:
			p.set(r, "FB_ID", 0)
			p.set(r, "CRTC_ID", 0)
		}
	}
}

func (o *Output) sentOverlay() {
	if o.overlayNext == o.overlayShown {
		return
	}
	o.overlayQueued = o.overlayNext
	for i, u := range o.overlayQueued {
		if u != nil && u != o.overlayShown[i] {
			u.client.shown++
		}
	}
}

func (o *Output) FlipOverlays() (bool, error) {
	if !o.overlaysUsable() {
		return false, nil
	}
	r := &o.b.req
	r.Reset()
	o.addOverlay()
	if err := o.b.dev.Commit(r, planeFlags, uint64(o.crtc.id)); err != nil {
		if refused(err) {
			o.overlayRefused()
			return false, nil
		}
		return false, fmt.Errorf("drm: flip the overlays of %s: %w", o.Name(), err)
	}
	o.sentOverlay()
	o.flipping = true
	return true, nil
}

func (o *Output) overlayFlipped() overlayFlip {
	if o.overlayQueued == o.overlayShown {
		return overlayFlip{}
	}
	var errs []error
	for i, old := range o.overlayShown {
		if old != nil && old != o.overlayQueued[i] {
			old.client.leave()
			errs = append(errs, old.release())
		}
	}
	o.overlayShown = o.overlayQueued
	return overlayFlip{err: errors.Join(errs...), changed: true}
}

func (o *Output) dropOverlays() error {
	var errs []error
	for i := range o.overlays {
		shown, queued := o.overlayShown[i], o.overlayQueued[i]
		if shown != nil {
			shown.client.leave()
			errs = append(errs, shown.release())
		}
		if queued != nil && queued != shown {
			queued.client.leave()
			errs = append(errs, queued.release())
		}
	}
	o.overlayShown, o.overlayQueued, o.overlayNext = overlayUses{}, overlayUses{}, overlayUses{}
	return errors.Join(errs...)
}

func (o *Output) overlayRefused() {
	if o.overlayNext == o.overlayShown {
		return
	}
	set := o.overlayNext.set()
	if layers := slices.DeleteFunc(slices.Clone(set[:]), func(t overlayTest) bool { return t.fb == 0 }); len(layers) == 1 {
		o.rememberOverlay(layers[0], false)
	} else {
		o.rememberOverlaySet(set, false)
	}
	o.overlayNext = o.overlayShown
}

func (o *Output) forgetOverlayTests(fb uint32) {
	maps.DeleteFunc(o.overlayTested, func(t overlayTest, _ bool) bool { return t.fb == fb })
	maps.DeleteFunc(o.overlaySets, func(s overlaySet, _ bool) bool { return s.holds(fb) })
}
