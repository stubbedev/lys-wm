package harness

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var everyTransform = []render.Transform{render.Normal, render.Rotate90, render.Rotate180, render.Rotate270, render.Flipped, render.Flipped90, render.Flipped180, render.Flipped270}

func transformed(t render.Transform) func(*compositor.Config) {
	return func(cfg *compositor.Config) { cfg.Outputs.Fallback.Transform = t }
}

func twoWindows(t *testing.T, h *Harness) (*soft.Buffer, []ipc.Window) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	a := c.Window("a", quadrants)
	c.DispatchUntil("a to map", a.Mapped)
	b := c.Window("b", testclient.Fill(0, 0x80, 0xff))
	c.DispatchUntil("b to map", b.Mapped)
	for _, w := range query[[]ipc.Window](h, ipc.KindWindows) {
		drawn := map[string]*testclient.Window{"a": a, "b": b}[w.Title]
		c.DispatchUntil(w.Title+" drawn at its tile", func() bool { return drawn.Logical == image.Pt(w.Rect.Width, w.Rect.Height) })
	}
	a.Stop()
	quiesce(t, h, c, b)
	return h.Rendered(0), query[[]ipc.Window](h, ipc.KindWindows)
}

func upright(t *testing.T, buf *soft.Buffer, tr render.Transform) *soft.Buffer {
	t.Helper()
	r := soft.New()
	size := buf.Size()
	tex, err := r.NewTexture(render.XRGB8888, size, buf.Stride, buf.Pix)
	if err != nil {
		t.Fatal(err)
	}
	logical := tr.Size(size)
	out := soft.NewBuffer(logical.X, logical.Y)
	p, err := r.Begin(out, render.Region{{Max: logical}})
	if err != nil {
		t.Fatal(err)
	}
	p.DrawTexture(tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Max: logical}, tr.Invert(), 1, render.Mask{})
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
	return out
}

func rects(ws []ipc.Window) []ipc.Rect {
	out := make([]ipc.Rect, len(ws))
	for i, w := range ws {
		out[i] = w.Rect
	}
	return out
}

func checkTransforms(t *testing.T, start gpuStarter) {
	panel := backend.Mode{Size: image.Pt(160, 100), RefreshMHz: 60000, Preferred: true}
	for _, tr := range everyTransform {
		t.Run(tr.String(), func(t *testing.T) {
			got, gotWindows := twoWindows(t, start(t, transformed(tr), panel))
			portrait := panel
			portrait.Size = tr.Size(panel.Size)
			want, wantWindows := twoWindows(t, start(t, func(*compositor.Config) {}, portrait))
			if !slices.Equal(rects(gotWindows), rects(wantWindows)) {
				t.Fatalf("windows tile at %v on a %s output, at %v on an upright one of its logical size", rects(gotWindows), tr, rects(wantWindows))
			}
			if got.Size() != panel.Size {
				t.Fatalf("the buffer is %v, want the mode's %v", got.Size(), panel.Size)
			}
			seen := upright(t, got, tr)
			for y := range portrait.Size.Y {
				for x := range portrait.Size.X {
					if a, b := pixel(seen, x, y), pixel(want, x, y); a != b {
						t.Fatalf("at %d,%d the %s buffer shown upright is %v, the upright output %v", x, y, tr, a, b)
					}
				}
			}
		})
	}
}

func TestEveryTransformRotatesTheOutputOnTheSoftRenderer(t *testing.T) {
	t.Parallel()
	checkTransforms(t, func(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
		return StartWith(t, configure, modes...)
	})
}

func TestEveryTransformRotatesTheOutputOnTheGPURenderers(t *testing.T) {
	onEachGPURenderer(t, checkTransforms)
}

func TestTheWorldIsToldTheTransformAndPointersFollowIt(t *testing.T) {
	t.Parallel()
	h := StartWith(t, transformed(render.Rotate90), small)
	c := testclient.Connect(t, h.Socket())
	outputs := query[[]ipc.Output](h, ipc.KindOutputs)
	if len(outputs) != 1 || outputs[0].Transform != "90" || outputs[0].Rect != (ipc.Rect{Width: small.Size.Y, Height: small.Size.X}) {
		t.Fatalf("outputs %+v", outputs)
	}
	if len(c.Outputs) != 1 {
		t.Fatalf("the client sees %d outputs", len(c.Outputs))
	}
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		p := b.Pointer()
		if err := errors.Join(p.MoveTo(b.Outputs()[0], 0.25, 0.125), p.Frame()); err != nil {
			return err
		}
		x, y := comp.Cursor()
		if wantX, wantY := 0.875*float64(small.Size.Y), 0.25*float64(small.Size.X); x != wantX || y != wantY {
			t.Errorf("an absolute pointer at 0.25, 0.125 of the panel lands at %v, %v; want %v, %v", x, y, wantX, wantY)
		}
		return nil
	})
}

func TestRotatingAtRuntimeRetilesOnce(t *testing.T) {
	t.Parallel()
	var cfg compositor.Config
	h := StartWith(t, func(c *compositor.Config) { cfg = *c }, desktop)
	c := testclient.Connect(t, h.Socket())
	for _, name := range []string{"a", "b"} {
		w := c.Window(name, quadrants)
		c.DispatchUntil(name+" to map", w.Mapped)
	}
	lines := h.watch()
	cfg.Outputs.Fallback.Transform = render.Rotate90
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
	if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: "submap done"}); err != nil {
		t.Fatal(err)
	}
	var got []streamed
	awaitStreamed(t, lines, "the end of the rotation", func(e streamed) bool {
		got = append(got, e)
		return e.Event == "submap"
	})
	moved := map[wm.WindowID]int{}
	for _, e := range got {
		if e.Event == "rect" {
			moved[*e.Window]++
		}
	}
	if len(moved) != 2 || moved[1]+moved[2] != 2 {
		t.Fatalf("rotating moved windows %v, want each once: %s", moved, raws(got))
	}
	windows := query[[]ipc.Window](h, ipc.KindWindows)
	for _, w := range windows {
		if w.Rect.Height != desktop.Size.X || w.Rect.Width != desktop.Size.Y/2 {
			t.Fatalf("on the portrait output window %d tiles at %+v", w.ID, w.Rect)
		}
	}
}

func TestReservedAreasKeepWindowsOut(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Outputs.Fallback.Reserved = compositor.Insets{Top: 10, Left: 20, Bottom: 5}
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("kept", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	if got := query[ipc.Window](h, ipc.KindActiveWindow).Rect; got != (ipc.Rect{X: 20, Y: 10, Width: small.Size.X - 20, Height: small.Size.Y - 15}) {
		t.Fatalf("the window tiles at %+v", got)
	}
}

func TestAMirrorShowsItsSourceWithOneDrawAndNoWorkspaces(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Outputs.Named = []compositor.OutputRule{{Match: wm.OutputMatch{Name: "HEADLESS-2"}, Mirror: &wm.OutputMatch{Name: "HEADLESS-1"}}}
	}, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("shown", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	outputs := query[[]ipc.Output](h, ipc.KindOutputs)
	if len(outputs) != 2 || outputs[1].Name != "HEADLESS-2" || outputs[1].MirrorOf != "HEADLESS-1" || outputs[0].MirrorOf != "" {
		t.Fatalf("outputs %+v", outputs)
	}
	for _, ws := range query[[]ipc.Workspace](h, ipc.KindWorkspaces) {
		if ws.Output != "HEADLESS-1" {
			t.Fatalf("workspace %+v is on the mirror", ws)
		}
	}
	if len(c.Outputs) != 1 {
		t.Fatalf("clients see %d outputs, want the mirror left out", len(c.Outputs))
	}
	var draws, presents uint64
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		draws, _ = comp.MirrorDraws("HEADLESS-2")
		presents = b.Outputs()[1].Presents()
		return nil
	})
	if draws == 0 || draws != presents {
		t.Fatalf("the mirror presented %d frames with %d textured draws, want one each", presents, draws)
	}
	source, mirror := h.Snapshot(0), h.Snapshot(1)
	for _, at := range []image.Point{{20, 20}, {140, 80}, {80, 50}} {
		if a, b := pixel(source, at.X, at.Y), pixel(mirror, at.X, at.Y); a != b {
			t.Fatalf("at %v the mirror shows %v, its source %v", at, b, a)
		}
	}
}

func TestAPreRotatedFullscreenBufferScansOutOnARotatedOutput(t *testing.T) {
	onEachDMABUFRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, transformed(render.Rotate90), small)
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			b.Outputs()[0].AllowScanout(primaryPlane)
			return nil
		})
		c := testclient.Connect(t, h.Socket())
		_, in := c.Seat()
		w := c.Window("game", quadrants)
		c.DispatchUntil("the window to map", w.Mapped)
		c.DispatchUntil("the pointer to enter", func() bool { return in.Serial != 0 })
		if err := errors.Join(in.Pointer.SetCursor(in.Serial, nil, 0, 0), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.dispatch("fullscreen")
		portrait := render.Rotate90.Size(small.Size)
		c.DispatchUntil("the fullscreen configure", func() bool { return w.Logical == portrait })
		quiesce(t, h, c, w)
		s := &scanoutScene{h: h, c: c, w: w}
		if err := w.Surface.SetBufferTransform(1); err != nil {
			t.Fatal(err)
		}
		before := composited(h)
		s.show(t, "the pre-rotated dmabuf", c.DMABUF(small.Size, testclient.Fill(0xff, 0, 0)))
		s.show(t, "another pre-rotated dmabuf", c.DMABUF(small.Size, testclient.Fill(0, 0xff, 0)))
		settle(t, h)
		if n := composited(h) - before; n > 1 {
			t.Fatalf("a pre-rotated fullscreen window composited %d frames", n)
		}
		if at := scannedAt(h); at.Dst != (image.Rectangle{Max: small.Size}) {
			t.Fatalf("the buffer is placed at %+v on the plane", at)
		}

		if err := w.Surface.SetBufferTransform(0); err != nil {
			t.Fatal(err)
		}
		scanned := scanouts(h)
		if err := errors.Join(w.Surface.Attach(c.DMABUF(portrait, testclient.Fill(0, 0, 0xff)).WL, 0, 0), w.Surface.DamageBuffer(0, 0, int32(portrait.X), int32(portrait.Y)), w.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.Sync()
		settle(t, h)
		if n := scanouts(h); n != scanned {
			t.Fatalf("an upright buffer on the rotated output scanned out %d frames", n-scanned)
		}
	})
}

func TestACaptureOfARotatedOutputIsUpright(t *testing.T) {
	t.Parallel()
	h := StartWith(t, transformed(render.Rotate90), small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("seen", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	r := newCaptureRig(t, h, c)
	src, err := r.outputs.CreateSource(c.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	session, view := r.session(src, 0)
	portrait := render.Rotate90.Size(small.Size)
	if view.size != portrait {
		t.Fatalf("the capture of a portrait output is %v, want %v", view.size, portrait)
	}
	pix := r.first(session, view.size)
	if got := rgbAt(pix, view.size.X*4, 10, 10); got != [3]byte{0xff, 0, 0} {
		t.Fatalf("the capture's top left shows %v, want the window's red quadrant", got)
	}
	if got := rgbAt(pix, view.size.X*4, portrait.X-10, portrait.Y-10); got != [3]byte{0xff, 0xff, 0} {
		t.Fatalf("the capture's bottom right shows %v, want the window's yellow quadrant", got)
	}
}
