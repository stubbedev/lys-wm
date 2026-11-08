package harness

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
)

const rightEdge ipc.BarrierID = 7

type inputCaptureRig struct {
	*inputRig
	conn   *ipc.CaptureConn
	events <-chan string
}

func startInputCapture(t *testing.T) *inputCaptureRig {
	t.Helper()
	r := &inputCaptureRig{inputRig: inputOn(t, Start(t, small, small))}
	r.focused()
	conn, err := ipc.OpenCapture(r.h.IPCSocket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	events := make(chan string, 256)
	go func() {
		defer close(events)
		for {
			line, err := conn.Next()
			if err != nil {
				return
			}
			events <- strings.TrimSpace(string(line))
		}
	}()
	r.conn, r.events = conn, events
	if e := r.next(); !strings.HasPrefix(e, `{"event":"keymap","keymap":"xkb_keymap`) {
		t.Fatalf("the capture opened with %.80s, want the keymap", e)
	}
	r.send(ipc.SetBarriers{Barriers: []ipc.Barrier{{ID: rightEdge, X1: 320, Y1: 0, X2: 320, Y2: 99}}}, ipc.EnableCapture{})
	return r
}

func (r *inputCaptureRig) send(cmds ...ipc.CaptureCommand) []string {
	r.t.Helper()
	var before []string
	for _, cmd := range cmds {
		if err := r.conn.Send(cmd); err != nil {
			r.t.Fatal(err)
		}
		for e := r.next(); e != `{"event":"applied"}`; e = r.next() {
			before = append(before, e)
		}
	}
	return before
}

func (r *inputCaptureRig) next() string {
	r.t.Helper()
	select {
	case e, ok := <-r.events:
		if !ok {
			r.t.Fatal("the capture stream ended")
		}
		return e
	case <-time.After(clientTimeout):
		r.t.Fatal("no capture event")
	}
	return ""
}

func (r *inputCaptureRig) expectEvents(want ...string) {
	r.t.Helper()
	got := make([]string, 0, len(want))
	for range want {
		got = append(got, r.next())
	}
	if !slices.Equal(got, want) {
		r.t.Fatalf("capture events %q, want %q", got, want)
	}
}

func (r *inputCaptureRig) quiet() {
	r.t.Helper()
	r.h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	select {
	case e := <-r.events:
		r.t.Fatalf("an unexpected capture event %s", e)
	default:
	}
}

func (r *inputCaptureRig) pushThroughTheRightEdge() {
	r.t.Helper()
	r.moveOn(1, 159, 50)
	r.expect("leave win", "frame")
	r.nudge(5, 0)
	r.expectEvents(`{"event":"activated","activation":1,"at":{"x":324,"y":50},"barrier":7}`, `{"event":"modifiers","depressed":0,"latched":0,"locked":0,"group":0}`)
	r.expect("kb_leave win")
}

func TestAPushThroughABarrierCapturesTheKeyboardAndPointer(t *testing.T) {
	t.Parallel()
	r := startInputCapture(t)
	r.pushThroughTheRightEdge()

	r.h.tap(keyA)
	r.nudge(-3, 2)
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame(), b.Pointer().Axis(backend.Vertical, backend.Wheel, 15, 120), b.Pointer().Frame())
	})
	r.expectEvents(
		`{"event":"key","key":30,"pressed":true}`, `{"event":"key","key":30,"pressed":false}`,
		`{"event":"motion","dx":-3,"dy":2}`, `{"event":"frame"}`,
		`{"event":"button","button":272,"pressed":true}`, `{"event":"frame"}`,
		`{"event":"scroll_discrete","dx":0,"dy":120}`, `{"event":"frame"}`,
	)
	if x, y := r.cursor(); x != 319 || y != 50 {
		t.Errorf("the cursor moved to %g,%g under the capture", x, y)
	}
	r.c.Sync()
	if tail := r.in.Log[r.seen:]; len(tail) != 0 {
		t.Fatalf("the focused window saw %q while captured", tail)
	}

	if got := r.send(ipc.ReleaseCapture{At: &wmipc.CapturePoint{X: 80, Y: 50}}); !slices.Equal(got, []string{`{"event":"deactivated","activation":1,"at":{"x":80,"y":50}}`}) {
		t.Errorf("the release answered %q", got)
	}
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.nothingLeakedBeforeASentinel()
	if x, y := r.cursor(); x != 80 || y != 50 {
		t.Errorf("the release left the cursor at %g,%g, want 80,50", x, y)
	}
}

func TestABarrierNeedsEnablingAndIsInertOffItsLine(t *testing.T) {
	t.Parallel()
	r := startInputCapture(t)
	r.send(ipc.DisableCapture{})
	r.moveOn(1, 159, 50)
	r.expect("leave win", "frame")
	r.nudge(5, 0)
	r.quiet()

	r.send(ipc.SetBarriers{Barriers: []ipc.Barrier{{ID: rightEdge, X1: 320, Y1: 60, X2: 320, Y2: 99}}}, ipc.EnableCapture{})
	r.nudge(5, 0)
	r.quiet()
	r.h.tap(keyA)
	r.expect("key 30 1", "key 30 0")
}

func TestASessionLockDisablesAnActiveCapture(t *testing.T) {
	t.Parallel()
	r := startInputCapture(t)
	r.pushThroughTheRightEdge()
	lockSession(t, r.h)
	r.expectEvents(`{"event":"deactivated","activation":1,"at":{"x":319,"y":50}}`, `{"event":"disabled"}`)
	r.h.tap(keyA)
	r.nudge(5, 0)
	r.quiet()
}

func TestSuperEscapeAlwaysReleasesTheCapture(t *testing.T) {
	t.Parallel()
	r := startInputCapture(t)
	r.pushThroughTheRightEdge()
	r.h.key(keySuper, true)
	r.h.tap(keyEscape)
	r.h.key(keySuper, false)
	r.expectEvents(
		`{"event":"key","key":125,"pressed":true}`, `{"event":"modifiers","depressed":64,"latched":0,"locked":0,"group":0}`,
		`{"event":"deactivated","activation":1,"at":{"x":319,"y":50}}`, `{"event":"disabled"}`,
	)
	r.expect("kb_enter win", "key 125 0")
	r.nothingLeakedBeforeASentinel()

	r.nudge(5, 0)
	r.quiet()
	r.send(ipc.EnableCapture{})
	r.nudge(5, 0)
	r.expectEvents(`{"event":"activated","activation":2,"at":{"x":324,"y":50},"barrier":7}`, `{"event":"modifiers","depressed":0,"latched":0,"locked":0,"group":0}`)
}

func TestClosingTheCaptureConnectionReleases(t *testing.T) {
	t.Parallel()
	r := startInputCapture(t)
	r.pushThroughTheRightEdge()
	if err := r.conn.Close(); err != nil {
		t.Fatal(err)
	}
	r.expect("kb_enter win")
	r.nothingLeakedBeforeASentinel()
}

func TestNewBarriersEndTheActivationAndWaitForEnable(t *testing.T) {
	t.Parallel()
	r := startInputCapture(t)
	r.pushThroughTheRightEdge()
	if got := r.send(ipc.SetBarriers{Barriers: []ipc.Barrier{{ID: 9, X1: 0, Y1: 0, X2: 0, Y2: 99}}}); !slices.Equal(got, []string{`{"event":"deactivated","activation":1,"at":{"x":319,"y":50}}`}) {
		t.Errorf("the new barriers answered %q", got)
	}
	r.expect("kb_enter win")
	r.moveOn(0, 0, 50)
	r.nudge(-4, 0)
	r.quiet()
	r.send(ipc.EnableCapture{})
	r.nudge(-4, 0)
	var activated struct {
		Event   string
		Barrier ipc.BarrierID
	}
	if err := json.Unmarshal([]byte(r.next()), &activated); err != nil || activated.Event != "activated" || activated.Barrier != 9 {
		t.Fatalf("activated %+v (%v), want the left edge's barrier 9", activated, err)
	}
}
