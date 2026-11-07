package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	ccm "github.com/stubbedev/lys-wm/internal/protocol/client/colormanagement"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func hdrRule(cfg *compositor.Config) {
	cfg.Outputs = compositor.OutputRules{Fallback: compositor.OutputRule{HDR: true}}
}

type cursorCounts struct {
	images, moves, composited uint64
	hdr                       bool
}

func cursorCountsOf(h *Harness) cursorCounts {
	var n cursorCounts
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		n = countsOn(b)
		return nil
	})
	return n
}

func countsOn(b *headless.Backend) cursorCounts {
	o := b.Outputs()[0]
	return cursorCounts{images: o.Cursor().Images(), moves: o.Cursor().Moves(), composited: o.Presents() - o.Scanouts(), hdr: o.State().HDR.Active}
}

func movePointerTo(h *Harness, x, y float64) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.MoveTo(b.Outputs()[0], x, y), p.Frame())
	})
}

func TestAnHDROutputMovesItsCursorOnThePlane(t *testing.T) {
	h := StartGLESWithCursorPlanes(t, image.Pt(64, 64), hdrRule, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("still", testclient.Fill(0x40, 0x40, 0x40))
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	movePointerTo(h, 0.1, 0.1)
	h.Eventually("the cursor on its plane", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Cursor().Visible()
	})
	settle(t, h)
	before := cursorCountsOf(h)
	if !before.hdr {
		t.Fatal("the output is not in HDR")
	}
	for i := range 100 {
		movePointerTo(h, 0.1+float64(i)*0.005, 0.2+float64(i%7)*0.01)
	}
	h.Eventually("the last move on the plane", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return countsOn(b).moves >= before.moves+90
	})
	settle(t, h)
	after := cursorCountsOf(h)
	if after.composited != before.composited || after.images != before.images {
		t.Fatalf("100 pointer motions on an HDR output composited %d frames and loaded %d cursor images, want neither", after.composited-before.composited, after.images-before.images)
	}
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		return c.ApplyColorMatrix(c.Outputs()[0], backend.Matrix{0.9, 0, 0, 0, 0.9, 0, 0, 0, 0.9})
	})
	h.Eventually("the cursor encoded again for the colour matrix", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return countsOn(b).images == after.images+1
	})
}

func TestAnHDRFullscreenBufferKeepsScanningOutUnderAMovingCursor(t *testing.T) {
	h := StartGLESWithCursorPlanes(t, image.Pt(64, 64), hdrRule, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[0].AllowScanout(primaryPlane)
		return nil
	})
	c := testclient.Connect(t, h.Socket())
	w := c.Window("game", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("fullscreen")
	c.DispatchUntil("the fullscreen configure", func() bool { return w.Logical == small.Size })
	w.Stop()
	m := bindColorManager(t, c)
	cs, err := m.GetSurface(w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.SetImageDescription(hdr10Description(t, c, m, 1000), ccm.WpColorManagerV1RenderIntentPerceptual); err != nil {
		t.Fatal(err)
	}
	s := &scanoutScene{h: h, c: c, w: w}
	buffers := [2]*testclient.DMABUF{c.DMABUF(small.Size, testclient.Fill(0, 0, 0xff)), c.DMABUF(small.Size, testclient.Fill(0, 0xff, 0))}
	movePointerTo(h, 0.5, 0.5)
	s.show(t, "the first PQ buffer", buffers[0])
	before := cursorCountsOf(h)
	for i := range 100 {
		movePointerTo(h, 0.2+float64(i%50)*0.01, 0.5)
		s.show(t, "a PQ buffer", buffers[(i+1)%2])
	}
	after := cursorCountsOf(h)
	if after.composited != before.composited || after.moves == before.moves {
		t.Fatalf("100 PQ commits with the pointer moving between them composited %d frames and moved the plane %d times, want 0 frames and moves on the plane", after.composited-before.composited, after.moves-before.moves)
	}
}
