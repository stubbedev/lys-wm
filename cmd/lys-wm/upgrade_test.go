package main

import (
	"encoding/json"
	"errors"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/ipc"
	cel "github.com/stubbedev/lys-wm/internal/protocol/client/extforeigntoplevellist"
	csl "github.com/stubbedev/lys-wm/internal/protocol/client/extsessionlock"
	csc "github.com/stubbedev/lys-wm/internal/protocol/client/securitycontext"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

func buildLysWM(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "lys-wm")
	cmd := exec.Command("go", "build", "-o", out, ".")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build lys-wm: %v\n%s", err, b)
	}
	return out
}

type running struct {
	t           *testing.T
	cmd         *exec.Cmd
	runtime     string
	socket      string
	waylandPath string
	logs        *os.File
	exited      chan error
}

func startProcess(t *testing.T, binary, config string, extra ...string) *running {
	t.Helper()
	return startBare(t, binary, config, append([]string{"--holder"}, extra...)...)
}

func startBare(t *testing.T, binary, config string, extra ...string) *running {
	t.Helper()
	testenv.Font(t)
	testenv.XKBCompiler(t)
	dir := testenv.SocketDir(t)
	runtime := filepath.Join(dir, "runtime")
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(noXwayland+config), 0o600); err != nil {
		t.Fatal(err)
	}
	logs, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, append([]string{"--backend", "headless", "--config", path}, extra...)...)
	cmd.Stderr, cmd.Stdout = logs, logs
	env := os.Environ()
	cmd.Env = append(slicesWithout(env, "WAYLAND_DISPLAY", ipc.SocketEnv, "XDG_RUNTIME_DIR"), "XDG_RUNTIME_DIR="+runtime)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &running{t: t, cmd: cmd, runtime: runtime, logs: logs, exited: make(chan error, 1)}
	go func() { s.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if t.Failed() {
			t.Log(s.log())
		}
		_ = logs.Close()
	})
	getenv := func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return runtime
		}
		return ""
	}
	for deadline := time.Now().Add(20 * time.Second); ; {
		socket, err := ipc.FindSocket(getenv)
		if err == nil {
			if _, err = ipc.Do(socket, ipc.Request{Request: ipc.KindVersion}); err == nil {
				s.socket = socket
				s.waylandPath = s.waylandSocket()
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("lys-wm did not start: %v\n%s", err, s.log())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func slicesWithout(env []string, names ...string) []string {
	var out []string
outer:
	for _, kv := range env {
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}

func (s *running) log() string {
	b, err := os.ReadFile(s.logs.Name())
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func (s *running) waylandSocket() string {
	matches, err := filepath.Glob(filepath.Join(s.runtime, "wayland-*"))
	if err != nil {
		s.t.Fatal(err)
	}
	for _, m := range matches {
		if !strings.HasSuffix(m, ".lock") {
			return m
		}
	}
	s.t.Fatalf("no Wayland socket in %s", s.runtime)
	return ""
}

func (s *running) windows() []ipc.Window {
	s.t.Helper()
	raw, err := ipc.Do(s.socket, ipc.Request{Request: ipc.KindWindows})
	if err != nil {
		s.t.Fatalf("query the windows: %v\n%s", err, s.log())
	}
	var out []ipc.Window
	if err := json.Unmarshal(raw, &out); err != nil {
		s.t.Fatal(err)
	}
	return out
}

func stable(ws []ipc.Window) []ipc.Window {
	out := make([]ipc.Window, len(ws))
	for i, w := range ws {
		w.Icon = nil
		out[i] = w
	}
	return out
}

func TestUpgradeKeepsClientsWindowsAndLayout(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	c := testclient.Connect(t, s.waylandSocket())
	a := c.Window("first", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the first window to map", a.Mapped)
	b := c.Window("second", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the second window to map", b.Mapped)
	if _, err := ipc.Do(s.socket, ipc.Request{Request: ipc.KindDispatch, Action: "workspace 3"}); err != nil {
		t.Fatal(err)
	}
	third := c.Window("third", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the third window to map", third.Mapped)
	before := stable(s.windows())
	if len(before) != 3 {
		t.Fatalf("%d windows before the upgrade: %+v", len(before), before)
	}

	next := buildCopy(t, binary)
	raw, err := ipc.Do(s.socket, ipc.Request{Request: ipc.KindUpgrade, Binary: next})
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, s.log())
	}
	var result ipc.UpgradeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Binary != next || result.PID == s.cmd.Process.Pid || result.Windows != 3 || result.Clients != 1 {
		t.Fatalf("upgrade result %+v", result)
	}
	target, err := os.Readlink(filepathProcExe(result.PID))
	if err != nil {
		t.Fatal(err)
	}
	if target != next {
		t.Fatalf("the new compositor runs %s, want %s", target, next)
	}
	after := stable(s.windows())
	if len(after) != len(before) {
		t.Fatalf("windows after the upgrade: %+v; before: %+v\n%s", after, before, s.log())
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Workspace != after[i].Workspace || before[i].Rect != after[i].Rect || before[i].Title != after[i].Title || before[i].Focused != after[i].Focused {
			t.Errorf("window %d changed: %+v, was %+v", i, after[i], before[i])
		}
	}

	if err := third.Redraw(); err != nil {
		t.Fatal(err)
	}
	frames := third.Frames
	c.DispatchUntil("a frame from the new compositor", func() bool { return third.Frames > frames })
}

func buildCopy(t *testing.T, binary string) string {
	t.Helper()
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(t.TempDir(), "lys-wm-next")
	if err := os.WriteFile(next, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return next
}

func filepathProcExe(pid int) string {
	return "/proc/" + strconv.Itoa(pid) + "/exe"
}

func (s *running) upgrade(binary string) (ipc.UpgradeResult, error) {
	raw, err := ipc.Do(s.socket, ipc.Request{Request: ipc.KindUpgrade, Binary: binary})
	if err != nil {
		return ipc.UpgradeResult{}, err
	}
	var result ipc.UpgradeResult
	return result, json.Unmarshal(raw, &result)
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-lys-wm")
	if err := os.WriteFile(path, []byte("#!"+shellPath(t)+"\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellPath(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func TestAFailedUpgradeLeavesTheSessionRunningAndSaysWhy(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	c := testclient.Connect(t, s.waylandSocket())
	w := c.Window("kept", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	before := stable(s.windows())

	info := `case "$1" in --handover-info) echo '{"format":1}'; exit 0;; esac` + "\n"
	cases := map[string]struct{ binary, want string }{
		"no such file":        {filepath.Join(t.TempDir(), "absent"), "does not answer"},
		"not a lys-wm":        {script(t, "echo hello\n"), "not a handover description"},
		"another format":      {script(t, `echo '{"format":999}'`+"\n"), "format 999"},
		"rejects the config":  {script(t, `echo '{"format":1,"config_error":"c.yaml:1: nope"}'`+"\n"), "c.yaml:1: nope"},
		"exits at once":       {script(t, info+"echo it broke >&2\nexit 3\n"), "it broke"},
		"crashes mid restore": {script(t, info+"echo reading >&2\ncat <&4 >/dev/null &\nsleep 0.1\nkill -SEGV $$\n"), "did not take over"},
	}
	for name, tc := range cases {
		_, err := s.upgrade(tc.binary)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
		after := stable(s.windows())
		if len(after) != len(before) || after[0].ID != before[0].ID || after[0].Rect != before[0].Rect {
			t.Fatalf("%s: windows %+v, were %+v\n%s", name, after, before, s.log())
		}
		frames := w.Frames
		if err := w.Redraw(); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil(name+": a frame from the old compositor", func() bool { return w.Frames > frames })
	}

	next := buildCopy(t, binary)
	if _, err := s.upgrade(next); err != nil {
		t.Fatalf("an upgrade after the failures: %v\n%s", err, s.log())
	}
}

func TestUpgradeTwiceThenTheSessionEndsCleanly(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	c := testclient.Connect(t, s.waylandSocket())
	w := c.Window("kept", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	before := stable(s.windows())
	var last ipc.UpgradeResult
	for i := range 2 {
		next := buildCopy(t, binary)
		result, err := s.upgrade(next)
		if err != nil {
			t.Fatalf("upgrade %d: %v\n%s", i, err, s.log())
		}
		if result.PID == last.PID {
			t.Fatalf("upgrade %d stayed on pid %d", i, result.PID)
		}
		last = result
		after := stable(s.windows())
		if len(after) != 1 || after[0].ID != before[0].ID || after[0].Rect != before[0].Rect {
			t.Fatalf("upgrade %d: windows %+v, were %+v\n%s", i, after, before, s.log())
		}
		frames := w.Frames
		if err := w.Redraw(); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("a frame", func() bool { return w.Frames > frames })
	}
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s.exited:
		if err != nil {
			t.Fatalf("the session ended with %v\n%s", err, s.log())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the session did not end\n%s", s.log())
	}
	if _, err := os.Stat(s.waylandPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the Wayland socket outlived the session: %v", err)
	}
}

type listWatcher struct {
	seen   map[*cel.ExtForeignToplevelHandleV1]string
	closed int
}

func (e *listWatcher) Toplevel(_ *cel.ExtForeignToplevelListV1, h *cel.ExtForeignToplevelHandleV1) (cel.ExtForeignToplevelHandleV1Handler, error) {
	e.seen[h] = ""
	return e, nil
}
func (e *listWatcher) Finished(*cel.ExtForeignToplevelListV1) error { return nil }
func (e *listWatcher) Closed(*cel.ExtForeignToplevelHandleV1) error {
	e.closed++
	return nil
}
func (e *listWatcher) Done(*cel.ExtForeignToplevelHandleV1) error { return nil }
func (e *listWatcher) Title(h *cel.ExtForeignToplevelHandleV1, title string) error {
	e.seen[h] = title
	return nil
}
func (e *listWatcher) AppID(*cel.ExtForeignToplevelHandleV1, string) error      { return nil }
func (e *listWatcher) Identifier(*cel.ExtForeignToplevelHandleV1, string) error { return nil }

func (e *listWatcher) titles() map[string]int {
	out := map[string]int{}
	for _, t := range e.seen {
		out[t]++
	}
	return out
}

func TestUpgradeKeepsEveryRoleAClientHolds(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	c := testclient.Connect(t, s.waylandSocket())
	seat, in := c.Seat()
	clipboard := c.Clipboard(seat)
	source := c.DataSource(map[string]string{"text/plain": "copied"})
	w := c.Window("main", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	bar := c.Layer(testclient.LayerSpec{Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight, Size: image.Pt(0, 30), ExclusiveZone: 30}, testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the bar to map", bar.Mapped)
	menu := c.Popup(w, testclient.Placement{Size: image.Pt(40, 30), Anchor: image.Rect(10, 10, 11, 11), Edge: cxdg.XdgPositionerAnchorBottomRight, Gravity: cxdg.XdgPositionerGravityBottomRight}, nil, testclient.Fill(0x80, 0x80, 0x80))
	c.DispatchUntil("the menu to map", menu.Mapped)
	child := c.Surface()
	sub, err := c.Subcompositor.GetSubsurface(child, w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(sub.SetPosition(5, 5), sub.SetDesync(), child.Attach(c.Buffer(image.Pt(20, 20), 0, testclient.Fill(0, 0, 0xff)), 0, 0), child.DamageBuffer(0, 0, 20, 20), child.Commit(), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	region, err := c.Compositor.CreateRegion()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(region.Add(0, 0, 10, 10), w.Surface.SetOpaqueRegion(region), region.Destroy(), w.Surface.Commit()); err != nil {
		t.Fatal(err)
	}
	watcher := testclient.Connect(t, s.waylandSocket())
	list := &listWatcher{seen: map[*cel.ExtForeignToplevelHandleV1]string{}}
	manager, err := cel.NewExtForeignToplevelListV1(watcher.Display, 1, list)
	if err != nil {
		t.Fatal(err)
	}
	watcher.Bind(manager)
	watcher.DispatchUntil("the watcher to see the window", func() bool { return list.titles()["main"] == 1 })
	before := stable(s.windows())
	stream, err := ipc.Subscribe(s.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	result, err := s.upgrade(buildCopy(t, binary))
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, s.log())
	}
	if result.Clients != 2 || result.Windows != 1 {
		t.Fatalf("result %+v", result)
	}
	after := stable(s.windows())
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].Rect != before[0].Rect {
		t.Fatalf("windows %+v, were %+v\n%s", after, before, s.log())
	}
	c.DispatchUntil("the menu to close", func() bool { return menu.Dismissed })
	if err := menu.Destroy(); err != nil {
		t.Fatal(err)
	}
	watcher.DispatchUntil("the watcher to see the old handle close and a new one", func() bool {
		return list.closed == 1 && list.titles()["main"] == 2
	})
	frames := w.Frames
	if err := w.Redraw(); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("a frame", func() bool { return w.Frames > frames })
	if err := errors.Join(child.Attach(c.Buffer(image.Pt(20, 20), 0, testclient.Fill(0, 0xff, 0xff)), 0, 0), child.DamageBuffer(0, 0, 20, 20), child.Commit(), bar.Redraw(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	c.DispatchUntil("the clipboard source to be cancelled", func() bool { return slices.Contains(source.Log, "cancelled") })
	if clipboard.Device == nil || in == nil {
		t.Fatal("the seat objects went away")
	}
	if _, err := ipc.Do(s.socket, ipc.Request{Request: ipc.KindDispatch, Action: "workspace 2"}); err != nil {
		t.Fatal(err)
	}
	opened := c.Window("after", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("a new window to map", opened.Mapped)
	if ws := stable(s.windows()); len(ws) != 2 {
		t.Fatalf("after a new window: %+v", ws)
	}
	for {
		line, err := stream.Next()
		if err != nil {
			t.Fatalf("the watch stream across the upgrade: %v", err)
		}
		if strings.Contains(string(line), `"event":"open"`) {
			break
		}
	}
}

func TestUpgradeIsRefusedWhileASandboxListenerIsOpen(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	c := testclient.Connect(t, s.waylandSocket())
	dir := testenv.SocketDir(t)
	listen, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(unix.Bind(listen, &unix.SockaddrUnix{Name: filepath.Join(dir, "sandbox")}), unix.Listen(listen, 1)); err != nil {
		t.Fatal(err)
	}
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	mgr, err := csc.NewWpSecurityContextManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	ctx, err := mgr.CreateListener(listen, pipe[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(ctx.SetAppID("sandboxed.app"), ctx.Commit(), ctx.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	_, err = s.upgrade(buildCopy(t, binary))
	if err == nil || !strings.Contains(err.Error(), "refused, nothing changed") || !strings.Contains(err.Error(), "sandboxed.app") {
		t.Fatalf("an upgrade with a sandbox open: %v", err)
	}
}

func TestUpgradeIsRefusedWhileTheSessionIsLocked(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	c := testclient.Connect(t, s.waylandSocket())
	mgr, err := csl.NewExtSessionLockManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	locked := false
	if _, err := mgr.Lock(lockEvents{&locked}); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	_, err = s.upgrade(buildCopy(t, binary))
	if err == nil || !strings.Contains(err.Error(), "refused, nothing changed") {
		t.Fatalf("an upgrade while locked: %v", err)
	}
}

type lockEvents struct{ locked *bool }

func (l lockEvents) Locked(*csl.ExtSessionLockV1) error {
	*l.locked = true
	return nil
}
func (lockEvents) Finished(*csl.ExtSessionLockV1) error { return nil }

func TestUpgradeKeepsAWindowOfDMABUFs(t *testing.T) {
	testenv.GPU(t)
	binary := buildLysWM(t)
	s := startProcess(t, binary, "", "--renderer", "gles")
	c := testclient.Connect(t, s.waylandSocket())
	w := c.WindowWith("gpu", testclient.Fill(0xff, 0, 0), nil)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	show := func(what string, b *testclient.DMABUF) {
		t.Helper()
		if err := errors.Join(w.Surface.Attach(b.WL, 0, 0), w.Surface.DamageBuffer(0, 0, int32(w.Size.X), int32(w.Size.Y)), w.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		c.Sync()
	}
	first := c.DMABUF(w.Size, testclient.Fill(0xff, 0, 0))
	show("the first buffer", first)
	before := stable(s.windows())

	if _, err := s.upgrade(buildCopy(t, binary)); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, s.log())
	}
	if after := stable(s.windows()); len(after) != 1 || after[0].ID != before[0].ID || after[0].Rect != before[0].Rect {
		t.Fatalf("windows %+v, were %+v\n%s", after, before, s.log())
	}
	show("a buffer after the upgrade", c.DMABUF(w.Size, testclient.Fill(0, 0xff, 0)))
	c.DispatchUntil("the new compositor to release the buffer the old one held", func() bool { return first.Released })
}

func TestWithoutTheSwitchNoHolderRunsAndUpgradeSaysHowToTurnItOn(t *testing.T) {
	binary := buildLysWM(t)
	s := startBare(t, binary, "")
	tasks, err := filepath.Glob("/proc/" + strconv.Itoa(s.cmd.Process.Pid) + "/task/*/children")
	if err != nil || len(tasks) == 0 {
		t.Fatalf("the process's tasks: %v, %v", tasks, err)
	}
	for _, task := range tasks {
		data, err := os.ReadFile(task)
		if err != nil {
			t.Fatal(err)
		}
		if len(strings.Fields(string(data))) != 0 {
			t.Fatalf("lys-wm without --holder has children %q: it is a holder", data)
		}
	}
	c := testclient.Connect(t, s.waylandSocket())
	w := c.Window("kept", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	_, err = s.upgrade(buildCopy(t, binary))
	if err == nil || !strings.Contains(err.Error(), "refused, nothing changed") || !strings.Contains(err.Error(), "--holder") {
		t.Fatalf("an upgrade without a holder: %v", err)
	}
	if ws := s.windows(); len(ws) != 1 {
		t.Fatalf("windows after the refusal: %+v", ws)
	}
}

func TestOnlyTheSwitchStartsAHolder(t *testing.T) {
	none := func(string) string { return "" }
	for args, want := range map[string]bool{
		"":                         false,
		"--systemd":                false,
		"--backend headless":       false,
		"--holder":                 true,
		"--systemd --holder":       true,
		"--holder --check-config":  false,
		"--holder --handover-info": false,
	} {
		if got := holds(strings.Fields(args), none); got != want {
			t.Errorf("%q: holds %t, want %t", args, got, want)
		}
	}
	inside := func(name string) string {
		if name == holderEnv {
			return "3"
		}
		return ""
	}
	if holds([]string{"--holder"}, inside) {
		t.Error("the worker a holder started became a holder too")
	}
}

func TestUpgradeKeepsWindowsWhoseObjectsAreNumberedOutOfOrder(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	c := testclient.Connect(t, s.waylandSocket())
	spare := make([]*cw.WlRegion, 40)
	for i := range spare {
		region, err := c.Compositor.CreateRegion()
		if err != nil {
			t.Fatal(err)
		}
		spare[i] = region
	}
	first := c.WindowWith("first", testclient.Fill(0xff, 0, 0), func(w *testclient.Window) error {
		w.Stop()
		return nil
	})
	c.DispatchUntil("the first window's only frame", func() bool { return first.Frames > 0 })
	for _, region := range spare {
		if err := region.Destroy(); err != nil {
			t.Fatal(err)
		}
	}
	c.Sync()
	for {
		probe, err := c.Compositor.CreateRegion()
		if err != nil {
			t.Fatal(err)
		}
		if probe.Base().ID() < first.Surface.Base().ID() {
			if err := probe.Destroy(); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	c.Sync()
	second := c.Window("second", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the second window to map", second.Mapped)
	if second.Surface.Base().ID() >= first.Surface.Base().ID() {
		t.Fatalf("the later window's surface is %d, the first's %d; the test needs them out of order", second.Surface.Base().ID(), first.Surface.Base().ID())
	}
	before := stable(s.windows())
	if _, err := s.upgrade(buildCopy(t, binary)); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, s.log())
	}
	after := stable(s.windows())
	if len(after) != len(before) {
		t.Fatalf("windows %+v, were %+v", after, before)
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Title != after[i].Title || before[i].Rect != after[i].Rect {
			t.Errorf("window %d is %+v, was %+v", i, after[i], before[i])
		}
	}
	opened := c.Window("third", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("a new window to map", opened.Mapped)
	for _, w := range s.windows() {
		if w.Title == "third" && w.ID <= before[len(before)-1].ID {
			t.Errorf("a new window took the used id %d", w.ID)
		}
	}
}

func TestUpgradeSurvivesAClientLeavingWhileTheNewBinaryStarts(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	stays := testclient.Connect(t, s.waylandSocket())
	kept := stays.Window("stays", testclient.Fill(0xff, 0, 0))
	stays.DispatchUntil("the kept window to map", kept.Mapped)
	leaves := testclient.Connect(t, s.waylandSocket())
	gone := leaves.Window("leaves", testclient.Fill(0, 0xff, 0))
	leaves.DispatchUntil("the other window to map", gone.Mapped)
	gate := t.TempDir()
	next := buildCopy(t, binary)
	wrapper := script(t, `case "$1" in --handover-info) exec `+next+` "$@";; esac
touch `+gate+`/started
while [ ! -e `+gate+`/go ]; do sleep 0.01; done
exec `+next+` "$@"
`)
	done := make(chan error, 1)
	go func() {
		_, err := s.upgrade(wrapper)
		done <- err
	}()
	for {
		if _, err := os.Stat(filepath.Join(gate, "started")); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := leaves.Display.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gate, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("upgrade: %v\n%s", err, s.log())
	}
	ws := s.windows()
	if len(ws) != 1 || ws[0].Title != "stays" {
		t.Fatalf("windows %+v\n%s", ws, s.log())
	}
	frames := kept.Frames
	if err := kept.Redraw(); err != nil {
		t.Fatal(err)
	}
	stays.DispatchUntil("a frame", func() bool { return kept.Frames > frames })
}

func TestANewBinaryFailingAfterItAdoptedLeavesTheSocketsInPlace(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	c := testclient.Connect(t, s.waylandSocket())
	w := c.Window("kept", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	next := buildCopy(t, binary)
	wrapper := script(t, `case "$1" in --handover-info) exec `+next+` "$@";; esac
XDG_RUNTIME_DIR= exec `+next+` "$@"
`)
	if _, err := s.upgrade(wrapper); err == nil || !strings.Contains(err.Error(), "keeps running") {
		t.Fatalf("a binary that fails after adopting the display: %v\n%s", err, s.log())
	}
	later := testclient.Connect(t, s.waylandPath)
	opened := later.Window("later", testclient.Fill(0, 0xff, 0))
	later.DispatchUntil("a client that connects after the failure to map", opened.Mapped)
	if ws := s.windows(); len(ws) != 2 {
		t.Fatalf("windows %+v", ws)
	}
	if _, err := s.upgrade(next); err != nil {
		t.Fatalf("an upgrade after the failure: %v\n%s", err, s.log())
	}
}

func TestUpgradeUnderClientChurn(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	stays := testclient.Connect(t, s.waylandSocket())
	kept := stays.Window("stays", testclient.Fill(0xff, 0, 0))
	stays.DispatchUntil("the kept window to map", kept.Mapped)
	for round := range 6 {
		churn := make([]*testclient.Client, 6)
		for i := range churn {
			churn[i] = testclient.Connect(t, s.waylandSocket())
			w := churn[i].Window("churn", testclient.Fill(0, 0xff, 0))
			churn[i].DispatchUntil("a churn window to map", w.Mapped)
		}
		done := make(chan error, 1)
		next := buildCopy(t, binary)
		go func() {
			_, err := s.upgrade(next)
			done <- err
		}()
		for i, c := range churn {
			if i%2 == 0 {
				if err := c.Display.Close(); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(time.Duration(round*i) * time.Millisecond)
		}
		if err := <-done; err != nil && !strings.Contains(err.Error(), "keeps running") {
			t.Fatalf("round %d: %v\n%s", round, err, s.log())
		}
		frames := kept.Frames
		if err := kept.Redraw(); err != nil {
			t.Fatalf("round %d: %v\n%s", round, err, s.log())
		}
		stays.DispatchUntil("a frame", func() bool { return kept.Frames > frames })
		for i, c := range churn {
			if i%2 != 0 {
				if err := c.Display.Close(); err != nil {
					t.Fatal(err)
				}
			}
		}
		stays.Sync()
	}
}

func TestALateExitNoticeLeavesTheConnectionOnTheReusedNumberAlone(t *testing.T) {
	old, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(old[1])
	number := old[0]
	peer := &handoverPeer{fd: number}
	peer.close()
	next, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(next[1])
	if next[0] != number {
		if err := unix.Dup3(next[0], number, unix.O_CLOEXEC); err != nil {
			t.Fatal(err)
		}
		defer unix.Close(next[0])
	}
	defer unix.Close(number)
	peer.shutdown()
	if _, err := unix.Write(next[1], []byte{1}); err != nil {
		t.Fatalf("the notice shut down whatever took the number: %v", err)
	}
	if _, err := unix.Write(number, []byte{1}); err != nil {
		t.Fatalf("the notice shut down whatever took the number: %v", err)
	}
}

func TestAFailedUpgradeNeverClosesTheNextConnection(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "")
	fake := script(t, `case "$1" in --handover-info) echo '{"format":1}'; exit 0;; esac`+"\nexit 3\n")
	for i := range 10 {
		if _, err := s.upgrade(fake); err == nil {
			t.Fatal("a binary that exits at once took over")
		}
		for range 20 {
			if _, err := ipc.Do(s.socket, ipc.Request{Request: ipc.KindWindows}); err != nil {
				t.Fatalf("round %d: the session did not answer right after the failed upgrade: %v\n%s", i, err, s.log())
			}
		}
	}
}
