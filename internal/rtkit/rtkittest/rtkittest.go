// Package rtkittest serves a fake RealtimeKit on a private bus, for tests of
// code that asks rtkit for a thread's priority.
package rtkittest

import (
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/prop"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/rtkit"
)

const RTTimeUSecMax = 200000

type Method uint8

const (
	Realtime Method = iota + 1
	HighPriority
)

type Request struct {
	Method Method
	TID    int
	Value  int64
	RTTime unix.Rlimit
}

type Grant func(Request) *dbus.Error

type kitObject struct{ grant Grant }

func (k kitObject) answer(method Method, tid uint64, value int64) *dbus.Error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTTIME, &limit); err != nil {
		return dbus.MakeFailedError(err)
	}
	return k.grant(Request{Method: method, TID: int(tid), Value: value, RTTime: limit}) //nolint:gosec // thread IDs fit an int
}

func (k kitObject) MakeThreadRealtime(tid uint64, priority uint32) *dbus.Error {
	return k.answer(Realtime, tid, int64(priority))
}

func (k kitObject) MakeThreadHighPriority(tid uint64, nice int32) *dbus.Error {
	return k.answer(HighPriority, tid, int64(nice))
}

func Serve(t testing.TB, grant Grant) (address string) {
	t.Helper()
	address = dbustest.Start(t).Address
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatalf("connect the fake rtkit to %s: %v", address, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Export(kitObject{grant}, rtkit.Path, rtkit.Service); err != nil {
		t.Fatalf("export the fake rtkit: %v", err)
	}
	if _, err := prop.Export(conn, rtkit.Path, prop.Map{rtkit.Service: {
		"RTTimeUSecMax":       {Value: int64(RTTimeUSecMax)},
		"MaxRealtimePriority": {Value: int32(20)},
	}}); err != nil {
		t.Fatalf("export the fake rtkit's properties: %v", err)
	}
	if reply, err := conn.RequestName(rtkit.Service, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", rtkit.Service, reply, err)
	}
	return address
}
