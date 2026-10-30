package harness

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func idleFrames(h *Harness, n uint64) {
	h.t.Helper()
	var o *headless.Output
	var before uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o = b.Outputs()[0]
		before = o.Frames()
		return o.ScheduleFrame()
	})
	h.Eventually("frames on the inherited output", func(*headless.Backend, *compositor.Compositor) bool {
		if o.Frames() < before+n {
			_ = o.ScheduleFrame()
			return false
		}
		return true
	})
}

func takeover(h *Harness) (presents uint64, inherited bool) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		presents, inherited = o.Presents(), o.Inherited()
		return nil
	})
	return presents, inherited
}

func TestAnInheritedFrameStaysUntilAClientDraws(t *testing.T) {
	h := StartInherited(t, func(*compositor.Config) {}, small)
	idleFrames(h, 5)
	if presents, inherited := takeover(h); presents != 0 || !inherited {
		t.Fatalf("lys-wm presented %d frames over the inherited one before a client drew (still inherited: %v)", presents, inherited)
	}
	c := testclient.Connect(t, h.Socket())
	w := c.Window("greeter", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the frame that replaces the inherited one", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Takeover() != nil
	})
	var first [3]byte
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		first = pixel(b.Outputs()[0].Takeover(), 1, 1)
		return nil
	})
	if first != [3]byte{0xff, 0, 0} {
		t.Fatalf("the frame that replaced the inherited one shows %v at (1, 1), not the client's red", first)
	}
}

func TestAnInheritedFrameGivesWayWhenNoClientDraws(t *testing.T) {
	var mu sync.Mutex
	var reports []string
	timeout := 50 * time.Millisecond
	start := time.Now()
	h := StartInherited(t, func(cfg *compositor.Config) {
		cfg.InheritTimeout = timeout
		cfg.Report = func(err error) {
			mu.Lock()
			defer mu.Unlock()
			reports = append(reports, err.Error())
		}
	}, small)
	h.Eventually("the frame that replaces the inherited one", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Takeover() != nil
	})
	if waited := time.Since(start); waited < timeout {
		t.Fatalf("lys-wm drew over the inherited frame after %v, before the %v timeout", waited, timeout)
	}
	var first [3]byte
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		first = pixel(b.Outputs()[0].Takeover(), 1, 1)
		return nil
	})
	if bg := compositor.Background; first != [3]byte{bg.R, bg.G, bg.B} {
		t.Fatalf("the frame drawn after the timeout shows %v, not the background", first)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 || !strings.Contains(reports[0], "no client drew on HEADLESS-1 within 50ms") {
		t.Fatalf("the timeout reported %q", reports)
	}
}
