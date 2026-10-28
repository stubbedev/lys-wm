package systemd

import (
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/stubbedev/lys-wm/internal/testenv"
)

type fakeManager struct {
	mu    sync.Mutex
	calls []string
	env   map[string]string
	fail  string
}

func (m *fakeManager) note(call string) *dbus.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
	if m.fail != "" && strings.HasPrefix(call, m.fail) {
		return dbus.MakeFailedError(errors.New("refused"))
	}
	return nil
}

func (m *fakeManager) SetEnvironment(assignments []string) *dbus.Error {
	m.mu.Lock()
	for _, a := range assignments {
		k, v, _ := strings.Cut(a, "=")
		m.env[k] = v
	}
	m.mu.Unlock()
	return m.note("SetEnvironment " + strings.Join(assignments, " "))
}

func (m *fakeManager) UnsetEnvironment(names []string) *dbus.Error {
	m.mu.Lock()
	for _, k := range names {
		delete(m.env, k)
	}
	m.mu.Unlock()
	return m.note("UnsetEnvironment " + strings.Join(names, " "))
}

func (m *fakeManager) UnsetAndSetEnvironment(names, assignments []string) *dbus.Error {
	m.mu.Lock()
	for _, k := range names {
		delete(m.env, k)
	}
	for _, a := range assignments {
		k, v, _ := strings.Cut(a, "=")
		m.env[k] = v
	}
	m.mu.Unlock()
	return m.note("UnsetAndSetEnvironment " + strings.Join(names, " ") + " | " + strings.Join(assignments, " "))
}

type fakeProperties struct{ m *fakeManager }

func (p fakeProperties) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if iface != manager || name != "Environment" || p.m.fail == "Get" {
		return dbus.Variant{}, dbus.MakeFailedError(errors.New("no property " + iface + "." + name))
	}
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	assignments := make([]string, 0, len(p.m.env))
	for _, k := range slices.Sorted(maps.Keys(p.m.env)) {
		assignments = append(assignments, k+"="+p.m.env[k])
	}
	return dbus.MakeVariant(assignments), nil
}

func (m *fakeManager) StartUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	return "/org/freedesktop/systemd1/job/1", m.note("StartUnit " + name + " " + mode)
}

func (m *fakeManager) StopUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	return "/org/freedesktop/systemd1/job/2", m.note("StopUnit " + name + " " + mode)
}

func (m *fakeManager) state() ([]string, map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls), maps.Clone(m.env)
}

func systemd(t *testing.T, address, fail string, prior map[string]string) *fakeManager {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	m := &fakeManager{env: prior, fail: fail}
	if err := conn.Export(m, "/org/freedesktop/systemd1", manager); err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(fakeProperties{m}, "/org/freedesktop/systemd1", "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName("org.freedesktop.systemd1", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own org.freedesktop.systemd1: %v, %v", reply, err)
	}
	return m
}

var env = [][2]string{{"WAYLAND_DISPLAY", "wayland-1"}, {"XDG_CURRENT_DESKTOP", "lys-wm"}, {"LYS_WM_SOCKET", "/run/user/1000/lys-wm/wayland-1.sock"}}

func TestSessionLifecycle(t *testing.T) {
	address := testenv.DBus(t)
	m := systemd(t, address, "", map[string]string{"KEEP": "1"})
	s, err := Start(address, env)
	if err != nil {
		t.Fatal(err)
	}
	calls, got := m.state()
	wantCalls := []string{
		"SetEnvironment WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=lys-wm LYS_WM_SOCKET=/run/user/1000/lys-wm/wayland-1.sock",
		"StartUnit lys-wm-session.target replace",
	}
	if !slices.Equal(calls, wantCalls) || got["XDG_CURRENT_DESKTOP"] != "lys-wm" {
		t.Fatalf("calls %q, env %v", calls, got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	calls, got = m.state()
	wantCalls = append(wantCalls, "StopUnit lys-wm-session.target replace", "UnsetAndSetEnvironment WAYLAND_DISPLAY XDG_CURRENT_DESKTOP LYS_WM_SOCKET | ")
	if !slices.Equal(calls, wantCalls) || len(got) != 1 || got["KEEP"] != "1" {
		t.Fatalf("after close: calls %q, env %v", calls, got)
	}
}

func TestAFailedStartLeavesNoEnvironment(t *testing.T) {
	address := testenv.DBus(t)
	prior := map[string]string{"KEEP": "1", "PATH": "/usr/bin"}
	m := systemd(t, address, "StartUnit", maps.Clone(prior))
	if _, err := Start(address, append(slices.Clone(env), [2]string{"PATH", "/nix/bin:/usr/bin"})); err == nil || !strings.Contains(err.Error(), "StartUnit") {
		t.Fatalf("a refused StartUnit: %v", err)
	}
	if _, got := m.state(); !maps.Equal(got, prior) {
		t.Fatalf("env %v, want the prior %v back", got, prior)
	}
	if _, err := Start("", env); err == nil {
		t.Fatal("no bus address was accepted")
	}
	if _, err := Start("unix:path="+filepath.Join(testenv.SocketDir(t), "none"), env); err == nil {
		t.Fatal("a missing bus was accepted")
	}
}

func TestCloseRestoresWhatTheManagerHadBefore(t *testing.T) {
	address := testenv.DBus(t)
	prior := map[string]string{"KEEP": "1", "PATH": "/usr/bin", "XCURSOR_SIZE": "32"}
	m := systemd(t, address, "", maps.Clone(prior))
	s, err := Start(address, [][2]string{{"PATH", "/nix/bin:/usr/bin"}, {"XCURSOR_SIZE", "24"}, {"WAYLAND_DISPLAY", "wayland-1"}})
	if err != nil {
		t.Fatal(err)
	}
	during := map[string]string{"KEEP": "1", "PATH": "/nix/bin:/usr/bin", "XCURSOR_SIZE": "24", "WAYLAND_DISPLAY": "wayland-1"}
	if _, got := m.state(); !maps.Equal(got, during) {
		t.Fatalf("while running: env %v, want %v", got, during)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, got := m.state(); !maps.Equal(got, prior) {
		t.Fatalf("after close: env %v, want the prior %v with PATH and XCURSOR_SIZE restored, not unset", got, prior)
	}
}

func TestAnUnreadableManagerEnvironmentFailsTheStart(t *testing.T) {
	address := testenv.DBus(t)
	m := systemd(t, address, "Get", map[string]string{"KEEP": "1"})
	if _, err := Start(address, env); err == nil || !strings.Contains(err.Error(), "user manager's environment") {
		t.Fatalf("a refused Environment read: %v", err)
	}
	if calls, got := m.state(); len(calls) != 0 || len(got) != 1 {
		t.Fatalf("calls %q, env %v: nothing should be set without the prior environment", calls, got)
	}
}
