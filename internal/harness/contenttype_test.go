package harness

import (
	"errors"
	"image"
	"maps"
	"regexp"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cct "github.com/stubbedev/lys-wm/internal/protocol/client/contenttype"
	ctc "github.com/stubbedev/lys-wm/internal/protocol/client/tearingcontrol"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func contentTypeManager(t *testing.T, c *testclient.Client) *cct.WpContentTypeManagerV1 {
	t.Helper()
	m, err := cct.NewWpContentTypeManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	return m
}

func expectContentTypes(t *testing.T, h *Harness, want map[string]wm.ContentType) {
	t.Helper()
	deadline := time.Now().Add(clientTimeout)
	for {
		got := map[string]wm.ContentType{}
		for _, w := range query[[]ipc.Window](h, ipc.KindWindows) {
			got[w.Title] = w.ContentType
		}
		if maps.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("IPC reports content types %v, want %v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIPCReportsEachWindowsContentType(t *testing.T) {
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	m := contentTypeManager(t, c)
	tagged := func(title string, ct cct.WpContentTypeV1Type) (*testclient.Window, *cct.WpContentTypeV1) {
		var obj *cct.WpContentTypeV1
		w := c.WindowWith(title, quadrants, func(w *testclient.Window) error {
			var err error
			if obj, err = m.GetSurfaceContentType(w.Surface); err != nil {
				return err
			}
			return obj.SetContentType(ct)
		})
		c.DispatchUntil(title+" to map", w.Mapped)
		return w, obj
	}
	_, _ = tagged("video", cct.WpContentTypeV1TypeVideo)
	game, gameType := tagged("game", cct.WpContentTypeV1TypeGame)
	plain := c.Window("plain", quadrants)
	c.DispatchUntil("plain to map", plain.Mapped)
	expectContentTypes(t, h, map[string]wm.ContentType{"video": wm.ContentVideo, "game": wm.ContentGame, "plain": wm.ContentNone})

	if err := errors.Join(gameType.SetContentType(cct.WpContentTypeV1TypePhoto), game.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	expectContentTypes(t, h, map[string]wm.ContentType{"video": wm.ContentVideo, "game": wm.ContentPhoto, "plain": wm.ContentNone})

	if err := errors.Join(gameType.Destroy(), game.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	expectContentTypes(t, h, map[string]wm.ContentType{"video": wm.ContentVideo, "game": wm.ContentNone, "plain": wm.ContentNone})
}

func TestOneContentTypePerSurfaceUntilItIsGone(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := contentTypeManager(t, c)
	s := c.Surface()
	first, err := m.GetSurfaceContentType(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetSurfaceContentType(s); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatalf("a content type after the first was destroyed: %v", err)
	}
	if _, err := m.GetSurfaceContentType(s); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(cct.WpContentTypeManagerV1ErrorAlreadyConstructed))
}

func TestAFullscreenGameTearsLikeARuleWouldMakeIt(t *testing.T) {
	testclientRule := func(tearing wm.RuleTearing) []wm.Rule {
		return []wm.Rule{{AppID: regexp.MustCompile(`^lys-wm\.testclient$`), Tearing: tearing}}
	}
	cases := []struct {
		name    string
		tearing bool
		rules   []wm.Rule
		content cct.WpContentTypeV1Type
		vsync   bool
		torn    bool
	}{
		{name: "a game", tearing: true, content: cct.WpContentTypeV1TypeGame, torn: true},
		{name: "a rule that allows tearing", tearing: true, rules: testclientRule(wm.TearingAllow), torn: true},
		{name: "a game with tearing off", content: cct.WpContentTypeV1TypeGame},
		{name: "a video", tearing: true, content: cct.WpContentTypeV1TypeVideo},
		{name: "a game that asks for vsync", tearing: true, content: cct.WpContentTypeV1TypeGame, vsync: true},
		{name: "a game and a rule that denies tearing", tearing: true, rules: testclientRule(wm.TearingDeny), content: cct.WpContentTypeV1TypeGame},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fullscreenScanout(t, func(cfg *compositor.Config) {
				cfg.Tearing, cfg.Rules = tc.tearing, tc.rules
			})
			if tc.content != cct.WpContentTypeV1TypeNone {
				ct, err := contentTypeManager(t, s.c).GetSurfaceContentType(s.w.Surface)
				if err != nil {
					t.Fatal(err)
				}
				if err := ct.SetContentType(tc.content); err != nil {
					t.Fatal(err)
				}
			}
			if tc.vsync {
				hint, err := tearingManager(t, s.c).GetTearingControl(s.w.Surface)
				if err != nil {
					t.Fatal(err)
				}
				if err := hint.SetPresentationHint(ctc.WpTearingControlV1PresentationHintVsync); err != nil {
					t.Fatal(err)
				}
			}
			s.show(t, "the dmabuf", s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0xff, 0, 0)))
			var torn uint64
			s.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
				torn = b.Outputs()[0].Torn()
				return nil
			})
			if (torn == 1) != tc.torn {
				t.Fatalf("%d torn scanouts, want torn %v", torn, tc.torn)
			}
		})
	}
}
