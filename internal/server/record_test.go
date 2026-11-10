package server

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var recordedLine = regexp.MustCompile(`^\[ *([0-9.]+)\] \{([0-9]+)/([0-9]+)\} (.*)$`)

type dumpedLine struct {
	ms      float64
	conn    string
	message string
}

func (h *harness) dump() []dumpedLine {
	h.t.Helper()
	var text []byte
	var err error
	h.on(func() { text, err = h.disp.AppendProtocol(nil) })
	if err != nil {
		h.t.Fatal(err)
	}
	header, body, _ := strings.Cut(string(text), "\n")
	if !strings.HasPrefix(header, "# lys-wm protocol record of ") {
		h.t.Fatalf("dump starts with %q", header)
	}
	var lines []dumpedLine
	for l := range strings.Lines(body) {
		m := recordedLine.FindStringSubmatch(strings.TrimSuffix(l, "\n"))
		if m == nil {
			h.t.Fatalf("dump line %q is not a WAYLAND_DEBUG line", l)
		}
		ms, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			h.t.Fatal(err)
		}
		if m[2] != strconv.Itoa(os.Getpid()) {
			h.t.Errorf("line %q names pid %s, want this test's %d", l, m[2], os.Getpid())
		}
		lines = append(lines, dumpedLine{ms: ms, conn: m[3], message: m[4]})
	}
	return lines
}

func TestProtocolRecordKeepsTheNewestMessagesOfEveryClientInOrder(t *testing.T) {
	h := start(t)
	first := h.connect()
	h.registry(first)
	h.on(func() { h.disp.RecordProtocol(true) })
	second := h.connect()
	if err := second.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if err := first.Roundtrip(); err != nil {
		t.Fatal(err)
	}

	lines := h.dump()
	var got []string
	for _, l := range lines {
		got = append(got, l.conn+" "+l.message)
	}
	want := []string{
		"2 wl_display@1.sync(new id wl_callback@2)",
		"2  -> wl_callback@2.done(",
		"2  -> wl_display@1.delete_id(2)",
		"1 wl_display@1.sync(new id wl_callback@3)",
		"1  -> wl_callback@3.done(",
		"1  -> wl_display@1.delete_id(3)",
	}
	if len(got) != len(want) {
		t.Fatalf("dump has %d messages, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("message %d is %q, want it to start with %q", i, got[i], want[i])
		}
	}

	const roundtrips = recordedMessages/3 + 10
	for range roundtrips {
		if err := first.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	lines = h.dump()
	var firstConn []dumpedLine
	for i, l := range lines {
		if i > 0 && l.ms < lines[i-1].ms {
			t.Fatalf("message %d at %.3f ms comes after one at %.3f ms", i, l.ms, lines[i-1].ms)
		}
		if l.conn == "1" {
			firstConn = append(firstConn, l)
		}
	}
	if len(firstConn) != recordedMessages {
		t.Fatalf("the wrapped ring dumps %d messages of the first client, want %d", len(firstConn), recordedMessages)
	}
	roundtrip := []string{"wl_display@1.sync(new id wl_callback@3)", " -> wl_callback@3.done(", " -> wl_display@1.delete_id(3)"}
	dropped := len(roundtrip)*(1+roundtrips) - recordedMessages
	for i, l := range firstConn {
		want := roundtrip[(dropped+i)%len(roundtrip)]
		if !strings.HasPrefix(l.message, want) {
			t.Fatalf("message %d of the wrapped ring is %q, want %q: the oldest were not the ones dropped", i, l.message, want)
		}
	}
	if last := firstConn[len(firstConn)-1].message; last != " -> wl_display@1.delete_id(3)" {
		t.Errorf("the newest message is %q, want the last roundtrip's delete_id", last)
	}
	if n := len(lines) - len(firstConn); n != len(want)/2 {
		t.Errorf("the second client lost messages it never wrapped over: %d left, want %d", n, len(want)/2)
	}

	h.on(func() { h.disp.RecordProtocol(false) })
	var err error
	h.on(func() { _, err = h.disp.AppendProtocol(nil) })
	if err == nil || !strings.Contains(err.Error(), "debug protocol on") {
		t.Errorf("a dump with recording off returned %v, want it refused", err)
	}
	h.on(func() {
		for _, c := range h.disp.clients {
			if c.recorded != nil {
				t.Errorf("client %d still holds its ring after recording stopped", c.serial)
			}
		}
	})
}

func TestRecordedMessagesKeepTheirNamesAfterTheObjectIsGone(t *testing.T) {
	h := start(t)
	d := h.connect()
	h.on(func() { h.disp.RecordProtocol(true) })
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	lines := h.dump()
	if len(lines) == 0 {
		t.Fatal("nothing was recorded")
	}
	for _, l := range lines {
		if strings.Contains(l.message, "unknown@") {
			t.Errorf("%q lost its interface once its object was deleted", l.message)
		}
	}
}
