package harness

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwsc "github.com/stubbedev/lys-wm/internal/protocol/client/wlrscreencopy"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const (
	invertShader = `uniform sampler2D tex;
varying vec2 v_texcoord;
void main() { gl_FragColor = vec4(1.0 - texture2D(tex, v_texcoord).rgb, 1.0); }
`
	blackShader  = `void main() { gl_FragColor = vec4(0.0, 0.0, 0.0, 1.0); }`
	brokenShader = `void main() {
    gl_FragColor = vec4(1.0;
}
`
	pulseShader = `uniform float time;
void main() { gl_FragColor = vec4(fract(time), 0.0, 0.0, 1.0); }
`
)

func writeShader(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "screen.frag")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func shaded(path string) func(*compositor.Config) {
	return func(cfg *compositor.Config) {
		cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{Shader: path}}
	}
}

func outputShader(h *Harness) (path, problem string) {
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		o := comp.Snapshot().Outputs[0]
		path, problem = o.Shader, o.ShaderError
		return nil
	})
	return path, problem
}

func TestAScreenShaderShadesTheOutputOnEveryRenderer(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, testAScreenShaderShadesTheOutput)
}

func testAScreenShaderShadesTheOutput(t *testing.T, start gpuStarter) {
	h := start(t, shaded(writeShader(t, invertShader)), small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("shaded", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window red inverted to cyan", showing(50, 50, [3]byte{0, 0xff, 0xff}))
	if err := errors.Join(w.Surface.Attach(c.Buffer(image.Pt(160, 100), 1, testclient.Fill(0, 0xff, 0)), 0, 0), w.Surface.DamageBuffer(0, 0, 160, 100), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the new frame green inverted to magenta", showing(50, 50, [3]byte{0xff, 0, 0xff}))
}

func TestTheExampleShadersRunOnEveryRenderer(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, testTheExampleShadersRun)
}

func testTheExampleShadersRun(t *testing.T, start gpuStarter) {
	path, err := filepath.Abs("../../docs/shaders/grayscale.frag")
	if err != nil {
		t.Fatal(err)
	}
	h := start(t, shaded(path), small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("grey", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("pure red as its luminance", func(b *headless.Backend, _ *compositor.Compositor) bool {
		px := snapshot(b.Outputs()[0])
		i := 50*px.Stride + 50*4
		g, ok := px.Pix[i+1], px.Pix[i] == px.Pix[i+1] && px.Pix[i+1] == px.Pix[i+2]
		return ok && g >= 0x34 && g <= 0x38
	})
	if path, problem := outputShader(h); problem != "" || !strings.HasSuffix(path, "grayscale.frag") {
		t.Errorf("IPC says shader %q, problem %q", path, problem)
	}
}

func TestABrokenShaderLeavesTheOutputUnshadedAndSaysWhy(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reports []string
	path := writeShader(t, brokenShader)
	h := StartWith(t, func(cfg *compositor.Config) {
		shaded(path)(cfg)
		cfg.Report = func(err error) {
			mu.Lock()
			defer mu.Unlock()
			reports = append(reports, err.Error())
		}
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("plain", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window unshaded", showing(50, 50, [3]byte{0xff, 0, 0}))
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 || !strings.Contains(reports[0], path) || !strings.Contains(reports[0], "line 2, column 28") {
		t.Errorf("reports %q, want one naming the file and line 2", reports)
	}
	if got, problem := outputShader(h); got != path || !strings.Contains(problem, "line 2") {
		t.Errorf("IPC says shader %q, problem %q", got, problem)
	}
}

func TestAMissingShaderLeavesTheOutputUnshaded(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.frag")
	var reports int
	h := StartWith(t, func(cfg *compositor.Config) {
		shaded(missing)(cfg)
		cfg.Report = func(error) { reports++ }
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("plain", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window unshaded", showing(50, 50, [3]byte{0xff, 0, 0}))
	if _, problem := outputShader(h); !strings.Contains(problem, "nope.frag") {
		t.Errorf("IPC says problem %q", problem)
	}
}

func TestAReloadChangesTheShader(t *testing.T) {
	t.Parallel()
	var base compositor.Config
	path := writeShader(t, invertShader)
	h := StartWith(t, func(cfg *compositor.Config) {
		base = *cfg
		shaded(path)(cfg)
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("reloaded", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the first shader", showing(50, 50, [3]byte{0, 0xff, 0xff}))
	reload := func(configure func(*compositor.Config)) {
		next := base
		configure(&next)
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(next) })
	}
	if err := os.WriteFile(path, []byte(blackShader), 0o600); err != nil {
		t.Fatal(err)
	}
	reload(shaded(path))
	h.Eventually("the edited file", showing(50, 50, [3]byte{0, 0, 0}))
	var reports []string
	if err := os.WriteFile(path, []byte(brokenShader), 0o600); err != nil {
		t.Fatal(err)
	}
	reload(func(cfg *compositor.Config) {
		shaded(path)(cfg)
		cfg.Report = func(err error) { reports = append(reports, err.Error()) }
	})
	h.Eventually("a broken edit showing the window plainly", showing(50, 50, [3]byte{0xff, 0, 0}))
	if len(reports) != 1 {
		t.Errorf("reports %q", reports)
	}
	reload(func(cfg *compositor.Config) { cfg.Report = base.Report })
	h.Eventually("no shader", showing(50, 50, [3]byte{0xff, 0, 0}))
	if got, _ := outputShader(h); got != "" {
		t.Errorf("IPC still names shader %q", got)
	}
}

func TestTheLockScreenAndScreencopySeeNoShader(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, testTheLockScreenAndScreencopySeeNoShader)
}

func testTheLockScreenAndScreencopySeeNoShader(t *testing.T, start gpuStarter) {
	h := start(t, shaded(writeShader(t, blackShader)), small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("hidden by the shader", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("a black output", showing(50, 50, [3]byte{0, 0, 0}))
	settle(t, h)

	mgr, err := cwsc.NewZwlrScreencopyManagerV1(c.Display, 3)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	f, _, pix := screenshot(t, c, mgr, image.Rectangle{}, false)
	c.DispatchUntil("the capture", func() bool { return f.ready || f.failed })
	if got := rgbAt(pix, int(f.stride), 50, 50); f.failed || got != [3]byte{0xff, 0, 0} {
		t.Errorf("screencopy saw %v, failed %v; want the window itself", got, f.failed)
	}

	l := lockSession(t, h)
	l.seen("locked")
	l.cover(image.Pt(160, 100), 0, 0xff, 0)
	h.Eventually("the lock screen unshaded", showing(50, 50, [3]byte{0, 0xff, 0}))
	if err := errors.Join(l.lock.UnlockAndDestroy(), l.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the shader back after the lock", showing(50, 50, [3]byte{0, 0, 0}))
}

func TestAShaderDoesNotDrawWhenNothingChanges(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, testAShaderDoesNotDrawWhenNothingChanges)
}

func testAShaderDoesNotDrawWhenNothingChanges(t *testing.T, start gpuStarter) {
	h := start(t, shaded(writeShader(t, invertShader)), small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("idle", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the shaded window", showing(50, 50, [3]byte{0, 0xff, 0xff}))
	quiesce(t, h, c, w)
	before := presentCount(h)
	h.Advance(150 * time.Millisecond)
	if n := presentCount(h); n != before {
		t.Errorf("%d frames presented over a still scene", n-before)
	}
}

func TestAShaderReadingTimeKeepsDrawing(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, testAShaderReadingTimeKeepsDrawing)
}

func testAShaderReadingTimeKeepsDrawing(t *testing.T, start gpuStarter) {
	now := time.Hour
	h := start(t, func(cfg *compositor.Config) {
		shaded(writeShader(t, pulseShader))(cfg)
		cfg.Clock = func() time.Duration { return now }
	}, small)
	set := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	set(time.Hour + 250*time.Millisecond)
	h.Eventually("a quarter of the pulse", func(b *headless.Backend, _ *compositor.Compositor) bool {
		px := snapshot(b.Outputs()[0])
		return px.Pix[50*px.Stride+50*4+2] >= 0x3e && px.Pix[50*px.Stride+50*4+2] <= 0x42
	})
	set(time.Hour + 750*time.Millisecond)
	h.Eventually("three quarters of the pulse, with nothing else changing", func(b *headless.Backend, _ *compositor.Compositor) bool {
		px := snapshot(b.Outputs()[0])
		return px.Pix[50*px.Stride+50*4+2] >= 0xbd && px.Pix[50*px.Stride+50*4+2] <= 0xc1
	})
}

func TestScanoutStopsOnlyWhileAShaderIsOn(t *testing.T) {
	onEachDMABUFRenderer(t, testScanoutStopsOnlyWhileAShaderIsOn)
}

func testScanoutStopsOnlyWhileAShaderIsOn(t *testing.T, start gpuStarter) {
	var base compositor.Config
	s := fullscreenScanout(t, start, func(cfg *compositor.Config) { base = *cfg })
	b := s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0xff, 0, 0))
	s.show(t, "a dmabuf without a shader", b)
	next := base
	shaded(writeShader(t, invertShader))(&next)
	s.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(next) })
	before := scanouts(s.h)
	s.h.Eventually("the shaded frame", showing(50, 50, [3]byte{0xff, 0xff, 0}))
	other := s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0, 0xff, 0))
	if err := errors.Join(s.w.Surface.Attach(other.WL, 0, 0), s.w.Surface.DamageBuffer(0, 0, 160, 100), s.w.Surface.Commit(), s.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	s.h.Eventually("the next frame shaded", showing(50, 50, [3]byte{0xff, 0, 0xff}))
	if n := scanouts(s.h); n != before {
		t.Errorf("%d frames scanned out under a shader", n-before)
	}
	s.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(base) })
	s.h.Eventually("scanout back without the shader", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Scanouts() > before
	})
}

func TestACursorPlaneIsNotUsedUnderAShader(t *testing.T) {
	t.Parallel()
	h := StartWithCursorPlanes(t, image.Pt(64, 64), shaded(writeShader(t, invertShader)), small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("under", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the shaded window", showing(50, 50, [3]byte{0, 0xff, 0xff}))
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.MoveTo(b.Outputs()[0], 0.5, 0.5), p.Frame())
	})
	settle(t, h)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if b.Outputs()[0].Cursor().Visible() {
			t.Error("the cursor plane is on under a shader, which would leave the cursor unshaded")
		}
		return nil
	})
}
