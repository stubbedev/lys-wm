package harness

import (
	"errors"
	"fmt"
	"image"
	"math"
	"regexp"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cam "github.com/stubbedev/lys-wm/internal/protocol/client/alphamodifier"
	cbe "github.com/stubbedev/lys-wm/internal/protocol/client/backgroundeffect"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func fineChecker(pix []byte, stride int, size image.Point, _ int) {
	for y := range size.Y {
		for x := range size.X {
			v := byte(0xff)
			if (x/2+y/2)%2 == 0 {
				v = 0
			}
			copy(pix[y*stride+x*4:], []byte{v, v, v, 0xff})
		}
	}
}

func clearGlass(pix []byte, _ int, _ image.Point, _ int) {
	clear(pix)
}

func sharpAt(x, y int) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		v := pixel(snapshot(b.Outputs()[0]), x, y)[0]
		return v < 0x08 || v > 0xf7
	}
}

func frostedAt(x, y int) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		v := pixel(snapshot(b.Outputs()[0]), x, y)[0]
		return v > 0x40 && v < 0xc0
	}
}

type effects struct {
	c *testclient.Client
	m *cbe.ExtBackgroundEffectManagerV1
}

type capabilities struct {
	flags *cbe.ExtBackgroundEffectManagerV1Capability
}

func (e capabilities) Capabilities(_ *cbe.ExtBackgroundEffectManagerV1, flags cbe.ExtBackgroundEffectManagerV1Capability) error {
	*e.flags = flags
	return nil
}

func bindEffects(t *testing.T, c *testclient.Client) effects {
	t.Helper()
	var flags cbe.ExtBackgroundEffectManagerV1Capability
	m, err := cbe.NewExtBackgroundEffectManagerV1(c.Display, 1, capabilities{&flags})
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !flags.Has(cbe.ExtBackgroundEffectManagerV1CapabilityBlur) {
		t.Fatalf("the manager offered capabilities %v, want blur", flags)
	}
	return effects{c, m}
}

func (e effects) of(t *testing.T, s *cw.WlSurface) *cbe.ExtBackgroundEffectSurfaceV1 {
	t.Helper()
	obj, err := e.m.GetBackgroundEffect(s)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func (e effects) set(t *testing.T, obj *cbe.ExtBackgroundEffectSurfaceV1, rects ...image.Rectangle) {
	t.Helper()
	g, err := e.c.Compositor.CreateRegion()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rects {
		if err := g.Add(int32(r.Min.X), int32(r.Min.Y), int32(r.Dx()), int32(r.Dy())); err != nil { //nolint:gosec // test rects are small
			t.Fatal(err)
		}
	}
	if err := errors.Join(obj.SetBlurRegion(g), g.Destroy()); err != nil {
		t.Fatal(err)
	}
}

var glassRule = []wm.Rule{{Title: regexp.MustCompile(`^glass$`), Mode: wm.RuleFloat, Size: image.Pt(60, 30)}}

func startGlass(t *testing.T, start gpuStarter, configure func(*compositor.Config)) (*Harness, *testclient.Client, *testclient.Window) {
	t.Helper()
	h := start(t, func(cfg *compositor.Config) {
		cfg.Rules = glassRule
		cfg.Decor.Blur.Size, cfg.Decor.Blur.Passes = 3, 2
		if configure != nil {
			configure(cfg)
		}
	}, small)
	c := testclient.Connect(t, h.Socket())
	bg := c.Window("background", fineChecker)
	c.DispatchUntil("the background to map", bg.Mapped)
	bg.Stop()
	glass := c.Window("glass", clearGlass)
	glass.Translucent = true
	c.DispatchUntil("the glass to map", glass.Mapped)
	glass.Stop()
	return h, c, glass
}

func TestABlurRegionBlursBehindItAlone(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h, c, glass := startGlass(t, start, nil)
		fx := bindEffects(t, c)
		obj := fx.of(t, glass.Surface)
		h.Eventually("no blur before a region", sharpAt(55, 40))

		fx.set(t, obj, image.Rect(0, 0, 20, 30), image.Rect(40, 0, 60, 30))
		if err := c.Display.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		settle(t, h)
		h.Eventually("the region waiting for its commit", sharpAt(55, 40))

		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the left rect blurred", frostedAt(55, 40))
		h.Eventually("the right rect blurred", frostedAt(105, 40))
		h.Eventually("the gap between the rects sharp", sharpAt(75, 45))
		h.Eventually("outside the glass sharp", sharpAt(45, 50))

		if err := errors.Join(obj.SetBlurRegion(nil), glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("a null region removes the blur", sharpAt(55, 40))

		fx.set(t, obj, image.Rect(0, 0, 60, 30))
		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("a region back on", frostedAt(75, 45))
		if err := errors.Join(obj.Destroy(), glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("destroying the object removes the blur on the next commit", sharpAt(75, 45))
	})
}

func TestARegionIsClippedToItsSurface(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h, c, glass := startGlass(t, start, nil)
		fx := bindEffects(t, c)
		fx.set(t, fx.of(t, glass.Surface), image.Rect(-30, -30, 200, 200))
		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the surface blurred", frostedAt(75, 45))
		h.Eventually("left of the surface sharp", sharpAt(45, 50))
		h.Eventually("below the surface sharp", sharpAt(80, 70))
	})
}

func TestABlurRegionFollowsRoundedCorners(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h, c, glass := startGlass(t, start, func(cfg *compositor.Config) {
			cfg.Decor.BorderWidth, cfg.Decor.Rounding = 0, 10
		})
		fx := bindEffects(t, c)
		fx.set(t, fx.of(t, glass.Surface), image.Rect(0, 0, 60, 30))
		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the middle blurred", frostedAt(75, 45))
		h.Eventually("the rounded-off corner sharp", sharpAt(51, 36))
		h.Eventually("just inside the corner blurred", frostedAt(56, 41))
	})
}

func TestARuleOffTurnsBlurRegionsOff(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		off := false
		h, c, glass := startGlass(t, start, func(cfg *compositor.Config) {
			cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^glass$`), Mode: wm.RuleFloat, Size: image.Pt(60, 30), Blur: &off}}
		})
		fx := bindEffects(t, c)
		fx.set(t, fx.of(t, glass.Surface), image.Rect(0, 0, 60, 30))
		menu := c.Popup(glass, testclient.Placement{Size: image.Pt(30, 20), Anchor: image.Rect(0, 0, 1, 1), Edge: cxdg.XdgPositionerAnchorTopLeft, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, clearGlass)
		menu.Translucent = true
		fx.set(t, fx.of(t, menu.Surface), image.Rect(0, 0, 30, 20))
		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the menu to map", menu.Mapped)
		menu.Stop()
		settle(t, h)
		h.Eventually("the window sharp", sharpAt(105, 40))
		h.Eventually("its popup sharp", sharpAt(60, 40))
	})
}

func TestADecorationBlurLeavesARegionItsShape(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h, c, glass := startGlass(t, start, func(cfg *compositor.Config) {
			cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 3, Passes: 2}
		})
		h.Eventually("decoration.blur blurring the whole window", frostedAt(105, 40))
		fx := bindEffects(t, c)
		fx.set(t, fx.of(t, glass.Surface), image.Rect(0, 0, 20, 30))
		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("outside the region sharp", sharpAt(105, 40))
		h.Eventually("inside the region blurred", frostedAt(55, 50))
	})
}

func TestXRayAppliesToBlurRegions(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Blur.XRay = true
			cfg.Rules = glassRule
			cfg.Decor.Blur.Size, cfg.Decor.Blur.Passes = 3, 2
		}, small)
		shell := testclient.Connect(t, h.Socket())
		wallpaper := shell.Layer(testclient.LayerSpec{
			Layer:  cwlr.ZwlrLayerShellV1LayerBackground,
			Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight,
		}, testclient.Fill(0xff, 0, 0))
		shell.DispatchUntil("the wallpaper to map", wallpaper.Mapped)
		wallpaper.Stop()
		c := testclient.Connect(t, h.Socket())
		under := c.Window("under", testclient.Fill(0xff, 0xff, 0xff))
		c.DispatchUntil("the window below to map", under.Mapped)
		under.Stop()
		glass := c.Window("glass", clearGlass)
		glass.Translucent = true
		c.DispatchUntil("the glass to map", glass.Mapped)
		glass.Stop()
		h.Eventually("the window below showing without a region", showing(75, 45, [3]byte{0xff, 0xff, 0xff}))
		fx := bindEffects(t, c)
		fx.set(t, fx.of(t, glass.Surface), image.Rect(0, 0, 30, 30))
		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the region showing the blurred wallpaper", func(b *headless.Backend, _ *compositor.Compositor) bool {
			px := pixel(snapshot(b.Outputs()[0]), 60, 50)
			return px[0] < 0x08 && px[1] < 0x08 && px[2] > 0xf7
		})
		h.Eventually("the rest showing the window below", showing(105, 40, [3]byte{0xff, 0xff, 0xff}))
	})
}

func TestASubsurfaceRegionBlursWhatIsBehindIt(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h, c, glass := startGlass(t, start, nil)
		child := c.Surface()
		sub, err := c.Subcompositor.GetSubsurface(child, glass.Surface)
		if err != nil {
			t.Fatal(err)
		}
		fx := bindEffects(t, c)
		fx.set(t, fx.of(t, child), image.Rect(0, 0, 20, 20))
		buf := c.FormatBuffer(image.Pt(20, 20), cw.WlShmFormatArgb8888, 0, clearGlass)
		if err := errors.Join(sub.SetPosition(20, 5), sub.SetDesync(), child.Attach(buf, 0, 0), child.DamageBuffer(0, 0, 20, 20), child.Commit(), glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the subsurface's region blurred", frostedAt(75, 45))
		h.Eventually("the parent's rest sharp", sharpAt(105, 40))
	})
}

func TestAnAlphaModifierFadesTheBlur(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h, c, glass := startGlass(t, start, nil)
		fx := bindEffects(t, c)
		fx.set(t, fx.of(t, glass.Surface), image.Rect(0, 0, 60, 30))
		m, err := cam.NewWpAlphaModifierV1(c.Display, 1)
		if err != nil {
			t.Fatal(err)
		}
		c.Bind(m)
		fade, err := m.GetSurface(glass.Surface)
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(fade.SetMultiplier(0), glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the faded-out surface showing no blur", sharpAt(75, 45))
		if err := errors.Join(fade.SetMultiplier(math.MaxUint32), glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the faded-in surface blurring", frostedAt(75, 45))
	})
}

func TestPopupsBlurByTheirRegion(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) { cfg.Decor.Blur.Size, cfg.Decor.Blur.Passes = 3, 2 }, small)
		c := testclient.Connect(t, h.Socket())
		bg := c.Window("background", fineChecker)
		c.DispatchUntil("the background to map", bg.Mapped)
		bg.Stop()
		fx := bindEffects(t, c)

		menu := c.Popup(bg, testclient.Placement{Size: image.Pt(40, 20), Anchor: image.Rect(20, 20, 21, 21), Edge: cxdg.XdgPositionerAnchorTopLeft, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, clearGlass)
		menu.Translucent = true
		fx.set(t, fx.of(t, menu.Surface), image.Rect(0, 0, 20, 20))
		if err := errors.Join(menu.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the toplevel's popup to map", menu.Mapped)
		menu.Stop()
		h.Eventually("the toplevel's popup blurred inside its region", frostedAt(30, 30))
		h.Eventually("the toplevel's popup sharp outside its region", sharpAt(50, 30))

		layer := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorRight, Size: image.Pt(40, 10)}, clearGlass)
		layer.Translucent = true
		c.DispatchUntil("the layer to map", layer.Mapped)
		layer.Stop()
		dropdown := c.Popup(layer, testclient.Placement{Size: image.Pt(40, 40), Anchor: image.Rect(0, 9, 1, 10), Edge: cxdg.XdgPositionerAnchorBottomLeft, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, clearGlass)
		dropdown.Translucent = true
		fx.set(t, fx.of(t, dropdown.Surface), image.Rect(0, 0, 40, 20))
		if err := errors.Join(dropdown.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the layer's popup to map", dropdown.Mapped)
		dropdown.Stop()
		h.Eventually("the layer's popup blurred inside its region", frostedAt(140, 20))
		h.Eventually("the layer's popup sharp outside its region", sharpAt(140, 40))
	})
}

func TestDecorationBlurReachesPopupsOfBlurredParents(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		off := false
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Blur = compositor.Blur{Enabled: true, Size: 3, Passes: 2}
			cfg.BlurLayers = []string{"lys-wm.testclient"}
			cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^plain$`), Mode: wm.RuleFloat, Size: image.Pt(20, 20), Blur: &off}}
		}, small)
		c := testclient.Connect(t, h.Socket())
		bg := c.Window("background", fineChecker)
		c.DispatchUntil("the background to map", bg.Mapped)
		bg.Stop()

		menu := c.Popup(bg, testclient.Placement{Size: image.Pt(40, 20), Anchor: image.Rect(10, 10, 11, 11), Edge: cxdg.XdgPositionerAnchorTopLeft, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, clearGlass)
		menu.Translucent = true
		if err := errors.Join(menu.XdgSurface.SetWindowGeometry(5, 5, 20, 10), menu.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the popup to map", menu.Mapped)
		menu.Stop()
		h.Eventually("the popup's geometry blurred", frostedAt(20, 15))
		h.Eventually("the popup's margin left sharp", sharpAt(32, 15))

		layer := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorRight, Size: image.Pt(40, 10)}, clearGlass)
		layer.Translucent = true
		c.DispatchUntil("the layer to map", layer.Mapped)
		layer.Stop()
		dropdown := c.Popup(layer, testclient.Placement{Size: image.Pt(40, 30), Anchor: image.Rect(0, 9, 1, 10), Edge: cxdg.XdgPositionerAnchorBottomLeft, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, clearGlass)
		dropdown.Translucent = true
		c.DispatchUntil("the layer's popup to map", dropdown.Mapped)
		dropdown.Stop()
		h.Eventually("the listed layer's popup blurred", frostedAt(140, 25))

		plain := c.Window("plain", clearGlass)
		plain.Translucent = true
		c.DispatchUntil("the plain window to map", plain.Mapped)
		plain.Stop()
		opaquePlain := c.Popup(plain, testclient.Placement{Size: image.Pt(16, 16), Anchor: image.Rect(0, 0, 1, 1), Edge: cxdg.XdgPositionerAnchorTopLeft, Gravity: cxdg.XdgPositionerGravityTopLeft}, nil, clearGlass)
		opaquePlain.Translucent = true
		c.DispatchUntil("the plain window's popup to map", opaquePlain.Mapped)
		opaquePlain.Stop()
		h.Eventually("the popup of a window the rule leaves unblurred sharp", sharpAt(60, 30))
	})
}

func TestABlurRegionIsNotRedrawnWhenNothingBehindItChanged(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h, c, glass := startGlass(t, start, func(cfg *compositor.Config) {
			cfg.Decor.Blur.Size, cfg.Decor.Blur.Passes = 1, 2
		})
		fx := bindEffects(t, c)
		fx.set(t, fx.of(t, glass.Surface), image.Rect(0, 0, 60, 30))
		if err := errors.Join(glass.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the region blurred", frostedAt(75, 45))
		settle(t, h)
		far := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft, Size: image.Pt(6, 6)}, func(pix []byte, _ int, _ image.Point, frame int) {
			for i := range pix {
				pix[i] = byte(frame)
			}
		})
		c.DispatchUntil("the far layer to map", far.Mapped)
		awayFromTheCursor := image.Rect(50, 35, 80, 50)
		for range 5 {
			presents := presentCount(h)
			deadline := time.Now().Add(clientTimeout)
			for presentCount(h) == presents {
				if time.Now().After(deadline) {
					t.Fatal("the far layer stopped presenting")
				}
				if err := c.Display.Flush(); err != nil {
					t.Fatal(err)
				}
				c.Sync()
			}
			h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
				for _, r := range b.Outputs()[0].LastDamage() {
					if r.Overlaps(awayFromTheCursor) {
						return fmt.Errorf("damage %v reaches the glass at %v, though nothing behind it changed", r, awayFromTheCursor)
					}
				}
				return nil
			})
		}
	})
}

func TestBackgroundEffectProtocolErrors(t *testing.T) {
	t.Run("a surface's second object", func(t *testing.T) {
		h := Start(t, small)
		c := testclient.Connect(t, h.Socket())
		fx := bindEffects(t, c)
		s := c.Surface()
		fx.of(t, s)
		fx.of(t, s)
		c.ExpectProtocolError(uint32(cbe.ExtBackgroundEffectManagerV1ErrorBackgroundEffectExists))
	})
	t.Run("a region after the surface", func(t *testing.T) {
		h := Start(t, small)
		c := testclient.Connect(t, h.Socket())
		fx := bindEffects(t, c)
		s := c.Surface()
		obj := fx.of(t, s)
		if err := s.Destroy(); err != nil {
			t.Fatal(err)
		}
		if err := obj.SetBlurRegion(nil); err != nil {
			t.Fatal(err)
		}
		c.ExpectProtocolError(uint32(cbe.ExtBackgroundEffectSurfaceV1ErrorSurfaceDestroyed))
	})
}
