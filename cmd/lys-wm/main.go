// Command lys-wm is a pure-Go tiling Wayland compositor.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	rtdebug "runtime/debug"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	gtext "github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/xcursor"
	"github.com/stubbedev/lys-lib/xdg"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/backend/nested"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/config"
	"github.com/stubbedev/lys-wm/internal/frametrace"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/handover"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/powerprofile"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/session"
	"github.com/stubbedev/lys-wm/internal/systemd"
	"github.com/stubbedev/lys-wm/internal/wlclient"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xkb"
	"github.com/stubbedev/lys-wm/internal/xwayland"
)

func main() {
	if holds(os.Args[1:], os.Getenv) {
		status, err := runHolder(os.Args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "lys-wm:", err)
			if status == 0 {
				status = 1
			}
		}
		os.Exit(status)
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lys-wm:", err)
		os.Exit(1)
	}
}

type debug struct {
	wire bool
}

func parseDebug(s string) (debug, error) {
	var d debug
	for category := range strings.SplitSeq(s, ",") {
		switch strings.TrimSpace(category) {
		case "":
		case "wire":
			d.wire = true
		default:
			return debug{}, fmt.Errorf("LYS_WM_DEBUG: unknown category %q (known: wire)", category)
		}
	}
	return d, nil
}

var headlessMode = backend.Mode{Size: image.Pt(1920, 1080), RefreshMHz: 60000, Preferred: true}

type backendKind uint8

const (
	nestedBackend backendKind = iota + 1
	headlessBackend
	drmBackend
)

var backendNames = map[string]backendKind{"nested": nestedBackend, "headless": headlessBackend, "drm": drmBackend}

func (k backendKind) String() string {
	for name, kind := range backendNames {
		if kind == k {
			return name
		}
	}
	return fmt.Sprintf("backend%d", k)
}

func chooseBackend(requested, waylandDisplay string) (backendKind, error) {
	if requested == "" {
		if waylandDisplay != "" {
			return nestedBackend, nil
		}
		return drmBackend, nil
	}
	if k, ok := backendNames[requested]; ok {
		return k, nil
	}
	return 0, fmt.Errorf("unknown backend %q (available: drm, nested, headless)", requested)
}

type rendererKind uint8

const (
	softRenderer rendererKind = iota + 1
	glesRenderer
	vulkanRenderer
)

func (k rendererKind) String() string {
	switch k {
	case softRenderer:
		return "soft"
	case glesRenderer:
		return "gles"
	case vulkanRenderer:
		return "vulkan"
	}
	return fmt.Sprintf("renderer%d", k)
}

func chooseRenderer(requested string, kind backendKind) (rendererKind, error) {
	switch requested {
	case "":
		if kind == headlessBackend {
			return softRenderer, nil
		}
		return glesRenderer, nil
	case "soft":
		if kind == drmBackend {
			return 0, errors.New("the drm backend renders with GLES or Vulkan")
		}
		return softRenderer, nil
	case "gles":
		return glesRenderer, nil
	case "vulkan":
		return vulkanRenderer, nil
	}
	return 0, fmt.Errorf("unknown renderer %q (available: gles, soft, vulkan)", requested)
}

func openHeadless(l *loop.Loop, renderer rendererKind, paths gpu.Paths) (*headless.Backend, error) {
	switch renderer {
	case glesRenderer:
		return headless.NewGLES(l, paths)
	case vulkanRenderer:
		return headless.NewVulkan(l)
	}
	return headless.New(l), nil
}

type opened struct {
	backend.Backend
	switchVT       func(vt uint32) error
	configureInput func(session.InputConfig) error
	lid            compositor.LidPolicy
	onPower        func(changed func())
	native         *native
}

func hostInput(session.InputConfig) error { return nil }

func openBackend(kind backendKind, renderer rendererKind, l *loop.Loop, opts nativeOptions, report func(error)) (opened, error) {
	if kind == drmBackend {
		opts.vulkan = renderer == vulkanRenderer
		n, err := openNative(l, os.Getenv, opts, report)
		if err != nil {
			return opened{}, err
		}
		return opened{Backend: n, switchVT: n.switchVT, configureInput: n.input.Reconfigure, lid: n.lidPolicy(), onPower: n.onPower, native: n}, nil
	}
	b, err := openOther(kind, renderer, l)
	return opened{Backend: b, configureInput: hostInput}, err
}

func openOther(kind backendKind, renderer rendererKind, l *loop.Loop) (backend.Backend, error) {
	paths := gpu.PathsFromEnv(os.Getenv)
	hint := func(err error) error {
		if err != nil && renderer != softRenderer {
			return fmt.Errorf("%w (run with --renderer soft to render in software)", err)
		}
		return err
	}
	if kind == nestedBackend {
		host, err := wlclient.SocketPath(os.Getenv)
		if err != nil {
			return nil, err
		}
		opts := nested.Options{Renderer: nested.Soft, GPU: paths}
		switch renderer {
		case glesRenderer:
			opts.Renderer = nested.GLES
		case vulkanRenderer:
			opts.Renderer = nested.Vulkan
		}
		b, err := nested.New(l, host, opts)
		return b, hint(err)
	}
	b, err := openHeadless(l, renderer, paths)
	if err != nil {
		return nil, hint(err)
	}
	if _, err := b.AddOutput(headlessMode); err != nil {
		return nil, errors.Join(err, b.Close())
	}
	return b, nil
}

var defaultFont = "DejaVuSans.ttf"

func fontPath(configured string, getenv func(string) string) string {
	if configured != "" {
		return configured
	}
	if env := getenv("LYS_WM_FONT"); env != "" {
		return env
	}
	return defaultFont
}

func loadFont(path string) (*gtext.Typeface, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the font path comes from the build, the config or the environment
	if err != nil {
		return nil, fmt.Errorf("read the tab font (set tabs.font in the config, or LYS_WM_FONT): %w", err)
	}
	f, err := gtext.LoadFont(data)
	if err != nil {
		return nil, fmt.Errorf("load the tab font %s: %w", path, err)
	}
	return f, nil
}

func configPath(getenv func(string) string) (string, error) {
	dir, ok := xdg.Env(getenv).ConfigDir("lys-wm")
	if !ok {
		return "", errors.New("neither XDG_CONFIG_HOME nor HOME is an absolute path; pass --config")
	}
	return filepath.Join(dir, "config.yaml"), nil
}

type source struct {
	path            string
	explicit        bool
	runtime         string
	runtimeExplicit bool
	keymaps         *xkb.Compiler
	getenv          func(string) string
	notices         io.Writer
}

func newSource(path, runtime string, getenv func(string) string, notices io.Writer) (source, error) {
	s := source{path: path, explicit: path != "", runtime: runtime, runtimeExplicit: runtime != "", keymaps: xkb.NewCompiler(getenv), getenv: getenv, notices: notices}
	if !s.explicit {
		var err error
		if s.path, err = configPath(getenv); err != nil {
			return source{}, err
		}
	}
	if !s.runtimeExplicit {
		s.runtime = filepath.Join(filepath.Dir(s.path), config.RuntimeFile)
	}
	return s, nil
}

func (s source) read() ([]config.Layer, error) {
	base := config.Layer{Source: config.SourceConfig, File: s.path}
	data, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		base.Data = data
	case errors.Is(err, fs.ErrNotExist) && !s.explicit:
		fmt.Fprintf(s.notices, "lys-wm: no config at %s; using the built-in one\n", s.path)
		base = config.Layer{Source: config.SourceBuiltIn, File: "built-in config", Data: config.Default}
	default:
		return nil, fmt.Errorf("read the config: %w", err)
	}
	layers := []config.Layer{base}
	data, err = os.ReadFile(s.runtime)
	switch {
	case err == nil:
		layers = append(layers, config.Layer{Source: config.SourceRuntime, File: s.runtime, Data: data})
	case !errors.Is(err, fs.ErrNotExist) || s.runtimeExplicit:
		return nil, fmt.Errorf("read the runtime overrides: %w", err)
	}
	return layers, nil
}

type loaded struct {
	cfg       compositor.Config
	keymap    *xkb.Keymap
	env       [][2]string
	autostart []string
	xwayland  bool
	zero      bool
	native    nativeOptions
	warnings  []error
}

func (s source) load(accent render.Color) (loaded, error) {
	layers, err := s.read()
	if err != nil {
		return loaded{}, err
	}
	file, err := config.ParseLayers(accent, layers...)
	if err != nil {
		return loaded{}, err
	}
	font, err := loadFont(fontPath(file.Font, s.getenv))
	if err != nil {
		return loaded{}, err
	}
	names := file.Keyboard
	if names == (xkb.Names{}) {
		names = xkb.NamesFromEnv(s.getenv)
	}
	keymap, err := s.keymaps.Compile(names)
	if err != nil {
		return loaded{}, err
	}
	cfg, err := file.Compositor(keymap, font)
	if err != nil {
		return loaded{}, errors.Join(err, keymap.Close())
	}
	return loaded{cfg: cfg, keymap: keymap, env: file.Env, autostart: file.Autostart, xwayland: file.Xwayland, zero: file.ZeroScaling, native: nativeOptions{input: file.Input, gpu: file.GPU, lidSuspend: file.LidSuspend}, warnings: file.Warnings}, nil
}

func printHandoverInfo(path, runtime string, getenv func(string) string, out io.Writer) error {
	info := preflightInfo{Format: handover.Format}
	s, err := newSource(path, runtime, getenv, os.Stderr)
	if err != nil {
		return err
	}
	l, err := s.load(compositor.DefaultAccent)
	if err != nil {
		info.Config = err.Error()
	} else {
		for _, w := range l.warnings {
			info.Warnings = append(info.Warnings, w.Error())
		}
		if err := l.keymap.Close(); err != nil {
			return err
		}
	}
	return json.NewEncoder(out).Encode(info)
}

func checkConfigFile(path, runtime string, getenv func(string) string) error {
	s, err := newSource(path, runtime, getenv, io.Discard)
	if err != nil {
		return err
	}
	l, err := s.load(compositor.DefaultAccent)
	if err != nil {
		return err
	}
	return errors.Join(errors.Join(l.warnings...), l.keymap.Close())
}

type effectiveLayer struct {
	Source string `json:"source"`
	File   string `json:"file"`
}

type effectiveConfig struct {
	Layers []effectiveLayer   `json:"layers"`
	Keys   []config.KeySource `json:"keys"`
}

func printEffectiveConfig(path, runtime string, getenv func(string) string, out io.Writer) error {
	s, err := newSource(path, runtime, getenv, os.Stderr)
	if err != nil {
		return err
	}
	layers, err := s.read()
	if err != nil {
		return err
	}
	keys, err := config.Sources(layers...)
	if err != nil {
		return err
	}
	info := effectiveConfig{Keys: keys}
	for _, l := range layers {
		info.Layers = append(info.Layers, effectiveLayer{Source: l.Source, File: l.File})
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

func sessionVars(env [][2]string, getenv func(string) string) [][2]string {
	out := slices.Clone(env)
	for _, name := range []string{"PATH", "WAYLAND_DISPLAY", "DISPLAY", ipc.SocketEnv} {
		if slices.ContainsFunc(env, func(kv [2]string) bool { return kv[0] == name }) {
			continue
		}
		if v := getenv(name); v != "" {
			out = append(out, [2]string{name, v})
		}
	}
	return out
}

type reloader struct {
	source
	loading        sync.Mutex
	accent         render.Color
	cursor         compositor.CursorConfig
	comp           *compositor.Compositor
	exit           func() error
	keymap         *xkb.Keymap
	configureInput func(session.InputConfig) error
}

const (
	gcPercent       = 400
	softMemoryLimit = 1 << 30
)

func (s *reloader) reload() func() error {
	s.loading.Lock()
	next, err := s.load(s.accent)
	s.loading.Unlock()
	return func() error { return s.apply(next, err) }
}

func (s *reloader) follow(accent render.Color) bool {
	s.loading.Lock()
	defer s.loading.Unlock()
	if s.accent == accent {
		return false
	}
	s.accent = accent
	return true
}

func (s *reloader) apply(next loaded, err error) error {
	if err != nil {
		s.comp.ShowConfigProblems(compositor.ConfigProblems{Rejected: true, Messages: errorLines(err)})
		return err
	}
	next.cfg.Cursor = s.cursor
	if err := s.comp.Reconfigure(next.cfg); err != nil {
		err = errors.Join(err, next.keymap.Close())
		s.comp.ShowConfigProblems(compositor.ConfigProblems{Rejected: true, Messages: errorLines(err)})
		return err
	}
	old := s.keymap
	s.keymap = next.keymap
	err = errors.Join(old.Close(), s.configureInput(next.native.input))
	s.comp.ShowConfigProblems(compositor.ConfigProblems{Messages: errorLines(errors.Join(errors.Join(next.warnings...), err))})
	return err
}

func errorLines(err error) []string {
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "\n")
}

func withEnv(env [][2]string, getenv func(string) string) func(string) string {
	return func(name string) string {
		if i := slices.IndexFunc(env, func(kv [2]string) bool { return kv[0] == name }); i >= 0 {
			return env[i][1]
		}
		return getenv(name)
	}
}

func cursorConfig(getenv func(string) string) (compositor.CursorConfig, error) {
	cfg := compositor.CursorConfig{Size: compositor.DefaultCursorSize}
	if v := getenv("XCURSOR_SIZE"); v != "" {
		size, err := strconv.Atoi(v)
		if err != nil || size <= 0 {
			return cfg, fmt.Errorf("XCURSOR_SIZE %q is not a positive integer", v)
		}
		cfg.Size = size
	}
	name := getenv("XCURSOR_THEME")
	if name == "" {
		name = "default"
	}
	theme, err := xcursor.Open(name, xcursor.SearchPath(getenv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "lys-wm: %v; drawing the built-in arrow\n", err)
		return cfg, nil
	}
	cfg.Theme = theme
	return cfg, nil
}

func startCPUProfile(path string) (func() error, error) {
	if path == "" {
		return func() error { return nil }, nil
	}
	f, err := os.Create(path) //nolint:gosec // the path comes from the command line
	if err != nil {
		return nil, fmt.Errorf("create the CPU profile: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		return nil, errors.Join(fmt.Errorf("start the CPU profile: %w", err), f.Close())
	}
	return func() error {
		pprof.StopCPUProfile()
		return f.Close()
	}, nil
}

func openFrameTrace(path string, kind backendKind, renderer rendererKind, b backend.Backend, report func(error)) (*frametrace.Recorder, error) {
	if path == "" {
		return nil, nil
	}
	r := b.Renderer()
	named, ok := r.(render.NamedDevice)
	if !ok {
		return nil, fmt.Errorf("frame trace: the %s renderer does not name its device", renderer)
	}
	h := frametrace.Header{Backend: kind.String(), Renderer: renderer.String(), Device: named.DeviceName(), Software: render.SoftwareDevice(named.DeviceName()), Vsync: kind == drmBackend}
	sync, _ := r.(frametrace.Sync)
	return frametrace.Create(path, h, sync, frametrace.Clock{Now: backend.Now, Signal: kdrm.SyncFileSignalTime}, report)
}

func run(args []string) (err error) {
	var incoming *adoption
	committed := false
	leave := func(release func() error) {
		if incoming != nil && !committed {
			fmt.Fprintln(os.Stderr, "lys-wm:", err)
			os.Exit(1)
		}
		err = errors.Join(err, release())
	}
	flags := flag.NewFlagSet("lys-wm", flag.ContinueOnError)
	path := flags.String("config", "", "config file (default: $XDG_CONFIG_HOME/lys-wm/config.yaml)")
	runtime := flags.String("runtime", "", "runtime overrides merged over the config, which must exist when given (default: "+config.RuntimeFile+" beside the config file, when it exists)")
	withSystemd := flags.Bool("systemd", false, "export the session environment to D-Bus activation and the systemd user manager, and run "+systemd.Target+" while lys-wm runs")
	rendererName := flags.String("renderer", "", "gles, soft or vulkan (default: gles for the drm and nested backends, soft for headless; drm takes gles or vulkan)")
	requested := flags.String("backend", "", "output and input backend: drm, nested or headless (default: nested when WAYLAND_DISPLAY is set, else drm)")
	framesPath := flags.String("frame-trace", "", "record every frame's timing to this file as JSON lines; see docs/frame-pacing.md")
	cpuProfile := flags.String("cpu-profile", "", "write a Go CPU profile to this file while lys-wm runs")
	flags.Bool("holder", false, "run the compositor under a small process that holds the session, so that lys-wmctl upgrade can replace the compositor; see docs/guide/upgrade.md")
	handoverFD := flags.Int("handover-fd", -1, "take over a running session from the lys-wm that started this process and passed this descriptor")
	handoverInfo := flags.Bool("handover-info", false, "print what this binary needs to take over a running session, the config's problems included, as JSON, and exit")
	swipes := flags.Bool("headless-swipes", false, "feed the headless backend's fake touchpad from stdin: lines of begin, swipe <dx> <dy> and end, for lys-wm-pace")
	checkConfig := flags.Bool("check-config", false, "load the config and its runtime overrides as a reload would, and exit without starting: nothing printed and status 0 when it would be accepted")
	effective := flags.Bool("effective-config", false, "print every key the config and its runtime overrides set, with the layer, file and line its value comes from, as JSON, and exit")
	defaultConfig := flags.Bool("default-config", false, "print the built-in config and exit")
	configSchema := flags.Bool("config-schema", false, "print the config's JSON schema and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	switch {
	case *defaultConfig:
		_, err := os.Stdout.Write(config.Default)
		return err
	case *configSchema:
		_, err := os.Stdout.Write(config.Schema())
		return err
	case *checkConfig:
		return checkConfigFile(*path, *runtime, os.Getenv)
	case *effective:
		return printEffectiveConfig(*path, *runtime, os.Getenv, os.Stdout)
	}
	if *handoverInfo {
		return printHandoverInfo(*path, *runtime, os.Getenv, os.Stdout)
	}
	stopProfile, err := startCPUProfile(*cpuProfile)
	if err != nil {
		return err
	}
	defer leave(func() error { return stopProfile() })
	kind, err := chooseBackend(*requested, os.Getenv("WAYLAND_DISPLAY"))
	if err != nil {
		return err
	}
	renderer, err := chooseRenderer(*rendererName, kind)
	if err != nil {
		return err
	}
	dbg, err := parseDebug(os.Getenv("LYS_WM_DEBUG"))
	if err != nil {
		return err
	}
	var trace io.Writer
	if dbg.wire {
		trace = os.Stderr
	}
	src, err := newSource(*path, *runtime, os.Getenv, os.Stderr)
	if err != nil {
		return err
	}
	s := &reloader{source: src, accent: compositor.DefaultAccent}
	if *handoverFD >= 0 {
		unix.CloseOnExec(*handoverFD)
		if incoming, err = receiveHandover(*handoverFD); err != nil {
			return err
		}
	}
	first, err := s.load(s.accent)
	if err != nil {
		return err
	}
	s.keymap = first.keymap
	defer leave(func() error { return s.keymap.Close() })
	if s.cursor, err = cursorConfig(withEnv(first.env, os.Getenv)); err != nil {
		return err
	}
	cfg := first.cfg
	cfg.Cursor = s.cursor
	cfg.Hooks.Reload = s.reload
	for _, kv := range first.env {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s from the config: %w", kv[0], err)
		}
	}
	l, err := loop.New()
	if err != nil {
		return err
	}
	defer leave(func() error { return l.Close() })
	s.exit = func() error {
		l.Stop()
		return nil
	}
	hold, err := dialHolder(l)
	if err != nil {
		return err
	}
	cfg.Hooks.Exit = s.exit
	if _, err := l.AddSignals(func(sig os.Signal) error {
		if sig != syscall.SIGHUP {
			l.Stop()
			return nil
		}
		s.comp.ReloadConfig()
		return nil
	}, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP); err != nil {
		return err
	}
	first.native.holder = hold
	b, err := openBackend(kind, renderer, l, first.native, cfg.Report)
	if err != nil {
		return err
	}
	cfg.Hooks.SwitchVT, cfg.Hooks.Lid, s.configureInput = b.switchVT, b.lid, b.configureInput
	defer leave(func() error { return b.Close() })
	frames, err := openFrameTrace(*framesPath, kind, renderer, b, cfg.Report)
	if err != nil {
		return err
	}
	defer leave(func() error { return frames.Close() })
	var display *server.Display
	if incoming != nil {
		display, err = server.Adopt(l, server.Options{Trace: trace, Report: cfg.Report}, incoming.state.Compositor.Display, incoming.fds)
	} else {
		display, err = server.New(l, server.Options{RuntimeDir: os.Getenv("XDG_RUNTIME_DIR"), Trace: trace, Report: cfg.Report})
	}
	if err != nil {
		return err
	}
	if hold != nil {
		display.KeepHandoverJournal()
	}
	defer leave(func() error { return display.Close() })
	if s.comp, err = compositor.New(l, display, b, cfg); err != nil {
		return err
	}
	defer leave(func() error { return s.comp.Close() })
	s.comp.TraceFrames(frames)
	s.comp.ShowConfigProblems(compositor.ConfigProblems{Messages: errorLines(errors.Join(first.warnings...))})
	if *swipes {
		if err := readSwipes(l, b.Backend, os.Stdin); err != nil {
			return err
		}
	}
	socket, err := ipc.SocketPath(os.Getenv("XDG_RUNTIME_DIR"), display.Name())
	if err != nil {
		return err
	}
	var srv *ipc.Server
	if incoming != nil {
		var ln *os.File
		if ln, err = incoming.file(incoming.state.IPC, "ipc"); err == nil {
			srv, err = ipc.Adopt(l, ln, s.comp, cfg.Report)
		}
	} else {
		srv, err = ipc.Listen(l, socket, s.comp, cfg.Report)
	}
	if err != nil {
		return err
	}
	defer leave(func() error { return srv.Close() })
	s.comp.OnIPCEvents(srv)
	s.comp.OnStateChange(srv.Changed)
	if b.onPower != nil {
		b.onPower(s.comp.PowerChanged)
	}
	if b.native != nil {
		profiles, err := powerprofile.Watch(l, session.SystemBus(os.Getenv), func(saver bool) error {
			s.comp.SetPowerSaver(saver)
			return nil
		}, cfg.Report)
		if err != nil {
			return err
		}
		defer leave(profiles.Close)
	}
	exports := [][2]string{{"WAYLAND_DISPLAY", display.Name()}, {ipc.SocketEnv, socket}}
	var xwRunning *xwayland.Server
	if first.xwayland {
		var xw *xwayland.Server
		opts := xwayland.Options{Binary: xwayland.Path(os.Getenv), Dir: "/tmp", Env: os.Environ(), Log: os.Stderr, Report: cfg.Report, Listener: s.comp, TransferTimeout: 30 * time.Second}
		if incoming != nil && incoming.state.X != nil {
			xw, err = xwayland.Adopt(l, display, opts, *incoming.state.X, incoming.fds)
		} else {
			xw, err = xwayland.Listen(l, display, opts)
		}
		if err != nil {
			return fmt.Errorf("%w (or turn it off with xwayland.enabled: false)", err)
		}
		defer leave(func() error { return xw.Close() })
		xwRunning = xw
		s.comp.WatchSelection(xw.SelectionChanged)
		s.comp.SetDropTarget(xw)
		s.comp.WatchWorkArea(xw.WorkAreaChanged)
		if first.zero {
			if err := s.comp.ScaleXwayland(xw.IsClient); err != nil {
				return err
			}
		}
		exports = append(exports, [2]string{"DISPLAY", xw.Display()})
	}
	for _, kv := range exports {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return fmt.Errorf("export %s: %w", kv[0], err)
		}
	}
	if *withSystemd {
		sess, startErr := systemd.Start(os.Getenv("DBUS_SESSION_BUS_ADDRESS"), sessionVars(first.env, os.Getenv))
		if startErr != nil {
			cfg.Report(fmt.Errorf("start %s: %w", systemd.Target, startErr))
		} else {
			defer leave(func() error { return sess.Close() })
		}
	}
	accents := followAccent(func(accent render.Color) {
		if !s.follow(accent) {
			return
		}
		if err := l.Post(func() error {
			s.comp.ReloadConfig()
			return nil
		}); err != nil && !errors.Is(err, loop.ErrClosed) {
			cfg.Report(fmt.Errorf("follow the desktop accent: %w", err))
		}
	})
	defer leave(func() error {
		accents.Close()
		return nil
	})
	u := &upgrader{loop: l, comp: s.comp, display: display, srv: srv, xw: xwRunning, args: slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--handover-fd" || a == strconv.Itoa(*handoverFD) }), path: s.path, runtime: *runtime, holder: hold, opened: b}
	srv.OnUpgrade(u.start)
	if incoming == nil {
		for _, command := range first.autostart {
			if err := s.comp.Dispatch(wm.ExecAction{Command: command}); err != nil {
				cfg.Report(fmt.Errorf("autostart: %w", err))
			}
		}
	} else {
		a := &adopted{a: incoming, loop: l, comp: s.comp, srv: srv, holder: hold, binary: os.Args[0], committed: &committed, stop: func(cause error) {
			fmt.Fprintln(os.Stderr, "lys-wm:", cause)
			os.Exit(1)
		}}
		if err := a.begin(); err != nil {
			return fmt.Errorf("take over the session: %w", err)
		}
	}
	listening := display.Name()
	if first.xwayland {
		listening += " and X display " + os.Getenv("DISPLAY")
	}
	fmt.Fprintf(os.Stderr, "lys-wm: listening on %s with the %s backend\n", listening, kind)
	rtdebug.SetGCPercent(gcPercent)
	rtdebug.SetMemoryLimit(softMemoryLimit)
	unpin := gpu.PinThread()
	how, release, err := raiseThreadPriority(session.SystemBus(os.Getenv))
	if how != "" {
		fmt.Fprintf(os.Stderr, "lys-wm: the compositor thread runs at %s\n", how)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lys-wm: %v\n", err)
	}
	ran := l.Run()
	release()
	if errors.Is(ran, nested.ErrClosed) {
		ran = nil
	}
	return errors.Join(ran, unpin())
}
