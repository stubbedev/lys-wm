package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	chte "github.com/stubbedev/lys-wm/internal/protocol/client/hyprlandtoplevelexport"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwft "github.com/stubbedev/lys-wm/internal/protocol/client/wlrforeigntoplevel"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type exportFrame struct {
	size          image.Point
	stride        uint32
	ready, failed bool
	damage        image.Rectangle
	bufferDone    bool
	dmabuf        fourcc.Format
	dmabufSize    image.Point
}

func (f *exportFrame) Buffer(_ *chte.HyprlandToplevelExportFrameV1, _ cw.WlShmFormat, w, h, stride uint32) error {
	f.size, f.stride = image.Pt(int(w), int(h)), stride
	return nil
}

func (f *exportFrame) Damage(_ *chte.HyprlandToplevelExportFrameV1, x, y, w, h uint32) error {
	f.damage = f.damage.Union(image.Rect(int(x), int(y), int(x+w), int(y+h)))
	return nil
}

func (*exportFrame) Flags(*chte.HyprlandToplevelExportFrameV1, chte.HyprlandToplevelExportFrameV1Flags) error {
	return nil
}

func (f *exportFrame) Ready(*chte.HyprlandToplevelExportFrameV1, uint32, uint32, uint32) error {
	f.ready = true
	return nil
}

func (f *exportFrame) Failed(*chte.HyprlandToplevelExportFrameV1) error {
	f.failed = true
	return nil
}

func (f *exportFrame) LinuxDmabuf(_ *chte.HyprlandToplevelExportFrameV1, format, w, h uint32) error {
	f.dmabuf, f.dmabufSize = fourcc.Format(format), image.Pt(int(w), int(h))
	return nil
}

func (f *exportFrame) BufferDone(*chte.HyprlandToplevelExportFrameV1) error {
	f.bufferDone = true
	return nil
}

type exportRig struct {
	t   *testing.T
	c   *testclient.Client
	mgr *chte.HyprlandToplevelExportManagerV1
	wlr *wlrWatcher
}

func newExportRig(t *testing.T, c *testclient.Client) *exportRig {
	t.Helper()
	r := &exportRig{t: t, c: c, wlr: &wlrWatcher{toplevels: map[*cwft.ZwlrForeignToplevelHandleV1]*wlrToplevel{}}}
	toplevels, err := cwft.NewZwlrForeignToplevelManagerV1(c.Display, 3, r.wlr)
	if err != nil {
		t.Fatal(err)
	}
	if r.mgr, err = chte.NewHyprlandToplevelExportManagerV1(c.Display, 2); err != nil {
		t.Fatal(err)
	}
	c.Bind(toplevels)
	c.Bind(r.mgr)
	c.DispatchUntil("the window listed", func() bool { return len(r.wlr.order) == 1 })
	return r
}

func (r *exportRig) frame() (*exportFrame, *chte.HyprlandToplevelExportFrameV1) {
	r.t.Helper()
	f := &exportFrame{}
	frame, err := r.mgr.CaptureToplevelWithWlrToplevelHandle(f, 0, r.wlr.order[0])
	if err != nil {
		r.t.Fatal(err)
	}
	r.c.DispatchUntil("the buffer description", func() bool { return f.bufferDone || f.failed })
	return f, frame
}

func (r *exportRig) copy(frame *chte.HyprlandToplevelExportFrameV1, wl *cw.WlBuffer, waitForDamage bool) {
	r.t.Helper()
	ignore := int32(1)
	if waitForDamage {
		ignore = 0
	}
	if err := errors.Join(frame.Copy(wl, ignore), r.c.Display.Flush()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *exportRig) shot(waitForDamage bool) (*exportFrame, []byte) {
	r.t.Helper()
	f, frame := r.frame()
	if f.failed {
		r.t.Fatalf("the export frame failed before its buffer: %+v", *f)
	}
	wl, pix := r.c.Target(f.size)
	r.copy(frame, wl, waitForDamage)
	return f, pix
}

func TestAToplevelExportCapturesAWindowByItsForeignToplevelHandle(t *testing.T) {
	t.Parallel()
	onEachRenderer(t, func(t *testing.T, start starter) {
		h := start(t, small)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("exported", quadrants)
		c.DispatchUntil("the window to map", w.Mapped)
		quiesce(t, h, c, w)
		r := newExportRig(t, c)

		f, pix := r.shot(false)
		c.DispatchUntil("the window frame", func() bool { return f.ready || f.failed })
		stride := int(f.stride)
		if f.failed || f.size != image.Pt(160, 100) || rgbAt(pix, stride, 20, 20) != [3]byte{0xff, 0, 0} || rgbAt(pix, stride, 140, 80) != [3]byte{0xff, 0xff, 0} {
			t.Fatalf("export %+v shows %v and %v", *f, rgbAt(pix, stride, 20, 20), rgbAt(pix, stride, 140, 80))
		}

		f, pix = r.shot(true)
		settle(t, h)
		if err := c.Display.Roundtrip(); err != nil || f.ready || f.failed {
			t.Fatalf("a damage-waiting export finished without a commit: %+v (%v)", *f, err)
		}
		if err := errors.Join(w.Surface.Attach(c.Buffer(w.Logical, 1, testclient.Fill(0xff, 0, 0)), 0, 0), w.Surface.Damage(0, 0, 160, 100), w.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the export after the commit", func() bool { return f.ready || f.failed })
		if f.failed || f.damage != image.Rect(0, 0, 160, 100) || rgbAt(pix, stride, 140, 80) != [3]byte{0, 0, 0xff} {
			t.Fatalf("the damaged export %+v shows %v", *f, rgbAt(pix, stride, 140, 80))
		}
	})
}

func TestAToplevelExportOfAHiddenWindowFollowsItsCommits(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("hidden export", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	r := newExportRig(t, c)
	h.dispatch("workspace 2")
	settle(t, h)

	f, pix := r.shot(true)
	settle(t, h)
	presents := presentCount(h)
	if err := errors.Join(w.Surface.Attach(c.Buffer(w.Logical, 1, testclient.Fill(0xff, 0, 0)), 0, 0), w.Surface.Damage(0, 0, 160, 100), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("an export of the hidden window's new content", func() bool { return f.ready || f.failed })
	if f.failed || rgbAt(pix, int(f.stride), 140, 80) != [3]byte{0, 0, 0xff} {
		t.Fatalf("export %+v shows %v", *f, rgbAt(pix, int(f.stride), 140, 80))
	}
	if n := presentCount(h); n != presents {
		t.Fatalf("exporting a hidden window presented %d frames", n-presents)
	}
}

func TestAToplevelExportByWindowIDAndOfGoneWindows(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("by id", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	r := newExportRig(t, c)
	var id uint32
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		id = uint32(comp.Snapshot().Windows[0].ID)
		return nil
	})

	f := &exportFrame{}
	frame, err := r.mgr.CaptureToplevel(f, 0, id)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the buffer description", func() bool { return f.bufferDone || f.failed })
	wl, pix := c.Target(f.size)
	r.copy(frame, wl, false)
	c.DispatchUntil("the frame by window ID", func() bool { return f.ready || f.failed })
	if f.failed || rgbAt(pix, int(f.stride), 30, 30) != [3]byte{0xff, 0, 0} {
		t.Fatalf("export by ID %+v shows %v", *f, rgbAt(pix, int(f.stride), 30, 30))
	}

	unknown := &exportFrame{}
	if _, err := r.mgr.CaptureToplevel(unknown, 0, id+100); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("an unknown window's frame to fail", func() bool { return unknown.failed })

	pending, frame := r.frame()
	if err := errors.Join(w.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the window closed", func() bool { return r.wlr.toplevels[r.wlr.order[0]].closed })
	wl, _ = c.Target(pending.size)
	r.copy(frame, wl, false)
	c.DispatchUntil("a copy of a closed window to fail", func() bool { return pending.failed })
	gone, _ := r.frame()
	if !gone.failed || gone.bufferDone {
		t.Fatalf("a frame of a closed window %+v", *gone)
	}
}

func TestAToplevelExportFrameCopiesOnce(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("once", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	r := newExportRig(t, c)
	f, frame := r.frame()
	wl, _ := c.Target(f.size)
	r.copy(frame, wl, false)
	r.copy(frame, wl, false)
	c.ExpectProtocolError(uint32(chte.HyprlandToplevelExportFrameV1ErrorAlreadyUsed))
}

func TestAToplevelExportIntoDMABUFs(t *testing.T) {
	onEachDMABUFRenderer(t, testAToplevelExportIntoDMABUFs)
}

func testAToplevelExportIntoDMABUFs(t *testing.T, start gpuStarter) {
	c, _, view := dmabufCapture(t, start)
	if !c.HasDMABUFFeedback() {
		t.Skip("the renderer names no main device, so it offers no dmabuf captures")
	}
	r := newExportRig(t, c)
	f, frame := r.frame()
	if f.dmabuf != fourcc.XRGB8888 || f.dmabufSize != image.Pt(160, 100) {
		t.Fatalf("linux_dmabuf offered %v at %v", f.dmabuf, f.dmabufSize)
	}
	target := c.RenderTarget(f.dmabufSize, view.modifiers)
	r.copy(frame, target.WL, false)
	c.DispatchUntil("the dmabuf export", func() bool { return f.ready || f.failed })
	pix, stride := target.Pixels(), f.dmabufSize.X*4
	if f.failed || rgbAt(pix, stride, 20, 20) != [3]byte{0xff, 0, 0} || rgbAt(pix, stride, 140, 80) != [3]byte{0xff, 0xff, 0} {
		t.Fatalf("dmabuf export %+v: %v and %v", *f, rgbAt(pix, stride, 20, 20), rgbAt(pix, stride, 140, 80))
	}
}
