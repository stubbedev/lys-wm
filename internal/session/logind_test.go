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

	"github.com/stubbedev/lys-lib/logind"
	"github.com/stubbedev/lys-lib/logind/logindtest"
	"github.com/stubbedev/lys-lib/third_party/godbus"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
)

const own logind.SessionID = "1"

var null = Device{Major: 1, Minor: 3}

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
	return errors.Join(unix.Close(fd), e.add("resumed"))
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

func openLogind(t *testing.T, env map[string]string) (*logindtest.Logind, *loop.Loop, *Logind, *events) {
	t.Helper()
	fake := logindtest.Start(t, logindtest.Config{Own: own})
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
	lg, err := Open(l, fake.Bus.Address, func(k string) string { return env[k] }, ev)
	if err != nil {
		t.Fatal(err)
	}
	return fake, l, lg, ev
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
	fake, l, lg, ev := openLogind(t, map[string]string{"XDG_SESSION_ID": string(own)})
	run(t, l)
	if !lg.Active() || lg.Seat() != string(logindtest.Seat) {
		t.Fatalf("active %v on seat %q", lg.Active(), lg.Seat())
	}
	fd, dev, err := lg.TakeDevice(os.DevNull)
	if err != nil || fd < 0 || dev != null {
		t.Fatalf("TakeDevice: %d %+v %v", fd, dev, err)
	}
	if err := errors.Join(unix.Close(fd), lg.ReleaseDevice(dev), lg.SwitchTo(2)); err != nil {
		t.Fatal(err)
	}
	inhibitor, err := lg.InhibitLidSwitch()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the inhibitor held", func() bool { return fake.Inhibitors.Load() == 1 })
	if err := inhibitor.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the inhibitor released", func() bool { return fake.Inhibitors.Load() == 0 })
	if err := lg.SwitchTo(0); err == nil {
		t.Fatal("a refused VT switch succeeded")
	}
	if _, _, err := lg.TakeDevice("/"); err == nil {
		t.Fatal("a directory was taken as a device")
	}
	forger := fake.Bus.Conn(t)
	path := logind.SessionPath(own)
	if err := errors.Join(
		forger.Emit(path, logind.SessionInterface+".PauseDevice", uint32(1), uint32(3), "pause"),
		forger.Emit(path, "org.freedesktop.DBus.Properties.PropertiesChanged", logind.SessionInterface, map[string]dbus.Variant{"Active": dbus.MakeVariant(false)}, []string{}),
		forger.Emit(logind.ManagerPath, logind.ManagerInterface+".PrepareForSleep", true),
	); err != nil {
		t.Fatal(err)
	}
	fake.PauseDevice(t, own, null, logind.PausePause)
	fake.PauseDevice(t, own, null, logind.PauseGone)
	fake.ResumeDevice(t, own, null)
	fake.SetActive(t, own, false)
	fake.PrepareForSleep(t, true)
	fake.PrepareForSleep(t, false)
	waitFor(t, "the signals", func() bool { return len(ev.seen()) == 6 })
	if got := ev.seen(); !slices.Equal(got, []string{"paused", "paused", "resumed", "inactive", "sleep", "wake"}) {
		t.Fatalf("events %v", got)
	}
	waitFor(t, "the pause acknowledged", func() bool { return len(fake.CallsTo("Session.PauseDeviceComplete")) == 1 })
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"Manager.GetSession", "Session.TakeControl", "Session.TakeDevice", "Session.ReleaseDevice", "Seat.SwitchTo", "Manager.Inhibit", "Seat.SwitchTo", "Session.PauseDeviceComplete", "Session.ReleaseControl"}
	if got := fake.Methods(); !slices.Equal(got, want) {
		t.Fatalf("logind saw %v, want %v", got, want)
	}
	if got := fake.CallsTo("Manager.GetSession")[0].Args; !slices.Equal(got, []any{string(own)}) {
		t.Errorf("GetSession%v, want XDG_SESSION_ID", got)
	}
	if got := fake.CallsTo("Manager.Inhibit")[0].Args; got[0] != "handle-lid-switch" || got[3] != "block" {
		t.Errorf("Inhibit%v, want a handle-lid-switch block", got)
	}
}

func TestWithoutXDGSessionIDTheSessionIsThisProcesss(t *testing.T) {
	fake, _, lg, _ := openLogind(t, nil)
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	calls := fake.CallsTo("Manager.GetSessionByPID")
	if len(calls) != 1 || !slices.Equal(calls[0].Args, []any{uint32(os.Getpid())}) {
		t.Fatalf("GetSessionByPID calls %v, want one for pid %d", calls, os.Getpid())
	}
	if got := fake.CallsTo("Manager.GetSession"); len(got) != 0 {
		t.Errorf("GetSession calls %v without XDG_SESSION_ID", got)
	}
}

var acUnplug = [][]byte{
	udevMessage("ACTION=change", "SUBSYSTEM=power_supply", "DEVPATH=/devices/LNXSYSTM:00/ACPI0003:00/power_supply/AC", "POWER_SUPPLY_ONLINE=0"),
	udevMessage("ACTION=change", "SUBSYSTEM=power_supply", "DEVPATH=/devices/LNXSYSTM:00/PNP0C0A:00/power_supply/BAT0", "POWER_SUPPLY_STATUS=Discharging"),
}

func suspendOnPowerChanges(t *testing.T) (*logindtest.Logind, *events, func(msgs ...[]byte)) {
	t.Helper()
	f, l, lg, ev := openLogind(t, map[string]string{"XDG_SESSION_ID": string(own)})
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

func suspends(f *logindtest.Logind) int {
	n := 0
	for _, c := range f.CallsTo("Manager.Suspend") {
		if slices.Equal(c.Args, []any{false}) {
			n++
		}
	}
	return n
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

	send(acUnplug...)
	expectLog(t, ev, map[string]int{"power": 2, "sleep": 1})
	if n := suspends(f); n != 1 {
		t.Fatalf("the AC and battery uevents of one unplug called Suspend %d times, want 1", n)
	}

	f.PrepareForSleep(t, false)
	expectLog(t, ev, map[string]int{"power": 2, "sleep": 1, "wake": 1})
	send(acUnplug...)
	expectLog(t, ev, map[string]int{"power": 4, "sleep": 2, "wake": 1})
	if n := suspends(f); n != 2 {
		t.Fatalf("an unplug after waking called Suspend %d times in all, want 2", n)
	}
}

func TestARefusedSuspendLeavesNoSleepPending(t *testing.T) {
	f, ev, send := suspendOnPowerChanges(t)
	f.Refuse("Manager.Suspend", dbus.NewError(logind.ErrOperationInProgress, []any{"There's already a shutdown or sleep operation in progress"}))
	send(acUnplug[0])
	waitFor(t, "the refusal", func() bool { return len(ev.seen()) == 1 })
	if got := ev.seen()[0]; !strings.Contains(got, logind.ErrOperationInProgress) {
		t.Fatalf("a refused Suspend reported %q, want logind's %s", got, logind.ErrOperationInProgress)
	}

	f.Refuse("Manager.Suspend", nil)
	send(acUnplug[0])
	waitFor(t, "the suspend after the refusal", func() bool { return len(ev.seen()) == 3 })
	if got := tally(ev.seen()[1:]); !maps.Equal(got, map[string]int{"power": 1, "sleep": 1}) {
		t.Fatalf("after a refusal: events %v", got)
	}
	if n := suspends(f); n != 2 {
		t.Fatalf("logind saw %d Suspend calls, want 2", n)
	}
}
