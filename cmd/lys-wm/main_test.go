package main

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/config"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

func TestParseDebug(t *testing.T) {
	if d, err := parseDebug(""); err != nil || d.wire {
		t.Fatalf("empty = %+v, %v", d, err)
	}
	if d, err := parseDebug("wire"); err != nil || !d.wire {
		t.Fatalf("wire = %+v, %v", d, err)
	}
	if _, err := parseDebug("wire,nope"); err == nil {
		t.Fatal("unknown category accepted")
	}
}

func TestChooseBackend(t *testing.T) {
	cases := []struct {
		requested, display string
		want               backendKind
	}{
		{"", "wayland-1", nestedBackend},
		{"", "", drmBackend},
		{"headless", "wayland-1", headlessBackend},
		{"nested", "", nestedBackend},
		{"drm", "wayland-1", drmBackend},
	}
	for _, c := range cases {
		if got, err := chooseBackend(c.requested, c.display); err != nil || got != c.want {
			t.Errorf("chooseBackend(%q, %q) = %v, %v; want %v", c.requested, c.display, got, err, c.want)
		}
	}
	if _, err := chooseBackend("vnc", ""); err == nil {
		t.Error("an unknown backend was accepted")
	}
	if drmBackend.String() != "drm" {
		t.Errorf("drm backend is called %q", drmBackend)
	}
}

func TestChooseRenderer(t *testing.T) {
	for _, c := range []struct {
		requested string
		backend   backendKind
		want      rendererKind
	}{{"", nestedBackend, glesRenderer}, {"", headlessBackend, softRenderer}, {"", drmBackend, vulkanRenderer}, {"gles", drmBackend, glesRenderer}, {"soft", nestedBackend, softRenderer}, {"gles", headlessBackend, glesRenderer}, {"vulkan", headlessBackend, vulkanRenderer}, {"vulkan", drmBackend, vulkanRenderer}, {"vulkan", nestedBackend, vulkanRenderer}} {
		if got, err := chooseRenderer(c.requested, c.backend); err != nil || got != c.want {
			t.Errorf("chooseRenderer(%q, %v) = %v, %v; want %v", c.requested, c.backend, got, err, c.want)
		}
	}
	for _, c := range []struct {
		requested string
		backend   backendKind
	}{{"metal", nestedBackend}, {"metal", headlessBackend}, {"soft", drmBackend}} {
		if _, err := chooseRenderer(c.requested, c.backend); err == nil {
			t.Errorf("chooseRenderer(%q, %v) succeeded", c.requested, c.backend)
		}
	}
}

func TestConfigPath(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"XDG_CONFIG_HOME": "/cfg", "HOME": "/home/u"}, "/cfg/lys-wm/config.yaml"},
		{map[string]string{"XDG_CONFIG_HOME": "relative", "HOME": "/home/u"}, "/home/u/.config/lys-wm/config.yaml"},
		{map[string]string{"HOME": "/home/u"}, "/home/u/.config/lys-wm/config.yaml"},
	}
	for _, c := range cases {
		if got, err := configPath(func(k string) string { return c.env[k] }); err != nil || got != c.want {
			t.Errorf("%v: %q, %v; want %q", c.env, got, err, c.want)
		}
	}
	if _, err := configPath(func(string) string { return "" }); err == nil {
		t.Error("no HOME resolved a config path")
	}
}

func TestFontPath(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if got := fontPath("/a.ttf", env("/b.ttf")); got != "/a.ttf" {
		t.Errorf("the config font lost to %q", got)
	}
	if got := fontPath("", env("/b.ttf")); got != "/b.ttf" {
		t.Errorf("LYS_WM_FONT lost to %q", got)
	}
	if got := fontPath("", env("")); got != defaultFont {
		t.Errorf("the built-in font lost to %q", got)
	}
}

func testSource(t *testing.T, path string, explicit bool) source {
	t.Helper()
	testenv.Font(t)
	return source{path: path, explicit: explicit, runtime: filepath.Join(filepath.Dir(path), config.RuntimeFile), keymaps: testenv.XKBCompiler(t), getenv: os.Getenv, notices: io.Discard}
}

func (s source) withRuntime(path string) source {
	s.runtime, s.runtimeExplicit = path, true
	return s
}

func TestLoadingTheConfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	first, err := testSource(t, missing, false).load(compositor.DefaultAccent)
	if err != nil {
		t.Fatalf("a missing default config: %v", err)
	}
	if len(first.cfg.Binds) == 0 || len(first.env) == 0 {
		t.Errorf("the built-in config gave %d binds, env %v", len(first.cfg.Binds), first.env)
	}
	if err := first.keymaps.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := testSource(t, missing, true).load(compositor.DefaultAccent); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing --config file: %v", err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("binds:\n  - chord: SUPER + Nope\n    action: kill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := testSource(t, bad, true).load(compositor.DefaultAccent); err == nil || !strings.HasPrefix(err.Error(), bad+":2: chord: bad chord") {
		t.Errorf("an unknown key: %v", err)
	}
	layout := filepath.Join(dir, "layout.yaml")
	if err := os.WriteFile(layout, []byte("input:\n  keyboard:\n    layout: no-such-layout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := testSource(t, layout, true).load(compositor.DefaultAccent); err == nil || !strings.Contains(err.Error(), "no-such-layout") {
		t.Errorf("an unknown layout: %v", err)
	}
}

func TestTheConfigEnvChoosesTheCursor(t *testing.T) {
	process := func(name string) string {
		return map[string]string{"XCURSOR_SIZE": "32", "XCURSOR_PATH": t.TempDir()}[name]
	}
	cursor, err := cursorConfig(withEnv([][2]string{{"XCURSOR_SIZE", "48"}}, process))
	if err != nil || cursor.Size != 48 {
		t.Fatalf("cursor %+v (%v), want the size 48 the config env sets over the process's 32", cursor, err)
	}
	if cursor, err := cursorConfig(withEnv(nil, process)); err != nil || cursor.Size != 32 {
		t.Fatalf("cursor %+v (%v), want the process's size 32 without a config env", cursor, err)
	}
}

func TestSessionVars(t *testing.T) {
	env := map[string]string{"PATH": "/nix/bin:/usr/bin", "WAYLAND_DISPLAY": "wayland-1", "LYS_WM_SOCKET": "/run/t.sock"}
	got := sessionVars([][2]string{{"XDG_CURRENT_DESKTOP", "lys-wm"}}, func(k string) string { return env[k] })
	want := [][2]string{{"XDG_CURRENT_DESKTOP", "lys-wm"}, {"PATH", "/nix/bin:/usr/bin"}, {"WAYLAND_DISPLAY", "wayland-1"}, {"LYS_WM_SOCKET", "/run/t.sock"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

func TestSessionVarsLetTheConfigEnvWin(t *testing.T) {
	env := map[string]string{"PATH": "/usr/bin", "WAYLAND_DISPLAY": "wayland-1"}
	got := sessionVars([][2]string{{"PATH", "/opt/bin"}}, func(k string) string { return env[k] })
	want := [][2]string{{"PATH", "/opt/bin"}, {"WAYLAND_DISPLAY", "wayland-1"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v, want %v: the config's PATH once, the process's dropped, unset DISPLAY left out", got, want)
	}
}

func outputScale(t *testing.T, socket string) float64 {
	t.Helper()
	raw, err := ipc.Do(socket, ipc.Request{Request: wmipc.KindOutputs})
	if err != nil {
		t.Fatal(err)
	}
	var outputs []ipc.Output
	if err := json.Unmarshal(raw, &outputs); err != nil || len(outputs) != 1 {
		t.Fatalf("outputs %s: %v", raw, err)
	}
	return outputs[0].Scale
}

const noXwayland = "xwayland:\n  enabled: false\n"

func startSession(t *testing.T, config string) (string, string, <-chan error) {
	t.Helper()
	return startSessionOnBus(t, config, "")
}

func startSessionOnBus(t *testing.T, config, bus string) (string, string, <-chan error) {
	t.Helper()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", bus)
	testenv.Font(t)
	testenv.XKBCompiler(t)
	dir := testenv.SocketDir(t)
	runtime := filepath.Join(dir, "runtime")
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"WAYLAND_DISPLAY", wmipc.SocketEnv, "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE"} {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(noXwayland+config), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run([]string{"--backend", "headless", "--config", path}) }()
	getenv := func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return runtime
		}
		return ""
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		socket, err := ipc.FindSocket(getenv)
		if err == nil {
			if _, err = ipc.Do(socket, ipc.Request{Request: wmipc.KindVersion}); err == nil {
				return socket, path, done
			}
		}
		select {
		case err := <-done:
			t.Fatalf("lys-wm exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no IPC socket: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func expectCleanExit(t *testing.T, socket string, done <-chan error, how string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("lys-wm ignored %s", how)
	}
	if _, err := os.Stat(socket); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the socket outlived lys-wm: %v", err)
	}
}

func TestConfiguredSessionOverIPC(t *testing.T) {
	socket, path, done := startSession(t, "outputs:\n  - {match: HEADLESS-1, scale: 2}\nbinds:\n  - {chord: SUPER + Q, action: kill}\n")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(noXwayland+text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := outputScale(t, socket); got != 2 {
		t.Fatalf("scale %g from the config, want 2", got)
	}
	stream, err := ipc.Subscribe(socket, false)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	write("outputs:\n  - {match: HEADLESS-1, scale: 1.5}\n")
	if _, err := ipc.Do(socket, ipc.Request{Request: wmipc.KindDispatch, Action: "reload"}); err != nil {
		t.Fatal(err)
	}
	if got := outputScale(t, socket); got != 1.5 {
		t.Fatalf("scale %g after the reload, want 1.5", got)
	}
	for _, want := range []string{`{"event":"dispatch","action":"reload"}`, `{"event":"output","change":"changed"`} {
		line, err := stream.Next()
		if err != nil || !strings.HasPrefix(string(line), want) {
			t.Fatalf("event %s, %v; want %s", line, err, want)
		}
	}

	write("outputs:\n  - {match: HEADLESS-1, scale: 1.5}\nbinds:\n  - {chord: SUPER + Q, action: teleport}\n")
	if _, err := ipc.Do(socket, ipc.Request{Request: wmipc.KindDispatch, Action: "reload"}); err == nil || !strings.Contains(err.Error(), path+":6: binds[0].action") {
		t.Fatalf("a bad config reloaded: %v", err)
	}
	if got := outputScale(t, socket); got != 1.5 {
		t.Fatalf("scale %g after a rejected reload, want 1.5", got)
	}
	if got := configProblems(t, socket); !got.Rejected || len(got.Messages) != 1 || !strings.Contains(got.Messages[0], path+":6: binds[0].action") {
		t.Fatalf("a rejected reload reported %+v", got)
	}

	write("outputs:\n  - {match: HEADLESS-1, scale: 1.25}\nfrom_a_newer_lys_wm: true\n")
	if _, err := ipc.Do(socket, ipc.Request{Request: wmipc.KindDispatch, Action: "reload"}); err != nil {
		t.Fatalf("a file with one unknown key did not reload: %v", err)
	}
	if got := outputScale(t, socket); got != 1.25 {
		t.Fatalf("scale %g beside an unknown key, want 1.25", got)
	}
	if got := configProblems(t, socket); got.Rejected || len(got.Messages) != 1 || !strings.Contains(got.Messages[0], "from_a_newer_lys_wm: unknown key") {
		t.Fatalf("an unknown key reported %+v", got)
	}

	write("outputs:\n  - {match: HEADLESS-1, scale: 1.25}\n")
	if _, err := ipc.Do(socket, ipc.Request{Request: wmipc.KindDispatch, Action: "reload"}); err != nil {
		t.Fatal(err)
	}
	if got := configProblems(t, socket); got.Rejected || len(got.Messages) != 0 {
		t.Fatalf("a clean reload still reports %+v", got)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	expectCleanExit(t, socket, done, "SIGTERM")
}

func TestReloadPicksUpTheRuntimeOverridesAndRefusesABadOne(t *testing.T) {
	socket, path, done := startSession(t, "outputs:\n  - {match: HEADLESS-1, scale: 2}\n")
	override := filepath.Join(filepath.Dir(path), config.RuntimeFile)
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(override, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reload := func() error {
		_, err := ipc.Do(socket, ipc.Request{Request: wmipc.KindDispatch, Action: "reload"})
		return err
	}
	write("outputs:\n  - {match: HEADLESS-1, scale: 1.5}\n")
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	if got := outputScale(t, socket); got != 1.5 {
		t.Fatalf("scale %g after an override, want 1.5", got)
	}
	write("outputs:\n  - {match: HEADLESS-1, scale: 1.25}\n")
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	if got := outputScale(t, socket); got != 1.25 {
		t.Fatalf("scale %g after a changed override, want 1.25", got)
	}
	write("outputs:\n  - {match: HEADLESS-1, scale: 1.5}\ngaps:\n  inner: -1\n")
	if err := reload(); err == nil || !strings.Contains(err.Error(), override+":4: gaps.inner") {
		t.Fatalf("a bad override reloaded: %v", err)
	}
	if got := outputScale(t, socket); got != 1.25 {
		t.Fatalf("scale %g after a rejected override, want 1.25", got)
	}
	if err := os.Remove(override); err != nil {
		t.Fatal(err)
	}
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	if got := outputScale(t, socket); got != 2 {
		t.Fatalf("scale %g without the override, want the file's 2", got)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	expectCleanExit(t, socket, done, "SIGTERM")
}

func configProblems(t *testing.T, socket string) ipc.ConfigProblems {
	t.Helper()
	result, err := ipc.Do(socket, ipc.Request{Request: wmipc.KindConfigProblems})
	if err != nil {
		t.Fatal(err)
	}
	var p ipc.ConfigProblems
	if err := json.Unmarshal(result, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAConfigWithAnUnknownKeyStartsAndSaysSo(t *testing.T) {
	socket, _, done := startSession(t, "from_a_newer_lys_wm: true\n")
	if got := configProblems(t, socket); got.Rejected || len(got.Messages) != 1 || !strings.Contains(got.Messages[0], "from_a_newer_lys_wm: unknown key") {
		t.Fatalf("reported %+v", got)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	expectCleanExit(t, socket, done, "SIGTERM")
}

func TestTheExitActionEndsTheSession(t *testing.T) {
	socket, _, done := startSession(t, "binds:\n  - {chord: SUPER + Delete, action: exit}\n")
	if _, err := ipc.Do(socket, ipc.Request{Request: wmipc.KindDispatch, Action: "exit"}); err != nil {
		t.Fatal(err)
	}
	expectCleanExit(t, socket, done, "exit")
}

func TestCheckConfigAcceptsWhatAReloadWouldAndRefusesTheRest(t *testing.T) {
	testenv.Font(t)
	testenv.XKBCompiler(t)
	dir := t.TempDir()
	getenv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return dir
		}
		return os.Getenv(name)
	}
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte("gaps: {inner: 3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkConfigFile(good, "", getenv); err != nil {
		t.Fatalf("a good file: %v", err)
	}
	if err := checkConfigFile("", "", getenv); err != nil {
		t.Fatalf("no file falls back to the built-in config: %v", err)
	}
	for name, content := range map[string]string{
		"unknown key": "no_such_key: 1\n",
		"bad value":   "gaps: {inner: -1}\n",
		"bad layout":  "input: {keyboard: {layout: no-such-layout}}\n",
		"not yaml":    "gaps: [\n",
	} {
		bad := filepath.Join(dir, "bad.yaml")
		if err := os.WriteFile(bad, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		err := checkConfigFile(bad, "", getenv)
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "bad.yaml") && name != "bad layout" {
			t.Errorf("%s: the error names no file: %v", name, err)
		}
	}
	if err := checkConfigFile(filepath.Join(dir, "absent.yaml"), "", getenv); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a named file that is not there: %v", err)
	}
}

func TestCheckConfigWithACandidateOverrideJudgesItAsAReloadWould(t *testing.T) {
	testenv.Font(t)
	testenv.XKBCompiler(t)
	dir := t.TempDir()
	getenv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return dir
		}
		return os.Getenv(name)
	}
	main := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(main, []byte("binds:\n  - {chord: SUPER + Q, action: kill}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(dir, "candidate.yaml")
	for content, accepted := range map[string]bool{
		"gaps: {inner: 4}\n":                                    true,
		"gaps: {inner: -4}\n":                                   false,
		"input: {keyboard: {layout: no-such-layout}}\n":         false,
		"submaps: {s: [{chord: SUPER + Nope, action: kill}]}\n": false,
	} {
		if err := os.WriteFile(candidate, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		err := checkConfigFile(main, candidate, getenv)
		if (err == nil) != accepted {
			t.Errorf("%q: %v, want accepted %v", content, err, accepted)
		}
		if err != nil && !strings.Contains(err.Error(), "no-such-layout") && !strings.Contains(err.Error(), candidate+":1:") {
			t.Errorf("%q: the error does not name the candidate: %v", content, err)
		}
		_, reload := testSource(t, main, true).withRuntime(candidate).load(compositor.DefaultAccent)
		if (reload == nil) != accepted {
			t.Errorf("%q: a reload says %v, the check %v", content, reload, err)
		}
	}
	if err := checkConfigFile(main, filepath.Join(dir, "absent.yaml"), getenv); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a named override that is not there: %v", err)
	}
}

func TestAReadOnlyLinkedConfigLoadsWithItsOverrides(t *testing.T) {
	store := t.TempDir()
	dir := t.TempDir()
	target := filepath.Join(store, "config.yaml")
	if err := os.WriteFile(target, []byte("gaps:\n  inner: 3\nborders:\n  width: 5\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store, 0o700) })
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.RuntimeFile), []byte("gaps:\n  inner: 8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := testSource(t, link, false).load(compositor.DefaultAccent)
	if err != nil {
		t.Fatal(err)
	}
	if l.cfg.WM.Gaps.Inner != 8 || l.cfg.Decor.BorderWidth != 5 {
		t.Fatalf("gaps %d, borders %d; want the override's 8 and the linked file's 5", l.cfg.WM.Gaps.Inner, l.cfg.Decor.BorderWidth)
	}
	if err := l.keymaps.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveConfigNamesEachKeysLayer(t *testing.T) {
	dir := t.TempDir()
	getenv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return dir
		}
		return ""
	}
	main := filepath.Join(dir, "lys-wm", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(main), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte("gaps:\n  inner: 3\n  outer: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(dir, "lys-wm", config.RuntimeFile)
	if err := os.WriteFile(override, []byte("gaps:\n  inner: 6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := printEffectiveConfig("", "", getenv, &out); err != nil {
		t.Fatal(err)
	}
	var got effectiveConfig
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatal(err)
	}
	wantLayers := []effectiveLayer{{Source: config.SourceConfig, File: main}, {Source: config.SourceRuntime, File: override}}
	if !slices.Equal(got.Layers, wantLayers) || len(got.Keys) != 2 {
		t.Fatalf("%s", out.String())
	}
	for _, k := range got.Keys {
		if want := map[string]string{"inner": config.SourceRuntime, "outer": config.SourceConfig}[k.Path[1]]; k.Source != want {
			t.Errorf("%v comes from %s, want %s", k.Path, k.Source, want)
		}
	}
}

func TestCheckConfigPrintsNothingWhenItAcceptsTheBuiltInConfig(t *testing.T) {
	testenv.Font(t)
	testenv.XKBCompiler(t)
	dir := t.TempDir()
	getenv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return dir
		}
		return os.Getenv(name)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	err = checkConfigFile("", "", getenv)
	os.Stderr = stderr
	if cerr := w.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	printed, rerr := io.ReadAll(r)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err != nil || len(printed) > 0 {
		t.Fatalf("printed %q, %v", printed, err)
	}
	var notices strings.Builder
	s, err := newSource("", "", getenv, &notices)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.read(); err != nil || !strings.Contains(notices.String(), "no config at") {
		t.Fatalf("a start says it runs the built-in config: %q, %v", notices.String(), err)
	}
}
