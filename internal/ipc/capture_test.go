package ipc

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

type fakeCapture struct {
	send    func(Event)
	applied []CaptureCommand
	closed  bool
}

func (c *fakeCapture) Apply(cmd CaptureCommand) error {
	if _, ok := cmd.(DisableCapture); ok && c.closed {
		return errors.New("applied after close")
	}
	c.applied = append(c.applied, cmd)
	if r, ok := cmd.(ReleaseCapture); ok && r.At != nil && r.At.X < 0 {
		return errors.New("the fake refuses a release left of 0")
	}
	return nil
}

func (c *fakeCapture) Close() error {
	c.closed = true
	return nil
}

func (f *fake) Capture(send func(Event)) (CaptureSession, error) {
	c := &fakeCapture{send: send}
	f.captures = append(f.captures, c)
	return c, nil
}

func TestParseCaptureCommand(t *testing.T) {
	ok := map[string]CaptureCommand{
		`{"command":"enable"}`:                 EnableCapture{},
		`{"command":"disable"}`:                DisableCapture{},
		`{"command":"release"}`:                ReleaseCapture{},
		`{"command":"barriers","barriers":[]}`: SetBarriers{Barriers: []Barrier{}},
		`{"command":"barriers","barriers":[{"id":1,"x1":1920,"y1":1079,"x2":1920,"y2":0},{"id":2,"x1":0,"y1":0,"x2":1919,"y2":0}]}`: SetBarriers{Barriers: []Barrier{
			{ID: 1, X1: 1920, Y1: 0, X2: 1920, Y2: 1079}, {ID: 2, X1: 0, Y1: 0, X2: 1919, Y2: 0},
		}},
	}
	for line, want := range ok {
		got, err := parseCaptureCommand([]byte(line))
		if err != nil {
			t.Errorf("%s: %v", line, err)
			continue
		}
		if g, w := describe(got), describe(want); g != w {
			t.Errorf("%s = %s, want %s", line, g, w)
		}
	}
	release, err := parseCaptureCommand([]byte(`{"command":"release","at":{"x":10.5,"y":20}}`))
	if r, isRelease := release.(ReleaseCapture); err != nil || !isRelease || r.At == nil || *r.At != (CapturePoint{X: 10.5, Y: 20}) {
		t.Errorf("release at a point = %#v, %v", release, err)
	}
	bad := map[string]string{
		`{"command":"enable","barriers":[]}`:      "only the barriers command takes barriers",
		`{"command":"barriers"}`:                  "only the barriers command takes barriers",
		`{"command":"enable","at":{"x":1,"y":1}}`: "only the release command takes a point",
		`{"command":"grab"}`:                      `unknown capture command "grab"`,
		`{"command":"enable","extra":1}`:          "unknown field",
		`{"command":"barriers","barriers":[{"id":0,"x1":0,"y1":0,"x2":0,"y2":9}]}`:                                      "has id 0",
		`{"command":"barriers","barriers":[{"id":3,"x1":0,"y1":0,"x2":9,"y2":9}]}`:                                      "barrier 3 from 0,0 to 9,9 is diagonal",
		`{"command":"barriers","barriers":[{"id":4,"x1":5,"y1":5,"x2":5,"y2":5}]}`:                                      "barrier 4 is a single point",
		`{"command":"barriers","barriers":[{"id":5,"x1":0,"y1":0,"x2":0,"y2":9},{"id":5,"x1":9,"y1":0,"x2":9,"y2":9}]}`: "barrier id 5 given twice",
	}
	for line, want := range bad {
		_, err := parseCaptureCommand([]byte(line))
		if !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s = %v, want a bad request saying %q", line, err, want)
		}
	}
	for _, at := range []CapturePoint{{X: math.NaN()}, {Y: math.Inf(1)}} {
		line, err := EncodeCaptureCommand(ReleaseCapture{At: &at})
		if err == nil {
			_, err = parseCaptureCommand(line)
		}
		if err == nil {
			t.Errorf("release at %v was taken", at)
		}
	}
}

func describe(cmd CaptureCommand) string {
	line, err := EncodeCaptureCommand(cmd)
	if err != nil {
		return err.Error()
	}
	return strings.TrimSpace(string(line))
}

func TestEveryCaptureCommandRoundTrips(t *testing.T) {
	for _, cmd := range []CaptureCommand{
		SetBarriers{Barriers: []Barrier{{ID: 7, X1: 0, Y1: 0, X2: 0, Y2: 99}}},
		SetBarriers{},
		EnableCapture{},
		DisableCapture{},
		ReleaseCapture{},
		ReleaseCapture{At: &CapturePoint{X: 1, Y: 2}},
	} {
		line, err := EncodeCaptureCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		back, err := parseCaptureCommand(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if describe(back) != describe(cmd) {
			t.Errorf("%s came back as %s", describe(cmd), describe(back))
		}
	}
}

func (r *rig) captureLines(c *CaptureConn) <-chan string {
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		for {
			line, err := c.Next()
			if err != nil {
				return
			}
			lines <- strings.TrimSpace(string(line))
		}
	}()
	return lines
}

func (r *rig) capture() (*CaptureConn, <-chan string, *fakeCapture) {
	r.t.Helper()
	c, err := OpenCapture(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = c.Close() })
	var fc *fakeCapture
	r.do(func(f *fake) { fc = f.captures[len(f.captures)-1] })
	return c, r.captureLines(c), fc
}

func (r *rig) applied(fc *fakeCapture, n int) []CaptureCommand {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got []CaptureCommand
		r.do(func(*fake) { got = append(got, fc.applied...) })
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("%d capture commands applied, want %d", len(got), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *rig) closedCapture(fc *fakeCapture) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var closed bool
		r.do(func(*fake) { closed = fc.closed })
		if closed {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatal("the capture session was never closed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestACaptureCarriesCommandsInAndEventsOut(t *testing.T) {
	r := start(t)
	c, lines, fc := r.capture()
	for _, cmd := range []CaptureCommand{SetBarriers{Barriers: []Barrier{{ID: 1, X1: 9, Y1: 0, X2: 9, Y2: 5}}}, EnableCapture{}, ReleaseCapture{At: &CapturePoint{X: 3, Y: 4}}} {
		if err := c.Send(cmd); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if l := next(t, lines); l != `{"event":"applied"}` {
			t.Fatalf("event %s, want each command acknowledged", l)
		}
	}
	got := r.applied(fc, 3)
	if describe(got[0]) != `{"command":"barriers","barriers":[{"id":1,"x1":9,"y1":0,"x2":9,"y2":5}]}` || describe(got[2]) != `{"command":"release","at":{"x":3,"y":4}}` {
		t.Errorf("applied %v", got)
	}
	r.do(func(*fake) {
		fc.send(CaptureActivatedEvent{Activation: 4, At: CapturePoint{X: 9.5, Y: 2}, Barrier: 1})
		fc.send(CaptureKeyEvent{Key: 30, Pressed: true})
	})
	if l := next(t, lines); l != `{"event":"activated","activation":4,"at":{"x":9.5,"y":2},"barrier":1}` {
		t.Errorf("first event %s", l)
	}
	if l := next(t, lines); l != `{"event":"key","key":30,"pressed":true}` {
		t.Errorf("second event %s", l)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	r.closedCapture(fc)
}

func TestABadCaptureCommandEndsTheCaptureWithAnError(t *testing.T) {
	r := start(t)
	c, lines, fc := r.capture()
	if err := c.Send(EnableCapture{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.cn.c.Write([]byte(`{"command":"barriers","barriers":[{"id":2,"x1":0,"y1":0,"x2":3,"y2":3}]}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if l := next(t, lines); l != `{"event":"applied"}` {
		t.Errorf("event %s, want the enable acknowledged", l)
	}
	if l := next(t, lines); !strings.HasPrefix(l, `{"event":"error","error":"ipc: bad request: barrier 2 from 0,0 to 3,3 is diagonal"`) {
		t.Errorf("event %s, want the error", l)
	}
	if l, open := <-lines; open {
		t.Errorf("the stream went on after the error: %s", l)
	}
	r.closedCapture(fc)
	if got := r.applied(fc, 1); len(got) != 1 {
		t.Errorf("applied %v, want the enable alone", got)
	}
}

func TestARefusedCaptureCommandEndsTheCaptureWithItsError(t *testing.T) {
	r := start(t)
	c, lines, fc := r.capture()
	if err := c.Send(ReleaseCapture{At: &CapturePoint{X: -1, Y: 0}}); err != nil {
		t.Fatal(err)
	}
	if l := next(t, lines); l != `{"event":"error","error":"the fake refuses a release left of 0"}` {
		t.Errorf("event %s, want the session's error", l)
	}
	r.closedCapture(fc)
}

func TestDoRefusesACapture(t *testing.T) {
	r := start(t)
	if _, err := Do(r.path, Request{Request: KindCapture}); !errors.Is(err, ErrBadRequest) {
		t.Errorf("Do(capture) = %v, want a bad request pointing at OpenCapture", err)
	}
}
