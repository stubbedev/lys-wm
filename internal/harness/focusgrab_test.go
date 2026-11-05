package harness

import (
	"errors"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cfg "github.com/stubbedev/lys-wm/internal/protocol/client/hyprlandfocusgrab"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type grabEvents struct{ cleared int }

func (g *grabEvents) Cleared(*cfg.HyprlandFocusGrabV1) error {
	g.cleared++
	return nil
}

func clickTitled(t *testing.T, h *Harness, title string) {
	t.Helper()
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		i := slices.IndexFunc(windowsOf(c), func(w ipc.Window) bool { return w.Title == title })
		if i < 0 {
			t.Fatalf("no window %q", title)
		}
		r := windowsOf(c)[i].Rect
		o := b.Outputs()[0]
		x, y := float64(r.X)+float64(r.Width)/2, float64(r.Y)+float64(r.Height)/2
		p := b.Pointer()
		return errors.Join(p.MoveTo(o, x/160, y/100), p.Frame(), p.Button(btnLeft, true), p.Frame(), p.Button(btnLeft, false), p.Frame())
	})
}

func logged(log *testclient.Log, line string) func() bool {
	return func() bool { return slices.Contains(*log, line) }
}

func TestAFocusGrabKeepsInputOnItsSurfacesUntilAClickOutside(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	a := join(t, h, "a1")
	a2 := a.c.Window("a2", quadrants)
	a.in.Name(a2.Surface, "a2")
	a.c.DispatchUntil("a2 to map", a2.Mapped)
	b := join(t, h, "b")
	b.seen(t, "b focused", logged(&b.in.Log, "kb_enter b"))

	m, err := cfg.NewHyprlandFocusGrabManagerV1(a.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.c.Bind(m)
	events := &grabEvents{}
	grab, err := m.CreateGrab(events)
	if err != nil {
		t.Fatal(err)
	}
	send(t, func() error { return grab.AddSurface(a.w.Surface) }, grab.Commit, a.c.Display.Flush)
	a.seen(t, "the keyboard entering the whitelisted a1", logged(&a.in.Log, "kb_enter a1"))
	b.seen(t, "b losing the keyboard to the grab", logged(&b.in.Log, "kb_leave b"))

	a.in.Log = nil
	clickTitled(t, h, "a1")
	a.seen(t, "a click inside the grab reaching a1", logged(&a.in.Log, "button 272 1"))

	send(t, func() error { return grab.AddSurface(a2.Surface) }, grab.Commit, a.c.Display.Flush)
	a.c.Sync()
	a.in.Log = nil
	clickTitled(t, h, "a2")
	a.seen(t, "a click on the newly added a2 reaching it", logged(&a.in.Log, "button 272 1"))
	if events.cleared != 0 {
		t.Fatalf("clicks inside the whitelist cleared the grab %d times", events.cleared)
	}

	b.in.Log = nil
	clickTitled(t, h, "b")
	a.seen(t, "a click outside clearing the grab", func() bool { return events.cleared == 1 })
	b.c.Sync()
	if slices.Contains(b.in.Log, "button 272 1") {
		t.Fatalf("the click that cleared the grab also reached b: %q", b.in.Log)
	}
	clickTitled(t, h, "b")
	b.seen(t, "the next click reaching b", logged(&b.in.Log, "button 272 1"))
}
