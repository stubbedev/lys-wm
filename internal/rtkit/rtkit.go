// Package rtkit asks RealtimeKit on the system bus to raise a thread's
// scheduling priority, for a process that has neither the limit nor the
// capability to raise it itself.
package rtkit

import (
	"context"
	"fmt"
	"time"

	"github.com/stubbedev/lys-lib/third_party/godbus"
	"golang.org/x/sys/unix"
)

const (
	Service     = "org.freedesktop.RealtimeKit1"
	Path        = dbus.ObjectPath("/org/freedesktop/RealtimeKit1")
	callTimeout = 5 * time.Second
)

type Kit struct {
	conn *dbus.Conn
	obj  dbus.BusObject
}

func Open(address string) (*Kit, error) {
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("rtkit: connect to the system bus at %s: %w", address, err)
	}
	return &Kit{conn: conn, obj: conn.Object(Service, Path)}, nil
}

func (k *Kit) Close() error {
	return k.conn.Close()
}

func (k *Kit) call(method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return k.obj.CallWithContext(ctx, method, 0, args...)
}

func (k *Kit) Realtime(tid int, priority uint32) error {
	if err := k.limitRTTime(); err != nil {
		return err
	}
	if err := k.call(Service+".MakeThreadRealtime", uint64(tid), priority).Err; err != nil { //nolint:gosec // a thread ID is positive
		return fmt.Errorf("rtkit: SCHED_RR %d for thread %d: %w", priority, tid, err)
	}
	return nil
}

func (k *Kit) HighPriority(tid int, nice int32) error {
	if err := k.call(Service+".MakeThreadHighPriority", uint64(tid), nice).Err; err != nil { //nolint:gosec // a thread ID is positive
		return fmt.Errorf("rtkit: nice %d for thread %d: %w", nice, tid, err)
	}
	return nil
}

func (k *Kit) limitRTTime() error {
	var v dbus.Variant
	if err := k.call("org.freedesktop.DBus.Properties.Get", Service, "RTTimeUSecMax").Store(&v); err != nil {
		return fmt.Errorf("rtkit: read RTTimeUSecMax: %w", err)
	}
	var most int64
	if err := v.Store(&most); err != nil {
		return fmt.Errorf("rtkit: decode RTTimeUSecMax %v: %w", v, err)
	}
	if most <= 0 {
		return fmt.Errorf("rtkit: RTTimeUSecMax is %d µs", most)
	}
	var was unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTTIME, &was); err != nil {
		return fmt.Errorf("rtkit: read RLIMIT_RTTIME: %w", err)
	}
	limit := rttimeLimit(was, uint64(most))
	if limit == was {
		return nil
	}
	if err := unix.Setrlimit(unix.RLIMIT_RTTIME, &limit); err != nil {
		return fmt.Errorf("rtkit: limit RLIMIT_RTTIME to %d/%d µs for rtkit's %d: %w", limit.Cur, limit.Max, most, err)
	}
	return nil
}

func rttimeLimit(was unix.Rlimit, most uint64) unix.Rlimit {
	hard := min(was.Max, most)
	return unix.Rlimit{Cur: min(was.Cur, hard), Max: hard}
}
