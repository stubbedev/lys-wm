package harness

import (
	"errors"
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
