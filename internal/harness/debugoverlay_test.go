package harness

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/frametrace"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const debugFadeFrames = 8

func TestTheDamageOverlayFadesAStaticSceneToNoFrames(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("still", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	frames := func(action string) uint64 {
		t.Helper()
		before := presentCount(h)
		h.dispatch(action)
		settle(t, h)
		return presentCount(h) - before
	}
	if n := frames("debug damage on"); n == 0 || n > debugFadeFrames+1 {
		t.Fatalf("the damage overlay over a static scene presented %d frames, want 1 to %d", n, debugFadeFrames+1)
	}
	for _, action := range []string{"debug damage off", "debug opaque on", "debug opaque off"} {
		if n := frames(action); n != 1 {
			t.Fatalf("%s over a static scene presented %d frames, want 1", action, n)
		}
	}
}

func TestTheOpaqueOverlayTintsTheOpaqueWindow(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("solid", testclient.Fill(0, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	h.dispatch("debug opaque on")
	settle(t, h)
	if got := pixel(h.Snapshot(0), small.Size.X/2, small.Size.Y/2); got[1] == 0 || got[1] <= got[0] {
		t.Fatalf("the middle of an opaque black window shows %v, want the opaque tint", got)
	}
}

func TestThePlanesOverlayLeavesAScannedOutWindowScannedOut(t *testing.T) {
	onEachDMABUFRenderer(t, func(t *testing.T, start gpuStarter) {
		s := fullscreenScanout(t, start, func(*compositor.Config) {})
		s.show(t, "the dmabuf", s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0xff, 0, 0)))
		settle(t, s.h)
		s.h.dispatch("debug planes on")
		settle(t, s.h)
		before, scanned := composited(s.h), scanouts(s.h)
		s.show(t, "another dmabuf", s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0, 0xff, 0)))
		settle(t, s.h)
		if n := composited(s.h) - before; n != 0 {
			t.Fatalf("with the planes overlay on, a scanned-out window composited %d frames", n)
		}
		if scanouts(s.h) == scanned {
			t.Fatal("the new dmabuf was not scanned out")
		}
	})
}

func TestADebugTraceRecordsARunningSession(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Hooks.OpenFrameTrace = func(path string) (*frametrace.Recorder, error) {
			return frametrace.Create(path, frametrace.Header{Backend: "headless", Renderer: "soft"}, nil, frametrace.Clock{Now: backend.Now}, cfg.Report)
		}
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("busy", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	h.dispatch("debug trace start " + path)
	from := w.Frames
	c.DispatchUntil("ten frames", func() bool { return w.Frames >= from+10 })
	h.dispatch("debug trace stop")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	header, frames, err := frametrace.Read(f)
	if err != nil {
		t.Fatal(err)
	}
	if header.Backend != "headless" || len(frames) < 10 {
		t.Fatalf("the trace has header %+v and %d frames", header, len(frames))
	}
	for i, fr := range frames {
		if fr.Cost.FileIO != 0 {
			t.Fatalf("frame %d did %d file I/O calls on the loop", i, fr.Cost.FileIO)
		}
	}
	if err := dispatchErr(h, "debug trace stop"); err == nil {
		t.Fatal("stopping a trace that is not running did not fail")
	}
}

func dispatchErr(h *Harness, text string) error {
	var err error
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		a, perr := wm.ParseAction(text)
		if perr != nil {
			return perr
		}
		err = c.Dispatch(a)
		return nil
	})
	return err
}

func TestThePlanesOverlayTintsACompositedFrame(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("drawn", testclient.Fill(0, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	mid := image.Pt(small.Size.X/2, small.Size.Y/2)
	if got := pixel(h.Snapshot(0), mid.X, mid.Y); got != [3]byte{} {
		t.Fatalf("the black window shows %v", got)
	}
	h.dispatch("debug planes on")
	settle(t, h)
	if got := pixel(h.Snapshot(0), mid.X, mid.Y); got[2] == 0 {
		t.Fatalf("a composited frame under the planes overlay shows %v, want the tint", got)
	}
	h.dispatch("debug planes off")
	settle(t, h)
	if got := pixel(h.Snapshot(0), mid.X, mid.Y); got != [3]byte{} {
		t.Fatalf("the tint stayed after the overlay went off: %v", got)
	}
}
