package compositor

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strconv"

	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const liveTokens = 64

type activationToken struct {
	value      string
	granted    bool
	focus      *server.Client
	workspace  wm.WorkspaceID
	scratchpad wm.ScratchpadName
	pid        int
	claimant   *shell.Toplevel
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
	return c.mintLaunchToken(granted, c.shownTarget())
}

type launchTarget struct {
	workspace  wm.WorkspaceID
	scratchpad wm.ScratchpadName
}

func (c *Compositor) shownTarget() launchTarget {
	workspace, _ := c.wm.ActiveOn(c.wm.FocusedOutput())
	return launchTarget{workspace: workspace}
}

func (c *Compositor) mintLaunchToken(granted bool, target launchTarget) (string, error) {
	value, err := shell.NewHandle()
	if err != nil {
		return "", fmt.Errorf("compositor: mint an activation token: %w", err)
	}
	if len(c.tokens) == liveTokens {
		c.tokens = slices.Delete(c.tokens, 0, 1)
	}
	c.tokens = append(c.tokens, activationToken{value: value, granted: granted, focus: c.keyboardClient(), workspace: target.workspace, scratchpad: target.scratchpad})
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
	d := c.decision(w)
	if d.OnActivate != nil {
		policy = *d.OnActivate
	}
	if d.Suppress.Has(wm.SuppressActivate) {
		policy = wm.ActivateIgnore
	}
	switch {
	case policy == wm.ActivateIgnore:
		return nil
	case policy == wm.ActivateFocus && tok.granted && c.keyboardClient() == tok.focus:
		return c.activate(w)
	}
	return c.markUrgent(w)
}

func (c *Compositor) launchToken(t toplevel) int {
	if x, ok := t.(xdgToplevel); ok {
		if i := slices.IndexFunc(c.tokens, func(tok activationToken) bool { return tok.claimant == x.Toplevel }); i >= 0 {
			return i
		}
	}
	if !slices.ContainsFunc(c.tokens, func(tok activationToken) bool { return tok.pid != 0 && tok.claimant == nil }) {
		return -1
	}
	for _, pid := range c.ancestry(int(t.pid())) {
		if i := slices.IndexFunc(c.tokens, func(tok activationToken) bool { return tok.pid == pid && tok.claimant == nil }); i >= 0 {
			return i
		}
	}
	return -1
}

func (c *Compositor) consumeLaunch(t toplevel) {
	if i := c.launchToken(t); i >= 0 {
		c.tokens = slices.Delete(c.tokens, i, i+1)
	}
}

func (c *Compositor) spawned(value string, pid int) {
	if i := slices.IndexFunc(c.tokens, func(tok activationToken) bool { return tok.value == value }); i >= 0 {
		c.tokens[i].pid = pid
	}
}

func (c *Compositor) ActivationTokens() int {
	return len(c.tokens)
}

const maxAncestry = 32

func (c *Compositor) ancestry(pid int) []int {
	var out []int
	for pid > 1 && len(out) < maxAncestry {
		out = append(out, pid)
		parent, err := parentPID(pid)
		if err != nil {
			return out
		}
		pid = parent
	}
	return out
}

func parentPID(pid int) (int, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, fmt.Errorf("compositor: read the parent of process %d: %w", pid, err)
	}
	end := bytes.LastIndexByte(stat, ')')
	fields := bytes.Fields(stat[end+1:])
	if end < 0 || len(fields) < 2 {
		return 0, fmt.Errorf("compositor: process %d has an unreadable stat %q", pid, stat)
	}
	parent, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return 0, fmt.Errorf("compositor: process %d names parent %q: %w", pid, fields[1], err)
	}
	return parent, nil
}
