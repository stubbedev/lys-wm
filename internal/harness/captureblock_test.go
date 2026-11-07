package harness

import (
	"errors"
	"image"
	"regexp"
	"testing"

	"github.com/stubbedev/lys-lib/capture"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	clch "github.com/stubbedev/lys-wm/internal/protocol/client/lyscapturehint"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	cwsc "github.com/stubbedev/lys-wm/internal/protocol/client/wlrscreencopy"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	shown  = [3]byte{0xff, 0, 0}
	masked = [3]byte{0, 0, 0}
)

type blockRig struct {
	*captureRig
	hint *clch.LysCaptureHintManagerV1
	sc   *cwsc.ZwlrScreencopyManagerV1
}

func startBlockRig(t *testing.T, from wm.BlockFrom, layer bool) *blockRig {
	t.Helper()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor.BorderWidth = 0
		cfg.WM.Gaps = wm.Gaps{}
		cfg.Rules = []wm.Rule{titled("secret", wm.Decision{BlockOutFrom: from})}
		cfg.LayerRules = []wm.LayerRule{{Namespace: regexp.MustCompile("^lys-wm.testclient$"), BlockOutFrom: from}}
	}, small)
	c := testclient.Connect(t, h.Socket())
	if layer {
		l := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerTop, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorBottom, Size: image.Pt(80, 0)}, testclient.Fill(0, 0, 0xff))
		c.DispatchUntil("the layer to map", l.Mapped)
	} else {
		c.DispatchUntil("secret to map", c.Window("secret", testclient.Fill(0, 0, 0xff)).Mapped)
	}
	h.Eventually("the red surface on screen", showing(20, 50, shown))
	settle(t, h)
	r := &blockRig{captureRig: newCaptureRig(t, h, c)}
	var err error
	if r.hint, err = clch.NewLysCaptureHintManagerV1(c.Display, 1); err != nil {
		t.Fatal(err)
	}
	if r.sc, err = cwsc.NewZwlrScreencopyManagerV1(c.Display, 3); err != nil {
		t.Fatal(err)
	}
	c.Bind(r.hint)
	c.Bind(r.sc)
	return r
}

func (r *blockRig) session(oneshot bool) [3]byte {
	r.t.Helper()
	session, _ := r.outputSession(0)
	if oneshot {
		if err := r.hint.OneshotSession(session); err != nil {
			r.t.Fatal(err)
		}
	}
	return rgbAt(r.first(session, image.Pt(160, 100)), 160*4, 20, 50)
}

func (r *blockRig) screencopy(oneshot bool) [3]byte {
	r.t.Helper()
	f := &copyFrame{}
	frame, err := r.sc.CaptureOutput(f, 0, r.c.Outputs[0])
	if err != nil {
		r.t.Fatal(err)
	}
	r.c.DispatchUntil("the buffer description", func() bool { return f.bufferDone })
	if oneshot {
		if err := r.hint.OneshotScreencopy(frame); err != nil {
			r.t.Fatal(err)
		}
	}
	wl, pix := r.c.Target(f.size)
	if err := errors.Join(frame.Copy(wl), r.c.Display.Flush()); err != nil {
		r.t.Fatal(err)
	}
	r.c.DispatchUntil("the screencopy", func() bool { return f.ready || f.failed })
	if f.failed {
		r.t.Fatal("the screencopy failed")
	}
	return rgbAt(pix, int(f.stride), 20, 50)
}

func TestBlockedSurfacesAreMaskedInScreencastsAndShownInDeclaredScreenshots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		from          wm.BlockFrom
		layer         bool
		cast, oneshot [3]byte
	}{
		{"window screencast", wm.BlockScreencast, false, masked, shown},
		{"window capture", wm.BlockCapture, false, masked, masked},
		{"layer screencast", wm.BlockScreencast, true, masked, shown},
		{"layer capture", wm.BlockCapture, true, masked, masked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := startBlockRig(t, tc.from, tc.layer)
			if got := r.session(false); got != tc.cast {
				t.Errorf("an undeclared session's first frame shows %v, want %v", got, tc.cast)
			}
			if got := r.session(true); got != tc.oneshot {
				t.Errorf("a one-shot session shows %v, want %v", got, tc.oneshot)
			}
			if got := r.screencopy(false); got != tc.cast {
				t.Errorf("an undeclared screencopy shows %v, want %v", got, tc.cast)
			}
			if got := r.screencopy(true); got != tc.oneshot {
				t.Errorf("a one-shot screencopy shows %v, want %v", got, tc.oneshot)
			}
			if got := rgbAt(r.h.Rendered(0).Pix, 160*4, 20, 50); got != shown {
				t.Errorf("the screen itself shows %v", got)
			}
		})
	}
}

func TestAWindowCaptureOfABlockedWindowIsBlack(t *testing.T) {
	t.Parallel()
	r := startBlockRig(t, wm.BlockScreencast, false)
	session, _ := r.windowSession()
	pix := r.first(session, image.Pt(160, 100))
	if got := rgbAt(pix, 160*4, 80, 50); got != masked {
		t.Fatalf("the blocked window's own capture shows %v", got)
	}
}

func TestContentCommitsOfABlockedWindowWakeNoScreencast(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Rules = []wm.Rule{titled("secret", wm.Decision{BlockOutFrom: wm.BlockScreencast})}
	}, small)
	c := testclient.Connect(t, h.Socket())
	open := c.Window("open", fillGreen)
	c.DispatchUntil("open to map", open.Mapped)
	open.Stop()
	w := c.Window("secret", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("secret to map", w.Mapped)
	settle(t, h)
	quiesce(t, h, c, w)
	r := newCaptureRig(t, h, c)
	session, _ := r.outputSession(0)
	r.first(session, image.Pt(160, 100))
	f, _ := r.waiting(session, image.Pt(160, 100))
	before := presentCount(h)
	for range 5 {
		if err := errors.Join(w.Redraw(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.Sync()
	}
	h.Eventually("the commits drawn on the output", func(b *headless.Backend, _ *compositor.Compositor) bool { return b.Outputs()[0].Presents() > before })
	if err := c.Display.Roundtrip(); err != nil || f.ready {
		t.Fatalf("a blocked window's commits produced a cast frame (%v)", err)
	}
}

func TestLysLibScreenshotsShowABlockedWindowAndScreencastsMaskIt(t *testing.T) {
	t.Parallel()
	r := startBlockRig(t, wm.BlockScreencast, false)
	c, err := capture.ConnectTo(r.h.Socket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	o := c.Outputs()[0]
	tls, err := c.Toplevels()
	if err != nil || len(tls) != 1 {
		t.Fatalf("toplevels %+v, %v", tls, err)
	}
	for _, tc := range []struct {
		name string
		take func(capture.Options) (*capture.Frame, error)
		at   image.Point
	}{
		{"screencopy", func(opts capture.Options) (*capture.Frame, error) { return c.CaptureOutput(o, opts) }, image.Pt(20, 50)},
		{"output session", func(opts capture.Options) (*capture.Frame, error) { return c.CaptureOutputOnce(o, opts) }, image.Pt(20, 50)},
		{"window session", func(opts capture.Options) (*capture.Frame, error) { return c.CaptureToplevel(tls[0], opts) }, image.Pt(80, 50)},
	} {
		for intent, want := range map[capture.Intent][3]byte{capture.Screenshot: shown, capture.Screencast: masked} {
			f, err := tc.take(capture.Options{Intent: intent})
			if err != nil {
				t.Fatalf("%s, intent %d: %v", tc.name, intent, err)
			}
			img, err := f.Image()
			if err != nil {
				t.Fatal(err)
			}
			px := img.RGBAAt(tc.at.X, tc.at.Y)
			if got := [3]byte{px.R, px.G, px.B}; got != want {
				t.Errorf("%s, intent %d shows %v, want %v", tc.name, intent, got, want)
			}
		}
	}
}
