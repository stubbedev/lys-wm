package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func frameOwedWhenTheOutputGoes(t *testing.T, gone func(b *headless.Backend, c *compositor.Compositor) error) {
	t.Helper()
	slow := small
	slow.RefreshMHz = 1000
	h := StartWith(t, throttled, slow)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("painting", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	onScreen := showing(1, 1, [3]byte{0xff, 0, 0})
	c.DispatchUntil("the window on the output", func() bool {
		shown := false
		h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
			shown = onScreen(b, comp)
			return nil
		})
		return shown
	})
	w.Stop()
	var times frameTimes
	s := w.Surface
	if _, err := s.Frame(&times); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(s.Attach(c.Buffer(w.Size, 1, quadrants), 0, 0), s.DamageBuffer(0, 0, int32(w.Size.X), int32(w.Size.Y)), s.Commit()); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(times) > 0 {
		t.Fatal("the frame callback was answered before the output went, so the test proves nothing; the output's refresh is too fast")
	}
	fired := hiddenFired(h)
	h.Do(gone)
	c.DispatchUntil("the frame callback owed when the output went", func() bool { return len(times) > 0 })
	if hiddenFired(h) == fired {
		t.Fatal("the frame callback was answered without a hidden frame, so something other than the hidden rate answered it")
	}
}

func TestAFrameCallbackOwedWhenTheOutputPowersOffIsAnsweredAtTheHiddenRate(t *testing.T) {
	t.Parallel()
	frameOwedWhenTheOutputGoes(t, func(_ *headless.Backend, c *compositor.Compositor) error {
		return c.SetPower(c.Outputs()[0], false)
	})
}

func TestAFrameCallbackOwedWhenTheOutputIsUnpluggedIsAnsweredAtTheHiddenRate(t *testing.T) {
	t.Parallel()
	frameOwedWhenTheOutputGoes(t, func(b *headless.Backend, _ *compositor.Compositor) error {
		return b.RemoveOutput(b.Outputs()[0])
	})
}
