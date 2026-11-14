package harness

import (
	"encoding/binary"
	"errors"
	"image"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/xsync"
)

const gtkHoldBound = 2 * time.Second

func xReport(t *testing.T) func(error) {
	return func(err error) { t.Errorf("xwayland reported: %v", err) }
}

func xTiled(n int) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		return len(windowsOf(c)) == n && !c.Holding()
	}
}

func (c *xclient) syncedWindow(rect image.Rectangle, pixel uint32) (xproto.Window, xsync.Counter) {
	c.t.Helper()
	if err := xsync.Init(c.x); err != nil {
		c.t.Fatal(err)
	}
	c.intern("WM_PROTOCOLS", "_NET_WM_SYNC_REQUEST", "_NET_WM_SYNC_REQUEST_COUNTER")
	id, err := c.x.NewId()
	if err != nil {
		c.t.Fatal(err)
	}
	counter := xsync.Counter(id)
	xsync.CreateCounter(c.x, counter, 0)
	win := c.window(rect, pixel, false, map[string][]byte{
		"WM_CLASS":                     []byte("synced\x00XTest\x00"),
		"WM_PROTOCOLS":                 c.atom("_NET_WM_SYNC_REQUEST"),
		"_NET_WM_SYNC_REQUEST_COUNTER": binary.LittleEndian.AppendUint32(nil, uint32(counter)),
	})
	return win, counter
}

func (c *xclient) syncRequest() uint64 {
	c.t.Helper()
	for {
		ev, open := c.event(clientTimeout)
		if !open {
			c.t.Fatal("the X connection closed before a sync request")
		}
		m, ok := ev.(xproto.ClientMessageEvent)
		if ok && m.Type == c.atoms["WM_PROTOCOLS"] && xproto.Atom(m.Data.Data32[0]) == c.atoms["_NET_WM_SYNC_REQUEST"] {
			return uint64(m.Data.Data32[3])<<32 | uint64(m.Data.Data32[2])
		}
	}
}

func TestAnXWindowLandsOnItsCommitAtTheNewSize(t *testing.T) {
	t.Parallel()
	h := StartXwaylandWith(t, heldForever, xReport(t), small)
	xc := xDial(t, h)
	xc.window(image.Rect(0, 0, 90, 70), 0x00ff00, false, map[string][]byte{"WM_CLASS": []byte("plain\x00XTest\x00")})
	h.Eventually("the X window drawn whole", showing(150, 50, greenColor))
	_, _ = open(t, h, "right", fillBlue)
	h.Eventually("the X window's resize landed", xTiled(2))
	h.Eventually("the X window on the left", showing(70, 50, greenColor))
	h.Eventually("the Wayland window on the right", showing(150, 50, blueColor))
	if s := h.holds(); s.Expired != 0 {
		t.Errorf("holds %+v, want none expired", s)
	}
}

func TestAnXWindowWithASyncCounterIsWaitedFor(t *testing.T) {
	t.Parallel()
	h := StartXwaylandWith(t, heldForever, xReport(t), small)
	xc := xDial(t, h)
	_, counter := xc.syncedWindow(image.Rect(0, 0, 90, 70), 0x00ff00)
	first := xc.syncRequest()
	xsync.SetCounter(xc.x, counter, first)
	h.Eventually("the X window drawn whole", xTiled(1))
	h.Eventually("the X window drawn whole", showing(150, 50, greenColor))

	_, _ = open(t, h, "right", fillBlue)
	h.Eventually("the change held", holding)
	value := xc.syncRequest()
	h.Advance(50 * time.Millisecond)
	if !h.compositorHolding() {
		t.Fatal("the change landed before the client said it had drawn")
	}
	h.stillShows("the old layout stands", 150, 50, greenColor)
	xsync.SetCounter(xc.x, counter, value)
	h.Eventually("the counter landed the change", landed)
	h.Eventually("the X window on the left", showing(70, 50, greenColor))
	h.Eventually("the Wayland window on the right", showing(150, 50, blueColor))
}

func TestAnXWindowThatNeverSyncsHoldsOnlyOnce(t *testing.T) {
	t.Parallel()
	h := StartXwaylandWith(t, flat, xReport(t), small)
	xc := xDial(t, h)
	_, _ = xc.syncedWindow(image.Rect(0, 0, 90, 70), 0x00ff00)
	h.Eventually("the X window placed", xTiled(1))
	before := h.holds().Expired
	cr, right := open(t, h, "right", fillBlue)
	h.Eventually("the bound over", xTiled(2))
	if got := h.holds().Expired; got > before+1 {
		t.Fatalf("%d holds expired, want one", got-before)
	}
	expired := h.holds().Expired
	_, _ = open(t, h, "third", fillRed)
	cr.DispatchUntil("right answers", func() bool { return right.Logical.X < 80 })
	cr.Sync()
	h.Eventually("the next change landed", xTiled(3))
	if got := h.holds().Expired; got != expired {
		t.Errorf("%d more holds expired: a counter that missed once is not waited for", got-expired)
	}
}

const syncQueryCounter = 5

func (c *xclient) counterValue(id xsync.Counter) uint64 {
	c.t.Helper()
	reply, err := xproto.QueryExtension(c.x, uint16(len("SYNC")), "SYNC").Reply()
	if err != nil {
		c.t.Fatal(err)
	}
	request := []byte{reply.MajorOpcode, syncQueryCounter, 2, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(request[4:], uint32(id))
	cookie := c.x.NewCookie(true, true)
	c.x.NewRequest(request, cookie)
	answer, err := cookie.Reply()
	if err != nil {
		c.t.Fatalf("query counter %#x: %v", uint32(id), err)
	}
	return uint64(binary.LittleEndian.Uint32(answer[8:]))<<32 | uint64(binary.LittleEndian.Uint32(answer[12:]))
}

func (c *xclient) cardinalProperty(win xproto.Window, name string) []uint32 {
	c.t.Helper()
	c.intern(name)
	reply, err := xproto.GetProperty(c.x, false, win, c.atoms[name], xproto.GetPropertyTypeAny, 0, 16).Reply()
	if err != nil {
		c.t.Fatal(err)
	}
	out := make([]uint32, reply.ValueLen)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(reply.Value[4*i:])
	}
	return out
}

func TestARealGTKWindowAnswersItsHolds(t *testing.T) {
	t.Parallel()
	demo := testenv.Require(t, "gtk3-demo")
	h := StartXwaylandWith(t, func(cfg *compositor.Config) {
		held(cfg)
		cfg.HoldBound = gtkHoldBound
	}, xReport(t), desktop)
	xc := xDial(t, h)
	socket := h.xSocket()
	gtk := h.Run("env", "GDK_BACKEND=x11", "DISPLAY="+socket, "GTK_A11Y=none", "NO_AT_BRIDGE=1", "DBUS_SESSION_BUS_ADDRESS=disabled:", demo)
	defer func() {
		if t.Failed() {
			t.Logf("gtk3-demo said:\n%s", gtk.output.String())
		}
	}()
	h.Eventually("the GTK window managed and settled", xTiled(1))

	clients := xc.cardinalProperty(xc.root, "_NET_CLIENT_LIST")
	if len(clients) != 1 {
		t.Fatalf("X clients = %#x, want the GTK window", clients)
	}
	win := xproto.Window(clients[0])
	counters := xc.cardinalProperty(win, "_NET_WM_SYNC_REQUEST_COUNTER")
	if len(counters) == 0 {
		t.Fatal("GTK did not publish _NET_WM_SYNC_REQUEST_COUNTER")
	}
	counter := xsync.Counter(counters[0])
	t.Logf("GTK's counters: %#x", counters)

	answers := func(what string, change func()) {
		t.Helper()
		before, value := h.holds(), xc.counterValue(counter)
		change()
		h.Eventually(what+": the hold landed", func(_ *headless.Backend, c *compositor.Compositor) bool {
			return c.Holds().Landed > before.Landed && !c.Holding()
		})
		after := h.holds()
		if after.Expired != before.Expired {
			t.Errorf("%s: holds went from %+v to %+v: GTK let the hold expire", what, before, after)
		}
		if got := xc.counterValue(counter); got <= value {
			t.Errorf("%s: GTK's counter stayed at %d, it never answered the sync request", what, got)
		}
		t.Logf("%s: holds %+v, counter %d", what, after, xc.counterValue(counter))
	}
	var right *testclient.Window
	var cr *testclient.Client
	answers("a window opens beside it", func() { cr, right = open(t, h, "right", fillBlue) })
	h.Eventually("both tiled", xTiled(2))
	var rect wmipc.Rect
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		for _, w := range windowsOf(c) {
			if w.AppID != "lys-wm.testclient" {
				rect = w.Rect
			}
		}
		return nil
	})
	if got := xc.geometry(win); got.Dx() != rect.Width || got.Dy() != rect.Height {
		t.Errorf("the GTK window is %v, its tile %+v", got, rect)
	}
	answers("the window beside it closes", func() {
		if err := errors.Join(right.Destroy(), cr.Display.Flush()); err != nil {
			t.Fatal(err)
		}
	})
	h.Eventually("the GTK window alone again", xTiled(1))

	if err := gtk.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	before := h.holds()
	_, _ = open(t, h, "beside the stopped one", fillBlue)
	h.Eventually("the stopped GTK window's hold to expire", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Holds().Expired == before.Expired+1
	})
}
