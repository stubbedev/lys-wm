package compositor

import (
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
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
		c.refreshBorder(w, now, jump)
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

func (c *Compositor) borderTarget(w *window) Border {
	if w == c.active {
		return c.decor.ActiveBorder
	}
	return c.decor.InactiveBorder
}

func (c *Compositor) refreshBorder(w *window, now time.Duration, jump bool) {
	to := c.borderTarget(w)
	switch {
	case jump:
		w.borderFrom, w.borderTo = to, to
		w.borderFade = anim.NewTween(1.0, anim.Lerp)
		w.borderAngle = anim.NewTween(1.0, anim.Lerp)
	case to != w.borderTo:
		w.borderFrom, w.borderTo = w.borderTo, to
		w.borderFade = anim.NewTween(0.0, anim.Lerp)
		w.borderFade.Set(1, now, c.animations.Curve(anim.Border))
		if to.n > 1 {
			w.borderAngle = anim.NewTween(0.0, anim.Lerp)
			w.borderAngle.Set(1, now, c.animations.Curve(anim.BorderAngle))
		}
	}
}

func (c *Compositor) loopingBorders() bool {
	curve := c.animations.Curve(anim.BorderAngle)
	return curve != anim.Instant && c.animations.Style(anim.BorderAngle).Kind == anim.Loop && curve.Duration() > 0
}

func (c *Compositor) borderTurn(w *window) float64 {
	if !c.loopingBorders() {
		return 360 * w.borderAngle.At(c.frameTime)
	}
	period := c.animations.Curve(anim.BorderAngle).Duration()
	return 360 * float64(c.frameTime%period) / float64(period)
}

func (c *Compositor) borderMoving(w *window, now time.Duration) bool {
	return !w.borderFade.Done(now) || !w.borderAngle.Done(now) || (c.loopingBorders() && w.borderTo.n > 1 && w.layer != wm.LayerFullscreen)
}
