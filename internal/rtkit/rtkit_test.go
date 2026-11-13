package rtkit_test

import (
	"sync"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/third_party/godbus"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/rtkit"
	"github.com/stubbedev/lys-wm/internal/rtkit/rtkittest"
)

type recorder struct {
	mu       sync.Mutex
	requests []rtkittest.Request
	refusal  *dbus.Error
}

func (r *recorder) grant(req rtkittest.Request) *dbus.Error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	return r.refusal
}

func (r *recorder) refuse(e *dbus.Error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusal = e
}

func (r *recorder) seen() []rtkittest.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rtkittest.Request(nil), r.requests...)
}

func serve(t *testing.T) (string, *recorder) {
	t.Helper()
	r := &recorder{}
	return rtkittest.Serve(t, r.grant), r
}

func rttime(t *testing.T) unix.Rlimit {
	t.Helper()
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTTIME, &limit); err != nil {
		t.Fatal(err)
	}
	return limit
}

func open(t *testing.T, address string) *rtkit.Kit {
	t.Helper()
	kit, err := rtkit.Open(address)
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

func TestTheRTTimeLimitKeepsBothLimitsWithinRtkitsWithoutLoweringTheSoftOneFurther(t *testing.T) {
	const unlimited = unix.RLIM_INFINITY
	for _, tc := range []struct{ was, want unix.Rlimit }{
		{unix.Rlimit{Cur: unlimited, Max: unlimited}, unix.Rlimit{Cur: 200000, Max: 200000}},
		{unix.Rlimit{Cur: 50000, Max: unlimited}, unix.Rlimit{Cur: 50000, Max: 200000}},
		{unix.Rlimit{Cur: 80000, Max: 80000}, unix.Rlimit{Cur: 80000, Max: 80000}},
		{unix.Rlimit{Cur: unlimited, Max: 300000}, unix.Rlimit{Cur: 200000, Max: 200000}},
		{unix.Rlimit{Cur: 100000, Max: 200000}, unix.Rlimit{Cur: 100000, Max: 200000}},
	} {
		if got := rtkit.RTTimeLimit(tc.was, rtkittest.RTTimeUSecMax); got != tc.want {
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
	if len(got) != 1 || got[0].Method != rtkittest.Realtime || got[0].TID != tid || got[0].Value != 1 {
		t.Fatalf("rtkit saw %+v, want SCHED_RR 1 for thread %d", got, tid)
	}
	if got[0].RTTime.Max > rtkittest.RTTimeUSecMax || got[0].RTTime.Cur > got[0].RTTime.Max {
		t.Fatalf("rtkit was asked with RLIMIT_RTTIME %+v, want a hard limit within %d µs and the soft one within it", got[0].RTTime, rtkittest.RTTimeUSecMax)
	}
	if now := rttime(t); now != got[0].RTTime {
		t.Fatalf("a granted request left RLIMIT_RTTIME at %+v, not the %+v rtkit granted it under", now, got[0].RTTime)
	}
}

func TestARefusedRealtimeRequestLeavesRTTimeWithinRtkits(t *testing.T) {
	address, f := serve(t)
	was := rttime(t)
	f.refuse(dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"Operation not permitted"}))
	if err := open(t, address).Realtime(unix.Gettid(), 1); err == nil {
		t.Fatal("a refused request reported success")
	}
	now := rttime(t)
	if want := min(was.Cur, now.Max); now.Cur != want || now.Max > rtkittest.RTTimeUSecMax {
		t.Fatalf("a refused request left RLIMIT_RTTIME at %+v, want soft %d under a hard limit within %d", now, want, rtkittest.RTTimeUSecMax)
	}
}

func TestAHighPriorityRequestCarriesTheNiceValue(t *testing.T) {
	address, f := serve(t)
	tid := unix.Gettid()
	if err := open(t, address).HighPriority(tid, -10); err != nil {
		t.Fatal(err)
	}
	if got := f.seen(); len(got) != 1 || got[0].Method != rtkittest.HighPriority || got[0].TID != tid || got[0].Value != -10 {
		t.Fatalf("rtkit saw %+v, want one nice -10 for thread %d", got, tid)
	}
}

func TestNoRtkitOnTheBusIsAnError(t *testing.T) {
	if err := open(t, dbustest.Start(t).Address).Realtime(unix.Gettid(), 1); err == nil {
		t.Fatal("a bus without rtkit granted SCHED_RR")
	}
}
