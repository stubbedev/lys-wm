package headless

import (
	"errors"
	"fmt"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
)

var _ backend.OverlayPlanes = (*Output)(nil)

type overlayLayers [backend.MaxOverlayPlanes]*backend.Overlay

func (b *Backend) SetOverlayPlanes(n int, t gpu.ScanoutTarget) error {
	if n < 0 || n > backend.MaxOverlayPlanes {
		return fmt.Errorf("headless: %d overlay planes, want 0 to %d", n, backend.MaxOverlayPlanes)
	}
	b.overlayPlanes, b.overlayTarget = n, t
	return nil
}

func (b *Backend) RefuseOverlayFlips(refuse bool) {
	b.refuseOverlay = refuse
}

func (o *Output) OverlayPlanes() int {
	return o.backend.overlayPlanes
}

func (o *Output) OverlayTarget() gpu.ScanoutTarget {
	return o.backend.overlayTarget
}

func (o *Output) takesOverlay(l backend.Overlay) bool {
	return l.Key != nil && l.At.Valid(l.Buffer.Size, o.state.Mode.Size) && slices.Contains(o.backend.overlayTarget.Formats, gpu.FormatModifier{Format: l.Buffer.Format, Modifier: l.Buffer.Modifier})
}

func (o *Output) TestOverlays(layers []backend.Overlay) uint {
	var mask uint
	for i, l := range layers[:min(len(layers), o.backend.overlayPlanes)] {
		if o.takesOverlay(l) {
			mask |= 1 << i
		}
	}
	return mask
}

func (o *Output) SetOverlays(layers []backend.Overlay) error {
	if len(layers) > o.backend.overlayPlanes {
		return fmt.Errorf("headless: %d overlays on %s, which has %d overlay planes", len(layers), o.name, o.backend.overlayPlanes)
	}
	var next overlayLayers
	for i, l := range layers {
		if l.Key == nil {
			continue
		}
		if !o.takesOverlay(l) {
			return fmt.Errorf("headless: overlay plane %d of %s cannot show a %v buffer at %+v", i, o.name, l.Buffer.Format, l.At)
		}
		next[i] = &l
	}
	err := o.dropUnsent()
	o.overlayNext = next
	return err
}

func (o *Output) dropUnsent() error {
	var errs []error
	for i, l := range o.overlayNext {
		if l != nil && l != o.overlayShown[i] && l != o.overlayQueued[i] {
			errs = append(errs, l.Release())
		}
	}
	o.overlayNext = o.overlayShown
	return errors.Join(errs...)
}

func (o *Output) FlipOverlays() (backend.OverlayFlip, error) {
	if !o.state.Lit() || o.removed || o.inFrame || o.presentPending {
		return backend.OverlaysNotFlipped, nil
	}
	if o.backend.refuseOverlay {
		return backend.OverlaysRefused, o.dropUnsent()
	}
	o.latchOverlays()
	o.presentPending = true
	o.overlayFlips++
	return backend.OverlaysFlipped, o.arm()
}

func (o *Output) OverlayFlips() uint64 {
	return o.overlayFlips
}

func (o *Output) ShownOverlays() []backend.Placement {
	var at []backend.Placement
	for _, l := range o.overlayShown {
		if l != nil {
			at = append(at, l.At)
		}
	}
	return at
}

func (o *Output) latchOverlays() {
	o.overlayQueued, o.overlayLatched = o.overlayNext, true
}

func (o *Output) overlaysFlipped() error {
	if !o.overlayLatched {
		return nil
	}
	o.overlayLatched = false
	var errs []error
	for i, old := range o.overlayShown {
		if old != nil && old != o.overlayQueued[i] {
			errs = append(errs, old.Release())
		}
	}
	o.overlayShown = o.overlayQueued
	return errors.Join(errs...)
}

func (o *Output) releaseOverlays() error {
	errs := []error{o.dropUnsent()}
	for i := range o.overlayShown {
		shown, queued := o.overlayShown[i], o.overlayQueued[i]
		if shown != nil {
			errs = append(errs, shown.Release())
		}
		if queued != nil && queued != shown {
			errs = append(errs, queued.Release())
		}
	}
	o.overlayShown, o.overlayQueued, o.overlayNext, o.overlayLatched = overlayLayers{}, overlayLayers{}, overlayLayers{}, false
	return errors.Join(errs...)
}
