package xgb

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

const hangLimit = 10 * time.Second

func setupReply() []byte {
	reply := make([]byte, 32)
	reply[0] = 1
	binary.LittleEndian.PutUint16(reply[2:], 11)
	binary.LittleEndian.PutUint16(reply[6:], 6)
	binary.LittleEndian.PutUint32(reply[16:], 0xffffffff)
	return reply
}

func connectTo(t *testing.T, limit time.Duration, serve func(net.Conn)) *Conn {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { _ = theirs.Close() })
	go func() {
		request := make([]byte, 12)
		if _, err := io.ReadFull(theirs, request); err != nil {
			return
		}
		if _, err := theirs.Write(setupReply()); err != nil {
			return
		}
		serve(theirs)
	}()
	c := &Conn{serverLimit: limit}
	if err := c.connectNet(ours); err != nil {
		t.Fatal(err)
	}
	if _, err := postNewConn(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func within(t *testing.T, what string, run func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
	}()
	select {
	case <-done:
	case <-time.After(hangLimit):
		t.Fatalf("%s hung", what)
	}
}

func unchecked(c *Conn) {
	c.NewRequest(make([]byte, 4), c.NewCookie(false, false))
}

func wedged(net.Conn) {}

func TestARequestDoesNotWaitForAServerThatStoppedReading(t *testing.T) {
	c := connectTo(t, time.Hour, wedged)
	within(t, "queueing requests behind a wedged server", func() {
		for range reqBuffer {
			unchecked(c)
		}
	})
}

func TestAServerThatStopsReadingLosesTheConnection(t *testing.T) {
	c := connectTo(t, time.Millisecond, wedged)
	within(t, "requests to a wedged server", func() {
		for range 2 * reqBuffer {
			unchecked(c)
		}
		<-c.doneSend
		<-c.doneRead
	})
	if ev, xerr := c.WaitForEvent(); ev != nil || xerr != nil {
		t.Fatalf("a dropped connection delivered %v, %v", ev, xerr)
	}
}

func TestAServerThatNeverAnswersARoundTripLosesTheConnection(t *testing.T) {
	c := connectTo(t, time.Millisecond, func(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	within(t, "more requests than the cookie queue holds", func() {
		for range cookieBuffer + reqBuffer {
			unchecked(c)
		}
		<-c.doneSend
		<-c.doneRead
	})
}

func TestQueuedRequestsReachTheServerInOrderBeforeClose(t *testing.T) {
	const requests = 8
	got := make(chan []byte, 1)
	c := connectTo(t, time.Hour, func(conn net.Conn) {
		var all []byte
		buf := make([]byte, 4)
		for {
			if _, err := io.ReadFull(conn, buf); err != nil {
				got <- all
				return
			}
			all = append(all, buf[0])
			if buf[0] == 43 {
				reply := make([]byte, 32)
				reply[0] = 1
				binary.LittleEndian.PutUint16(reply[2:], uint16(len(all)))
				if _, err := conn.Write(reply); err != nil {
					got <- all
					return
				}
			}
		}
	})
	for i := range requests {
		c.NewRequest([]byte{byte(i + 1), 0, 1, 0}, c.NewCookie(false, false))
	}
	within(t, "closing", c.Close)
	select {
	case all := <-got:
		if len(all) != requests+1 || all[0] != 1 || all[requests-1] != requests || all[requests] != 43 {
			t.Fatalf("the server read opcodes %v", all)
		}
	case <-time.After(hangLimit):
		t.Fatal("the server never saw the connection close")
	}
}

func TestAReplyMayBeAwaitedAsSoonAsTheRequestIsQueued(t *testing.T) {
	c := connectTo(t, time.Hour, func(conn net.Conn) {
		buf := make([]byte, 4)
		for seq := uint16(1); ; seq++ {
			if _, err := io.ReadFull(conn, buf); err != nil {
				return
			}
			reply := make([]byte, 32)
			reply[0] = 1
			binary.LittleEndian.PutUint16(reply[2:], seq)
			if _, err := conn.Write(reply); err != nil {
				return
			}
		}
	})
	within(t, "round trips", func() {
		for range 100 {
			c.Sync()
		}
	})
}
