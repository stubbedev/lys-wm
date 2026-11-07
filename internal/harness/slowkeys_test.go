package harness

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func (h *Harness) quickTap(code uint32) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if err := b.Keyboard().Key(code, true); err != nil {
			return err
		}
		return b.Keyboard().Key(code, false)
	})
}

func TestSlowKeysDropAQuickTapAndTakeAHeldKey(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(*compositor.Config) {}, small)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	c.DispatchUntil("keyboard focus", func() bool { return slices.Contains(in.Log, "kb_enter win") })

	h.dispatch("slowkeys 1000")
	h.quickTap(keyQ)
	h.key(keyL, true)
	c.DispatchUntil("the held key, once held long enough", func() bool { return slices.Contains(in.Log, "key 38 1") })
	h.key(keyL, false)
	c.DispatchUntil("the held key's release", func() bool { return slices.Contains(in.Log, "key 38 0") })
	if slices.Contains(in.Log, "key 16 1") || slices.Contains(in.Log, "key 16 0") {
		t.Fatalf("a quick tap under slow keys reached the window: %v", in.Log)
	}

	h.dispatch("slowkeys 0")
	h.quickTap(keyQ)
	c.DispatchUntil("a quick tap with slow keys off", func() bool { return slices.Contains(in.Log, "key 16 0") })
}
