package compositor

import (
	"errors"
	"slices"

	"github.com/stubbedev/lys-wm/internal/wm"
)

type WorkspaceRule struct {
	wm.WorkspaceRule
	Bind *wm.OutputMatch
}

func (c *Compositor) resolvedWorkspaceRules() []wm.WorkspaceRule {
	if len(c.workspaceRules) == 0 {
		return nil
	}
	out := make([]wm.WorkspaceRule, 0, len(c.workspaceRules))
	for _, r := range c.workspaceRules {
		resolved := r.WorkspaceRule
		if r.Bind != nil {
			if o := c.outputMatching(*r.Bind); o != nil {
				resolved.Output = o.Backend().Name()
			}
		}
		out = append(out, resolved)
	}
	return out
}

func (c *Compositor) syncWorkspaceRules() {
	c.wm = c.wm.WithLayoutDefault(c.settings.Layout).WithWorkspaceRules(c.resolvedWorkspaceRules())
}

func (c *Compositor) runOnCreatedEmpty() error {
	if !slices.ContainsFunc(c.workspaceRules, func(r WorkspaceRule) bool { return r.OnCreatedEmpty != "" }) {
		return nil
	}
	var errs []error
	for _, out := range c.outputs {
		id, _ := c.wm.ActiveOn(out.Backend().Name())
		cmd := c.wm.OnCreatedEmpty(id)
		if cmd == "" {
			continue
		}
		switch {
		case !c.wm.WorkspaceEmpty(id):
			delete(c.shownEmpty, id)
		case !c.shownEmpty[id]:
			c.shownEmpty[id] = true
			errs = append(errs, c.spawn(cmd, launchTarget{workspace: id}))
		}
	}
	for id := range c.shownEmpty {
		if w, ok := c.wm.Workspace(id); !ok || !c.wm.Shown(w) {
			delete(c.shownEmpty, id)
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) frameBorder(d *wm.Decision, look wm.WorkspaceLook) int {
	switch {
	case d.BorderWidth != nil:
		return *d.BorderWidth
	case look.BorderWidth != nil:
		return *look.BorderWidth
	}
	return c.decor.BorderWidth
}
