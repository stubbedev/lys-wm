package harness

import (
	"errors"
	"fmt"
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	ceic "github.com/stubbedev/lys-wm/internal/protocol/client/extimagecopycapture"
	cldc "github.com/stubbedev/lys-wm/internal/protocol/client/lysdynamiccapture"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type countedView struct {
	captureView
	constraints int
}

func (v *countedView) BufferSize(s *ceic.ExtImageCopyCaptureSessionV1, w, h uint32) error {
	v.constraints++
	return v.captureView.BufferSize(s, w, h)
}

type dynamicRig struct {
	*captureRig
	session *ceic.ExtImageCopyCaptureSessionV1
	view    *countedView
}

func newDynamicRig(t *testing.T, h *Harness, c *testclient.Client) *dynamicRig {
	t.Helper()
	r := &dynamicRig{captureRig: newCaptureRig(t, h, c), view: &countedView{}}
	m, err := cldc.NewLysDynamicCaptureSourceManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	src, err := m.CreateSource()
	if err != nil {
		t.Fatal(err)
	}
	if r.session, err = r.mgr.CreateSession(r.view, src, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *dynamicRig) frame() (*capturedFrame, []byte) {
	r.t.Helper()
	r.c.DispatchUntil("the constraints", func() bool { return r.view.done })
	f, frame, pix := grab(r.t, r.c, r.session, r.view.size)
	r.c.DispatchUntil("a frame", func() bool { return f.ready || f.failed })
	if f.failed || r.view.stopped {
		r.t.Fatalf("frame %+v, session stopped %t", *f, r.view.stopped)
	}
	if err := errors.Join(frame.Destroy(), r.c.Display.Flush()); err != nil {
		r.t.Fatal(err)
	}
	return f, pix
}

func (r *dynamicRig) target(action string, size image.Point, constraints int) []byte {
	r.t.Helper()
	r.h.dispatch(action)
	if err := r.c.Display.Roundtrip(); err != nil {
		r.t.Fatal(err)
	}
	f, pix := r.frame()
	if r.view.size != size || r.view.constraints != constraints {
		r.t.Fatalf("after %q the session is %v after %d constraints, want %v after %d", action, r.view.size, r.view.constraints, size, constraints)
	}
	if f.damage != (image.Rectangle{Max: size}) {
		r.t.Fatalf("the first frame after %q has damage %v, want all of %v", action, f.damage, size)
	}
	return pix
}

func TestADynamicCaptureFollowsItsTargetWithoutStopping(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Rules = nil }, small)
	c := testclient.Connect(t, h.Socket())
	a := c.Window("a", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("a to map", a.Mapped)
	b := c.Window("b", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("b to map", b.Mapped)
	a.Stop()
	quiesce(t, h, c, b)
	windows := query[[]ipc.Window](h, ipc.KindWindows)
	idA, idB := windows[0].ID, windows[1].ID
	half := image.Pt(windows[0].Rect.Width, windows[0].Rect.Height)
	if half != image.Pt(windows[1].Rect.Width, windows[1].Rect.Height) {
		t.Fatalf("the windows tile at %+v", windows)
	}

	r := newDynamicRig(t, h, c)
	if r.view.done {
		t.Fatal("a dynamic session sent constraints before any target was chosen")
	}
	pix := r.target(fmt.Sprintf("casttarget window %d", idA), half, 1)
	if got := rgbAt(pix, half.X*4, half.X/2, half.Y/2); got != [3]byte{0xff, 0, 0} {
		t.Fatalf("the capture of window a shows %v", got)
	}
	r.target("casttarget output", small.Size, 2)
	a.Stop()
	if err := errors.Join(a.Redraw(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	f, _ := r.frame()
	if want := image.Rect(windows[0].Rect.X, windows[0].Rect.Y, windows[0].Rect.X+half.X, windows[0].Rect.Y+half.Y); f.damage != want {
		t.Fatalf("a later frame of the output target has damage %v, want only window a's %v", f.damage, want)
	}
	pix = r.target(fmt.Sprintf("casttarget window %d", idB), half, 3)
	if got := rgbAt(pix, half.X*4, half.X/2, half.Y/2); got != [3]byte{0, 0xff, 0} {
		t.Fatalf("the capture of window b shows %v", got)
	}
	settle(t, h)
	presents := presentCount(h)
	r.target(fmt.Sprintf("casttarget window %d", idA), half, 3)
	settle(t, h)
	if n := presentCount(h) - presents; n != 0 {
		t.Fatalf("switching the cast target presented %d frames on the output", n)
	}
	pix = r.target("casttarget clear", half, 3)
	if got := rgbAt(pix, half.X*4, half.X/2, half.Y/2); got != [3]byte{} {
		t.Fatalf("the capture of no target shows %v, want black", got)
	}
	r.waiting(r.session, half)
	if r.view.stopped {
		t.Fatal("the session stopped")
	}
}

func TestADynamicTargetClearsWhenItsWindowCloses(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	a := c.Window("a", quadrants)
	c.DispatchUntil("a to map", a.Mapped)
	r := newDynamicRig(t, h, c)
	h.dispatch("casttarget window")
	r.frame()
	if got := query[ipc.CastTarget](h, ipc.KindCastTarget); got.Kind != ipc.CastWindow || got.Window == 0 {
		t.Fatalf("the cast target is %+v", got)
	}
	lines := h.watch()
	if err := errors.Join(a.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	awaitStreamed(t, lines, "the target clearing", func(e streamed) bool { return e.Event == "casttarget" })
	if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: "submap done"}); err != nil {
		t.Fatal(err)
	}
	cleared := 0
	awaitStreamed(t, lines, "the end of the burst", func(e streamed) bool {
		if e.Event == "casttarget" {
			cleared++
		}
		return e.Event == "submap"
	})
	if cleared != 0 {
		t.Fatalf("closing the target window sent %d more casttarget events", cleared)
	}
	if got := query[ipc.CastTarget](h, ipc.KindCastTarget); got != (ipc.CastTarget{Kind: ipc.CastNone}) {
		t.Fatalf("the cast target is %+v after its window closed", got)
	}
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	if r.view.stopped {
		t.Fatal("the session stopped when its target window closed")
	}
}

func TestADynamicWindowCaptureCopiesWhatAPlainOneDoes(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("streamed", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	copied := func() uint64 {
		var n uint64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			n = comp.CopiedBytes()
			return nil
		})
		return n
	}
	perFrame := func(session *ceic.ExtImageCopyCaptureSessionV1, size image.Point) uint64 {
		const frames = 4
		before := copied()
		for range frames {
			if err := errors.Join(w.Redraw(), c.Display.Flush()); err != nil {
				t.Fatal(err)
			}
			f, frame, _ := grab(t, c, session, size)
			c.DispatchUntil("a frame", func() bool { return f.ready || f.failed })
			if f.failed {
				t.Fatalf("frame %+v", *f)
			}
			if err := frame.Destroy(); err != nil {
				t.Fatal(err)
			}
		}
		return (copied() - before) / frames
	}
	w.Stop()
	r := newDynamicRig(t, h, c)
	h.dispatch("casttarget window")
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the constraints", func() bool { return r.view.done })
	dynamic := perFrame(r.session, r.view.size)
	plain, view := r.windowSession()
	if direct := perFrame(plain, view.size); dynamic != direct || dynamic == 0 {
		t.Fatalf("a frame of the dynamic source copies %d bytes, of the window's own source %d", dynamic, direct)
	}
}
