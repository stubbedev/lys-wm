package harness

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cksi "github.com/stubbedev/lys-wm/internal/protocol/client/keyboardshortcutsinhibit"
)

func (l *constraintLog) Active(*cksi.ZwpKeyboardShortcutsInhibitorV1) error {
	return l.events.Note("active")
}

func (l *constraintLog) Inactive(*cksi.ZwpKeyboardShortcutsInhibitorV1) error {
	return l.events.Note("inactive")
}

func TestShortcutInhibitorPassesKeysButKeepsLockedBinds(t *testing.T) {
	t.Parallel()
	h := StartWith(t, withBinds(t,
		bindSpec{chord: "A", action: "workspace 2", flags: binds.Flags{Locked: true}},
		bindSpec{chord: "B", action: "workspace 3"},
	), small)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	mgr, err := cksi.NewZwpKeyboardShortcutsInhibitManagerV1(r.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.c.Bind(mgr)
	l := &constraintLog{}
	if _, err := mgr.InhibitShortcuts(l, r.w.Surface, r.seat); err != nil {
		t.Fatal(err)
	}
	r.await(l, "active")
	h.Eventually("the window reported as inhibiting shortcuts", inhibiting(true))
	h.tap(48)
	r.expect("key 48 1", "key 48 0")
	h.tap(30)
	h.Eventually("the locked bind ran", showingWorkspace(2))
	r.await(l, "active", "inactive")
	h.Eventually("the window no longer reported as inhibiting shortcuts", inhibiting(false))
}

func inhibiting(want bool) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := c.Snapshot().Windows
		return len(ws) == 1 && ws[0].InhibitsKeys == want
	}
}
