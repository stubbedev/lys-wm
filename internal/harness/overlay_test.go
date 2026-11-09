package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/gpu"
	cld "github.com/stubbedev/lys-wm/internal/protocol/client/linuxdmabuf"
	cvp "github.com/stubbedev/lys-wm/internal/protocol/client/viewporter"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wlclient/feedback"
)

var (
	wide         = backend.Mode{Size: image.Pt(1280, 720), RefreshMHz: 60000, Preferred: true}
	overlayPlane = gpu.ScanoutTarget{Device: 0xe202, Formats: []gpu.FormatModifier{{Format: fourcc.XRGB8888, Modifier: fourcc.Linear}}}
)

type noModifiers struct{}

func (noModifiers) Format(*cld.ZwpLinuxDmabufV1, uint32) error                   { return nil }
func (noModifiers) Modifier(*cld.ZwpLinuxDmabufV1, uint32, uint32, uint32) error { return nil }

type video struct {
	w        *testclient.Window
	vp       *cvp.WpViewport
	buffers  [2]*testclient.DMABUF
	next     int
	updates  int
	last     feedback.Feedback
	viewport image.Point
}

func (v *video) commit(t *testing.T, c *testclient.Client, dest image.Point) {
	t.Helper()
	done := false
	if _, err := v.w.Surface.Frame(frameDone{&done}); err != nil {
		t.Fatal(err)
	}
	b := v.buffers[v.next]
	v.next = 1 - v.next
	if dest != v.viewport {
		v.viewport = dest
		if err := v.vp.SetDestination(int32(dest.X), int32(dest.Y)); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(v.w.Surface.Attach(b.WL, 0, 0), v.w.Surface.DamageBuffer(0, 0, 1<<15, 1<<15), v.w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the video's frame", func() bool { return done })
}

func videoWindow(t *testing.T, c *testclient.Client, lin *cld.ZwpLinuxDmabufV1, title string) *video {
	t.Helper()
	v := &video{w: c.Window(title, quadrants)}
	c.DispatchUntil(title+" to map", v.w.Mapped)
	v.w.Stop()
	var err error
	if v.vp, err = c.Viewporter.GetViewport(v.w.Surface); err != nil {
		t.Fatal(err)
	}
	if lin == nil {
		return v.withBuffers(c)
	}
	if _, err := lin.GetSurfaceFeedback(feedback.NewListener(func(f feedback.Feedback) error {
		v.updates, v.last = v.updates+1, f
		return nil
	}), v.w.Surface); err != nil {
		t.Fatal(err)
	}
	return v.withBuffers(c)
}

func (v *video) withBuffers(c *testclient.Client) *video {
	half := v.w.Logical.Div(2)
	v.buffers = [2]*testclient.DMABUF{c.DMABUF(half, testclient.Fill(0xff, 0, 0)), c.DMABUF(half, testclient.Fill(0, 0, 0xff))}
	return v
}

func overlays(h *Harness) (flips, presents uint64, shown int) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		flips, presents, shown = o.OverlayFlips(), o.Presents()-o.Scanouts(), len(o.ShownOverlays())
		return nil
	})
	return flips, presents, shown
}

func TestWindowedVideosGoToOverlayPlanes(t *testing.T) {
	onEachDMABUFRenderer(t, testWindowedVideosGoToOverlayPlanes)
}

func testWindowedVideosGoToOverlayPlanes(t *testing.T, start gpuStarter) {
	h := start(t, func(cfg *compositor.Config) { cfg.Decor.BorderWidth = 2 }, wide)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return b.SetOverlayPlanes(2, overlayPlane)
	})
	c := testclient.Connect(t, h.Socket())
	var lin *cld.ZwpLinuxDmabufV1
	if c.HasDMABUFFeedback() {
		var err error
		if lin, err = cld.NewZwpLinuxDmabufV1(c.Display, 4, noModifiers{}); err != nil {
			t.Fatal(err)
		}
		c.Bind(lin)
	}
	_, in := c.Seat()
	videos := [2]*video{videoWindow(t, c, lin, "video a"), videoWindow(t, c, lin, "video b")}
	c.Sync()
	in.Serial = 0
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.25, 0.5), b.Pointer().Frame())
	})
	c.DispatchUntil("the pointer to enter video a", func() bool { return in.Serial != 0 })
	if err := errors.Join(in.Pointer.SetCursor(in.Serial, nil, 0, 0), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	full := [2]image.Point{videos[0].w.Logical, videos[1].w.Logical}
	for range 3 {
		for i, v := range videos {
			v.commit(t, c, full[i])
		}
	}
	h.Eventually("both videos on overlay planes", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return len(b.Outputs()[0].ShownOverlays()) == 2
	})
	_, composited, _ := overlays(h)
	for range 100 {
		for i, v := range videos {
			v.commit(t, c, full[i])
		}
	}
	if _, after, shown := overlays(h); after != composited || shown != 2 {
		t.Fatalf("100 commits of two videos on overlay planes composited %d frames with %d planes in use, want 0 frames on 2 planes", after-composited, shown)
	}
	if lin == nil {
		t.Skip("the compositor sends no dmabuf feedback without the renderer's render node, so the scanout tranche is checked on a GPU (just docker hardware)")
	}
	for i, v := range videos {
		if tr := v.last.Tranches; v.updates != 2 || len(tr) == 0 || tr[0].Target != overlayPlane.Device {
			t.Fatalf("video %d had %d feedback updates, the last leading with %+v; want 2, the overlay plane's device %#x first", i, v.updates, tr, overlayPlane.Device)
		}
	}

	v := videos[0]
	small := image.Pt(100, 100)
	updates := v.updates
	for range 10 {
		v.commit(t, c, small)
		v.commit(t, c, full[0])
	}
	if v.updates != updates {
		t.Fatalf("a video leaving the planes for one frame at a time sent %d feedback updates, want none", v.updates-updates)
	}
	for flip := range 50 {
		dest := small
		if flip%2 == 1 {
			dest = full[0]
		}
		for range 3 {
			v.commit(t, c, dest)
		}
	}
	if got := v.updates - updates; got != 50 {
		t.Fatalf("50 lasting changes of a video's candidacy sent %d feedback updates, want one each", got)
	}
}

func TestARefusedOverlayFlipCompositesTheWindowInstead(t *testing.T) {
	onEachDMABUFRenderer(t, testARefusedOverlayFlipCompositesTheWindowInstead)
}

func testARefusedOverlayFlipCompositesTheWindowInstead(t *testing.T, start gpuStarter) {
	h := start(t, func(cfg *compositor.Config) { cfg.Decor.BorderWidth = 2 }, wide)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return b.SetOverlayPlanes(1, overlayPlane)
	})
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	v := videoWindow(t, c, nil, "video")
	c.DispatchUntil("the pointer to enter the video", func() bool { return in.Serial != 0 })
	if err := errors.Join(in.Pointer.SetCursor(in.Serial, nil, 0, 0), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		v.commit(t, c, v.w.Logical)
	}
	h.Eventually("the video on an overlay plane", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return len(b.Outputs()[0].ShownOverlays()) == 1
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.RefuseOverlayFlips(true)
		return nil
	})
	v.commit(t, c, v.w.Logical)
	want := [2][3]byte{{0, 0, 0xff}, {0xff, 0, 0}}[1-v.next]
	h.Eventually("the refused video composited in its new colour", func(b *headless.Backend, _ *compositor.Compositor) bool {
		o := b.Outputs()[0]
		return len(o.ShownOverlays()) == 0 && pixel(primary(o), 200, 200) == want
	})
}
