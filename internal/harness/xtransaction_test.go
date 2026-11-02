package harness

import (
	"encoding/binary"
	"image"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/xsync"
)

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
	time.Sleep(50 * time.Millisecond)
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
