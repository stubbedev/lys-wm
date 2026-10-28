package session

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/prop"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

const (
	sessionPath         = dbus.ObjectPath("/org/freedesktop/login1/session/_31")
	operationInProgress = login1 + ".OperationInProgress"
)

type fakeLogind struct {
	address  string
	mu       sync.Mutex
	calls    []string
	conn     *dbus.Conn
	props    *prop.Properties
	sleeping bool
	refusal  *dbus.Error
}

func (f *fakeLogind) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeLogind) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeLogind) refuse(e *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusal = e
}

func (f *fakeLogind) sleep() *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.refusal != nil:
		return f.refusal
	case f.sleeping:
		return dbus.NewError(operationInProgress, []any{"There's already a shutdown or sleep operation in progress"})
	}
	f.sleeping = true
	if err := f.conn.Emit(managerPath, managerIface+".PrepareForSleep", true); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (f *fakeLogind) wake() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeping = false
	return f.conn.Emit(managerPath, managerIface+".PrepareForSleep", false)
}

type manager struct{ f *fakeLogind }

func (m manager) GetSession(id string) (dbus.ObjectPath, *dbus.Error) {
	m.f.note("GetSession " + id)
	return sessionPath, nil
}

func (m manager) GetSessionByPID(uint32) (dbus.ObjectPath, *dbus.Error) {
	m.f.note("GetSessionByPID")
	return sessionPath, nil
}

func (m manager) Inhibit(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
	m.f.note("Inhibit " + what + " " + mode)
	fd, err := os.Open(os.DevNull)
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	return dbus.UnixFD(fd.Fd()), nil
}

func (m manager) Suspend(interactive bool) *dbus.Error {
	m.f.note(fmt.Sprintf("Suspend %v", interactive))
	return m.f.sleep()
}

type session struct{ f *fakeLogind }

func (s session) TakeControl(force bool) *dbus.Error {
	s.f.note("TakeControl")
	return nil
}

func (s session) ReleaseControl() *dbus.Error {
	s.f.note("ReleaseControl")
	return nil
}

func (s session) TakeDevice(major, minor uint32) (dbus.UnixFD, bool, *dbus.Error) {
	s.f.note("TakeDevice")
	fd, err := os.Open(os.DevNull)
	if err != nil {
		return 0, false, dbus.MakeFailedError(err)
	}
	return dbus.UnixFD(fd.Fd()), false, nil
}

func (s session) ReleaseDevice(major, minor uint32) *dbus.Error {
	s.f.note("ReleaseDevice")
	return nil
}

func (s session) PauseDeviceComplete(major, minor uint32) *dbus.Error {
	s.f.note("PauseDeviceComplete")
	return nil
}

type seat struct{ f *fakeLogind }

func (s seat) SwitchTo(vt uint32) *dbus.Error {
	s.f.note("SwitchTo")
	if vt == 0 {
		return dbus.MakeFailedError(errors.New("no VT 0"))
	}
	return nil
}

func logind(t *testing.T, address string) *fakeLogind {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &fakeLogind{address: address, conn: conn}
	seatPath := dbus.ObjectPath("/org/freedesktop/login1/seat/seat0")
	for _, e := range []struct {
		v     any
		path  dbus.ObjectPath
		iface string
	}{{manager{f}, managerPath, managerIface}, {session{f}, sessionPath, sessionIface}, {seat{f}, seatPath, seatIface}} {
		if err := conn.Export(e.v, e.path, e.iface); err != nil {
			t.Fatal(err)
		}
	}
	f.props, err = prop.Export(conn, sessionPath, prop.Map{sessionIface: {
		"Seat": {Value: struct {
			ID   string
			Path dbus.ObjectPath
		}{"seat0", seatPath}, Emit: prop.EmitTrue},
		"Active": {Value: true, Emit: prop.EmitTrue},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(login1, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", login1, reply, err)
	}
	return f
}

type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
	return nil
}

func (e *events) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.log)
}

func (e *events) DevicePaused(d Device) error { return e.add("paused") }

func (e *events) DeviceResumed(d Device, fd int) error {
	if fd < 0 {
		return errors.New("resumed without an fd")
	}
	return e.add("resumed")
}

func (e *events) PrepareForSleep(sleeping bool) error {
	if sleeping {
		return e.add("sleep")
	}
	return e.add("wake")
}

func (e *events) ActiveChanged(active bool) error {
	if active {
		return e.add("active")
	}
	return e.add("inactive")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func openLogind(t *testing.T) (*fakeLogind, *loop.Loop, *Logind, *events) {
	t.Helper()
	address := testenv.DBus(t)
	f := logind(t, address)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	ev := &events{}
	lg, err := Open(l, address, func(k string) string {
		if k == "XDG_SESSION_ID" {
			return "1"
		}
		return ""
	}, ev)
	if err != nil {
		t.Fatal(err)
	}
	return f, l, lg, ev
}

func run(t *testing.T, l *loop.Loop) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		_ = l.Post(func() error {
			l.Stop()
			return nil
		})
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

func TestLogindSession(t *testing.T) {
	f, l, lg, ev := openLogind(t)
	run(t, l)
	if !lg.Active() || lg.Seat() != "seat0" {
		t.Fatalf("active %v on seat %q", lg.Active(), lg.Seat())
	}
	fd, dev, err := lg.TakeDevice(os.DevNull)
	if err != nil || fd < 0 || dev != (Device{Major: 1, Minor: 3}) {
		t.Fatalf("TakeDevice: %d %+v %v", fd, dev, err)
	}
	if err := errors.Join(lg.ReleaseDevice(dev), lg.SwitchTo(2)); err != nil {
		t.Fatal(err)
	}
	inhibitor, err := lg.InhibitLidSwitch()
	if err != nil {
		t.Fatal(err)
	}
	if err := inhibitor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lg.SwitchTo(0); err == nil {
		t.Fatal("a refused VT switch succeeded")
	}
	if _, _, err := lg.TakeDevice("/"); err == nil {
		t.Fatal("a directory was taken as a device")
	}
	forger, err := dbus.Connect(f.address)
	if err != nil {
		t.Fatal(err)
	}
	defer forger.Close()
	if err := errors.Join(
		forger.Emit(sessionPath, sessionIface+".PauseDevice", uint32(1), uint32(3), "pause"),
		forger.Emit(sessionPath, propsIface+".PropertiesChanged", sessionIface, map[string]dbus.Variant{"Active": dbus.MakeVariant(false)}, []string{}),
		forger.Emit(managerPath, managerIface+".PrepareForSleep", true),
	); err != nil {
		t.Fatal(err)
	}
	for _, sig := range []struct {
		name string
		args []any
	}{
		{"PauseDevice", []any{uint32(1), uint32(3), "pause"}},
		{"PauseDevice", []any{uint32(1), uint32(3), "gone"}},
		{"ResumeDevice", []any{uint32(1), uint32(3), dbus.UnixFD(0)}},
	} {
		if err := f.conn.Emit(sessionPath, sessionIface+"."+sig.name, sig.args...); err != nil {
			t.Fatal(err)
		}
	}
	f.props.SetMust(sessionIface, "Active", false)
	for _, sleeping := range []bool{true, false} {
		if err := f.conn.Emit(managerPath, managerIface+".PrepareForSleep", sleeping); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the signals", func() bool { return len(ev.seen()) == 6 })
	if got := ev.seen(); !slices.Equal(got, []string{"paused", "paused", "resumed", "inactive", "sleep", "wake"}) {
		t.Fatalf("events %v", got)
	}
	waitFor(t, "the pause acknowledged", func() bool { return slices.Contains(f.seen(), "PauseDeviceComplete") })
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"GetSession 1", "TakeControl", "TakeDevice", "ReleaseDevice", "SwitchTo", "Inhibit handle-lid-switch block", "SwitchTo", "PauseDeviceComplete", "ReleaseControl"}
	if got := f.seen(); !slices.Equal(got, want) {
		t.Fatalf("logind saw %v, want %v", got, want)
	}
}

var acUnplug = [][]byte{
	udevMessage("ACTION=change", "SUBSYSTEM=power_supply", "DEVPATH=/devices/LNXSYSTM:00/ACPI0003:00/power_supply/AC", "POWER_SUPPLY_ONLINE=0"),
	udevMessage("ACTION=change", "SUBSYSTEM=power_supply", "DEVPATH=/devices/LNXSYSTM:00/PNP0C0A:00/power_supply/BAT0", "POWER_SUPPLY_STATUS=Discharging"),
}

func suspendOnPowerChanges(t *testing.T) (*fakeLogind, *events, func(msgs ...[]byte)) {
	t.Helper()
	f, l, lg, ev := openLogind(t)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[1]) })
	m, err := watch(l, fds[0], DefaultSeat, "/sys", []Subsystem{PowerSupply}, trustSelf, func(UdevEvent) error {
		if err := lg.Suspend(); err != nil {
			return ev.add(err.Error())
		}
		return ev.add("power")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	run(t, l)
	t.Cleanup(func() {
		if err := lg.Close(); err != nil {
			t.Error(err)
		}
	})
	return f, ev, func(msgs ...[]byte) { sendUevents(t, fds[1], msgs...) }
}

func tally(log []string) map[string]int {
	n := map[string]int{}
	for _, s := range log {
		n[s]++
	}
	return n
}

func expectLog(t *testing.T, ev *events, want map[string]int) {
	t.Helper()
	total := 0
	for _, n := range want {
		total += n
	}
	waitFor(t, fmt.Sprintf("%d events", total), func() bool { return len(ev.seen()) >= total })
	if got := tally(ev.seen()); !maps.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}

func TestAnUnplugSuspendsOnceUntilTheSleepEnds(t *testing.T) {
	f, ev, send := suspendOnPowerChanges(t)
	suspends := func() int { return tally(f.seen())["Suspend false"] }

	send(acUnplug...)
	expectLog(t, ev, map[string]int{"power": 2, "sleep": 1})
	if n := suspends(); n != 1 {
		t.Fatalf("the AC and battery uevents of one unplug called Suspend %d times, want 1", n)
	}

	if err := f.wake(); err != nil {
		t.Fatal(err)
	}
	expectLog(t, ev, map[string]int{"power": 2, "sleep": 1, "wake": 1})
	send(acUnplug...)
	expectLog(t, ev, map[string]int{"power": 4, "sleep": 2, "wake": 1})
	if n := suspends(); n != 2 {
		t.Fatalf("an unplug after waking called Suspend %d times in all, want 2", n)
	}
}

func TestARefusedSuspendLeavesNoSleepPending(t *testing.T) {
	f, ev, send := suspendOnPowerChanges(t)
	f.refuse(dbus.NewError(operationInProgress, []any{"There's already a shutdown or sleep operation in progress"}))
	send(acUnplug[0])
	waitFor(t, "the refusal", func() bool { return len(ev.seen()) == 1 })
	if got := ev.seen()[0]; !strings.Contains(got, operationInProgress) {
		t.Fatalf("a refused Suspend reported %q, want logind's %s", got, operationInProgress)
	}

	f.refuse(nil)
	send(acUnplug[0])
	waitFor(t, "the suspend after the refusal", func() bool { return len(ev.seen()) == 3 })
	if got := tally(ev.seen()[1:]); !maps.Equal(got, map[string]int{"power": 1, "sleep": 1}) {
		t.Fatalf("after a refusal: events %v", got)
	}
	if n := tally(f.seen())["Suspend false"]; n != 2 {
		t.Fatalf("logind saw %d Suspend calls, want 2", n)
	}
}
