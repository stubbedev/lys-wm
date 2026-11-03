package harness

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/backend/nested"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/data"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type nestedLysWM struct {
	t       *testing.T
	host    *Harness
	loop    *loop.Loop
	comp    *compositor.Compositor
	dir     string
	display string
}

func startNested(t *testing.T, host *Harness) *nestedLysWM {
	t.Helper()
	return startNestedWith(t, host, func(*compositor.Config) {})
}

func startNestedWith(t *testing.T, host *Harness, configure func(*compositor.Config)) *nestedLysWM {
	t.Helper()
	return startNestedOn(t, host, nested.Soft, configure)
}

func startNestedOn(t *testing.T, host *Harness, renderer nested.Renderer, configure func(*compositor.Config)) *nestedLysWM {
	t.Helper()
	n := &nestedLysWM{t: t, host: host, dir: testenv.SocketDir(t)}
	var err error
	if n.loop, err = loop.New(); err != nil {
		t.Fatal(err)
	}
	b, err := nested.New(n.loop, host.Socket(), nested.Options{Renderer: renderer, GPU: gpu.PathsFromEnv(os.Getenv)})
	if err != nil {
		t.Fatal(err)
	}
	display, err := server.New(n.loop, server.Options{RuntimeDir: n.dir, Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	n.display = display.Name()
	cfg := compositor.PlainConfig(testenv.Keymap(t), testenv.Font(t))
	cfg.Report = func(err error) { t.Errorf("nested lys-wm reported: %v", err) }
	configure(&cfg)
	if n.comp, err = compositor.New(n.loop, display, b, cfg); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- n.loop.Run() }()
	t.Cleanup(func() {
		if err := n.loop.Post(func() error { n.loop.Stop(); return nil }); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil && !errors.Is(err, nested.ErrClosed) {
			t.Error(err)
		}
		if err := errors.Join(n.comp.Close(), display.Close(), b.Close(), n.loop.Close()); err != nil {
			t.Error(err)
		}
	})
	return n
}

func (n *nestedLysWM) do(f func(c *compositor.Compositor) error) {
	n.t.Helper()
	result := make(chan error, 1)
	if err := n.loop.Post(func() error {
		result <- f(n.comp)
		return nil
	}); err != nil {
		n.t.Fatal(err)
	}
	if err := <-result; err != nil {
		n.t.Fatal(err)
	}
}

func (n *nestedLysWM) eventually(what string, cond func(c *compositor.Compositor) bool) {
	n.t.Helper()
	deadline := time.Now().Add(clientTimeout)
	for {
		var ok bool
		n.do(func(c *compositor.Compositor) error {
			ok = cond(c)
			return nil
		})
		if ok {
			return
		}
		if time.Now().After(deadline) {
			n.t.Fatalf("nested lys-wm: timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (n *nestedLysWM) run(binary string, args ...string) *Client {
	n.t.Helper()
	return n.host.RunOn(n.dir, n.display, binary, args...)
}

func nestedToplevels(want int) func(c *compositor.Compositor) bool {
	return func(c *compositor.Compositor) bool {
		return mappedToplevels(want)(nil, c)
	}
}

func TestNestedLysWMHidesTheHostCursor(t *testing.T) {
	t.Parallel()
	host := Start(t, small)
	startNested(t, host)
	host.Eventually("the nested window under the pointer with no host cursor", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Seat().PointerFocus() != nil && c.Seat().Cursor() == seat.HiddenCursor{}
	})
}

func TestNestedSessionWithRealClients(t *testing.T) {
	t.Setenv("GOELM_DEBUG", "demo")
	host := Start(t, desktop)
	n := startNested(t, host)
	host.Eventually("the nested lys-wm window", mappedToplevels(1))
	n.do(func(c *compositor.Compositor) error {
		return c.SetOutputScale(c.Outputs()[0].Backend(), 180)
	})

	typed := filepath.Join(t.TempDir(), "typed")
	terminal := n.run("alacritty-swrast", "-e", "sh", "-c", `read -r line; printf %s "$line" > "$0"`, typed)
	n.eventually("alacritty to map", nestedToplevels(1))
	n.eventually("alacritty to take the keyboard", func(c *compositor.Compositor) bool {
		return c.Seat().Focus() == c.Toplevels()[0].Surface()
	})
	host.tap(keyE, keyC, keyH, keyO, keyEnter)
	terminal.Wait(t, clientTimeout)
	got, err := os.ReadFile(typed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo" {
		t.Fatalf("alacritty read %q from the host keyboard, want echo", got)
	}
	n.eventually("alacritty gone", func(c *compositor.Compositor) bool { return len(c.Toplevels()) == 0 })

	hello := n.run("lys-ui-hello")
	hello.Expect(t, "demo: mapped", clientTimeout)
	n.eventually("lys-ui-hello to map", nestedToplevels(1))
	host.click(0, 900, 500, btnRight)
	hello.Expect(t, "demo: menu open", clientTimeout)
	n.eventually("the menu with the keyboard", func(c *compositor.Compositor) bool {
		popups := c.Toplevels()[0].Popups()
		return len(popups) == 1 && popups[0].Mapped() && c.Seat().Focus() == popups[0].Surface()
	})

	host.tap(keyEsc)
	n.eventually("the menu closed by escape", func(c *compositor.Compositor) bool {
		return len(c.Toplevels()[0].Popups()) == 0 && c.Seat().Focus() == c.Toplevels()[0].Surface()
	})

	n.run("wl-copy", "--foreground", "nested clipboard")
	n.eventually("the copied selection", func(c *compositor.Compositor) bool { return c.Selection(data.Clipboard) })
	if out := n.run("wl-paste", "--no-newline").Wait(t, clientTimeout); out != "nested clipboard" {
		t.Fatalf("wl-paste printed %q", out)
	}
}

func (n *nestedLysWM) state() wm.State {
	var s wm.State
	n.do(func(c *compositor.Compositor) error {
		s = c.WM()
		return nil
	})
	return s
}

func placementsOf(s wm.State) map[wm.WindowID]wm.Placement {
	out := map[wm.WindowID]wm.Placement{}
	for _, p := range s.Arrange(wm.Spacing{}).Windows {
		out[p.ID] = p
	}
	return out
}

func (h *Harness) chord(keys ...uint32) {
	for _, k := range keys {
		h.key(k, true)
	}
	for _, k := range slices.Backward(keys) {
		h.key(k, false)
	}
}

const (
	keyLeft = 105
	keyT    = 20
	keyW    = 17
	keyOne  = 2
	keyTwo  = 3
	keyCtrl = 29
)

func TestNestedTilingSession(t *testing.T) {
	t.Parallel()
	host := Start(t, desktop)
	n := startNestedWith(t, host, func(cfg *compositor.Config) {
		specs := []bindSpec{
			{chord: "SUPER + Return", action: "exec alacritty-swrast"},
			{chord: "SUPER + CTRL + Return", action: "exec alacritty-swrast --title float-me"},
			{chord: "SUPER + Left", action: "focus left"},
			{chord: "SUPER + Right", action: "focus right"},
			{chord: "SUPER + SHIFT + Right", action: "move right"},
			{chord: "SUPER + W", action: "makegroup tab"},
			{chord: "SUPER + T", action: "togglefloating"},
			{chord: "SUPER + 1", action: "workspace 1"},
			{chord: "SUPER + 2", action: "workspace 2"},
		}
		withBinds(t, specs...)(cfg)
		cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^float-me$`), Mode: wm.RuleFloat, Size: image.Pt(300, 200)}}
	})
	host.Eventually("the nested lys-wm window", mappedToplevels(1))
	mapped := func(want int) {
		t.Helper()
		n.eventually(fmt.Sprintf("%d windows", want), func(c *compositor.Compositor) bool {
			count := 0
			for _, tl := range c.Toplevels() {
				if tl.Mapped() {
					count++
				}
			}
			return count == want
		})
	}
	ids := func() []wm.WindowID {
		var out []wm.WindowID
		for _, ws := range n.state().Workspaces() {
			out = append(out, ws.Tree.Windows()...)
		}
		return out
	}

	for i := range 3 {
		host.chord(keySuper, keyEnter)
		mapped(i + 1)
	}
	p := placementsOf(n.state())
	w := ids()
	if p[w[0]].Rect.Dx() < 400 || p[w[0]].Rect.Max.X != p[w[1]].Rect.Min.X || p[w[1]].Rect.Max.X != p[w[2]].Rect.Min.X {
		t.Fatalf("three terminals do not split the output side by side: %v", p)
	}

	host.chord(keySuper, keyEnter)
	mapped(4)
	p = placementsOf(n.state())
	w = ids()
	if p[w[3]].Rect.Min.X != p[w[2]].Rect.Min.X || p[w[3]].Rect.Min.Y == p[w[2]].Rect.Min.Y {
		t.Fatalf("autotile did not stack the fourth terminal under the third: %v", p)
	}

	host.chord(keySuper, keyLeft)
	n.eventually("focus left from the stacked column", func(c *compositor.Compositor) bool {
		f, _ := c.WM().Focused()
		return f == w[1]
	})
	host.chord(keySuper, keyShift, keyRight)
	n.eventually("the second terminal moved into the stacked column", func(c *compositor.Compositor) bool {
		p := placementsOf(c.WM())
		return p[w[1]].Rect.Min.X == p[w[3]].Rect.Min.X
	})

	host.chord(keySuper, keyW)
	n.eventually("a tab group", func(c *compositor.Compositor) bool { return len(c.WM().Arrange(wm.Spacing{TabBar: 20}).TabBars) == 1 })

	host.chord(keySuper, keyT)
	n.eventually("the focused terminal floating", func(c *compositor.Compositor) bool {
		f, _ := c.WM().Focused()
		return placementsOf(c.WM())[f].Layer == wm.LayerFloating
	})

	host.chord(keySuper, keyTwo)
	n.eventually("workspace 2", func(c *compositor.Compositor) bool { return c.WM().Outputs()[0].Active == 2 })
	host.chord(keySuper, keyCtrl, keyEnter)
	mapped(5)
	n.eventually("the rule-floated terminal", func(c *compositor.Compositor) bool {
		for _, pl := range c.WM().Arrange(wm.Spacing{}).Windows {
			if pl.Layer == wm.LayerFloating && pl.Visible && pl.Rect.Size() == image.Pt(302, 202) {
				return true
			}
		}
		return false
	})
	host.chord(keySuper, keyOne)
	n.eventually("workspace 1", func(c *compositor.Compositor) bool { return c.WM().Outputs()[0].Active == 1 })
}

func TestNestedLysWMRendersWithVulkan(t *testing.T) {
	if !testenv.Vulkan(t).Device().Exports() {
		t.Skip("this Vulkan device shares no dmabufs with a host; lavapipe needs /dev/udmabuf")
	}
	host := StartVulkan(t, func(*compositor.Config) {}, desktop)
	n := startNestedOn(t, host, nested.Vulkan, func(*compositor.Config) {})
	host.Eventually("the nested lys-wm window", mappedToplevels(1))
	c := testclient.Connect(t, filepath.Join(n.dir, n.display))
	w := c.Window("nested on Vulkan", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	n.eventually("the window in the nested lys-wm", nestedToplevels(1))
	const x, y = 100, 100
	host.Eventually("the nested window through Vulkan and the host", showing(x, y, [3]byte{0xff, 0, 0}))
	green := c.Buffer(w.Size, 0, testclient.Fill(0, 0xff, 0))
	w.Stop()
	if err := errors.Join(w.Surface.Attach(green, 0, 0), w.Surface.DamageBuffer(0, 0, int32(w.Size.X), int32(w.Size.Y)), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	host.Eventually("the next frame through both buffers of the chain", showing(x, y, [3]byte{0, 0xff, 0}))
}

func TestNestedGLESNamesWhatAHostWithoutAMainDeviceLacks(t *testing.T) {
	if !testenv.Vulkan(t).Device().Exports() {
		t.Skip("this Vulkan device shares no dmabufs; lavapipe needs /dev/udmabuf")
	}
	host := StartVulkan(t, func(*compositor.Config) {}, small)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, err = nested.New(l, host.Socket(), nested.Options{Renderer: nested.GLES, GPU: gpu.PathsFromEnv(os.Getenv)})
	if err == nil || !regexp.MustCompile(`GLES renderer needs the device the host names`).MatchString(err.Error()) {
		t.Fatalf("nested GLES on a host that names no device: %v", err)
	}
}

func TestNestedGPURenderersNameAHostWithoutDMABUFs(t *testing.T) {
	host := Start(t, small)
	for _, r := range []nested.Renderer{nested.GLES, nested.Vulkan} {
		l, err := loop.New()
		if err != nil {
			t.Fatal(err)
		}
		_, err = nested.New(l, host.Socket(), nested.Options{Renderer: r, GPU: gpu.PathsFromEnv(os.Getenv)})
		if err == nil || !regexp.MustCompile(`needs the host's zwp_linux_dmabuf_v1`).MatchString(err.Error()) {
			t.Errorf("renderer %d on a host without dmabufs: %v", r, err)
		}
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	}
}

type costMeter struct {
	t    *testing.T
	busy func() time.Duration
	c    *testclient.Client
	w    *testclient.Window
}

func (m costMeter) log(name string) {
	m.t.Helper()
	const frames = 240
	m.w.Stop()
	red, blue := m.c.Buffer(m.w.Size, 0, testclient.Fill(0, 0, 0xff)), m.c.Buffer(m.w.Size, 0, testclient.Fill(0xff, 0, 0))
	var before, after unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &before); err != nil {
		m.t.Fatal(err)
	}
	start, began := m.busy(), time.Now()
	for i := range frames {
		drawn := false
		if _, err := m.w.Surface.Frame(frameDone{&drawn}); err != nil {
			m.t.Fatal(err)
		}
		next := red
		if i%2 == 1 {
			next = blue
		}
		if err := errors.Join(m.w.Surface.Attach(next, 0, 0), m.w.Surface.DamageBuffer(0, 0, int32(m.w.Size.X), int32(m.w.Size.Y)), m.w.Surface.Commit(), m.c.Display.Flush()); err != nil {
			m.t.Fatal(err)
		}
		m.c.DispatchUntil("the frame", func() bool { return drawn })
	}
	spent, elapsed := m.busy()-start, time.Since(began)
	if err := unix.Getrusage(unix.RUSAGE_SELF, &after); err != nil {
		m.t.Fatal(err)
	}
	cpu := time.Duration(after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano())
	m.t.Logf("frame cost: %s, loop busy %v a frame, %v of wall clock and %v of process CPU over %d %v frames", name, spent/frames, elapsed/frames, cpu/frames, frames, m.w.Size)
}

func nestedCost(t *testing.T, name string, host *Harness, renderer nested.Renderer) {
	t.Helper()
	n := startNestedOn(t, host, renderer, func(*compositor.Config) {})
	host.Eventually("the nested lys-wm window", mappedToplevels(1))
	c := testclient.Connect(t, filepath.Join(n.dir, n.display))
	w := c.Window("frames", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	n.eventually("the window in the nested lys-wm", nestedToplevels(1))
	busy := func() time.Duration {
		var d time.Duration
		n.do(func(*compositor.Compositor) error {
			d = n.loop.Busy()
			return nil
		})
		return d
	}
	costMeter{t, busy, c, w}.log(name)
}

func headlessCost(t *testing.T, name string, h *Harness) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	w := c.Window("frames", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	costMeter{t, h.Busy, c, w}.log(name)
}

func hostNamesItsDevice(h *Harness) bool {
	named := false
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		r, ok := b.Renderer().(interface{ MainDevice() (uint64, error) })
		if ok {
			_, err := r.MainDevice()
			named = err == nil
		}
		return nil
	})
	return named
}

func TestNestedFrameCost(t *testing.T) {
	if !testenv.Vulkan(t).Device().Exports() {
		t.Skip("this Vulkan device shares no dmabufs with a host; lavapipe needs /dev/udmabuf")
	}
	newHost := func(t *testing.T) *Harness { return StartVulkan(t, func(*compositor.Config) {}, desktop) }
	for round := range 3 {
		t.Run(fmt.Sprintf("headless-gles/%d", round), func(t *testing.T) {
			headlessCost(t, "headless gles", StartGLES(t, func(*compositor.Config) {}, desktop))
		})
		t.Run(fmt.Sprintf("headless-vulkan/%d", round), func(t *testing.T) {
			headlessCost(t, "headless vulkan", newHost(t))
		})
		t.Run(fmt.Sprintf("nested-soft/%d", round), func(t *testing.T) { nestedCost(t, "nested soft", newHost(t), nested.Soft) })
		t.Run(fmt.Sprintf("nested-vulkan/%d", round), func(t *testing.T) { nestedCost(t, "nested vulkan", newHost(t), nested.Vulkan) })
		t.Run(fmt.Sprintf("gles/%d", round), func(t *testing.T) {
			host := newHost(t)
			if !hostNamesItsDevice(host) {
				t.Skip("nested GLES allocates through GBM on the device its host names, and a host on lavapipe names none")
			}
			nestedCost(t, "nested gles", host, nested.GLES)
		})
	}
}
