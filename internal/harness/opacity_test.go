package harness

import (
	"errors"
	"fmt"
	"image"
	"regexp"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func blend(c, bg [3]byte, a float64) [3]byte {
	var out [3]byte
	for i := range out {
		out[i] = byte(float64(c[i])*a + float64(bg[i])*(1-a) + 0.5)
	}
	return out
}

func near(got, want [3]byte) bool {
	for i := range got {
		if d := int(got[i]) - int(want[i]); d < -1 || d > 1 {
			return false
		}
	}
	return true
}

func TestInactiveWindowsAndRuledWindowsAreTranslucent(t *testing.T) {
	half := float32(0.25)
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.Opacity = compositor.Opacity{Active: 1, Inactive: 0.5, Fullscreen: 1}
		cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^ruled$`), Opacity: &half}}
	}, tall)
	c := testclient.Connect(t, h.Socket())
	white := [3]byte{0xff, 0xff, 0xff}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	at := func(x, y int, want [3]byte) func(*headless.Backend, *compositor.Compositor) bool {
		return func(b *headless.Backend, _ *compositor.Compositor) bool {
			return near(pixel(snapshot(b.Outputs()[0]), x, y), want)
		}
	}
	left := c.Window("left", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("left to map", left.Mapped)
	h.Eventually("the lone focused window opaque", at(20, 60, white))
	ruled := c.Window("ruled", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("ruled to map", ruled.Mapped)
	c.DispatchUntil("both windows tiled", func() bool { return left.Logical == image.Pt(80, 120) && ruled.Logical == image.Pt(80, 120) })
	h.Eventually("the unfocused window at the inactive opacity", at(20, 60, blend(white, bg, 0.5)))
	h.Eventually("the focused ruled window at its rule's opacity", at(140, 60, blend(white, bg, 0.25)))
}

func TestATranslucentFullscreenWindowIsComposited(t *testing.T) {
	s := fullscreenScanout(t, func(cfg *compositor.Config) { cfg.Decor.Opacity.Fullscreen = 0.5 })
	before := scanouts(s.h)
	b := s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0, 0xff, 0))
	if err := errors.Join(s.w.Surface.Attach(b.WL, 0, 0), s.w.Surface.DamageBuffer(0, 0, 160, 100), s.w.Surface.Commit(), s.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	settle(t, s.h)
	if n := scanouts(s.h); n != before {
		t.Fatalf("a fullscreen window at half opacity scanned out %d frames", n-before)
	}
}

func TestInactiveWindowsDimAndAFocusChangeRepaintsOnlyTheTwo(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.Dim = compositor.Dim{Inactive: true, Strength: 0.5}
		cfg.WM.Autotile.Enabled = false
	}, tall)
	c := testclient.Connect(t, h.Socket())
	white := [3]byte{0xff, 0xff, 0xff}
	dimmed := blend([3]byte{}, white, 0.5)
	at := func(x, y int, want [3]byte) func(*headless.Backend, *compositor.Compositor) bool {
		return func(b *headless.Backend, _ *compositor.Compositor) bool {
			return near(pixel(snapshot(b.Outputs()[0]), x, y), want)
		}
	}
	var ws []*testclient.Window
	for _, name := range []string{"one", "two", "three"} {
		w := c.Window(name, testclient.Fill(0xff, 0xff, 0xff))
		c.DispatchUntil(name+" to map", w.Mapped)
		ws = append(ws, w)
	}
	c.DispatchUntil("three columns", func() bool { return ws[0].Logical.X == 53 && ws[1].Logical.X == 54 && ws[2].Logical.X == 53 })
	h.Eventually("the first two dimmed, the focused third not", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return at(20, 100, dimmed)(b, comp) && at(90, 100, dimmed)(b, comp) && at(140, 100, white)(b, comp)
	})
	settle(t, h)
	h.dispatch("focus left")
	h.Eventually("the dim moved from the second to the third", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return at(90, 100, white)(b, comp) && at(140, 100, dimmed)(b, comp)
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		var damage image.Rectangle
		for _, r := range b.Outputs()[0].LastDamage() {
			damage = damage.Union(r)
		}
		if want := image.Rect(53, 0, 160, 120); damage != want {
			return fmt.Errorf("the focus change repainted %v, want the two windows whose dim changed, %v", damage, want)
		}
		return nil
	})
}

func TestADimAroundWindowDimsWhatIsBelowIt(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.Dim.Strength = 0.5
		cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^dialog$`), Mode: wm.RuleFloat, Size: image.Pt(60, 40), DimAround: true}}
	}, tall)
	c := testclient.Connect(t, h.Socket())
	white := [3]byte{0xff, 0xff, 0xff}
	under := c.Window("under", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("under to map", under.Mapped)
	dialog := c.Window("dialog", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("dialog to map", dialog.Mapped)
	h.Eventually("the window below dimmed and the dialog not", func(b *headless.Backend, _ *compositor.Compositor) bool {
		buf := snapshot(b.Outputs()[0])
		return near(pixel(buf, 2, 2), blend([3]byte{}, white, 0.5)) && near(pixel(buf, 60, 45), white)
	})
}
