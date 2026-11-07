package harness

import (
	"errors"
	"image"
	"regexp"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func layerRule(namespace string, p wm.LayerProps) wm.LayerRule {
	return wm.LayerRule{Namespace: regexp.MustCompile("^" + namespace + "$"), LayerProps: p}
}

func TestLayerRulesPickEachLayersAnimation(t *testing.T) {
	t.Parallel()
	now := time.Hour
	d := time.Second
	off, on := false, true
	fade := anim.Style{Kind: anim.FadeStyle}
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Layers: {Enabled: &on, Shape: anim.Linear, Duration: &d}, anim.LayersIn: {Enabled: &on, Shape: anim.Linear, Duration: &d, Style: &fade}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
		cfg.LayerRules = []wm.LayerRule{
			layerRule("sliding", wm.LayerProps{Animation: &wm.WindowAnimation{In: anim.Style{Kind: anim.Slide}}}),
			layerRule("still", wm.LayerProps{Animation: &wm.WindowAnimation{Off: true}}),
		}
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	c := testclient.Connect(t, h.Socket())
	open := func(namespace string, anchor cwlr.ZwlrLayerSurfaceV1Anchor) {
		l := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: anchor, Size: image.Pt(40, 20), Namespace: namespace}, testclient.Fill(0, 0, 0xff))
		c.DispatchUntil(namespace+" to map", l.Mapped)
	}
	red := [3]byte{0xff, 0, 0}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	open("sliding", cwlr.ZwlrLayerSurfaceV1AnchorTop|cwlr.ZwlrLayerSurfaceV1AnchorLeft)
	open("fading", cwlr.ZwlrLayerSurfaceV1AnchorTop|cwlr.ZwlrLayerSurfaceV1AnchorRight)
	open("still", cwlr.ZwlrLayerSurfaceV1AnchorBottom|cwlr.ZwlrLayerSurfaceV1AnchorLeft)
	at(time.Hour + 500*time.Millisecond)
	h.Eventually("halfway: the slide moved, the fade faded, the still one in place", func(b *headless.Backend, _ *compositor.Compositor) bool {
		s := snapshot(b.Outputs()[0])
		return pixel(s, 20, 5) == red && pixel(s, 20, 15) == bg &&
			near(pixel(s, 140, 10), blend(red, bg, 0.5)) &&
			pixel(s, 20, 110) == red
	})
}

func TestABlurAlphaThresholdLeavesTransparentTexelsSharp(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		on, threshold := true, float32(0.2)
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Blur = compositor.Blur{Size: 3, Passes: 2}
			cfg.LayerRules = []wm.LayerRule{layerRule("bar", wm.LayerProps{Blur: &on, BlurAlpha: &threshold})}
		}, small)
		c := testclient.Connect(t, h.Socket())
		bg := c.Window("background", fineChecker)
		c.DispatchUntil("the background to map", bg.Mapped)
		bg.Stop()
		halfClear := func(pix []byte, stride int, size image.Point, _ int) {
			clear(pix)
			for y := range size.Y {
				for x := size.X / 2; x < size.X; x++ {
					pix[y*stride+x*4+3] = 0x40
				}
			}
		}
		bar := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight, Size: image.Pt(0, 30), Namespace: "bar"}, halfClear)
		bar.Translucent = true
		c.DispatchUntil("the bar to map", bar.Mapped)
		bar.Stop()
		h.Eventually("the translucent half blurred", frostedAt(120, 15))
		h.Eventually("the transparent half sharp", sharpAt(40, 15))
	})
}

func TestABlurAlphaThresholdShapesADMABUFBar(t *testing.T) {
	t.Parallel()
	onEachDMABUFRenderer(t, func(t *testing.T, start gpuStarter) {
		threshold := float32(0.2)
		cut := dmabufBarFramePasses(t, start, &threshold)
		plain := dmabufBarFramePasses(t, start, nil)
		if cut != plain || cut == 0 {
			t.Errorf("a frame of the dmabuf bar takes %d passes with a threshold and %d without", cut, plain)
		}
	})
}

func dmabufBarFramePasses(t *testing.T, start gpuStarter, threshold *float32) uint64 {
	t.Helper()
	on := true
	h := start(t, func(cfg *compositor.Config) {
		cfg.Decor.Blur = compositor.Blur{Size: 3, Passes: 2}
		cfg.LayerRules = []wm.LayerRule{layerRule("bar", wm.LayerProps{Blur: &on, BlurAlpha: threshold})}
	}, small)
	c := testclient.Connect(t, h.Socket())
	bg := c.Window("background", fineChecker)
	c.DispatchUntil("the background to map", bg.Mapped)
	bg.Stop()
	size := image.Pt(160, 30)
	bar := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight, Size: image.Pt(0, size.Y), Namespace: "bar"}, testclient.Fill(0, 0, 0))
	bar.Translucent = true
	c.DispatchUntil("the bar to map", bar.Mapped)
	bar.Stop()
	buf := c.DMABUFIn(fourcc.ARGB8888, size, func(pix []byte, stride int, size image.Point, _ int) {
		for y := range size.Y {
			row := pix[y*stride:]
			clear(row[:size.X*4])
			for x := size.X / 2; x < size.X; x++ {
				row[x*4+3] = 0x40
			}
		}
	})
	commit := func() {
		t.Helper()
		if err := errors.Join(bar.Surface.Attach(buf.WL, 0, 0), bar.Surface.DamageBuffer(0, 0, int32(size.X), int32(size.Y)), bar.Surface.Commit(), c.Display.Roundtrip()); err != nil {
			t.Fatal(err)
		}
	}
	commit()
	h.Eventually("the translucent half of the dmabuf blurred", frostedAt(120, 15))
	if threshold != nil {
		h.Eventually("the transparent half of the dmabuf sharp", sharpAt(40, 15))
	} else {
		h.Eventually("the transparent half of the dmabuf blurred", frostedAt(40, 15))
	}
	settle(t, h)
	commit()
	var passes uint64
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		before := b.Renderer().Counts().Passes
		if err := comp.Frame(b.Outputs()[0]); err != nil {
			return err
		}
		passes = b.Renderer().Counts().Passes - before
		return nil
	})
	return passes
}
