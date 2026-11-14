package harness

import (
	"errors"
	"image"
	"image/color"
	"regexp"
	"testing"
	"time"

	gtext "github.com/stubbedev/lys-lib/render"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const glassParityTolerance = 3

var (
	glassCentre = image.Pt(80, 50)
	glassTint   = color.RGBA{R: 0x20, B: 0x10, A: 0x40}
)

func glassOn(t *testing.T, start gpuStarter, paint testclient.Painter, configure func(*compositor.Config)) *Harness {
	t.Helper()
	h := start(t, func(cfg *compositor.Config) {
		cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 2, Passes: 1}
		cfg.Rules = []wm.Rule{clearWindowRule("glass")}
		configure(cfg)
	}, small)
	c := testclient.Connect(t, h.Socket())
	bg := c.Window("background", paint)
	c.DispatchUntil("the background to map", bg.Mapped)
	bg.Stop()
	glass := c.Window("glass", testclient.Fill(0, 0, 0))
	c.DispatchUntil("the glass to map", glass.Mapped)
	glass.Stop()
	settle(t, h)
	return h
}

func glassLook(t *testing.T, start gpuStarter, paint testclient.Painter, look wm.Look) *soft.Buffer {
	t.Helper()
	h := glassOn(t, start, paint, func(cfg *compositor.Config) {
		cfg.Decor.Blur.Look = look
		cfg.Decor.Rounding = 6
	})
	movePointer(h, 5, 95)
	settle(t, h)
	return h.Rendered(0)
}

func maxDifference(a, b *soft.Buffer, area image.Rectangle) int {
	worst := 0
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			pa, pb := a.Pix[y*a.Stride+x*4:], b.Pix[y*b.Stride+x*4:]
			for c := range 3 {
				d := int(pa[c]) - int(pb[c])
				worst = max(worst, max(d, -d))
			}
		}
	}
	return worst
}

func TestGlassLooksTheSameOnEveryRenderer(t *testing.T) {
	t.Parallel()
	everything := wm.Look{Tint: glassTint, SaturationShift: 0.5, BrightnessShift: 0.1, ContrastShift: -0.1, Grain: 0.1, Refraction: wm.Refraction{Bezel: 6, Amount: 4}, Specular: wm.Specular{Strength: 0.5, Width: 1}}
	for name, look := range map[string]wm.Look{
		"off":        {},
		"tint":       {Tint: glassTint},
		"saturation": {SaturationShift: 0.8},
		"brightness": {BrightnessShift: -0.4},
		"contrast":   {ContrastShift: 0.5},
		"grain":      {Grain: 0.3},
		"refraction": {Refraction: wm.Refraction{Bezel: 10, Amount: 8}},
		"specular":   {Specular: wm.Specular{Strength: 0.8, Width: 2}},
		"everything": everything,
	} {
		t.Run(name, func(t *testing.T) {
			want := glassLook(t, StartWith, quadrants, look)
			onEachGPURenderer(t, func(t *testing.T, start gpuStarter) {
				got := glassLook(t, start, quadrants, look)
				d := maxDifference(want, got, image.Rectangle{Max: small.Size})
				t.Logf("largest channel difference from the software renderer: %d", d)
				if d > glassParityTolerance {
					t.Errorf("differs from the software renderer by %d, above the tolerance of %d", d, glassParityTolerance)
				}
			})
		})
	}
}

func TestGlassColourIsTheBackdropAdjustedThenTinted(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		plain := glassLook(t, start, quadrants, wm.Look{})
		look := wm.Look{Tint: glassTint, SaturationShift: 0.6, BrightnessShift: -0.3, ContrastShift: 0.2}
		got := glassLook(t, start, quadrants, look)
		g := render.Glass{Saturation: 1.6, Brightness: 0.7, Contrast: 1.2}
		for _, at := range []image.Point{{60, 42}, {100, 42}, {60, 58}, {100, 58}, {75, 46}} {
			p := pixel(plain, at.X, at.Y)
			a := g.Adjust([4]float32{float32(p[0]), float32(p[1]), float32(p[2]), 255})
			k := 1 - float32(glassTint.A)/255
			want := [3]float32{float32(glassTint.R) + a[0]*k, float32(glassTint.G) + a[1]*k, float32(glassTint.B) + a[2]*k}
			have := pixel(got, at.X, at.Y)
			for c := range 3 {
				if d := want[c] - float32(have[c]); d > 3 || d < -3 {
					t.Errorf("%v: backdrop %v gives %v, want %v", at, p, have, want)
					break
				}
			}
		}
	})
}

func TestGlassGrainIsLysUIsGrain(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		const strength = 0.3
		got := glassLook(t, start, testclient.Fill(0x60, 0x60, 0x60), wm.Look{Grain: strength})
		base := pixel(got, 150, 5)
		want := soft.NewBuffer(small.Size.X, small.Size.Y)
		cv := gtext.New(want.Pix, want.Stride, want.W, want.H)
		cv.Clear(gtext.Rect{W: want.W, H: want.H}, gtext.RGB(base[0], base[1], base[2]))
		cv.PaintGrain(gtext.Rect{X: glassRect.Min.X, Y: glassRect.Min.Y, W: glassRect.Dx(), H: glassRect.Dy()}, gtext.UniformCorners(6), strength, glassRect.Min.X, glassRect.Min.Y)
		if d := maxDifference(want, got, glassRect); d > 3 {
			t.Errorf("differs from PaintGrain on the same pixels by %d", d)
		}
		for x := 60; x < 66; x++ {
			if pixel(got, x, 50) != pixel(got, x+1, 50) {
				return
			}
		}
		t.Error("the grain is flat")
	})
}

func twoBands(pix []byte, stride int, size image.Point, _ int) {
	for y := range size.Y {
		v := byte(0)
		if y < 30 {
			v = 0xff
		}
		for x := range size.X {
			copy(pix[y*stride+x*4:], []byte{v, v, v, 0xff})
		}
	}
}

func TestGlassBendsTheBackdropAtItsBezel(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		plain := glassLook(t, start, twoBands, wm.Look{})
		bent := glassLook(t, start, twoBands, wm.Look{Refraction: wm.Refraction{Bezel: 12, Amount: 14}})
		if v := pixel(plain, 80, 42)[0]; v > 0x10 {
			t.Errorf("without refraction the rim is already %#x: the test's blur reaches too far", v)
		}
		if v := pixel(bent, 80, 36)[0]; v < 0x60 {
			t.Errorf("the rim shows %#x, not the white band just beyond it", v)
		}
		if a, b := pixel(plain, 80, 55), pixel(bent, 80, 55); a != b {
			t.Errorf("past the bezel the glass bends: %v, without %v", b, a)
		}
	})
}

func TestARuleOverridesTheLookForItsWindow(t *testing.T) {
	t.Parallel()
	bright := float32(-0.5)
	plain := glassLook(t, StartWith, testclient.Fill(0x80, 0x80, 0x80), wm.Look{})
	h := glassOn(t, StartWith, testclient.Fill(0x80, 0x80, 0x80), func(cfg *compositor.Config) {
		cfg.Decor.Rounding = 6
		rule := clearWindowRule("glass")
		rule.Look = wm.LookOverride{BrightnessShift: &bright}
		cfg.Rules = []wm.Rule{rule}
	})
	got := h.Rendered(0)
	a, b := pixel(plain, 100, 60)[0], pixel(got, 100, 60)[0]
	if d := int(b) - int(a)/2; d > 2 || d < -2 {
		t.Errorf("a rule's brightness 0.5 shows %#x over %#x", b, a)
	}
}

func specularCases(t *testing.T, test func(t *testing.T, start gpuStarter)) {
	cursor := image.Pt(16, 16)
	t.Run("soft", func(t *testing.T) {
		test(t, func(t *testing.T, cfg func(*compositor.Config), modes ...backend.Mode) *Harness {
			t.Helper()
			return StartWithCursorPlanes(t, cursor, cfg, modes...)
		})
	})
	t.Run("gles", func(t *testing.T) {
		test(t, func(t *testing.T, cfg func(*compositor.Config), modes ...backend.Mode) *Harness {
			t.Helper()
			return StartGLESWithCursorPlanes(t, cursor, cfg, modes...)
		})
	})
	t.Run("vulkan", func(t *testing.T) {
		test(t, func(t *testing.T, cfg func(*compositor.Config), modes ...backend.Mode) *Harness {
			t.Helper()
			return StartVulkanWithCursorPlanes(t, cursor, cfg, modes...)
		})
	})
}

func movePointer(h *Harness, x, y float64) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], x/160, y/100), b.Pointer().Frame())
	})
}

func presents(h *Harness) uint64 {
	var n uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		n = b.Outputs()[0].Presents()
		return nil
	})
	return n
}

func animated(t testing.TB) anim.Table {
	t.Helper()
	d := time.Millisecond
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return table
}

func TestTheRimFollowsThePointerAndRedrawsOnlyItself(t *testing.T) {
	t.Parallel()
	specularCases(t, func(t *testing.T, start gpuStarter) {
		h := glassOn(t, start, testclient.Fill(0x20, 0x20, 0x20), func(cfg *compositor.Config) {
			cfg.Animations = animated(t)
			cfg.Decor.Rounding = 4
			cfg.Decor.Blur.Look.Specular = wm.Specular{Strength: 1, Width: 2}
		})
		left, right := image.Pt(glassRect.Min.X, glassCentre.Y), image.Pt(glassRect.Max.X-1, glassCentre.Y)
		movePointer(h, 2, 50)
		settle(t, h)
		lit := h.Rendered(0)
		if a, b := pixel(lit, left.X, left.Y)[0], pixel(lit, right.X, right.Y)[0]; a <= b+0x40 {
			t.Errorf("the pointer on the left: the left rim %#x is not brighter than the right %#x", a, b)
		}
		movePointer(h, 158, 50)
		settle(t, h)
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			for _, r := range b.Outputs()[0].LastDamage() {
				if glassCentre.In(r) {
					t.Errorf("the rim's move damaged %v, across the glass's middle", r)
				}
			}
			return nil
		})
		lit = h.Rendered(0)
		if a, b := pixel(lit, right.X, right.Y)[0], pixel(lit, left.X, left.Y)[0]; a <= b+0x40 {
			t.Errorf("the pointer on the right: the right rim %#x is not brighter than the left %#x", a, b)
		}
		settle(t, h)
		before := presents(h)
		movePointer(h, 158, 50.3)
		settle(t, h)
		if got := presents(h); got != before {
			t.Errorf("a move that does not turn the light presented %d frames", got-before)
		}
		h.Advance(100 * time.Millisecond)
		if got := presents(h); got != before {
			t.Errorf("%d frames presented with nothing moving", got-before)
		}
	})
}

func TestTheRimStaysStillWithAnimationsOff(t *testing.T) {
	t.Parallel()
	specularCases(t, func(t *testing.T, start gpuStarter) {
		table, errs := anim.Resolve(false, nil)
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		h := glassOn(t, start, testclient.Fill(0x20, 0x20, 0x20), func(cfg *compositor.Config) {
			cfg.Animations = table
			cfg.Decor.Blur.Look.Specular = wm.Specular{Strength: 1, Width: 2}
		})
		movePointer(h, 158, 90)
		settle(t, h)
		still := h.Rendered(0)
		if a, b := pixel(still, glassRect.Min.X, glassRect.Min.Y)[0], pixel(still, glassRect.Max.X-1, glassRect.Max.Y-1)[0]; a <= b {
			t.Errorf("the resting light from the top left: top left %#x, bottom right %#x", a, b)
		}
		for _, at := range [][2]float64{{2, 90}, {158, 5}} {
			movePointer(h, at[0], at[1])
			settle(t, h)
			h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
				for _, r := range b.Outputs()[0].LastDamage() {
					if r.Overlaps(glassRect) {
						t.Errorf("moving the pointer to %v damaged the glass at %v", at, r)
					}
				}
				return nil
			})
		}
	})
}

func startWallpaper(t *testing.T, h *Harness) (*testclient.Client, *testclient.Window) {
	t.Helper()
	shell := testclient.Connect(t, h.Socket())
	wallpaper := shell.Layer(testclient.LayerSpec{
		Layer:  cwlr.ZwlrLayerShellV1LayerBackground,
		Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight,
	}, quadrants)
	shell.DispatchUntil("the wallpaper to map", wallpaper.Mapped)
	return shell, wallpaper
}

func TestMicaTintsWithTheWallpaperAtOneBlurPerOutput(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		on := true
		second := clearWindowRule("second")
		second.Mica = &on
		first := clearWindowRule("first")
		first.Mica = &on
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Blur = compositor.Blur{Size: 2, Passes: 1, Mica: compositor.Mica{Size: 2, Passes: 2}}
			cfg.Rules = []wm.Rule{first, second, {Match: wm.Match{Title: regexp.MustCompile(`^under$`)}, Mode: wm.RuleFloat, Size: wm.Px(140, 90)}}
		}, small)
		shell, wallpaper := startWallpaper(t, h)
		c := testclient.Connect(t, h.Socket())
		under := c.Window("under", testclient.Fill(0xff, 0xff, 0xff))
		c.DispatchUntil("the white window to map", under.Mapped)
		under.Stop()
		for _, title := range []string{"first", "second"} {
			w := c.Window(title, testclient.Fill(0, 0, 0))
			c.DispatchUntil("the mica window to map", w.Mapped)
			w.Stop()
		}
		h.Eventually("mica showing the wallpaper's blue quadrant, not the white window below", func(b *headless.Backend, _ *compositor.Compositor) bool {
			px := pixel(snapshot(b.Outputs()[0]), 55, 60)
			return px[1] < 0x40 && px[2] > 0x40 && px[0] < 0x40
		})
		f0, r0 := framesAndBlurs(h)
		pumpFrames(h, shell, 12)
		f1, r1 := framesAndBlurs(h)
		if r1-r0 > f1-f0 {
			t.Errorf("%d blur redraws over %d frames behind two mica windows, want one a frame", r1-r0, f1-f0)
		}
		wallpaper.Stop()
		settle(t, h)
		r0 = blurRedraws(h)
		h.Advance(100 * time.Millisecond)
		if r1 = blurRedraws(h); r1 != r0 {
			t.Errorf("%d blur redraws with nothing behind the mica moving", r1-r0)
		}
	})
}

func TestGlassDrawsOnAnHDROutput(t *testing.T) {
	t.Parallel()
	look := wm.Look{Tint: glassTint, SaturationShift: 0.5, BrightnessShift: 0.1, ContrastShift: -0.1, Grain: 0.1, Refraction: wm.Refraction{Bezel: 6, Amount: 4}, Specular: wm.Specular{Strength: 0.5, Width: 1}}
	pixels := func(t *testing.T, start gpuStarter, look wm.Look) *soft.Buffer {
		t.Helper()
		h := glassOn(t, start, quadrants, func(cfg *compositor.Config) {
			cfg.Decor.Blur.Look = look
			cfg.Decor.Rounding = 6
			cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: compositor.HDROn}}
		})
		movePointer(h, 5, 95)
		settle(t, h)
		return h.Rendered(0)
	}
	want := pixels(t, StartWith, look)
	if maxDifference(pixels(t, StartWith, wm.Look{}), want, glassRect) < 8 {
		t.Error("the look changes nothing on an HDR output")
	}
	onEachGPURenderer(t, func(t *testing.T, start gpuStarter) {
		got := pixels(t, start, look)
		d := maxDifference(want, got, image.Rectangle{Max: small.Size})
		t.Logf("largest channel difference from the software renderer: %d", d)
		if d > 8 {
			t.Errorf("differs from the software renderer by %d", d)
		}
	})
}

func framesAndBlurs(h *Harness) (frames, redraws uint64) {
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		frames, redraws = b.Outputs()[0].Frames(), c.BlurRedraws()
		return nil
	})
	return frames, redraws
}

func filteredPixels(h *Harness) uint64 {
	var n uint64
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		n = c.FilteredPixels()
		return nil
	})
	return n
}

const bandFrames = 6

var glassBand = testclient.Band{Height: 12, Step: 7}

func glassOverBand(t *testing.T, start gpuStarter, damage testclient.Damager) (rendered *soft.Buffer, filtered uint64, last int) {
	t.Helper()
	h := start(t, func(cfg *compositor.Config) {
		cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 2, Passes: 1}
		cfg.Rules = []wm.Rule{clearWindowRule("glass")}
	}, small)
	c := testclient.Connect(t, h.Socket())
	bg := c.PartialWindow("background", func(pix []byte, stride int, size image.Point, frame int) {
		last = frame
		glassBand.Paint(pix, stride, size, frame)
	}, damage)
	c.DispatchUntil("the background to map", bg.Mapped)
	bg.Stop()
	glass := c.Window("glass", testclient.Fill(0, 0, 0))
	c.DispatchUntil("the glass to map", glass.Mapped)
	glass.Stop()
	settle(t, h)
	before := filteredPixels(h)
	for range bandFrames {
		if err := bg.Redraw(); err != nil {
			t.Fatal(err)
		}
		c.Sync()
		settle(t, h)
	}
	return h.Rendered(0), filteredPixels(h) - before, last
}

func TestGlassOverAMovingBandRefiltersOnlyItsReachToTheSamePixelsAsAWholeRedraw(t *testing.T) {
	t.Parallel()
	whole := func(size image.Point, _ int) []image.Rectangle { return []image.Rectangle{{Max: size}} }
	onEachGPURenderer(t, func(t *testing.T, start gpuStarter) {
		got, partial, last := glassOverBand(t, start, glassBand.Damage)
		want, everything, _ := glassOverBand(t, start, whole)
		if d := maxDifference(want, got, image.Rectangle{Max: small.Size}); d != 0 {
			t.Errorf("after %d band moves the band's damage alone shows pixels up to %d from the window's whole damage", bandFrames, d)
		}
		fresh := glassOn(t, start, func(pix []byte, stride int, size image.Point, _ int) {
			glassBand.Paint(pix, stride, size, last)
		}, func(*compositor.Config) {}).Rendered(0)
		if d := maxDifference(fresh, got, image.Rectangle{Max: small.Size}); d != 0 {
			t.Errorf("after %d band moves the output differs from a fresh whole redraw by %d", bandFrames, d)
		}
		t.Logf("%d band moves refiltered %d pixels, %d with the whole window damaged", bandFrames, partial, everything)
		if partial*2 > everything {
			t.Errorf("%d band moves refiltered %d pixels, over half the %d the whole window's damage costs", bandFrames, partial, everything)
		}
	})
}
