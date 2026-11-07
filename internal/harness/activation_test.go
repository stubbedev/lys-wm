package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cxa "github.com/stubbedev/lys-wm/internal/protocol/client/xdgactivation"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type activator struct {
	*party
	act *cxa.XdgActivationV1
}

func joinActivating(t *testing.T, h *Harness, name string) activator {
	t.Helper()
	p := join(t, h, name)
	act, err := cxa.NewXdgActivationV1(p.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.c.Bind(act)
	return activator{p, act}
}

func (a activator) activate(t *testing.T, token string) {
	t.Helper()
	if err := errors.Join(a.act.Activate(token, a.w.Surface), a.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
}

func (a activator) keyPress(t *testing.T, h *Harness) uint32 {
	t.Helper()
	before := a.in.Press
	h.key(keyH, true)
	h.key(keyH, false)
	a.seen(t, "the key press", func() bool { return a.in.Press != before })
	return a.in.Press
}

type activationState struct {
	shown   wm.WorkspaceID
	focused string
	urgent  map[string]bool
}

func (h *Harness) activationState() activationState {
	h.t.Helper()
	st := activationState{urgent: map[string]bool{}}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		s := c.WM()
		st.shown, _ = s.ActiveOn(s.FocusedOutput())
		for _, w := range c.Snapshot().Windows {
			st.urgent[w.Title] = w.Urgent
			if w.Focused {
				st.focused = w.Title
			}
		}
		return nil
	})
	return st
}

func (h *Harness) expectActivation(what string, shown wm.WorkspaceID, focused string, urgent bool, window string) {
	h.t.Helper()
	h.Eventually(what, func(_ *headless.Backend, c *compositor.Compositor) bool {
		s := c.WM()
		active, _ := s.ActiveOn(s.FocusedOutput())
		gotFocus, gotUrgent := "", false
		for _, w := range c.Snapshot().Windows {
			if w.Focused {
				gotFocus = w.Title
			}
			if w.Title == window {
				gotUrgent = w.Urgent
			}
		}
		return active == shown && gotFocus == focused && gotUrgent == urgent
	})
}

func twoWorkspaces(t *testing.T, h *Harness) (target, minter activator) {
	t.Helper()
	target = joinActivating(t, h, "target")
	h.dispatch("workspace 2")
	minter = joinActivating(t, h, "minter")
	minter.seen(t, "the minter focused", func() bool { return slices.Contains(minter.in.Log, "kb_enter minter") })
	h.expectActivation("the minter focused on workspace 2", 2, "minter", false, "target")
	return target, minter
}

func TestAFreshTokenFromTheFocusedClientFocusesTheWindowOnItsWorkspace(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	target, minter := twoWorkspaces(t, h)
	token := requestToken(t, minter.c, minter.act, minter.seat, minter.keyPress(t, h))
	target.activate(t, token)
	h.expectActivation("the target focused and its workspace shown", 1, "target", false, "target")
}

func TestATokenWithoutUserInputOnlyMarksUrgent(t *testing.T) {
	t.Parallel()
	for name, mint := range map[string]func(t *testing.T, h *Harness, target, minter activator) string{
		"no serial": func(t *testing.T, _ *Harness, _, minter activator) string {
			return requestToken(t, minter.c, minter.act, minter.seat, 0)
		},
		"a serial from before the minter lost focus": func(t *testing.T, h *Harness, _, minter activator) string {
			serial := minter.keyPress(t, h)
			joinActivating(t, h, "other")
			h.expectActivation("another window focused", 2, "other", false, "target")
			return requestToken(t, minter.c, minter.act, minter.seat, serial)
		},
		"focus moved on after the token was minted": func(t *testing.T, h *Harness, _, minter activator) string {
			token := requestToken(t, minter.c, minter.act, minter.seat, minter.keyPress(t, h))
			joinActivating(t, h, "other")
			h.expectActivation("another window focused", 2, "other", false, "target")
			return token
		},
		"another client's serial": func(t *testing.T, h *Harness, target, minter activator) string {
			return requestToken(t, target.c, target.act, target.seat, minter.keyPress(t, h))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := Start(t, small)
			target, minter := twoWorkspaces(t, h)
			token := mint(t, h, target, minter)
			focused := h.activationState().focused
			target.activate(t, token)
			h.expectActivation("the target urgent, the focus where it was", 2, focused, true, "target")
		})
	}
}

func TestActivationRulesAndSetting(t *testing.T) {
	t.Parallel()
	urgent, ignore := wm.ActivateUrgent, wm.ActivateIgnore
	target := regexp.MustCompile(`^target$`)
	for name, tc := range map[string]struct {
		configure func(*compositor.Config)
		urgent    bool
	}{
		"focus.on_activate urgent": {func(cfg *compositor.Config) { cfg.WM.OnActivate = wm.ActivateUrgent }, true},
		"a rule's urgent":          {func(cfg *compositor.Config) { cfg.Rules = []wm.Rule{{Title: target, OnActivate: &urgent}} }, true},
		"a rule's ignore":          {func(cfg *compositor.Config) { cfg.Rules = []wm.Rule{{Title: target, OnActivate: &ignore}} }, false},
	} {
		t.Run(name, func(t *testing.T) {
			h := StartWith(t, tc.configure, small)
			target, minter := twoWorkspaces(t, h)
			token := requestToken(t, minter.c, minter.act, minter.seat, minter.keyPress(t, h))
			target.activate(t, token)
			minter.c.Sync()
			h.expectActivation("the minter still focused", 2, "minter", tc.urgent, "target")
		})
	}
}

func TestAWindowLaunchedThroughExecMapsOnTheWorkspaceItWasLaunchedFrom(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	joinActivating(t, h, "first")
	h.dispatch("workspace 2")
	joinActivating(t, h, "launcher")
	h.expectActivation("the launcher on workspace 2", 2, "launcher", false, "first")
	tokenFile := filepath.Join(t.TempDir(), "token")
	h.dispatch(fmt.Sprintf(`exec printf %%s "$XDG_ACTIVATION_TOKEN" > %s; printf %%s "$DESKTOP_STARTUP_ID" >> %s`, tokenFile, tokenFile))
	var token string
	h.Eventually("the launched command wrote its token", func(*headless.Backend, *compositor.Compositor) bool {
		b, err := os.ReadFile(tokenFile)
		if err != nil || len(b) == 0 || len(b)%2 != 0 {
			return false
		}
		token = string(b[:len(b)/2])
		return token == string(b[len(b)/2:])
	})
	h.dispatch("workspace 3")
	h.expectActivation("workspace 3 shown", 3, "", false, "first")

	app := testclient.Connect(t, h.Socket())
	act, err := cxa.NewXdgActivationV1(app.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	app.Bind(act)
	w := app.WindowWith("launched", fillGreen, func(w *testclient.Window) error { return act.Activate(token, w.Surface) })
	app.DispatchUntil("the launched window to map", w.Mapped)
	h.Eventually("the launched window on workspace 2, workspace 3 still shown", func(_ *headless.Backend, c *compositor.Compositor) bool {
		s := c.WM()
		shown, _ := s.ActiveOn(s.FocusedOutput())
		for _, win := range c.Snapshot().Windows {
			if win.Title == "launched" {
				return win.Workspace == 2 && shown == 3
			}
		}
		return false
	})

	plain := app.Window("unlaunched", fillGreen)
	app.DispatchUntil("a window without a token to map", plain.Mapped)
	h.Eventually("a window without a token on the shown workspace", func(_ *headless.Backend, c *compositor.Compositor) bool {
		for _, win := range c.Snapshot().Windows {
			if win.Title == "unlaunched" {
				return win.Workspace == 3
			}
		}
		return false
	})
}

func TestActivationTokensStayCapped(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	target, minter := twoWorkspaces(t, h)
	serial := minter.keyPress(t, h)
	first := requestToken(t, minter.c, minter.act, minter.seat, serial)
	for range 64 {
		requestToken(t, minter.c, minter.act, minter.seat, serial)
	}
	target.activate(t, first)
	minter.c.Sync()
	if st := h.activationState(); st.urgent["target"] || st.focused != "minter" || st.shown != 2 {
		t.Fatalf("a token pushed out by 64 newer ones still acted: %+v", st)
	}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if n := c.ActivationTokens(); n != 64 {
			t.Errorf("%d live tokens after 65 were minted, want the cap of 64", n)
		}
		return nil
	})
	last := requestToken(t, minter.c, minter.act, minter.seat, serial)
	target.activate(t, last)
	h.expectActivation("a live token still focuses", 1, "target", false, "target")
}
