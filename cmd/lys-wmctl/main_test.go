package main

import (
	"bytes"
	"errors"
	"image"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestParseArgs(t *testing.T) {
	for args, want := range map[string]ipc.Request{
		"dispatch movetoworkspace 3": {Request: wmipc.KindDispatch, Action: "movetoworkspace 3"},
		"dispatch exec foot -e htop": {Request: wmipc.KindDispatch, Action: "exec foot -e htop"},
		"reload":                     {Request: wmipc.KindDispatch, Action: "reload"},
		"windows":                    {Request: wmipc.KindWindows},
		"activewindow":               {Request: wmipc.KindActiveWindow},
		"submap":                     {Request: wmipc.KindSubmap},
		"layout":                     {Request: wmipc.KindLayout},
		"locks":                      {Request: wmipc.KindLocks},
		"reducedmotion":              {Request: wmipc.KindReducedMotion},
		"configproblems":             {Request: wmipc.KindConfigProblems},
		"watch":                      {Request: wmipc.KindWatch},
		"watch --snapshot":           {Request: wmipc.KindWatch, Snapshot: true},
		`batch submap\x20a kill`:     {Request: wmipc.KindDispatch, Actions: []string{"submap a", "kill"}},
		"layers":                     {Request: wmipc.KindLayers},
		"devices":                    {Request: wmipc.KindDevices},
		"pickwindow":                 {Request: wmipc.KindPickWindow},
		"binds":                      {Request: wmipc.KindBinds},
		"icon 7 64":                  {Request: wmipc.KindIcon, Window: 7, Size: 64},
	} {
		if got, err := parseArgs(splitArgs(args)); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %+v, %v; want %+v", args, got, err, want)
		}
	}
	for _, args := range []string{"", "dispatch", "windows 2", "submap resize", "layout 1", "reload now", "teleport", "icon", "icon 7", "icon 0 64", "icon 7 0", "icon x 64", "icon 7 64 1", "batch", "watch --all", "pickwindow 3"} {
		if _, err := parseArgs(strings.Fields(args)); !errors.Is(err, errUsage) {
			t.Errorf("%q: %v", args, err)
		}
	}
}

type handler struct {
	dispatched []wm.Action
	created    []ipc.OutputSpec
	removed    []string
}

func (h *handler) Snapshot() ipc.Snapshot {
	return ipc.Snapshot{Windows: []ipc.Window{{ID: 3, AppID: "foot", Focused: true}}, Submap: "resize", Binds: []ipc.Bind{{Chord: "SUPER", Action: "exec launcher", Description: "Open the launcher", Flags: []string{"release"}}}, ConfigProblems: ipc.ConfigProblems{Messages: []string{"c.yaml:3: colour: unknown key"}}}
}

func (h *handler) PrepareDispatch(wm.Action) func() error { return nil }

func (h *handler) DispatchPrepared(a wm.Action, _ func() error) error { return h.Dispatch(a) }

func (h *handler) Dispatch(a wm.Action) error {
	h.dispatched = append(h.dispatched, a)
	return nil
}

func (h *handler) Icon(wm.WindowID, int) (*image.RGBA, int, error) {
	return nil, 0, errors.New("no icons")
}

func (h *handler) CreateOutput(spec ipc.OutputSpec) (ipc.CreatedOutput, error) {
	h.created = append(h.created, spec)
	return ipc.CreatedOutput{Name: "VIRT-1"}, nil
}

func (h *handler) RemoveOutput(name string) error {
	h.removed = append(h.removed, name)
	return nil
}

func TestRunAgainstASocket(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{}
	path := filepath.Join(testenv.SocketDir(t), "lys-wm", "wayland-1.sock")
	s, err := ipc.Listen(l, path, h, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	defer func() {
		if err := l.Post(func() error {
			l.Stop()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(<-done, s.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	}()
	getenv := func(k string) string {
		if k == wmipc.SocketEnv {
			return path
		}
		return ""
	}
	var out bytes.Buffer
	if err := run([]string{"activewindow"}, getenv, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "{\n  \"id\": 3,\n  \"app_id\": \"foot\",") {
		t.Fatalf("activewindow printed %q", out.String())
	}
	out.Reset()
	if err := run([]string{"submap"}, getenv, &out, io.Discard); err != nil || out.String() != "\"resize\"\n" {
		t.Fatalf("submap printed %q, %v", out.String(), err)
	}
	out.Reset()
	if err := run([]string{"binds"}, getenv, &out, io.Discard); err != nil || !strings.Contains(out.String(), `"description": "Open the launcher"`) {
		t.Fatalf("binds printed %q, %v", out.String(), err)
	}
	out.Reset()
	var warnings bytes.Buffer
	if err := run([]string{"--socket", path, "reload"}, func(string) string { return "" }, &out, &warnings); err != nil || out.Len() != 0 {
		t.Fatalf("reload: %q, %v", out.String(), err)
	}
	if want := "lys-wmctl: warning: c.yaml:3: colour: unknown key\n"; warnings.String() != want {
		t.Fatalf("reload warned %q, want %q", warnings.String(), want)
	}
	if err := l.Post(func() error {
		if len(h.dispatched) != 1 || h.dispatched[0] != (wm.ReloadAction{}) {
			t.Errorf("dispatched %v", h.dispatched)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--socket", filepath.Join(testenv.SocketDir(t), "none.sock"), "windows"}, getenv, &out, io.Discard); err == nil {
		t.Fatal("a missing socket succeeded")
	}
}

func TestNoRunningCompositorHasItsOwnExitStatus(t *testing.T) {
	missing := filepath.Join(testenv.SocketDir(t), "no-such.sock")
	err := run([]string{"--socket", missing, "reload"}, func(string) string { return "" }, io.Discard, io.Discard)
	if code := exitCode(err); code != exitNotRunning {
		t.Fatalf("a socket that is not there: %v, status %d", err, code)
	}
	err = run([]string{"reload"}, func(string) string { return "" }, io.Discard, io.Discard)
	if code := exitCode(err); code != exitNotRunning {
		t.Fatalf("no socket to find: %v, status %d", err, code)
	}
	stale := filepath.Join(testenv.SocketDir(t), "stale.sock")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ul, ok := l.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener is a %T", l)
	}
	ul.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	err = run([]string{"--socket", stale, "reload"}, func(string) string { return "" }, io.Discard, io.Discard)
	if code := exitCode(err); code != exitNotRunning {
		t.Fatalf("a stale socket: %v, status %d", err, code)
	}
}

func TestAnyOtherFailureIsNotReportedAsNotRunning(t *testing.T) {
	if code := exitCode(errors.New("the compositor refused the action")); code != exitFailed {
		t.Fatalf("status %d", code)
	}
	_, err := parseArgs([]string{"teleport"})
	if code := exitCode(err); code != exitFailed {
		t.Fatalf("a usage error: status %d", code)
	}
}

func TestOutputCreateAndRemove(t *testing.T) {
	for args, want := range map[string]ipc.Request{
		"output create 1920x1080":                             {Request: wmipc.KindCreateOutput, Mode: &wmipc.Mode{Width: 1920, Height: 1080, RefreshMHz: 60000}},
		"output create --name tablet --scale 2 2560x1600@120": {Request: wmipc.KindCreateOutput, Name: "tablet", Scale: 2, Mode: &wmipc.Mode{Width: 2560, Height: 1600, RefreshMHz: 120000}},
		"output remove VIRT-1":                                {Request: wmipc.KindRemoveOutput, Name: "VIRT-1"},
	} {
		got, err := parseArgs(strings.Fields(args))
		if err != nil || got.Request != want.Request || got.Name != want.Name || got.Scale != want.Scale || (got.Mode == nil) != (want.Mode == nil) || got.Mode != nil && *got.Mode != *want.Mode {
			t.Errorf("%q: %+v (mode %v), %v; want %+v (mode %v)", args, got, got.Mode, err, want, want.Mode)
		}
	}
	for _, args := range []string{"output", "output list", "output create", "output create 1920", "output create 1920x1080@0", "output create --scale 0 800x600", "output create --scale -1 800x600", "output create 800x600 600x800", "output create --frobnicate 800x600", "output remove", "output remove VIRT-1 VIRT-2"} {
		if _, err := parseArgs(strings.Fields(args)); !errors.Is(err, errUsage) {
			t.Errorf("%q: %v", args, err)
		}
	}

	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{}
	path := filepath.Join(testenv.SocketDir(t), "lys-wm", "wayland-1.sock")
	s, err := ipc.Listen(l, path, h, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	defer func() {
		if err := l.Post(func() error {
			l.Stop()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(<-done, s.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	}()
	var out bytes.Buffer
	if err := run([]string{"--socket", path, "output", "create", "800x600@30"}, nil, &out, io.Discard); err != nil || out.String() != "{\n  \"name\": \"VIRT-1\"\n}\n" {
		t.Fatalf("output create printed %q, %v", out.String(), err)
	}
	out.Reset()
	if err := run([]string{"--socket", path, "output", "remove", "VIRT-1"}, nil, &out, io.Discard); err != nil || out.Len() != 0 {
		t.Fatalf("output remove printed %q, %v", out.String(), err)
	}
	if err := l.Post(func() error {
		want := ipc.OutputSpec{Size: image.Pt(800, 600), RefreshMHz: 30000}
		if len(h.created) != 1 || h.created[0] != want || len(h.removed) != 1 || h.removed[0] != "VIRT-1" {
			t.Errorf("created %+v, removed %v", h.created, h.removed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (h *handler) Capture(func(ipc.Event)) (ipc.CaptureSession, error) {
	return nil, errors.New("lys-wmctl starts no input capture")
}

func (h *handler) PickWindow(func(*wm.WindowID, error)) (func(), error) {
	return nil, errors.New("lys-wmctl picks no window")
}

func splitArgs(args string) []string {
	fields := strings.Fields(args)
	for i, f := range fields {
		fields[i] = strings.ReplaceAll(f, `\x20`, " ")
	}
	return fields
}
