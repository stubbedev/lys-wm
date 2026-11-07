package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/loop"
	cct "github.com/stubbedev/lys-wm/internal/protocol/client/contenttype"
	cwom "github.com/stubbedev/lys-wm/internal/protocol/client/wlroutputmanagement"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func startAdaptive(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	open := func(l *loop.Loop) (*headless.Backend, error) {
		b := headless.New(l)
		b.EnableAdaptiveSync()
		return b, nil
	}
	return start(t, setup{open: open, configure: configure}, modes...)
}

func adaptiveSync(on bool) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].AdaptiveSync() == on
	}
}

func headAdaptive(on bool) cwom.ZwlrOutputHeadV1AdaptiveSyncState {
	if on {
		return cwom.ZwlrOutputHeadV1AdaptiveSyncStateEnabled
	}
	return cwom.ZwlrOutputHeadV1AdaptiveSyncStateDisabled
}

func TestAFullscreenGameOrVideoVariesTheRefreshUnderTheFullscreenPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content cct.WpContentTypeV1Type
		on      bool
	}{
		{"a game", cct.WpContentTypeV1TypeGame, true},
		{"a video", cct.WpContentTypeV1TypeVideo, true},
		{"a photo", cct.WpContentTypeV1TypePhoto, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fullscreenScanout(t, startAdaptive, func(cfg *compositor.Config) {
				cfg.Outputs.Fallback.VRR = compositor.VRRFullscreen
			})
			m := outputManager(t, s.c)
			head := m.head("HEADLESS-1")
			ct, err := contentTypeManager(t, s.c).GetSurfaceContentType(s.w.Surface)
			if err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(ct.SetContentType(tc.content), s.w.Redraw(), s.c.Display.Flush()); err != nil {
				t.Fatal(err)
			}
			s.h.Eventually("adaptive sync following the content", adaptiveSync(tc.on))
			s.c.DispatchUntil("the head's adaptive sync", func() bool { return head.adaptive == headAdaptive(tc.on) })
			if !tc.on {
				return
			}
			if err := errors.Join(ct.SetContentType(cct.WpContentTypeV1TypeNone), s.w.Redraw(), s.c.Display.Flush()); err != nil {
				t.Fatal(err)
			}
			s.h.Eventually("adaptive sync off once the content is neither a game nor a video", adaptiveSync(false))
			s.c.DispatchUntil("the head's adaptive sync off", func() bool { return head.adaptive == headAdaptive(false) })
		})
	}
}

func TestOutputManagementTurnsOnAdaptiveSyncWhereTheOutputHasIt(t *testing.T) {
	t.Parallel()
	h := startAdaptive(t, func(*compositor.Config) {}, small)
	c := testclient.Connect(t, h.Socket())
	m := outputManager(t, c)
	head := m.head("HEADLESS-1")
	for _, on := range []bool{true, false} {
		r := configure(t, c, m, m.serial, true, everyHead(m, map[*omHead][]headSetting{head: {func(ch *cwom.ZwlrOutputConfigurationHeadV1) error { return ch.SetAdaptiveSync(headAdaptive(on)) }}}))
		if !r.succeeded {
			t.Fatalf("adaptive sync %v on a capable head answered %+v", on, *r)
		}
		h.Eventually("the asked adaptive sync", adaptiveSync(on))
		c.DispatchUntil("the head's adaptive sync", func() bool { return head.adaptive == headAdaptive(on) })
	}
}
