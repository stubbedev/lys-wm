package harness

import (
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/golden"
	"github.com/stubbedev/lys-wm/internal/loop"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

var small = backend.Mode{Size: image.Pt(160, 100), RefreshMHz: 60000, Preferred: true}

func presented(i int) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		outputs := b.Outputs()
		return i < len(outputs) && snapshot(outputs[i]).Pix[3] == 0xff
	}
}

func TestTheHangLimitStopsOnlyADoTheLoopNeverStarted(t *testing.T) {
	t.Parallel()
	h := &Harness{t: t, done: make(chan error, 1)}
	fired := func() <-chan time.Time {
		hang := make(chan time.Time, 1)
		hang <- time.Time{}
		return hang
	}
	running := make(chan struct{})
	close(running)
	if err := h.awaitStart(running, fired()); err != nil {
		t.Errorf("a Do the loop is running failed when the hang limit passed: %v", err)
	}
	if err := h.awaitStart(make(chan struct{}), fired()); err == nil {
		t.Error("a Do the loop never started passed the hang limit")
	}
}

func TestDesktopIsDrawnOnEveryOutput(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	h.Eventually("the first frame", presented(0))
	golden.Assert(t, "desktop", h.Snapshot(0))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddOutput(backend.Mode{Size: image.Pt(40, 30), RefreshMHz: 60000})
		return err
	})
	h.Eventually("a frame on the output added at runtime", presented(1))
	if snap := h.Snapshot(1); snap.W != 40 || snap.H != 30 {
		t.Fatalf("second output is %dx%d", snap.W, snap.H)
	}
}

func TestWaylandInfoConnects(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	out := h.Run("wayland-info").Wait(t, 10*time.Second)
	if trace := h.Trace(); !strings.Contains(trace, "wl_display@1.get_registry(new id wl_registry@2)") {
		t.Fatalf("wayland-info never reached the compositor:\n%s", trace)
	}
	for _, global := range []string{"'wl_compositor'", "'wl_subcompositor'", "'wl_shm'", "'wl_seat'", "'xdg_wm_base'", "'wl_output'", "'zxdg_output_manager_v1'", "'wp_viewporter'", "'wp_fractional_scale_manager_v1'"} {
		if !strings.Contains(out, global) {
			t.Errorf("wayland-info does not list %s:\n%s", global, out)
		}
	}
}

func quadrants(pix []byte, stride int, size image.Point, _ int) {
	colors := [4][3]byte{{0xff, 0, 0}, {0, 0xff, 0}, {0, 0, 0xff}, {0xff, 0xff, 0}}
	for y := range size.Y {
		for x := range size.X {
			c := colors[(y*2/size.Y)*2+x*2/size.X]
			if x == 0 || y == 0 || x == size.X-1 || y == size.Y-1 {
				c = [3]byte{0xff, 0xff, 0xff}
			}
			copy(pix[y*stride+x*4:], []byte{c[2], c[1], c[0], 0xff})
		}
	}
}

func pixel(b *soft.Buffer, x, y int) [3]byte {
	i := y*b.Stride + x*4
	return [3]byte{b.Pix[i+2], b.Pix[i+1], b.Pix[i]}
}

func landed(_ *headless.Backend, c *compositor.Compositor) bool {
	return !c.Holding()
}

func showing(x, y int, want [3]byte) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		outputs := b.Outputs()
		return len(outputs) > 0 && pixel(snapshot(outputs[0]), x, y) == want
	}
}

func TestWindowIsComposited(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("quadrants", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	if w.Size != small.Size {
		t.Fatalf("window configured to %v, want the whole %v output", w.Size, small.Size)
	}
	for _, st := range []cxdg.XdgToplevelState{cxdg.XdgToplevelStateTiledLeft, cxdg.XdgToplevelStateTiledRight, cxdg.XdgToplevelStateTiledTop, cxdg.XdgToplevelStateTiledBottom, cxdg.XdgToplevelStateActivated} {
		if !slices.Contains(w.States, st) {
			t.Fatalf("window states %v lack %d: tiled windows say so on every edge, and the focused one is activated", w.States, st)
		}
	}
	if slices.Contains(w.States, cxdg.XdgToplevelStateConstrainedLeft) {
		t.Fatalf("window states %v: constrained states need xdg_wm_base version 7, the test client binds 6", w.States)
	}
	h.Eventually("the window on the output", showing(1, 1, [3]byte{0xff, 0, 0}))
	golden.Assert(t, "window", h.Snapshot(0))
}

func TestWindowKeepsAnimating(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("counter", func(pix []byte, _ int, _ image.Point, frame int) {
		for i := 0; i < len(pix); i += 4 {
			pix[i], pix[i+1], pix[i+2], pix[i+3] = byte(frame), 0x10, 0x20, 0xff
		}
	})
	c.DispatchUntil("five frame callbacks", func() bool { return w.Frames >= 5 })
	h.Eventually("a later frame on the output", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[0]), 50, 50)[2] >= 4
	})
}

func TestSurfaceTreeIsComposited(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("tree", quadrants)
	child := c.Surface()
	sub, err := c.Subcompositor.GetSubsurface(child, w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	halves := func(pix []byte, stride int, size image.Point, _ int) {
		for y := range size.Y {
			for x := range size.X {
				c := []byte{0, 0, 0xff, 0xff}
				if x >= size.X/2 {
					c = []byte{0, 0xff, 0, 0xff}
				}
				copy(pix[y*stride+x*4:], c)
			}
		}
	}
	for _, err := range []error{
		w.XdgSurface.SetWindowGeometry(10, 5, 140, 90),
		sub.SetPosition(30, 20),
		child.SetBufferTransform(cw.WlOutputTransform90),
		child.Attach(c.Buffer(image.Pt(20, 10), 0, halves), 0, 0),
		child.Commit(),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the rotated subsurface", showing(25, 17, [3]byte{0xff, 0, 0}))
	if got := pixel(h.Snapshot(0), 25, 30); got != [3]byte{0, 0xff, 0} {
		t.Fatalf("pixel below the subsurface's middle is %v: buffer_transform 90 must put the buffer's left half on top", got)
	}
	golden.Assert(t, "tree", h.Snapshot(0))
}

var scaled = backend.Mode{Size: image.Pt(240, 150), RefreshMHz: 60000, Preferred: true}

func startScaled(t *testing.T) *Harness {
	h := Start(t, scaled)
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		return c.SetOutputScale(b.Outputs()[0], 180)
	})
	return h
}

func checker(pix []byte, stride int, size image.Point, _ int) {
	for y := range size.Y {
		for x := range size.X {
			v := byte(0)
			if (x+y)%2 == 0 {
				v = 0xff
			}
			copy(pix[y*stride+x*4:], []byte{v, byte(x), byte(y), 0xff})
		}
	}
}

func TestFractionalScaleRendersCrisp(t *testing.T) {
	t.Parallel()
	h := startScaled(t)
	c := testclient.Connect(t, h.Socket())
	w := c.ScaledWindow("crisp", testclient.FractionalScale, checker)
	c.DispatchUntil("the window to map and enter the output", func() bool { return w.Mapped() && w.Entered == 1 })
	if w.Preferred120 != 180 || w.Logical != image.Pt(160, 100) || w.Size != scaled.Size {
		t.Fatalf("preferred %d, logical %v, buffer %v; want 180, 160x100 and a 240x150 buffer", w.Preferred120, w.Logical, w.Size)
	}
	want := soft.NewBuffer(scaled.Size.X, scaled.Size.Y)
	checker(want.Pix, want.Stride, scaled.Size, 0)
	var cursor image.Rectangle
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		x, y := c.Cursor()
		tip := image.Pt(int(x*1.5), int(y*1.5))
		cursor = image.Rectangle{Min: tip, Max: tip.Add(image.Pt(36, 36))}
		return nil
	})
	h.Eventually("the buffer pixel for pixel on the output", func(b *headless.Backend, _ *compositor.Compositor) bool {
		got := snapshot(b.Outputs()[0])
		for y := range scaled.Size.Y {
			for x := range scaled.Size.X {
				i := y*got.Stride + x*4
				if !image.Pt(x, y).In(cursor) && string(got.Pix[i:i+4]) != string(want.Pix[i:i+4]) {
					return false
				}
			}
		}
		return true
	})
}

func TestIntegerScaleIsScaledOnce(t *testing.T) {
	t.Parallel()
	h := startScaled(t)
	c := testclient.Connect(t, h.Socket())
	w := c.ScaledWindow("integer", testclient.IntegerScale, quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	if w.BufferScale != 2 || w.Size != image.Pt(320, 200) {
		t.Fatalf("buffer scale %d, buffer %v; want 2 and 320x200 for a 160x100 window on a 1.5 output", w.BufferScale, w.Size)
	}
	h.Eventually("the window on the output", showing(scaled.Size.X-2, scaled.Size.Y-2, [3]byte{0xff, 0xff, 0}))
	golden.Assert(t, "scaled", h.Snapshot(0))
}

type starter func(t *testing.T, modes ...backend.Mode) *Harness

type gpuStarter func(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness

func onEachGPURenderer(t *testing.T, test func(t *testing.T, start gpuStarter)) {
	t.Run("gles", func(t *testing.T) { test(t, StartGLES) })
	t.Run("vulkan", func(t *testing.T) { test(t, StartVulkan) })
}

func onEachDMABUFRenderer(t *testing.T, test func(t *testing.T, start gpuStarter)) {
	t.Run("gles", func(t *testing.T) { test(t, StartGLES) })
	t.Run("vulkan", func(t *testing.T) {
		if !testenv.Vulkan(t).Device().Exports() {
			t.Skip("this Vulkan device shares no dmabufs; lavapipe needs /dev/udmabuf")
		}
		test(t, StartVulkan)
	})
}

const gpuGoldenTolerance = 1

func (h *Harness) assertGolden(name string) {
	h.t.Helper()
	tolerance := gpuGoldenTolerance
	if _, isSoft := h.backend.Renderer().(*soft.Renderer); isSoft {
		tolerance = 0
	}
	golden.Within(h.t, name, h.renderedLoaded(), tolerance)
}

const loadedFrameTries = 20

type loads struct {
	pending bool
	landed  uint64
}

func (h *Harness) loads() loads {
	h.t.Helper()
	var l loads
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		l = loads{c.LabelsPending() || c.CursorsLoading(), c.Landed()}
		return nil
	})
	return l
}

func drewEverything(before, after loads) bool {
	return !before.pending && after == before
}

func (h *Harness) renderedLoaded() *soft.Buffer {
	h.t.Helper()
	for range loadedFrameTries {
		h.Eventually("labels and cursors loaded", func(_ *headless.Backend, c *compositor.Compositor) bool {
			return !c.LabelsPending() && !c.CursorsLoading()
		})
		before := h.loads()
		frame := h.Rendered(0)
		if drewEverything(before, h.loads()) {
			return frame
		}
	}
	h.t.Fatalf("harness: every one of %d frames asked for a label or cursor it did not have", loadedFrameTries)
	return nil
}

func TestAGoldenFrameIsOneThatAskedForNothingItLacked(t *testing.T) {
	idle := loads{landed: 3}
	for _, c := range []struct {
		name          string
		before, after loads
		want          bool
	}{
		{"nothing asked for and nothing landed", idle, idle, true},
		{"a label the frame asked for is still loading", idle, loads{pending: true, landed: 3}, false},
		{"a label the frame asked for landed before the check", idle, loads{landed: 4}, false},
		{"a load was running before the frame", loads{pending: true, landed: 3}, loads{pending: true, landed: 3}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := drewEverything(c.before, c.after); got != c.want {
				t.Fatalf("drewEverything(%+v, %+v) = %v, want %v", c.before, c.after, got, c.want)
			}
		})
	}
}

func onEachConfiguredRenderer(t *testing.T, test func(t *testing.T, start gpuStarter)) {
	t.Run("soft", func(t *testing.T) { test(t, StartWith) })
	onEachGPURenderer(t, test)
}

func onEachRenderer(t *testing.T, test func(t *testing.T, start starter)) {
	for _, r := range []struct {
		name  string
		start starter
	}{
		{"soft", Start},
		{"gles", func(t *testing.T, modes ...backend.Mode) *Harness {
			return StartGLES(t, func(*compositor.Config) {}, modes...)
		}},
		{"vulkan", func(t *testing.T, modes ...backend.Mode) *Harness {
			return StartVulkan(t, func(*compositor.Config) {}, modes...)
		}},
	} {
		t.Run(r.name, func(t *testing.T) { test(t, r.start) })
	}
}

func snapshot(o *headless.Output) *soft.Buffer {
	b, err := o.Snapshot()
	if err != nil {
		panic(fmt.Sprintf("harness: snapshot of %s: %v", o.Name(), err))
	}
	return b
}

func primary(o *headless.Output) *soft.Buffer {
	b, err := o.Primary()
	if err != nil {
		panic(fmt.Sprintf("harness: primary plane of %s: %v", o.Name(), err))
	}
	return b
}

func (h *Harness) ticks(every time.Duration) func() []time.Duration {
	h.t.Helper()
	var fired []time.Duration
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		var tick *loop.Timer
		tick, err := h.loop.AddTimer(func() error {
			fired = append(fired, h.loop.Now())
			return tick.Arm(every)
		})
		if err != nil {
			return err
		}
		return tick.Arm(every)
	})
	return func() []time.Duration {
		var got []time.Duration
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			got = slices.Clone(fired)
			return nil
		})
		return got
	}
}

func quiet(b *headless.Backend, _ *compositor.Compositor) bool {
	return !slices.ContainsFunc(b.Outputs(), (*headless.Output).FrameOwed)
}

func TestAFrozenHarnessClockMovesOnlyByAdvanceAndFrames(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	h.Freeze()
	h.Eventually("no frame owed", quiet)
	start := h.Now()
	fired := h.ticks(time.Hour)
	if now := h.Now(); now != start {
		t.Fatalf("a frozen clock went from %v to %v with nothing to show", start, now)
	}
	h.Advance(3*time.Hour + time.Minute)
	if got, want := fired(), []time.Duration{start + time.Hour, start + 2*time.Hour, start + 3*time.Hour}; !slices.Equal(got, want) {
		t.Fatalf("advancing three hours fired an hourly timer at %v, want %v", got, want)
	}
	if now := h.Now(); now != start+3*time.Hour+time.Minute {
		t.Fatalf("advancing %v from %v left the clock at %v", 3*time.Hour+time.Minute, start, now)
	}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return b.Outputs()[0].ScheduleFrame() })
	h.Eventually("the frame presented and none owed", quiet)
	if got := len(fired()); got != 3 {
		t.Fatalf("stepping to a frame fired the hourly timer %d times, want still 3", got)
	}
}

func TestAnAdvanceFiresTimersAtTheirOwnDeadlinesWhileTheClockFollowsTheWall(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	start := h.Now()
	fired := h.ticks(time.Hour)
	h.Advance(2*time.Hour + time.Minute)
	got := fired()
	if len(got) != 2 || got[0] < start+time.Hour || got[1]-got[0] != time.Hour {
		t.Fatalf("advancing two hours from %v fired an hourly timer at %v, want twice an hour apart", start, got)
	}
	if now := h.Now(); now < start+2*time.Hour+time.Minute {
		t.Fatalf("advancing %v from %v left the clock at %v", 2*time.Hour+time.Minute, start, now)
	}
}
