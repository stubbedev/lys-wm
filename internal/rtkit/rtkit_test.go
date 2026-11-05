package rtkit

import (
	"sync"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/prop"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/testenv"
)

const fakeRTTimeMax = 200000

type request struct {
	method string
	tid    uint64
	value  int64
	rttime unix.Rlimit
}

type fakeKit struct {
	mu       sync.Mutex
	requests []request
	refusal  *dbus.Error
}

func (f *fakeKit) record(method string, tid uint64, value int64) *dbus.Error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTTIME, &limit); err != nil {
		return dbus.MakeFailedError(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request{method, tid, value, limit})
	return f.refusal
}

func (f *fakeKit) refuse(e *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusal = e
}

func (f *fakeKit) seen() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

type kitObject struct{ f *fakeKit }

func (k kitObject) MakeThreadRealtime(tid uint64, priority uint32) *dbus.Error {
	return k.f.record("realtime", tid, int64(priority))
}

func (k kitObject) MakeThreadHighPriority(tid uint64, nice int32) *dbus.Error {
	return k.f.record("high", tid, int64(nice))
}

func serve(t *testing.T) (string, *fakeKit) {
	t.Helper()
	address := testenv.DBus(t)
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &fakeKit{}
	if err := conn.Export(kitObject{f}, Path, Service); err != nil {
		t.Fatal(err)
	}
	if _, err := prop.Export(conn, Path, prop.Map{Service: {
		"RTTimeUSecMax":       {Value: int64(fakeRTTimeMax)},
		"MaxRealtimePriority": {Value: int32(20)},
	}}); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(Service, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", Service, reply, err)
	}
	return address, f
}

func rttime(t *testing.T) unix.Rlimit {
	t.Helper()
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTTIME, &limit); err != nil {
		t.Fatal(err)
	}
	return limit
}

func open(t *testing.T, address string) *Kit {
	t.Helper()
	kit, err := Open(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := kit.Close(); err != nil {
			t.Error(err)
		}
	})
	return kit
}

func TestTheRTTimeLimitKeepsTheHardLimitWithinRtkitsAndTheSoftAtHalf(t *testing.T) {
	const unlimited = unix.RLIM_INFINITY
	for _, tc := range []struct{ was, want unix.Rlimit }{
		{unix.Rlimit{Cur: unlimited, Max: unlimited}, unix.Rlimit{Cur: 100000, Max: 200000}},
		{unix.Rlimit{Cur: 50000, Max: unlimited}, unix.Rlimit{Cur: 50000, Max: 200000}},
		{unix.Rlimit{Cur: 80000, Max: 80000}, unix.Rlimit{Cur: 40000, Max: 80000}},
		{unix.Rlimit{Cur: 100000, Max: 200000}, unix.Rlimit{Cur: 100000, Max: 200000}},
	} {
		if got := rttimeLimit(tc.was, fakeRTTimeMax); got != tc.want {
			t.Errorf("RLIMIT_RTTIME %+v became %+v, want %+v", tc.was, got, tc.want)
		}
	}
}

func TestARealtimeRequestLimitsRTTimeToWhatRtkitAllowsFirst(t *testing.T) {
	address, f := serve(t)
	tid := unix.Gettid()
	if err := open(t, address).Realtime(tid, 1); err != nil {
		t.Fatal(err)
	}
	got := f.seen()
	if len(got) != 1 || got[0].method != "realtime" || got[0].tid != uint64(tid) || got[0].value != 1 {
		t.Fatalf("rtkit saw %+v, want SCHED_RR 1 for thread %d", got, tid)
	}
	if got[0].rttime.Max > fakeRTTimeMax || got[0].rttime.Cur > got[0].rttime.Max/2 {
		t.Fatalf("rtkit was asked with RLIMIT_RTTIME %+v, want a hard limit within %d µs and a soft one at most half of it", got[0].rttime, fakeRTTimeMax)
	}
	if now := rttime(t); now != got[0].rttime {
		t.Fatalf("a granted request left RLIMIT_RTTIME at %+v, not the %+v rtkit granted it under", now, got[0].rttime)
	}
}

func TestARefusedRealtimeRequestPutsTheSoftRTTimeBack(t *testing.T) {
	address, f := serve(t)
	was := rttime(t)
	f.refuse(dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"Operation not permitted"}))
	if err := open(t, address).Realtime(unix.Gettid(), 1); err == nil {
		t.Fatal("a refused request reported success")
	}
	now := rttime(t)
	if want := min(was.Cur, now.Max); now.Cur != want || now.Max > fakeRTTimeMax {
		t.Fatalf("a refused request left RLIMIT_RTTIME at %+v, want soft %d under a hard limit within %d", now, want, fakeRTTimeMax)
	}
}

func TestAHighPriorityRequestCarriesTheNiceValue(t *testing.T) {
	address, f := serve(t)
	tid := unix.Gettid()
	if err := open(t, address).HighPriority(tid, -10); err != nil {
		t.Fatal(err)
	}
	if got := f.seen(); len(got) != 1 || got[0].method != "high" || got[0].tid != uint64(tid) || got[0].value != -10 {
		t.Fatalf("rtkit saw %+v, want one nice -10 for thread %d", got, tid)
	}
}

func TestNoRtkitOnTheBusIsAnError(t *testing.T) {
	if err := open(t, testenv.DBus(t)).Realtime(unix.Gettid(), 1); err == nil {
		t.Fatal("a bus without rtkit granted SCHED_RR")
	}
}
