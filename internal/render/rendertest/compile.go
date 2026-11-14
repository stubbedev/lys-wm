package rendertest

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shader"
)

func Effects(t *testing.T, r render.Renderer, formats []render.Format) render.Effects {
	t.Helper()
	var all []render.Texture
	blank := func(f render.Format, size image.Point) render.Texture {
		tex, err := r.NewBlankTexture(f, size)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, tex)
		return tex
	}
	e := render.Effects{Stops: blank(render.ARGB8888, image.Pt(2, 1))}
	for _, f := range formats {
		e.Textures = append(e.Textures, blank(f, image.Pt(2, 2)))
	}
	target, err := r.NewWorkingTarget(image.Pt(2, 2))
	if errors.Is(err, gpu.ErrUnsupported) {
		target, err = r.NewTextureTarget(image.Pt(2, 2))
	}
	if err != nil {
		t.Fatal(err)
	}
	e.Sources = []render.Texture{target.Texture()}
	t.Cleanup(func() {
		errs := []error{target.Destroy()}
		for _, tex := range all {
			errs = append(errs, tex.Destroy())
		}
		if err := errors.Join(errs...); err != nil {
			t.Error(err)
		}
	})
	return e
}

func EveryEffect(t *testing.T, r render.Renderer) render.Effects {
	t.Helper()
	e := Effects(t, r, r.Formats())
	cut, err := r.NewBlankTexture(render.ARGB8888, image.Pt(2, 2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cut.Destroy(); err != nil {
			t.Error(err)
		}
	})
	e.Cut, e.Color = cut, &render.ColorTransform{Matrix: [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1}, Scale: 1}
	return e
}

func CompilesOnAPinnedThread(job func() error) (uint64, error) {
	type result struct {
		compiles uint64
		err      error
	}
	done := make(chan result, 1)
	go func() {
		unpin := gpu.PinThread()
		var compiles uint64
		err := gpu.CompileThrough(func() { compiles++ })
		if err == nil {
			err = job()
		}
		done <- result{compiles, errors.Join(err, unpin())}
	}()
	got := <-done
	return got.compiles, got.err
}

type CompileStep struct {
	What                string
	Compiles, ColdDraws uint64
}

func MeasureCompiles(t *testing.T, r render.Renderer, what string, job func() error) CompileStep {
	t.Helper()
	before := r.Counts().ColdDraws
	n, err := CompilesOnAPinnedThread(job)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return CompileStep{what, n, r.Counts().ColdDraws - before}
}

func PaintEffects(r render.Renderer, target render.Target, e render.Effects, shaders ...render.ScreenShader) func() error {
	return func() error {
		for _, s := range shaders {
			if err := s.Prepare(target); err != nil {
				return err
			}
		}
		p, err := r.Begin(target, render.Region{{Max: target.Size()}})
		if err != nil {
			return err
		}
		render.DrawEffects(p, e)
		for _, s := range shaders {
			p.DrawScreenShader(s, e.Sources[0], image.Rect(0, 0, 1, 1), 1)
		}
		return p.End()
	}
}

func CompilesLeaveRealtimeOncePerDrawState(t *testing.T, r render.Renderer, target, other render.Target) {
	t.Helper()
	e := EveryEffect(t, r)
	first := MeasureCompiles(t, r, "every effect, first drawn", PaintEffects(r, target, e))
	if first.ColdDraws == 0 || first.Compiles != first.ColdDraws {
		t.Fatalf("%+v: want realtime left before each draw in a state not drawn before, and only then", first)
	}
	program, err := shader.Compile([]byte(ScreenShaderProgram))
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewScreenShader(program)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Destroy(); err != nil {
			t.Error(err)
		}
	})
	ordinary := render.Effects{Stops: e.Stops, Sources: e.Sources}
	got := []CompileStep{
		MeasureCompiles(t, r, "every effect, drawn again", PaintEffects(r, target, e)),
		MeasureCompiles(t, r, "every effect into another target of the format", PaintEffects(r, other, e)),
		MeasureCompiles(t, r, "a screen shader's first frame", PaintEffects(r, target, ordinary, s)),
		MeasureCompiles(t, r, "its next", PaintEffects(r, target, ordinary, s)),
	}
	shaderFirst := got[2]
	if shaderFirst.ColdDraws == 0 || shaderFirst.Compiles != shaderFirst.ColdDraws {
		t.Fatalf("%+v: want realtime left for the screen shader's first draw", shaderFirst)
	}
	got[2] = CompileStep{shaderFirst.What, 0, 0}
	for _, step := range got {
		if step.Compiles != 0 || step.ColdDraws != 0 {
			t.Fatalf("compiles and cold draws per step %+v: want none but the first frame of each", got)
		}
	}
}

func WarmedTargetDrawsOrdinaryFramesWithoutLeavingRealtime(t *testing.T, r render.Renderer, target render.Target, warm func() error) {
	t.Helper()
	if err := warm(); err != nil {
		t.Fatal(err)
	}
	e := Effects(t, r, []render.Format{render.ARGB8888, render.XRGB8888})
	if got := MeasureCompiles(t, r, "an ordinary frame after the warm-up", PaintEffects(r, target, e)); got.Compiles != 0 || got.ColdDraws != 0 {
		t.Fatalf("%+v: want nothing compiled", got)
	}
}
