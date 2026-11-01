package main

import (
	"runtime"
	"testing"

	"golang.org/x/sys/unix"

	spt "github.com/stubbedev/lys-wm/internal/protocol/server/presentationtime"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/wire"
)

func feedbackCycle(t testing.TB, p *peer) {
	fb, err := spt.CreateWpPresentationFeedback(p, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := fb.SendPresented(0, 1, 2, 3, 4, 5, spt.WpPresentationFeedbackKindVsync); err != nil {
		t.Fatal(err)
	}
}

func callbackCycle(t testing.TB, p *peer) {
	cb, err := sw.CreateWlCallback(p, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb.SendDone(1); err != nil {
		t.Fatal(err)
	}
}

func TestObjectsAnEventEndsDoNotAllocate(t *testing.T) {
	server, _ := connect(t)
	if n := testing.AllocsPerRun(1000, func() { feedbackCycle(t, server) }); n != 0 {
		t.Errorf("a presentation feedback allocated %v times", n)
	}
	if n := testing.AllocsPerRun(1000, func() { callbackCycle(t, server) }); n != 0 {
		t.Errorf("a callback allocated %v times", n)
	}
}

func TestAReusedObjectSendsWithItsOwnID(t *testing.T) {
	server, _ := connect(t)
	first, err := sw.CreateWlCallback(server, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SendDone(1); err != nil {
		t.Fatal(err)
	}
	second, err := sw.CreateWlCallback(server, 101, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Base().ID(); got != 101 || second.Base().Removed() {
		t.Fatalf("the reused object has id %d, removed %v", got, second.Base().Removed())
	}
	if _, ok := server.objects[100]; ok {
		t.Fatal("the finished object is still registered")
	}
	if server.objects[101] != second {
		t.Fatal("the new object is not registered")
	}
}

func BenchmarkFeedbackCycle(b *testing.B) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = unix.Close(fds[1]) }()
	go func() {
		buf := make([]byte, 65536)
		for {
			if n, err := unix.Read(fds[1], buf); n <= 0 || err != nil {
				return
			}
		}
	}()
	conn := wire.NewConn(fds[0])
	defer func() { _ = conn.Close() }()
	server := &peer{conn: conn, objects: map[wire.ObjectID]wire.Resource{}}
	b.ReportAllocs()
	for i := range b.N {
		feedbackCycle(b, server)
		server.removed = server.removed[:0]
		if i%64 == 0 {
			for conn.Flush() != nil {
				runtime.Gosched()
			}
		}
	}
}
