// Package harness boots the compositor on the headless backend for
// integration tests and drives real clients against it.
package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/xwayland"
)

type Harness struct {
	t          testing.TB
	RuntimeDir string
	loop       *loop.Loop
	clock      *loop.Manual
	frozen     bool
	skew       time.Duration
	goal       time.Duration
	advances   []advance
	display    *server.Display
	backend    *headless.Backend
	compositor *compositor.Compositor
	ipc        *ipc.Server
	ipcPath    string
	trace      *syncBuffer
	xwayland   *xwayland.Server
	xlog       *syncBuffer
	done       chan error
	loopG      atomic.Uint64
	clients    []*Client
}

type advance struct {
	to      time.Duration
	reached chan struct{}
}

type setup struct {
	open      func(*loop.Loop) (*headless.Backend, error)
	configure func(*compositor.Config)
	traced    bool
	xwayland  func(error)
}

func Start(t *testing.T, modes ...backend.Mode) *Harness {
	t.Helper()
	return StartWith(t, func(*compositor.Config) {}, modes...)
}

func softBackend(l *loop.Loop) (*headless.Backend, error) {
	return headless.New(l), nil
}

func StartWith(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	return start(t, setup{open: softBackend, configure: configure, traced: true}, modes...)
}

func withCursorPlanes(open func(*loop.Loop) (*headless.Backend, error), size image.Point) func(*loop.Loop) (*headless.Backend, error) {
	return func(l *loop.Loop) (*headless.Backend, error) {
		b, err := open(l)
		if err != nil {
			return nil, err
		}
		return b, b.SetCursorPlanes(size)
	}
}

func StartWithCursorPlanes(t *testing.T, size image.Point, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	return start(t, setup{open: withCursorPlanes(softBackend, size), configure: configure, traced: true}, modes...)
}

func StartInherited(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	open := func(l *loop.Loop) (*headless.Backend, error) {
		b := headless.New(l)
		b.InheritFrames()
		return b, nil
	}
	return start(t, setup{open: open, configure: configure, traced: true}, modes...)
}

func StartUntraced(t *testing.T, modes ...backend.Mode) *Harness {
	t.Helper()
	return StartUntracedWith(t, func(*compositor.Config) {}, modes...)
}

func StartUntracedWith(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	return start(t, setup{open: softBackend, configure: configure}, modes...)
}

func glesBackend(l *loop.Loop) (*headless.Backend, error) {
	return headless.NewGLES(l, gpu.PathsFromEnv(os.Getenv))
}

func StartGLES(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	glesTurn.hold(t)
	testenv.GPU(t)
	return start(t, setup{open: glesBackend, configure: configure, traced: true}, modes...)
}

type rendererTurn struct {
	sync.Mutex
	turn   chan struct{}
	holder testing.TB
}

var (
	glesTurn   = rendererTurn{turn: make(chan struct{}, 1)}
	vulkanTurn = rendererTurn{turn: make(chan struct{}, 1)}
)

func (r *rendererTurn) hold(t testing.TB) {
	t.Helper()
	r.Lock()
	held := r.holder == t
	r.Unlock()
	if held {
		return
	}
	r.turn <- struct{}{}
	r.Lock()
	r.holder = t
	r.Unlock()
	t.Cleanup(func() {
		r.Lock()
		r.holder = nil
		r.Unlock()
		<-r.turn
	})
}

func vulkanBackend(l *loop.Loop) (*headless.Backend, error) {
	return headless.NewVulkan(l)
}

func StartVulkan(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	vulkanTurn.hold(t)
	testenv.Vulkan(t)
	return start(t, setup{open: vulkanBackend, configure: configure, traced: true}, modes...)
}

func StartGLESWithCursorPlanes(t *testing.T, size image.Point, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	glesTurn.hold(t)
	testenv.GPU(t)
	return start(t, setup{open: withCursorPlanes(glesBackend, size), configure: configure, traced: true}, modes...)
}

func StartVulkanWithCursorPlanes(t *testing.T, size image.Point, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	vulkanTurn.hold(t)
	testenv.Vulkan(t)
	return start(t, setup{open: withCursorPlanes(vulkanBackend, size), configure: configure, traced: true}, modes...)
}

const XTransferTimeout = 2 * time.Second

const (
	firstHarnessDisplay = 16
	harnessDisplays     = 16
)

var xwaylandStarts atomic.Int32

func StartXwayland(t *testing.T, report func(error), modes ...backend.Mode) *Harness {
	t.Helper()
	return StartXwaylandWith(t, func(*compositor.Config) {}, report, modes...)
}

func StartXwaylandWith(t *testing.T, configure func(*compositor.Config), report func(error), modes ...backend.Mode) *Harness {
	t.Helper()
	testenv.Require(t, xwayland.Path(os.Getenv))
	return start(t, setup{open: softBackend, configure: configure, traced: true, xwayland: report}, modes...)
}

func xwaylandDisplay() int {
	return firstHarnessDisplay + int(xwaylandStarts.Add(1))%harnessDisplays
}

func start(t testing.TB, s setup, modes ...backend.Mode) *Harness {
	t.Helper()
	dir := testenv.SocketDir(t)
	h := &Harness{t: t, RuntimeDir: dir, trace: &syncBuffer{}, xlog: &syncBuffer{}, done: make(chan error, 1)}
	t.Cleanup(h.stop)
	var err error
	if h.loop, h.clock, err = loop.NewManual(loop.Monotonic()); err != nil {
		t.Fatalf("harness: %v", err)
	}
	h.clock.Follow(h.wall)
	h.clock.WhenDry(h.dry)
	h.loop.BeforeWait(func() error {
		h.tick()
		return nil
	})
	opts := server.Options{RuntimeDir: dir, Report: func(err error) { t.Errorf("server reported: %v", err) }}
	if s.traced {
		opts.Trace = h.trace
	}
	if h.display, err = server.New(h.loop, opts); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if h.backend, err = s.open(h.loop); err != nil {
		t.Fatalf("harness: %v", err)
	}
	for _, m := range modes {
		if _, err := h.backend.AddOutput(m); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	cfg := compositor.PlainConfig(testenv.Keymap(t), testenv.Font(t))
	cfg.Report = func(err error) { t.Errorf("compositor reported: %v", err) }
	cfg.Decor.BorderWidth = 0
	cfg.Clock = h.loop.Now
	s.configure(&cfg)
	if h.compositor, err = compositor.New(h.loop, h.display, h.backend, cfg); err != nil {
		t.Fatalf("harness: %v", err)
	}
	socket, err := ipc.SocketPath(dir, h.display.Name())
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	h.ipcPath = socket
	if h.ipc, err = ipc.Listen(h.loop, socket, h.compositor, cfg.Report); err != nil {
		t.Fatalf("harness: %v", err)
	}
	h.compositor.OnIPCEvents(h.ipc)
	h.auditIPC()
	if s.xwayland != nil {
		opts := xwayland.Options{Binary: xwayland.Path(os.Getenv), Dir: dir, FirstDisplay: xwaylandDisplay(), Env: os.Environ(), Log: h.xlog, Report: s.xwayland, Listener: h.compositor, TransferTimeout: XTransferTimeout}
		if h.xwayland, err = xwayland.Listen(h.loop, h.display, opts); err != nil {
			t.Fatalf("harness: %v", err)
		}
		h.compositor.WatchSelection(h.xwayland.SelectionChanged)
		h.compositor.SetDropTarget(h.xwayland)
		h.compositor.WatchWorkArea(h.xwayland.WorkAreaChanged)
		if err := h.compositor.ScaleXwayland(h.xwayland.IsClient); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	go func() {
		h.loopG.Store(goroutineID())
		unpin := gpu.PinThread()
		err := h.loop.Run()
		h.done <- errors.Join(err, unpin())
	}()
	return h
}

func (h *Harness) stop() {
	for _, c := range h.clients {
		c.kill()
	}
	if h.loop != nil && h.compositor != nil {
		if err := h.loop.Post(func() error {
			h.loop.Stop()
			return nil
		}); err == nil {
			if err := <-h.done; err != nil {
				h.t.Errorf("harness: compositor loop: %v", err)
			}
		}
	}
	var errs []error
	if h.xwayland != nil {
		errs = append(errs, h.xwayland.Close())
	}
	if h.ipc != nil {
		errs = append(errs, h.ipc.Close())
	}
	if h.compositor != nil {
		errs = append(errs, h.compositor.Close())
	}
	if h.display != nil {
		errs = append(errs, h.display.Close())
	}
	if h.backend != nil {
		errs = append(errs, testenv.CloseDriver(h.backend.Close))
	}
	if h.loop != nil {
		errs = append(errs, h.loop.Close())
	}
	if err := errors.Join(errs...); err != nil {
		h.t.Errorf("harness: teardown: %v", err)
	}
	if h.t.Failed() {
		h.t.Logf("compositor wire trace:\n%s", h.trace.String())
		if log := h.xlog.String(); log != "" {
			h.t.Logf("Xwayland output:\n%s", log)
		}
	}
}

func (h *Harness) wall() time.Duration {
	return loop.Monotonic() + h.skew
}

func (h *Harness) tick() {
	now := h.clock.Now()
	if h.goal <= now {
		h.release()
		return
	}
	if next, ok := h.clock.NextTimer(); ok && next <= h.goal {
		h.clock.Set(max(next, now))
		return
	}
	h.clock.Set(h.goal)
	h.skew = max(h.skew, h.goal-loop.Monotonic())
	h.release()
}

func (h *Harness) dry() {
	for {
		h.tick()
		next, ok := h.clock.NextTimer()
		if ok && next <= h.clock.Now() {
			return
		}
		if !h.frozen {
			if ok {
				h.clock.WaitAtMost(next - h.clock.Now())
			}
			return
		}
		frame, ok := h.clock.NextFrame()
		if !ok {
			return
		}
		h.goal = max(h.goal, frame)
	}
}

func (h *Harness) Freeze() {
	h.t.Helper()
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		h.frozen = true
		h.clock.Follow(nil)
		return nil
	})
}

func (h *Harness) release() {
	now := h.clock.Now()
	h.advances = slices.DeleteFunc(h.advances, func(a advance) bool {
		if a.to > now {
			return false
		}
		close(a.reached)
		return true
	})
}

func (h *Harness) Advance(d time.Duration) {
	h.t.Helper()
	reached := make(chan struct{})
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		to := h.clock.Now() + d
		h.goal = max(h.goal, to)
		h.advances = append(h.advances, advance{to: to, reached: reached})
		return nil
	})
	hang := time.NewTimer(testenv.HangLimit)
	defer hang.Stop()
	select {
	case <-reached:
	case <-hang.C:
		h.t.Fatalf("harness: the compositor loop did not run %v of its clock within %v", d, testenv.HangLimit)
	case err := <-h.done:
		h.done <- err
		h.t.Fatalf("harness: compositor loop ended before running %v of its clock: %v", d, err)
	}
}

func (h *Harness) Now() time.Duration {
	h.t.Helper()
	var now time.Duration
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		now = h.loop.Now()
		return nil
	})
	return now
}

func (h *Harness) Ended() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		h.done <- nil
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("harness: the compositor loop is still running")
		return nil
	}
}

func (h *Harness) Do(f func(b *headless.Backend, c *compositor.Compositor) error) {
	h.t.Helper()
	if goroutineID() == h.loopG.Load() {
		panic("harness: Do called on the compositor loop, from inside a Do or Eventually function; it would wait on the loop it runs on forever")
	}
	started, result := make(chan struct{}), make(chan error, 1)
	if err := h.loop.Post(func() error {
		close(started)
		result <- f(h.backend, h.compositor)
		return nil
	}); err != nil {
		h.t.Fatalf("harness: post to the compositor loop: %v", err)
	}
	hang := time.NewTimer(testenv.HangLimit)
	defer hang.Stop()
	if err := h.awaitStart(started, hang.C); err != nil {
		h.t.Fatal(err)
	}
	if err := <-result; err != nil {
		h.t.Fatal(err)
	}
}

func (h *Harness) awaitStart(started <-chan struct{}, hang <-chan time.Time) error {
	select {
	case <-started:
		return nil
	default:
	}
	select {
	case <-started:
		return nil
	case <-hang:
		return fmt.Errorf("harness: the compositor loop did not start a Do within %v", testenv.HangLimit)
	case err := <-h.done:
		h.done <- err
		return fmt.Errorf("harness: compositor loop ended: %w", err)
	}
}

func (h *Harness) Eventually(what string, cond func(b *headless.Backend, c *compositor.Compositor) bool) {
	h.t.Helper()
	deadline := time.Now().Add(testenv.HangLimit)
	for {
		var ok bool
		h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
			ok = cond(b, c)
			return nil
		})
		if ok {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("harness: timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *Harness) Rendered(i int) *soft.Buffer {
	h.t.Helper()
	var o *headless.Output
	var before uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		outputs := b.Outputs()
		if i >= len(outputs) {
			return fmt.Errorf("harness: no output %d of %d", i, len(outputs))
		}
		o, before = outputs[i], outputs[i].Frames()
		return o.ScheduleFrame()
	})
	h.Eventually(fmt.Sprintf("a new frame on output %d", i), func(*headless.Backend, *compositor.Compositor) bool {
		return o.Frames() > before
	})
	return h.Snapshot(i)
}

func (h *Harness) Snapshot(i int) *soft.Buffer {
	h.t.Helper()
	var snap *soft.Buffer
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		outputs := b.Outputs()
		if i >= len(outputs) {
			return fmt.Errorf("harness: no output %d of %d", i, len(outputs))
		}
		var err error
		snap, err = outputs[i].Snapshot()
		return err
	})
	return snap
}

func (h *Harness) Socket() string {
	return filepath.Join(h.RuntimeDir, h.display.Name())
}

func (h *Harness) Wakeups() int {
	h.t.Helper()
	var n int
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		n = h.loop.Wakeups()
		return nil
	})
	return n
}

func (h *Harness) Busy() time.Duration {
	h.t.Helper()
	var d time.Duration
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		d = h.loop.Busy()
		return nil
	})
	return d
}

func (h *Harness) IPCSocket() string {
	return h.ipcPath
}

func (h *Harness) Trace() string {
	return h.trace.String()
}

type Client struct {
	cmd    *exec.Cmd
	output syncBuffer
	exited chan error
}

func (h *Harness) Run(binary string, args ...string) *Client {
	h.t.Helper()
	return h.RunOn(h.RuntimeDir, h.display.Name(), binary, args...)
}

func (h *Harness) xSocket() string {
	return filepath.Join(h.RuntimeDir, ".X11-unix", "X"+strings.TrimPrefix(h.xwayland.Display(), ":"))
}

func (h *Harness) RunX(binary string, args ...string) *Client {
	h.t.Helper()
	return h.run(h.RuntimeDir, h.display.Name(), []string{"DISPLAY=" + h.xSocket()}, binary, args...)
}

func (h *Harness) RunOn(runtimeDir, display, binary string, args ...string) *Client {
	h.t.Helper()
	return h.run(runtimeDir, display, nil, binary, args...)
}

func (h *Harness) run(runtimeDir, display string, env []string, binary string, args ...string) *Client {
	h.t.Helper()
	path := testenv.Require(h.t, binary)
	cmd := exec.CommandContext(context.Background(), path, args...)
	socket, err := ipc.SocketPath(runtimeDir, display)
	if err != nil {
		h.t.Fatalf("harness: %v", err)
	}
	cmd.Env = append(append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeDir, "WAYLAND_DISPLAY="+display, wmipc.SocketEnv+"="+socket), env...)
	c := &Client{cmd: cmd, exited: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = &c.output, &c.output
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("harness: start %s: %v", binary, err)
	}
	go func() { c.exited <- cmd.Wait() }()
	h.clients = append(h.clients, c)
	return c
}

func (c *Client) Output() string {
	return c.output.String()
}

func (c *Client) Wait(t *testing.T, timeout time.Duration) string {
	t.Helper()
	select {
	case err := <-c.exited:
		c.exited <- err
		if err != nil {
			t.Fatalf("harness: %s failed: %v\n%s", filepath.Base(c.cmd.Path), err, c.output.String())
		}
	case <-time.After(timeout):
		t.Fatalf("harness: %s still running after %v\n%s", filepath.Base(c.cmd.Path), timeout, c.output.String())
	}
	return c.output.String()
}

func (c *Client) Expect(t *testing.T, text string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out := c.output.String()
		if i := strings.Index(out, text); i >= 0 {
			return out[i:]
		}
		select {
		case err := <-c.exited:
			c.exited <- err
			t.Fatalf("harness: %s exited (%v) before printing %q:\n%s", filepath.Base(c.cmd.Path), err, text, out)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("harness: %s did not print %q within %v:\n%s", filepath.Base(c.cmd.Path), text, timeout, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *Client) kill() {
	select {
	case err := <-c.exited:
		c.exited <- err
	default:
		_ = c.cmd.Process.Kill()
		c.exited <- <-c.exited
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (h *Harness) auditIPC() {
	said := false
	h.compositor.OnStateChange(func() {
		said = true
		h.ipc.Changed()
	})
	last := h.compositor.Snapshot()
	h.loop.BeforeWait(func() error {
		next := h.compositor.Snapshot()
		if events := ipc.Diff(last, next); len(events) > 0 && !said {
			h.t.Errorf("harness: the compositor changed what IPC watchers see without saying so: %#v", events)
		}
		last, said = next, false
		return nil
	})
}

func goroutineID() uint64 {
	var buf [32]byte
	n := runtime.Stack(buf[:], false)
	var id uint64
	for _, b := range bytes.TrimPrefix(buf[:n], []byte("goroutine ")) {
		if b < '0' || b > '9' {
			break
		}
		id = id*10 + uint64(b-'0')
	}
	return id
}
