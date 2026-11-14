package harness

import (
	"encoding/json"
	"errors"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/backend/virtual"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var virtualMode = wmipc.Mode{Width: 200, Height: 120, RefreshMHz: 60000}

func (h *Harness) createOutput(req ipc.Request) string {
	h.t.Helper()
	req.Request = wmipc.KindCreateOutput
	raw, err := ipc.Do(h.IPCSocket(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	var created ipc.CreatedOutput
	if err := json.Unmarshal(raw, &created); err != nil {
		h.t.Fatalf("decode createoutput %s: %v", raw, err)
	}
	return created.Name
}

func (h *Harness) removeOutput(name string) error {
	_, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: wmipc.KindRemoveOutput, Name: name})
	return err
}

func outputNamed(t *testing.T, outputs []ipc.Output, name string) ipc.Output {
	t.Helper()
	i := slices.IndexFunc(outputs, func(o ipc.Output) bool { return o.Name == name })
	if i < 0 {
		t.Fatalf("no output %s among %+v", name, outputs)
	}
	return outputs[i]
}

func virtualFrames(h *Harness, name string) uint64 {
	h.t.Helper()
	var n uint64
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		for _, o := range c.Outputs() {
			if v, ok := o.Backend().(*virtual.Output); ok && v.Name() == name {
				n = v.Frames()
				return nil
			}
		}
		return errors.New("harness: no virtual output " + name)
	})
	return n
}

func windowOutput(h *Harness) string {
	h.t.Helper()
	windows := query[[]ipc.Window](h, wmipc.KindWindows)
	if len(windows) != 1 {
		h.t.Fatalf("windows %+v, want one", windows)
	}
	return windows[0].Output
}

func (h *Harness) awaitWindowOn(name string) {
	h.t.Helper()
	deadline := time.Now().Add(testenv.HangLimit)
	for windowOutput(h) != name {
		if time.Now().After(deadline) {
			h.t.Fatalf("harness: the window never reached output %s", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAVirtualOutputComesAndGoesLikeAMonitor(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("moved", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)

	name := h.createOutput(ipc.Request{Mode: &virtualMode})
	if name != "VIRT-1" {
		t.Fatalf("the first virtual output is %q, want VIRT-1", name)
	}
	outputs := query[[]ipc.Output](h, wmipc.KindOutputs)
	got := outputNamed(t, outputs, name)
	if len(outputs) != 2 || got.Rect != (wmipc.Rect{X: 160, Width: 200, Height: 120}) || got.Mode != virtualMode || got.Scale != 1 || got.Description != "Virtual output VIRT-1" || !got.Powered {
		t.Fatalf("outputs %+v", outputs)
	}
	info := h.Run("wayland-info").Wait(t, clientTimeout)
	for _, want := range []string{"name: VIRT-1", "description: Virtual output VIRT-1", "logical_x: 160, logical_y: 0", "logical_width: 200, logical_height: 120"} {
		if !strings.Contains(info, want) {
			t.Errorf("wayland-info does not show %q:\n%s", want, info)
		}
	}

	h.dispatch("movetoworkspace 2")
	h.awaitWindowOn(name)
	if err := h.removeOutput(name); err != nil {
		t.Fatal(err)
	}
	outputs = query[[]ipc.Output](h, wmipc.KindOutputs)
	if len(outputs) != 1 || outputs[0].Name != "HEADLESS-1" {
		t.Fatalf("outputs after the removal %+v", outputs)
	}
	if on := windowOutput(h); on != "HEADLESS-1" {
		t.Fatalf("the window stayed on %q after its output went", on)
	}
	for _, ws := range query[[]ipc.Workspace](h, wmipc.KindWorkspaces) {
		if ws.Output != "HEADLESS-1" {
			t.Fatalf("workspace %+v outlived its output", ws)
		}
	}
	if late := testclient.Connect(t, h.Socket()); len(late.Outputs) != 1 {
		t.Fatalf("a client connecting after the removal sees %d wl_outputs", len(late.Outputs))
	}
	if again := h.createOutput(ipc.Request{Mode: &virtualMode}); again != "VIRT-1" {
		t.Fatalf("the freed name was not reused: %q", again)
	}
	if next := h.createOutput(ipc.Request{Mode: &virtualMode}); next != "VIRT-2" {
		t.Fatalf("the second virtual output is %q, want VIRT-2", next)
	}
}

func TestOnlyVirtualOutputsAreRemovedAndNamesStayUnique(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	for _, c := range []struct {
		req  ipc.Request
		want string
	}{
		{ipc.Request{Name: "HEADLESS-1", Mode: &virtualMode}, "an output of that name exists"},
		{ipc.Request{Mode: &virtualMode, Scale: 40}, "scale 40 is outside"},
		{ipc.Request{Mode: &wmipc.Mode{Width: 1 << 20, Height: 1 << 20, RefreshMHz: 60000}}, "is beyond 16384x16384"},
	} {
		c.req.Request = wmipc.KindCreateOutput
		if _, err := ipc.Do(h.IPCSocket(), c.req); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("createoutput %+v: %v, want %q", c.req, err, c.want)
		}
	}
	if outputs := query[[]ipc.Output](h, wmipc.KindOutputs); len(outputs) != 1 {
		t.Fatalf("refused requests left outputs %+v", outputs)
	}
	for name, want := range map[string]string{
		"HEADLESS-1": "only virtual outputs can be removed",
		"VIRT-9":     "no output of that name",
	} {
		if err := h.removeOutput(name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("removeoutput %s: %v, want %q", name, err, want)
		}
	}
	h.createOutput(ipc.Request{Name: "tablet", Mode: &virtualMode})
	if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: wmipc.KindCreateOutput, Name: "tablet", Mode: &virtualMode}); err == nil {
		t.Fatal("a second output took the name tablet")
	}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if _, err := c.HandoverState(nil, nil); err == nil || !strings.Contains(err.Error(), "virtual output tablet cannot move to another binary") {
			t.Errorf("an upgrade with a virtual output: %v", err)
		}
		return nil
	})
	if err := h.removeOutput("tablet"); err != nil {
		t.Fatal(err)
	}
}

func TestOutputRulesApplyToAVirtualOutput(t *testing.T) {
	t.Parallel()
	at := image.Pt(500, 40)
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Outputs.Named = []compositor.OutputRule{{Match: wm.OutputMatch{Name: "VIRT-1"}, Scale: 2 * output.Unit, Position: &at}}
	}, small)
	h.createOutput(ipc.Request{Mode: &virtualMode})
	h.createOutput(ipc.Request{Mode: &virtualMode, Scale: 1.25})
	h.createOutput(ipc.Request{Name: "VIRT-3", Mode: &virtualMode})
	outputs := query[[]ipc.Output](h, wmipc.KindOutputs)
	if got := outputNamed(t, outputs, "VIRT-1"); got.Scale != 2 || got.Rect != (wmipc.Rect{X: 500, Y: 40, Width: 100, Height: 60}) {
		t.Fatalf("the ruled output is %+v", got)
	}
	if got := outputNamed(t, outputs, "VIRT-2"); got.Scale != 1.25 || got.Rect.Width != 160 || got.Rect.Height != 96 {
		t.Fatalf("the output asked at 1.25 is %+v", got)
	}
	if got := outputNamed(t, outputs, "VIRT-3"); got.Scale != 1 {
		t.Fatalf("the output without a scale or a rule is %+v", got)
	}
}

func TestAVirtualOutputRendersOnlyWhileCaptured(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		throttled(cfg)
		cfg.Rules = nil
	}, small)
	name := h.createOutput(ipc.Request{Mode: &virtualMode})
	c := testclient.Connect(t, h.Socket())
	w := c.Window("streamed", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("movetoworkspace 2")
	h.awaitWindowOn(name)
	frames := func() int { return w.Frames }
	awaitHiddenFrame(h, c, "the window on the uncaptured virtual output")
	atTheHiddenRate(t, h, c, "the window on the uncaptured virtual output", frames)
	if n := virtualFrames(h, name); n != 0 {
		t.Fatalf("the uncaptured virtual output rendered %d frames while its window committed", n)
	}

	r := newCaptureRig(t, h, c)
	src, err := r.outputs.CreateSource(c.Outputs[1])
	if err != nil {
		t.Fatal(err)
	}
	session, view := r.session(src, 0)
	if view.size != image.Pt(virtualMode.Width, virtualMode.Height) {
		t.Fatalf("the capture of the virtual output is %v", view.size)
	}
	pix := r.first(session, view.size)
	if got := rgbAt(pix, view.size.X*4, 20, 20); got != [3]byte{0xff, 0, 0} {
		t.Fatalf("the virtual output's frame shows %v at the window's red quadrant", got)
	}
	if n := virtualFrames(h, name); n == 0 {
		t.Fatal("a captured frame came without the virtual output rendering")
	}
	atTheRefreshRate(t, h, c, "the window on the captured output", frames)

	if err := errors.Join(session.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	awaitHiddenFrame(h, c, "the window after the capture ended")
	stopped := virtualFrames(h, name)
	atTheHiddenRate(t, h, c, "the window after the capture ended", frames)
	if n := virtualFrames(h, name); n != stopped {
		t.Fatalf("the virtual output rendered %d frames after its capture ended", n-stopped)
	}
}

func TestAVirtualOutputStreamsIntoDMABUFs(t *testing.T) {
	onEachDMABUFRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(*compositor.Config) {}, small)
		name := h.createOutput(ipc.Request{Mode: &virtualMode})
		c := testclient.Connect(t, h.Socket())
		w := c.Window("streamed", quadrants)
		c.DispatchUntil("the window to map", w.Mapped)
		h.dispatch("movetoworkspace 2")
		h.awaitWindowOn(name)
		r := newCaptureRig(t, h, c)
		src, err := r.outputs.CreateSource(c.Outputs[1])
		if err != nil {
			t.Fatal(err)
		}
		session, view := r.session(src, 0)
		target := c.RenderTarget(view.size, view.modifiers)
		const frames = 5
		for i := range frames {
			f, frame := captureInto(t, c, session, target.WL, view.size)
			c.DispatchUntil("a dmabuf frame of the virtual output", func() bool { return f.ready || f.failed })
			if f.failed {
				t.Fatalf("dmabuf frame %d %+v", i, *f)
			}
			if err := errors.Join(frame.Destroy(), c.Display.Flush()); err != nil {
				t.Fatal(err)
			}
		}
		pix, stride := target.Pixels(), view.size.X*4
		if rgbAt(pix, stride, 20, 20) != [3]byte{0xff, 0, 0} || rgbAt(pix, stride, view.size.X-20, view.size.Y-20) != [3]byte{0xff, 0xff, 0} {
			t.Fatalf("dmabuf frame: %v and %v", rgbAt(pix, stride, 20, 20), rgbAt(pix, stride, view.size.X-20, view.size.Y-20))
		}
		if n := virtualFrames(h, name); n < frames {
			t.Fatalf("%d dmabuf frames from %d rendered frames of the virtual output", frames, n)
		}
	})
}

func TestANestedSessionAddsAndRemovesAVirtualOutput(t *testing.T) {
	t.Parallel()
	host := Start(t, small)
	n := startNested(t, host)
	n.eventually("the nested window's output", func(c *compositor.Compositor) bool { return len(c.Outputs()) == 1 })
	n.do(func(c *compositor.Compositor) error {
		created, err := c.CreateOutput(ipc.OutputSpec{Size: image.Pt(320, 200), RefreshMHz: 30000})
		if err != nil {
			return err
		}
		outputs := c.Outputs()
		if created.Name != "VIRT-1" || len(outputs) != 2 || outputs[1].Backend().Name() != "VIRT-1" {
			t.Fatalf("created %+v; outputs %v", created, outputs)
		}
		if x := outputs[0].LogicalSize().X; outputs[1].Logical() != image.Rect(x, 0, x+320, 200) {
			t.Errorf("created %+v; outputs %v", created, outputs)
		}
		return nil
	})
	n.do(func(c *compositor.Compositor) error { return c.RemoveOutput("VIRT-1") })
	n.eventually("the virtual output gone", func(c *compositor.Compositor) bool { return len(c.Outputs()) == 1 })
}

func TestTheConfigKeepsVirtualOutputsAcrossReloads(t *testing.T) {
	t.Parallel()
	var cfg compositor.Config
	var reported []error
	tablet := compositor.VirtualOutput{Name: "tablet", Size: image.Pt(200, 120), RefreshMHz: 30000}
	h := StartWith(t, func(c *compositor.Config) {
		c.VirtualOutputs = []compositor.VirtualOutput{tablet}
		c.Report = func(err error) { reported = append(reported, err) }
		cfg = *c
	}, small)
	outputs := query[[]ipc.Output](h, wmipc.KindOutputs)
	if len(outputs) != 2 || outputs[0].Name != "HEADLESS-1" {
		t.Fatalf("outputs at start %+v, want the monitor first", outputs)
	}
	if got := outputNamed(t, outputs, "tablet"); got.Mode != (wmipc.Mode{Width: 200, Height: 120, RefreshMHz: 30000}) || got.Rect.X != small.Size.X {
		t.Fatalf("the configured virtual output is %+v", got)
	}
	if err := h.removeOutput("tablet"); err == nil || !strings.Contains(err.Error(), "virtual_outputs keeps it") {
		t.Fatalf("removing a configured virtual output: %v", err)
	}
	h.createOutput(ipc.Request{Name: "extra", Mode: &virtualMode})
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if _, err := c.HandoverState(nil, nil); err == nil || !strings.Contains(err.Error(), "virtual output extra cannot move") {
			t.Errorf("an upgrade with a virtual output made by createoutput: %v", err)
		}
		return nil
	})
	if err := h.removeOutput("extra"); err != nil {
		t.Fatal(err)
	}

	resized := tablet
	resized.Size = image.Pt(300, 200)
	cfg.VirtualOutputs = []compositor.VirtualOutput{resized, {Name: "HEADLESS-1", Size: image.Pt(10, 10), RefreshMHz: 60000}}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.Reconfigure(cfg) })
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if len(reported) != 1 || !strings.Contains(reported[0].Error(), "virtual output HEADLESS-1 from the config: an output of that name exists") {
			t.Errorf("a configured virtual output named after a monitor reported %v", reported)
		}
		reported = nil
		return nil
	})
	outputs = query[[]ipc.Output](h, wmipc.KindOutputs)
	if got := outputNamed(t, outputs, "tablet"); len(outputs) != 2 || got.Mode.Width != 300 || got.Mode.Height != 200 {
		t.Fatalf("after the reload resized it: %+v", outputs)
	}

	cfg.VirtualOutputs = nil
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.Reconfigure(cfg) })
	if outputs := query[[]ipc.Output](h, wmipc.KindOutputs); len(outputs) != 1 {
		t.Fatalf("after the reload dropped it: %+v", outputs)
	}
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if len(reported) != 0 {
			t.Errorf("reported %v", reported)
		}
		return nil
	})
}
