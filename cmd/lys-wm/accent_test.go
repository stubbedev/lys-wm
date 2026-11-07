package main

import (
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-ui/appearance"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

const (
	portalBus   = "org.freedesktop.portal.Desktop"
	portalPath  = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalIface = "org.freedesktop.portal.Settings"
)

type rgb struct{ R, G, B float64 }

type fakePortal struct {
	mu     sync.Mutex
	conn   *dbus.Conn
	accent rgb
}

func (p *fakePortal) ReadAll([]string) (map[string]map[string]dbus.Variant, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]map[string]dbus.Variant{"org.freedesktop.appearance": {"accent-color": dbus.MakeVariant(p.accent)}}, nil
}

func (p *fakePortal) set(t *testing.T, accent rgb) {
	t.Helper()
	p.mu.Lock()
	p.accent = accent
	p.mu.Unlock()
	if err := p.conn.Emit(portalPath, portalIface+".SettingChanged", "org.freedesktop.appearance", "accent-color", dbus.MakeVariant(accent)); err != nil {
		t.Fatal(err)
	}
}

func publishPortal(t *testing.T, address string, accent rgb) *fakePortal {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &fakePortal{conn: conn, accent: accent}
	if err := conn.Export(p, portalPath, portalIface); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(portalBus, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v, %v", portalBus, reply, err)
	}
	return p
}

func TestTheDesktopAccentIsTheBuiltInOneUntilTheDesktopPublishesOne(t *testing.T) {
	if got := desktopAccent(appearance.Accent{}); got != compositor.DefaultAccent {
		t.Errorf("no published accent gives %+v, want the built-in %+v", got, compositor.DefaultAccent)
	}
	want := render.Color{R: 0x33, G: 0x66, B: 0x99, A: 0xff}
	if got := desktopAccent(appearance.Accent{R: 0.2, G: 0.4, B: 0.6, Known: true}); got != want {
		t.Errorf("a published accent gives %+v, want %+v", got, want)
	}
}

func TestFollowingTheSameAccentAgainAsksForNoReload(t *testing.T) {
	s := &reloader{accent: compositor.DefaultAccent}
	if s.follow(compositor.DefaultAccent) {
		t.Error("the accent it already has asked for a reload")
	}
	next := render.Color{R: 1, A: 0xff}
	if !s.follow(next) || s.accent != next {
		t.Errorf("a new accent asked for no reload, or was not kept: %+v", s.accent)
	}
	if s.follow(next) {
		t.Error("the accent it just took asked for a second reload")
	}
}

func waitForProblem(t *testing.T, socket, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := configProblems(t, socket)
		for _, m := range got.Messages {
			if strings.Contains(m, want) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no reload reported %q; the config problems are %+v", want, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestADesktopAccentChangeReloadsTheConfig(t *testing.T) {
	address := testenv.DBus(t)
	socket, path, done := startSessionOnBus(t, "", address)
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(noXwayland+text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("set_by_the_first_accent: true\n")
	portal := publishPortal(t, address, rgb{0.2, 0.4, 0.6})
	waitForProblem(t, socket, "set_by_the_first_accent: unknown key")

	write("set_by_the_second_accent: true\n")
	portal.set(t, rgb{0.6, 0.4, 0.2})
	waitForProblem(t, socket, "set_by_the_second_accent: unknown key")

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	expectCleanExit(t, socket, done, "SIGTERM")
}
