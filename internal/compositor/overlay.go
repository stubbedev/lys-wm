package compositor

import (
	"errors"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/output"
	spt "github.com/stubbedev/lys-wm/internal/protocol/server/presentationtime"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

const (
	overlayMinPixels  = 256 * 256
	overlayHysteresis = 2
)

func overlayCandidates(ops []drawOp, cursorFrom int, bounds image.Rectangle, picked []int) []int {
	for i := cursorFrom - 1; i >= 0 && len(picked) < cap(picked); i-- {
		if op := &ops[i]; op.overlayable(bounds) && !paintedOver(ops[i+1:], op.dst) {
			picked = append(picked, i)
		}
	}
	return picked
}

func paintedOver(above []drawOp, r image.Rectangle) bool {
	for i := range above {
		if above[i].paints(r) {
			return true
		}
	}
	return false
}

func (op *drawOp) paints(r image.Rectangle) bool {
	m := &op.mask
	area := op.dst
	if !m.Keep.Rect.Empty() {
		area = area.Intersect(m.Keep.Rect)
	}
	if !area.Overlaps(r) {
		return false
	}
	if m.Cut.Rect.Empty() || !m.Merge.Empty() {
		return true
	}
	edge := int(math.Ceil(float64(m.Smooth + m.Falloff.Range)))
	corner := edge + int(math.Ceil(float64(m.Cut.Radius)))
	cut := m.Cut.Rect
	wide := image.Rect(cut.Min.X+edge, cut.Min.Y+corner, cut.Max.X-edge, cut.Max.Y-corner)
	tall := image.Rect(cut.Min.X+corner, cut.Min.Y+edge, cut.Max.X-corner, cut.Max.Y-edge)
	return !r.In(wide) && !r.In(tall)
}

func (op *drawOp) overlayable(bounds image.Rectangle) bool {
	return op.surface != nil && op.covers() && op.transform == render.Normal && op.alpha == 1 && op.whole.Empty() &&
		op.dst != bounds && op.dst.In(bounds) && op.dst.Dx()*op.dst.Dy() >= overlayMinPixels && op.src.W > 0 && op.src.H > 0
}

type overlayShown struct {
	buffer *dmabuf.Buffer
	at     backend.Placement
}

type overlaysShown [backend.MaxOverlayPlanes]overlayShown

type overlayWork struct {
	picks   [backend.MaxOverlayPlanes]int
	layers  [backend.MaxOverlayPlanes]backend.Overlay
	ops     [backend.MaxOverlayPlanes]*drawOp
	buffers [backend.MaxOverlayPlanes]*dmabuf.Buffer
}

type candidacy struct {
	output       *output.Output
	out          backend.OverlayPlanes
	hits, misses int
	offered      bool
}

func (c *Compositor) assignOverlays(o *output.Output, sc *scene, bounds image.Rectangle, shading bool) error {
	out, ok := o.Backend().(backend.OverlayPlanes)
	if !ok {
		return nil
	}
	w := &c.overlayWork
	*w = overlayWork{}
	picks, layers, ops, buffers := &w.picks, &w.layers, &w.ops, &w.buffers
	var want overlaysShown
	n := 0
	candidates := picks[:0:min(out.OverlayPlanes(), backend.MaxOverlayPlanes)]
	if !shading && sc.zoom == nil && c.adopting == nil && c.hdr[o.Backend()] == nil && !c.composedApart(o) {
		candidates = overlayCandidates(sc.cur.ops, sc.cur.cursorFrom, bounds, candidates)
	}
	for _, i := range candidates {
		op := &sc.cur.ops[i]
		t, isImported := c.textures[op.surface].(imported)
		if !isImported || !colorspace.Passthrough(op.space, c.scanoutTarget(o)) {
			continue
		}
		layers[n] = backend.Overlay{Key: t.buffer, Buffer: c.overlayBuffer(t.buffer, op.surface), At: backend.Placement{Src: op.src, Dst: op.dst}, Release: c.imported[t.buffer].left}
		ops[n], buffers[n] = op, t.buffer
		n++
	}
	err := c.offerOverlays(o, out, sc.cur.ops, candidates)
	taken := out.TestOverlays(layers[:n])
	for i := range n {
		if taken&(1<<i) == 0 {
			layers[i] = backend.Overlay{}
			continue
		}
		want[i] = overlayShown{buffer: buffers[i], at: layers[i].At}
		ops[i].overlay, sc.zeroCopy[1+i] = true, ops[i].surface
	}
	if want == c.overlays[o] {
		return err
	}
	last := n
	for last > 0 && layers[last-1].Key == nil {
		last--
	}
	if setErr := out.SetOverlays(layers[:last]); setErr != nil {
		for i := range n {
			if ops[i] != nil {
				ops[i].overlay, sc.zeroCopy[1+i] = false, nil
			}
		}
		return errors.Join(err, setErr)
	}
	for _, w := range want {
		if w.buffer == nil {
			continue
		}
		held, ok := c.scannedOut[w.buffer]
		if !ok {
			held = c.takeScannedOut()
			c.scannedOut[w.buffer] = held
		}
		held.outputs++
	}
	if want == (overlaysShown{}) {
		delete(c.overlays, o)
		return err
	}
	if c.overlays == nil {
		c.overlays = map[*output.Output]overlaysShown{}
	}
	c.overlays[o] = want
	return err
}

func (c *Compositor) offerOverlays(o *output.Output, out backend.OverlayPlanes, ops []drawOp, candidates []int) error {
	var err error
	for _, i := range candidates {
		s := ops[i].surface
		k, ok := c.candidates[s]
		if !ok {
			if c.candidates == nil {
				c.candidates = map[*surface.Surface]*candidacy{}
			}
			k = &candidacy{}
			c.candidates[s] = k
		}
		k.output, k.out, k.misses = o, out, 0
		if k.hits++; k.hits >= overlayHysteresis && !k.offered {
			k.offered = true
			err = errors.Join(err, c.scanoutChanged(s))
		}
	}
	for s, k := range c.candidates {
		if k.output != o || slices.ContainsFunc(candidates, func(i int) bool { return ops[i].surface == s }) {
			continue
		}
		k.hits = 0
		if k.misses++; k.misses < overlayHysteresis {
			continue
		}
		delete(c.candidates, s)
		if k.offered {
			err = errors.Join(err, c.scanoutChanged(s))
		}
	}
	return err
}

func (c *Compositor) scanoutChanged(s *surface.Surface) error {
	if c.dmabuf == nil {
		return nil
	}
	return c.dmabuf.ScanoutChanged(s)
}

func (c *Compositor) dropCandidates(o *output.Output) {
	for s, k := range c.candidates {
		if k.output == o {
			delete(c.candidates, s)
		}
	}
}

func (c *Compositor) overlayBuffer(b *dmabuf.Buffer, s *surface.Surface) gpu.DMABUF {
	d := b.DMABUF()
	d.Format = d.Format.Opaque()
	if d.Format.YUV() {
		d.YUV = c.colorrep.Of(s)
	}
	return d
}

func (c *Compositor) flipOverlays(o *output.Output, sc *scene) (bool, error) {
	shown, ok := c.overlays[o]
	out, planes := o.Backend().(backend.OverlayPlanes)
	if !ok || !planes || !sc.drawn || sc.targetStale {
		return false, nil
	}
	if shown == (overlaysShown{}) || !sc.onlyOverlaidChanged() {
		return false, nil
	}
	flip, err := out.FlipOverlays()
	switch flip {
	case backend.OverlaysFlipped:
		return true, err
	case backend.OverlaysRefused:
		return false, errors.Join(err, c.composeOverlaid(o, out, sc))
	}
	return false, err
}

func (c *Compositor) composeOverlaid(o *output.Output, out backend.OverlayPlanes, sc *scene) error {
	delete(c.overlays, o)
	for i := range sc.cur.ops {
		if op := &sc.cur.ops[i]; op.overlay {
			op.overlay = false
			sc.damage.Add(op.dst)
		}
	}
	clear(sc.zeroCopy[1:])
	return out.SetOverlays(nil)
}

func (sc *scene) onlyOverlaidChanged() bool {
	prev, cur := sc.prev.ops, sc.cur.ops
	if !sc.blurClean || len(prev) != len(cur) {
		return false
	}
	for i := range cur {
		if p, c := &prev[i], &cur[i]; *p != *c && (!c.overlay || c.surface != p.surface || c.dst != p.dst) {
			return false
		}
	}
	for _, s := range sc.changed {
		if !slices.Contains(sc.zeroCopy[1:], s) {
			return false
		}
	}
	return true
}

func (sc *scene) feedbackKind(s *surface.Surface, kind spt.WpPresentationFeedbackKind, idle bool) spt.WpPresentationFeedbackKind {
	if !idle && slices.Contains(sc.zeroCopy[:], s) {
		kind |= spt.WpPresentationFeedbackKindZeroCopy
	}
	return kind
}
