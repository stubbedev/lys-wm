package compositor

import (
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/render"
)

type focusLook struct {
	opacity anim.Tween[float64]
	dim     anim.Tween[float64]
	shadow  anim.Tween[[4]uint8]
}

func newFocusLook() focusLook {
	return focusLook{opacity: anim.NewTween(1.0, anim.Lerp), dim: anim.NewTween(0.0, anim.Lerp), shadow: anim.NewTween([4]uint8{}, anim.LerpColor)}
}

func colorWords(c render.Color) [4]uint8 {
	return [4]uint8{c.R, c.G, c.B, c.A}
}

func (c *Compositor) dimTarget(w *window) float64 {
	if !c.decor.Dim.Inactive || w == c.active {
		return 0
	}
	return float64(c.decor.Dim.Strength)
}

func (c *Compositor) shadowTarget(w *window) [4]uint8 {
	if w == c.active {
		return colorWords(c.decor.Shadow.Active)
	}
	return colorWords(c.decor.Shadow.Inactive)
}

func retarget[V comparable](t *anim.Tween[V], to V, now time.Duration, curve anim.Curve, jump bool) {
	switch {
	case jump:
		t.Jump(to)
	case t.Target() != to:
		t.Set(to, now, curve)
	}
}

func (c *Compositor) refreshLooks(now time.Duration) {
	for _, w := range c.windows {
		jump := !w.looked
		w.looked = true
		retarget(&w.look.opacity, float64(c.opacity(w)), now, c.animations.Curve(anim.FadeSwitch), jump)
		retarget(&w.look.dim, c.dimTarget(w), now, c.animations.Curve(anim.FadeDim), jump)
		retarget(&w.look.shadow, c.shadowTarget(w), now, c.animations.Curve(anim.FadeShadow), jump)
	}
}

func (w *window) looking(now time.Duration) bool {
	return !w.look.opacity.Done(now) || !w.look.dim.Done(now) || !w.look.shadow.Done(now)
}

func (c *Compositor) shownOpacity(w *window) float32 {
	return float32(w.look.opacity.At(c.frameTime))
}

func (c *Compositor) shownDim(w *window) render.Color {
	return render.Color{A: uint8(min(max(w.look.dim.At(c.frameTime), 0), 1)*0xff + 0.5)}
}

func (c *Compositor) shownShadow(w *window) render.Color {
	v := w.look.shadow.At(c.frameTime)
	return render.Color{R: v[0], G: v[1], B: v[2], A: v[3]}
}
