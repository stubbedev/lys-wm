package harness

import (
	"errors"
	"fmt"
	"image"
	"os"
	"regexp"
	"testing"

	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/loop"
	cct "github.com/stubbedev/lys-wm/internal/protocol/client/contenttype"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xwayland"
)

func contentRule(appID, title string, t wm.ContentType) wm.Rule {
	r := wm.Rule{Match: wm.Match{AppID: regexp.MustCompile(appID)}, Content: &t}
	if title != "" {
		r.Match.Title = regexp.MustCompile(title)
	}
	return r
}

func gameHints(on bool, shown, window wm.ContentType) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, c *compositor.Compositor) bool {
		o, ws := b.Outputs()[0], windowsOf(c)
		return o.AdaptiveSync() == on && o.ContentType() == shown && len(ws) == 1 && ws[0].ContentType == window && ws[0].ContentSource == wm.ContentFromRule
	}
}

func commits(b *headless.Backend) uint64 {
	o := b.Outputs()[0]
	return o.Presents() + o.Scanouts()
}

func TestAContentRuleMakesAnUntaggedXGameAGame(t *testing.T) {
	t.Parallel()
	testenv.Require(t, xwayland.Path(os.Getenv))
	open := func(l *loop.Loop) (*headless.Backend, error) {
		b := headless.New(l)
		b.EnableAdaptiveSync()
		return b, nil
	}
	h := start(t, setup{open: open, traced: true, xwayland: func(err error) { t.Errorf("xwayland reported: %v", err) }, configure: func(cfg *compositor.Config) {
		cfg.Decor.BorderWidth = 2
		cfg.Outputs.Fallback.VRR = compositor.VRRFullscreen
		cfg.Rules = []wm.Rule{contentRule(`^XGame$`, "", wm.ContentGame), contentRule(`^XGame$`, `^menu$`, wm.ContentNone)}
	}}, small)
	xc := xDial(t, h)
	win := xc.window(image.Rect(0, 0, 160, 100), 0xff0000, false, map[string][]byte{"WM_CLASS": []byte("xgame\x00XGame\x00"), "_NET_WM_NAME": []byte("level 1")})
	h.Eventually("the X window a game by the rule", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].ContentType == wm.ContentGame && ws[0].ContentSource == wm.ContentFromRule
	})
	state := func(add uint32) {
		t.Helper()
		ev := xproto.ClientMessageEvent{
			Format: 32, Window: win, Type: xc.atoms["_NET_WM_STATE"],
			Data: xproto.ClientMessageDataUnionData32New([]uint32{add, uint32(xc.atoms["_NET_WM_STATE_FULLSCREEN"]), 0, 1, 0}),
		}
		if err := xproto.SendEventChecked(xc.x, false, xc.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(ev.Bytes())).Check(); err != nil {
			t.Fatal(err)
		}
	}
	rename := func(title string) {
		t.Helper()
		if err := xproto.ChangePropertyChecked(xc.x, xproto.PropModeReplace, win, xc.atoms["_NET_WM_NAME"], xc.atoms["UTF8_STRING"], 8, uint32(len(title)), []byte(title)).Check(); err != nil {
			t.Fatal(err)
		}
	}
	titled := func(title string) func(*headless.Backend, *compositor.Compositor) bool {
		return func(_ *headless.Backend, c *compositor.Compositor) bool {
			ws := windowsOf(c)
			return len(ws) == 1 && ws[0].Title == title
		}
	}
	state(1)
	h.Eventually("the fullscreen X game under VRR as a game", gameHints(true, wm.ContentGame, wm.ContentGame))
	before := xQuiesce(t, h, xc)
	for i := range 100 {
		rename(fmt.Sprintf("level %d", i+2))
	}
	h.Eventually("the last title", titled("level 101"))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if n := commits(b) - before; n != 0 {
			t.Errorf("100 title changes that keep the content a game committed %d frames, want 0", n)
		}
		return nil
	})
	rename("menu")
	h.Eventually("a title the none rule matches turning VRR and the game off", gameHints(false, wm.ContentNone, wm.ContentNone))
	rename("level 102")
	h.Eventually("the game back", gameHints(true, wm.ContentGame, wm.ContentGame))
	state(0)
	h.Eventually("leaving fullscreen clearing VRR and the content type", gameHints(false, wm.ContentNone, wm.ContentGame))
}

func xQuiesce(t *testing.T, h *Harness, xc *xclient) uint64 {
	t.Helper()
	for range 5 {
		settle(t, h)
		before := presentCount(h)
		if _, err := xproto.GetInputFocus(xc.x).Reply(); err != nil {
			t.Fatal(err)
		}
		settle(t, h)
		if presentCount(h) == before {
			var n uint64
			h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
				n = commits(b)
				return nil
			})
			return n
		}
	}
	t.Fatal("the X client kept redrawing")
	return 0
}

func TestAContentRuleOverridesAClientsGameTag(t *testing.T) {
	s := fullscreenScanout(t, startAdaptive, func(cfg *compositor.Config) {
		cfg.Outputs.Fallback.VRR = compositor.VRRFullscreen
		cfg.Tearing = true
		cfg.Rules = []wm.Rule{contentRule(`^lys-wm\.testclient$`, "", wm.ContentNone)}
	})
	ct, err := contentTypeManager(t, s.c).GetSurfaceContentType(s.w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	var before uint64
	s.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		before = commits(b)
		return nil
	})
	if err := errors.Join(ct.SetContentType(cct.WpContentTypeV1TypeGame), s.w.Redraw(), s.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	s.h.Eventually("the game's frame", func(b *headless.Backend, _ *compositor.Compositor) bool { return commits(b) > before })
	s.h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		o, ws := b.Outputs()[0], windowsOf(c)
		if o.AdaptiveSync() || o.ContentType() != wm.ContentNone || o.TearsFrames() {
			t.Errorf("a client tagged game under a content: none rule got VRR %v, content type %v, tearing %v, want none of them", o.AdaptiveSync(), o.ContentType(), o.TearsFrames())
		}
		if len(ws) != 1 || ws[0].ContentType != wm.ContentNone || ws[0].ContentSource != wm.ContentFromRule {
			t.Errorf("IPC reports %+v, want content none from the rule", ws)
		}
		return nil
	})
}
