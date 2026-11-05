package harness

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
)

func TestKeyButtonAxisAndTouchEventsReachTheClientWithoutAllocating(t *testing.T) {
	r := inputOn(t, StartUntraced(t, small))
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		key := &backend.Key{Code: 30}
		button := &backend.PointerButton{Button: 0x110}
		axis := &backend.PointerAxis{Orientation: backend.Horizontal, Source: backend.Finger, Value: 1}
		down := &backend.TouchDown{Output: b.Outputs()[0], ID: 1, X: 0.5, Y: 0.5}
		motion := &backend.TouchMotion{ID: 1, X: 0.25, Y: 0.25}
		up := &backend.TouchUp{ID: 1}
		var frame, touchFrame backend.Event = &backend.PointerFrame{}, &backend.TouchFrame{}
		press := func(pressed bool) error {
			key.Pressed, button.Pressed = pressed, pressed
			return errors.Join(comp.Input(b.Keyboard(), key), comp.Input(b.Pointer(), button), comp.Input(b.Pointer(), frame))
		}
		cycle := func() {
			err := errors.Join(press(true), press(false), comp.Input(b.Pointer(), axis), comp.Input(b.Pointer(), frame),
				comp.Input(b.Touch(), down), comp.Input(b.Touch(), touchFrame),
				comp.Input(b.Touch(), motion), comp.Input(b.Touch(), touchFrame),
				comp.Input(b.Touch(), up), comp.Input(b.Touch(), touchFrame))
			if err != nil {
				t.Fatal(err)
			}
		}
		for range 20 {
			cycle()
		}
		if n := allocsPerRun(100, func() {}, cycle); n != 0 {
			t.Errorf("key, button, axis and touch events allocated %d times a cycle", n)
		}
		return nil
	})
	want := []string{"key 30 1", "key 30 0", "button 272 1", "button 272 0", "axis 1 1", "down 1 win 80,50", "touch_motion 1 40,25", "up 1"}
	r.c.DispatchUntil(strings.Join(want, ", "), func() bool {
		return !slices.ContainsFunc(want, func(w string) bool { return !slices.Contains(r.in.Log, w) })
	})
}
