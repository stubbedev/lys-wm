package powerprofile

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/third_party/godbus"

	"github.com/stubbedev/lys-wm/internal/loop"
)

type daemon struct {
	t       *testing.T
	conn    *dbus.Conn
	s       service
	profile atomic.Value
	stop    func()
}

func runDaemon(t *testing.T, address string, s service, profile string) *daemon {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	d := &daemon{t: t, conn: conn, s: s}
	d.profile.Store(profile)
	if d.stop, err = dbusx.Serve(conn, dbusx.Service{Name: s.name, Path: s.path, Interface: s.name, Methods: struct{}{}, Properties: dbusx.Getters{activeProfile: func() any { return d.profile.Load().(string) }}}); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *daemon) set(profile string) {
	d.t.Helper()
	d.profile.Store(profile)
	if err := d.conn.Emit(d.s.path, propsIface+".PropertiesChanged", d.s.name, map[string]dbus.Variant{activeProfile: dbus.MakeVariant(profile)}, []string{}); err != nil {
		d.t.Fatal(err)
	}
}

type run struct {
	t      *testing.T
	l      *loop.Loop
	saver  []bool
	errors []error
}

func (r *run) until(what string, done func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var check *loop.Timer
	check, err := r.l.AddTimer(func() error {
		if done() || time.Now().After(deadline) {
			r.l.Stop()
			return nil
		}
		return check.Arm(time.Millisecond)
	})
	if err != nil {
		r.t.Fatal(err)
	}
	defer check.Remove()
	if err := check.Arm(time.Millisecond); err != nil {
		r.t.Fatal(err)
	}
	if err := r.l.Run(); err != nil {
		r.t.Fatal(err)
	}
	if !done() {
		r.t.Fatalf("waited 10s for %s: changes %v, errors %v", what, r.saver, r.errors)
	}
}

func watch(t *testing.T, address string) *run {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	r := &run{t: t, l: l}
	w, err := Watch(l, address, func(saver bool) error {
		r.saver = append(r.saver, saver)
		return nil
	}, func(err error) { r.errors = append(r.errors, err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func (r *run) changes(n int) func() bool {
	return func() bool { return len(r.saver) >= n }
}

func TestTheWatcherFollowsTheActiveProfile(t *testing.T) {
	bus := dbustest.Start(t)
	legacy := runDaemon(t, bus.Address, services[1], "power-saver")
	r := watch(t, bus.Address)
	r.until("the legacy daemon's power-saver", r.changes(1))
	legacy.set("balanced")
	r.until("balanced", r.changes(2))
	legacy.set("balanced")
	legacy.set("power-saver")
	r.until("power-saver again", r.changes(3))
	legacy.stop()
	r.until("the daemon gone", r.changes(4))
	runDaemon(t, bus.Address, services[0], "power-saver")
	r.until("the daemon back under its new name", r.changes(5))
	if want := []bool{true, false, true, false, true}; len(r.saver) != len(want) || len(r.errors) != 0 {
		t.Fatalf("changes %v and errors %v, want %v and none", r.saver, r.errors, want)
	} else {
		for i := range want {
			if r.saver[i] != want[i] {
				t.Fatalf("changes %v, want %v", r.saver, want)
			}
		}
	}
}

func TestNoDaemonIsNoPowerSaver(t *testing.T) {
	bus := dbustest.Start(t)
	r := watch(t, bus.Address)
	stranger, err := dbus.Connect(bus.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()
	if err := stranger.Emit(services[0].path, propsIface+".PropertiesChanged", "org.example.Other", map[string]dbus.Variant{activeProfile: dbus.MakeVariant(powerSaver)}, []string{}); err != nil {
		t.Fatal(err)
	}
	d := runDaemon(t, bus.Address, services[0], "balanced")
	d.set("power-saver")
	r.until("the daemon's power-saver", r.changes(1))
	if len(r.saver) != 1 || !r.saver[0] || len(r.errors) != 0 {
		t.Fatalf("changes %v and errors %v, want one change to power-saver: another interface's signal and a balanced daemon change nothing", r.saver, r.errors)
	}
}
