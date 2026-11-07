package harness

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
)

func TestAPensPressureReachesAnXClientThroughXwayland(t *testing.T) {
	t.Parallel()
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	xc.window(image.Rect(0, 0, 40, 30), 0xff0000, false, map[string][]byte{"WM_CLASS": []byte("canvas\x00XTablet\x00")})
	h.Eventually("the X window tiled", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 1 })
	addTablet(h)
	events := h.RunX("stdbuf", "-oL", "xinput", "test-xi2", "--root")
	for deadline := time.Now().Add(clientTimeout); !strings.Contains(events.Output(), "EVENT type"); {
		if time.Now().After(deadline) {
			t.Fatalf("xinput test-xi2 saw no pointer motion:\n%s", events.Output())
		}
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(b.Pointer().Move(1, 0), b.Pointer().Frame())
		})
		time.Sleep(20 * time.Millisecond)
	}

	draw := pressed(0.5, 0.5, 0.5)
	draw.Changed |= tabletinput.AxisPressure
	harder := pressed(0.6, 0.5, 0.75)
	tablet(h, &tabletinput.Proximity{Time: time.Millisecond, Tool: pen, In: true, Axes: at(0.5, 0.5)},
		&tabletinput.Tip{Time: 2 * time.Millisecond, Tool: pen, Down: true, Axes: draw},
		&tabletinput.Motion{Time: 3 * time.Millisecond, Tool: pen, Axes: harder})
	events.Expect(t, "2: 49151.00", 10*time.Second)
	devices := h.RunX("xinput", "list", "--name-only").Wait(t, 10*time.Second)
	if !strings.Contains(devices, "xwayland-tablet stylus") {
		t.Fatalf("Xwayland made no tablet stylus device:\n%s", devices)
	}
}
