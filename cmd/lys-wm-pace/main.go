// Command lys-wm-pace boots lys-wm with its frame trace on, drives every
// animation through a real client and the IPC socket, and reports how smoothly
// each one reached the screen.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/frametrace"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const (
	startTimeout = 15 * time.Second
	stopTimeout  = 5 * time.Second
	logTailBytes = 4096
	compositorID = "lys-wm"
)

var (
	backends  = []string{"headless", "drm"}
	renderers = []string{"gles", "vulkan", "soft"}
)

type options struct {
	backend       string
	renderer      string
	compositor    string
	jsonPath      string
	cpuProfile    string
	only          []string
	rounds        int
	gate          bool
	writeBaseline string
	allowSoftware bool
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var o options
	flags := flag.NewFlagSet("lys-wm-pace", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&o.backend, "backend", "headless", "headless, on a simulated 60 Hz clock, or drm, which takes the display and needs a text console: "+strings.Join(backends, ", "))
	flags.StringVar(&o.renderer, "renderer", "vulkan", strings.Join(renderers, ", "))
	flags.StringVar(&o.compositor, "compositor", "", "the lys-wm binary (default: next to this one, else on PATH)")
	flags.StringVar(&o.jsonPath, "json", "", "write the report with every frame to this file as JSON")
	flags.StringVar(&o.cpuProfile, "cpu-profile", "", "write a Go CPU profile of lys-wm to this file")
	flags.IntVar(&o.rounds, "rounds", 10, "how many times to play each animation")
	flags.BoolVar(&o.gate, "gate", false, "fail when a step's per-frame counts exceed the checked-in baseline")
	flags.StringVar(&o.writeBaseline, "write-baseline", "", "write limits for this renderer and class of device, derived from this run, into the baseline file at this path")
	flags.BoolVar(&o.allowSoftware, "allow-software", false, "accept a software rasteriser, for relative numbers only")
	only := flags.String("scenarios", "", "comma-separated scenarios to run (default: all): "+strings.Join(scenarioNames(), ", "))
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if !slices.Contains(backends, o.backend) {
		return options{}, fmt.Errorf("unknown backend %q (available: %s)", o.backend, strings.Join(backends, ", "))
	}
	if !slices.Contains(renderers, o.renderer) {
		return options{}, fmt.Errorf("unknown renderer %q (available: %s)", o.renderer, strings.Join(renderers, ", "))
	}
	if o.rounds < 1 {
		return options{}, fmt.Errorf("-rounds %d: needs at least one round", o.rounds)
	}
	if (o.gate || o.writeBaseline != "") && o.rounds < gateRounds {
		return options{}, fmt.Errorf("-rounds %d: a limit needs at least %d rounds, for growth to repeat", o.rounds, gateRounds)
	}
	if *only != "" {
		o.only = strings.Split(*only, ",")
		for _, name := range o.only {
			if !slices.Contains(scenarioNames(), name) {
				return options{}, fmt.Errorf("unknown scenario %q (available: %s)", name, strings.Join(scenarioNames(), ", "))
			}
		}
	}
	return o, nil
}

func refuseLiveSession(getenv func(string) string) error {
	for _, name := range []string{"WAYLAND_DISPLAY", "DISPLAY"} {
		if v := getenv(name); v != "" {
			return fmt.Errorf("-backend drm would take the display from the session at %s=%s: run it from a text console with no compositor running", name, v)
		}
	}
	if t := getenv("XDG_SESSION_TYPE"); t == "wayland" || t == "x11" {
		return fmt.Errorf("-backend drm would take the display from this %s session: run it from a text console with no compositor running", t)
	}
	return nil
}

func compositorPath(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), compositorID)
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}
	path, err := exec.LookPath(compositorID)
	if err != nil {
		return "", fmt.Errorf("find lys-wm (pass -compositor): %w", err)
	}
	return path, nil
}

func softwareRefusal(o options, h frametrace.Header) error {
	if !h.Software || o.allowSoftware {
		return nil
	}
	return fmt.Errorf("no real GPU: lys-wm -backend %s -renderer %s is rendering on %q, a software rasteriser, whose numbers say nothing about hardware. "+
		"Check that a render node in /dev/dri is present and that this user may open it, and that VK_DRIVER_FILES, __EGL_VENDOR_LIBRARY_DIRS and LIBGL_ALWAYS_SOFTWARE do not point at a software driver; "+
		"pass -allow-software for relative numbers only", h.Backend, h.Renderer, h.Device)
}

type compositor struct {
	cmd     *exec.Cmd
	exited  chan error
	logPath string
	swipes  io.Writer
	socket  string
	display string
	trace   string
	scanout bool
}

func tail(path string) string {
	f, err := os.Open(path) //nolint:gosec // our own log, in our own temp dir
	if err != nil {
		return fmt.Sprintf("(no log: %v)", err)
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > logTailBytes {
		if _, err := f.Seek(-logTailBytes, io.SeekEnd); err != nil {
			return fmt.Sprintf("(log unreadable: %v)", err)
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Sprintf("(log unreadable: %v)", err)
	}
	return string(data)
}

func childEnv(run string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"WAYLAND_DISPLAY", "DISPLAY", "XDG_RUNTIME_DIR", "XDG_STATE_HOME", "XDG_CACHE_HOME"}, name)
	})
	return append(env, "XDG_RUNTIME_DIR="+run, "XDG_STATE_HOME="+filepath.Join(run, "state"), "XDG_CACHE_HOME="+filepath.Join(run, "cache"))
}

func startCompositor(o options, binary, run string) (*compositor, error) {
	config := filepath.Join(run, "config.yaml")
	if err := os.WriteFile(config, []byte(paceConfig), 0o600); err != nil {
		return nil, fmt.Errorf("write the config: %w", err)
	}
	c := &compositor{logPath: filepath.Join(run, "lys-wm.log"), trace: filepath.Join(run, "trace.jsonl"), exited: make(chan error, 1)}
	logFile, err := os.Create(c.logPath)
	if err != nil {
		return nil, fmt.Errorf("create the compositor log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	args := []string{"-backend", o.backend, "-renderer", o.renderer, "-config", config, "-frame-trace", c.trace}
	if o.backend == "headless" {
		args = append(args, "-headless-swipes")
		if !o.allowSoftware && o.renderer != "soft" {
			args, c.scanout = append(args, "-headless-scanout"), true
		}
	}
	if o.cpuProfile != "" {
		args = append(args, "-cpu-profile", o.cpuProfile)
	}
	c.cmd = exec.Command(binary, args...) //nolint:gosec // the binary is what the user asked to measure
	c.cmd.Env = childEnv(run)
	c.cmd.Stdout, c.cmd.Stderr = logFile, logFile
	if o.backend == "headless" {
		if c.swipes, err = c.cmd.StdinPipe(); err != nil {
			return nil, fmt.Errorf("make the fake touchpad's pipe: %w", err)
		}
	}
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := c.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}
	go func() { c.exited <- c.cmd.Wait() }()
	deadline := time.After(startTimeout)
	for {
		matches, err := filepath.Glob(filepath.Join(run, "wayland-*"))
		if err != nil {
			return c, fmt.Errorf("list the compositor's socket: %w", err)
		}
		if i := slices.IndexFunc(matches, func(m string) bool { return !strings.HasSuffix(m, ".lock") }); i >= 0 {
			c.display = filepath.Base(matches[i])
			if c.socket, err = ipc.SocketPath(run, c.display); err != nil {
				return c, err
			}
			if accepting(matches[i]) && accepting(c.socket) {
				return c, nil
			}
		}
		select {
		case err := <-c.exited:
			c.exited <- err
			return c, fmt.Errorf("lys-wm -backend %s -renderer %s did not start (%w). If it found no GPU, check that /dev/dri holds a render node this user may open and that a driver for it is installed. Its log:\n%s", o.backend, o.renderer, err, tail(c.logPath))
		case <-deadline:
			return c, fmt.Errorf("lys-wm -backend %s -renderer %s accepted no connection on its sockets in %v. Its log:\n%s", o.backend, o.renderer, startTimeout, tail(c.logPath))
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func accepting(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, startTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (c *compositor) stop(ask bool) error {
	if ask && c.socket != "" {
		_, _ = ipc.Do(c.socket, ipc.Request{Request: ipc.KindDispatch, Action: "exit"})
	} else {
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case err := <-c.exited:
		c.exited <- err
		if err != nil {
			return fmt.Errorf("lys-wm ended with %w. Its log:\n%s", err, tail(c.logPath))
		}
		return nil
	case <-time.After(stopTimeout):
	}
	if err := c.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill lys-wm: %w", err)
	}
	err := <-c.exited
	c.exited <- err
	return fmt.Errorf("lys-wm did not exit within %v and was killed (%w). Its log:\n%s", stopTimeout, err, tail(c.logPath))
}

func rssKiB(pid int) int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		panic(fatal{fmt.Errorf("read the compositor's status: %w", err)})
	}
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			fields := strings.Fields(rest)
			kib, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				panic(fatal{fmt.Errorf("read the compositor's resident size from %q: %w", line, err)})
			}
			return kib
		}
	}
	panic(fatal{errors.New("the compositor's status has no VmRSS")})
}

func readHeader(path string) (frametrace.Header, error) {
	f, err := os.Open(path) //nolint:gosec // our own trace, in our own temp dir
	if err != nil {
		return frametrace.Header{}, fmt.Errorf("open the frame trace: %w", err)
	}
	defer func() { _ = f.Close() }()
	var h frametrace.Header
	if err := json.NewDecoder(f).Decode(&h); err != nil {
		return frametrace.Header{}, fmt.Errorf("read the frame trace header: %w", err)
	}
	return h, nil
}

func readTrace(path string) (frametrace.Header, []frametrace.Frame, error) {
	f, err := os.Open(path) //nolint:gosec // our own trace, in our own temp dir
	if err != nil {
		return frametrace.Header{}, nil, fmt.Errorf("open the frame trace: %w", err)
	}
	defer func() { _ = f.Close() }()
	return frametrace.Read(f)
}

type fatal struct{ err error }

type tb struct {
	testing.TB
	cleanups []func()
}

func (*tb) Helper() {}

func (*tb) Fatal(args ...any) {
	panic(fatal{errors.New(fmt.Sprint(args...))})
}

func (*tb) Fatalf(format string, args ...any) {
	panic(fatal{fmt.Errorf(format, args...)})
}

func (t *tb) Cleanup(f func()) {
	t.cleanups = append(t.cleanups, f)
}

func (t *tb) cleanup() {
	for _, f := range slices.Backward(t.cleanups) {
		f()
	}
}

func drive(o options, c *compositor, run string, out io.Writer) (marks []mark, growth []rssGrowth, err error) {
	t := &tb{}
	defer t.cleanup()
	defer func() {
		if p := recover(); p != nil {
			f, ok := p.(fatal)
			if !ok {
				panic(p)
			}
			err = f.err
		}
	}()
	s := &session{client: testclient.Connect(t, filepath.Join(run, c.display)), ipc: c.socket, runtime: filepath.Join(run, "runtime.yaml"), now: backend.Now, swipes: c.swipes, rss: func() int64 { return rssKiB(c.cmd.Process.Pid) }}
	for i := range scenarios {
		sc := &scenarios[i]
		if len(o.only) > 0 && !slices.Contains(o.only, sc.name) {
			continue
		}
		if sc.scansOut && !c.scanout {
			fmt.Fprintf(out, "not measured: %s needs planes to scan out on, which -backend headless fakes only on a real GPU, without -allow-software\n", sc.name)
			continue
		}
		if sc.fakeInput && c.swipes == nil {
			fmt.Fprintf(out, "not measured: %s needs fingers on a touchpad that -backend %s cannot fake; measure it with -backend headless\n", sc.name, o.backend)
			continue
		}
		s.run(sc, o.rounds)
	}
	return s.marks, s.growth, nil
}

func measure(o options, stdout io.Writer, getenv func(string) string) error {
	if o.backend == "drm" {
		if err := refuseLiveSession(getenv); err != nil {
			return err
		}
	}
	binary, err := compositorPath(o.compositor)
	if err != nil {
		return err
	}
	run, err := os.MkdirTemp("", "lys-wm-pace-")
	if err != nil {
		return fmt.Errorf("make a runtime dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(run) }()
	c, err := startCompositor(o, binary, run)
	if err != nil {
		if c != nil {
			_ = c.stop(false)
		}
		return err
	}
	h, err := readHeader(c.trace)
	if err == nil {
		err = softwareRefusal(o, h)
	}
	if err != nil {
		_ = c.stop(true)
		return err
	}
	marks, growth, driveErr := drive(o, c, run, stdout)
	if driveErr != nil {
		driveErr = fmt.Errorf("drive the animations: %w. The compositor's log:\n%s", driveErr, tail(c.logPath))
	}
	if err := errors.Join(driveErr, c.stop(true)); err != nil {
		return err
	}
	h, frames, err := readTrace(c.trace)
	if err != nil {
		return err
	}
	r := buildReport(h, o.rounds, marks, growth, frames)
	r.write(stdout)
	if o.jsonPath != "" {
		if err := r.save(o.jsonPath); err != nil {
			return err
		}
	}
	if o.writeBaseline != "" {
		if err := updateBaseline(o.writeBaseline, &r); err != nil {
			return err
		}
	}
	if !o.gate {
		return nil
	}
	return checkBaseline(checkedInBaseline, &r)
}

func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		err = measure(o, os.Stdout, os.Getenv)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-wm-pace:", err)
		os.Exit(1)
	}
}
