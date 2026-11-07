package compositor

import (
	"fmt"
	"slices"

	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const liveTokens = 64

type activationToken struct {
	value     string
	granted   bool
	focus     *server.Client
	workspace wm.WorkspaceID
	claimant  *shell.Toplevel
}

func (c *Compositor) MintActivationToken(cl *server.Client, serial uint32) (string, error) {
	return c.mintToken(c.seat.PressSerial(cl, serial) && c.inputFocus(cl))
}

func clientOf(s *surface.Surface) *server.Client {
	if s == nil {
		return nil
	}
	return s.Client()
}

func (c *Compositor) inputFocus(cl *server.Client) bool {
	return clientOf(c.seat.Focus()) == cl || clientOf(c.seat.PointerFocus()) == cl
}

func (c *Compositor) keyboardClient() *server.Client {
	return clientOf(c.seat.Focus())
}

func (c *Compositor) mintToken(granted bool) (string, error) {
	value, err := shell.NewHandle()
	if err != nil {
		return "", fmt.Errorf("compositor: mint an activation token: %w", err)
	}
	workspace, _ := c.wm.ActiveOn(c.wm.FocusedOutput())
	if len(c.tokens) == liveTokens {
		c.tokens = slices.Delete(c.tokens, 0, 1)
	}
	c.tokens = append(c.tokens, activationToken{value: value, granted: granted, focus: c.keyboardClient(), workspace: workspace})
	return value, nil
}

func (c *Compositor) presentToken(t *shell.Toplevel, value string) error {
	i := slices.IndexFunc(c.tokens, func(tok activationToken) bool { return tok.value == value })
	if i < 0 {
		return nil
	}
	w := c.windowOf(xdgToplevel{t})
	if w == nil {
		c.tokens[i].claimant = t
		return nil
	}
	tok := c.tokens[i]
	c.tokens = slices.Delete(c.tokens, i, i+1)
	return c.activated(w, tok)
}

func (c *Compositor) activated(w *window, tok activationToken) error {
	policy := c.settings.OnActivate
	if d := wm.DecideTagged(c.rules, w.top.AppID(), w.top.Title(), w.top.Tag()); d.OnActivate != nil {
		policy = *d.OnActivate
	}
	switch {
	case policy == wm.ActivateIgnore:
		return nil
	case policy == wm.ActivateFocus && tok.granted && c.keyboardClient() == tok.focus:
		return c.activate(w)
	}
	return c.markUrgent(w)
}

func (c *Compositor) launchWorkspace(t toplevel) (wm.WorkspaceID, bool) {
	x, ok := t.(xdgToplevel)
	if !ok {
		return 0, false
	}
	i := slices.IndexFunc(c.tokens, func(tok activationToken) bool { return tok.claimant == x.Toplevel })
	if i < 0 {
		return 0, false
	}
	id := c.tokens[i].workspace
	_, exists := c.wm.Workspace(id)
	return id, exists
}

func (c *Compositor) dropClaims(t toplevel) {
	if x, ok := t.(xdgToplevel); ok {
		c.tokens = slices.DeleteFunc(c.tokens, func(tok activationToken) bool { return tok.claimant == x.Toplevel })
	}
}

func (c *Compositor) ActivationTokens() int {
	return len(c.tokens)
}
