package harness

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/compositor"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type ignoreRelease struct{}

func (ignoreRelease) Release(*cw.WlBuffer) error { return nil }

func TestAShrunkPoolDisconnectsOnlyItsClientUnderGLES(t *testing.T) {
	h := StartGLES(t, func(*compositor.Config) {}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("shrinking", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()

	stride, rows := w.Size.X*4, w.Size.Y
	fd, err := unix.MemfdCreate("shrinking", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := unix.Ftruncate(fd, int64(stride*rows)); err != nil {
		t.Fatal(err)
	}
	pool, err := c.Shm.CreatePool(fd, int32(stride*rows))
	if err != nil {
		t.Fatal(err)
	}
	b, err := pool.CreateBuffer(ignoreRelease{}, 0, int32(w.Size.X), int32(rows), int32(stride), cw.WlShmFormatXrgb8888)
	if err != nil {
		t.Fatal(err)
	}
	show := func() error {
		return errors.Join(w.Surface.Attach(b, 0, 0), w.Surface.DamageBuffer(0, 0, int32(w.Size.X), int32(rows)), w.Surface.Commit())
	}
	if err := errors.Join(show(), c.Display.Roundtrip()); err != nil {
		t.Fatalf("the intact pool: %v", err)
	}
	if err := unix.Ftruncate(fd, 0); err != nil {
		t.Fatal(err)
	}
	if err := show(); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(cw.WlShmErrorInvalidFD))

	other := testclient.Connect(t, h.Socket())
	still := other.Window("still drawn", quadrants)
	other.DispatchUntil("another client's window to map after the fault", still.Mapped)
}

func tenBitFill(format cw.WlShmFormat, r, g, b uint32) testclient.Painter {
	hi, lo := r, b
	if format == cw.WlShmFormatAbgr2101010 || format == cw.WlShmFormatXbgr2101010 {
		hi, lo = lo, hi
	}
	return func(pix []byte, _ int, _ image.Point, _ int) {
		for i := 0; i < len(pix); i += 4 {
			binary.LittleEndian.PutUint32(pix[i:], 3<<30|hi<<20|g<<10|lo)
		}
	}
}

func TestTenBitShmBuffersShowTheirColour(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(t *testing.T) *Harness
	}{
		{"soft", func(t *testing.T) *Harness { return Start(t, small) }},
		{"gles", func(t *testing.T) *Harness { return StartGLES(t, func(*compositor.Config) {}, small) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.start(t)
			c := testclient.Connect(t, h.Socket())
			w := c.Window("deep", quadrants)
			c.DispatchUntil("the window to map", w.Mapped)
			w.Stop()
			show := func(b *cw.WlBuffer) {
				t.Helper()
				if err := errors.Join(w.Surface.Attach(b, 0, 0), w.Surface.DamageBuffer(0, 0, int32(w.Size.X), int32(w.Size.Y)), w.Surface.Commit(), c.Display.Flush()); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range []cw.WlShmFormat{cw.WlShmFormatArgb2101010, cw.WlShmFormatXrgb2101010, cw.WlShmFormatAbgr2101010, cw.WlShmFormatXbgr2101010} {
				show(c.Buffer(w.Size, 0, testclient.Fill(0, 0, 0)))
				h.Eventually("black before the next format", showing(2, 2, [3]byte{}))
				show(c.FormatBuffer(w.Size, f, 0, tenBitFill(f, 0x3ff, 0x2aa, 0x155)))
				h.Eventually(fmt.Sprintf("the colour of a %#x buffer", uint32(f)), showing(2, 2, [3]byte{0xff, 0xaa, 0x55}))
			}
		})
	}
}

func fullDamage(s *cw.WlSurface, b *cw.WlBuffer, size image.Point) error {
	return errors.Join(s.Attach(b, 0, 0), s.DamageBuffer(0, 0, int32(size.X), int32(size.Y)))
}

func TestAFifoQueuedCommitWhoseBufferWasDestroyedStillShowsIt(t *testing.T) {
	h := Start(t, small)
	pc := pace(t, h)
	w := pc.window(t)
	f, err := pc.fifos.GetFifo(w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	blue := pc.c.Buffer(w.Size, 0, testclient.Fill(0xff, 0, 0))
	red := pc.c.Buffer(w.Size, 0, testclient.Fill(0, 0, 0xff))
	green := pc.c.Buffer(w.Size, 0, testclient.Fill(0, 0xff, 0))
	if err := errors.Join(fullDamage(w.Surface, blue, w.Size), w.Surface.Commit(), pc.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the surface on screen, so a barrier it sets holds", showing(2, 2, [3]byte{0, 0, 0xff}))
	if err := errors.Join(
		f.SetBarrier(), fullDamage(w.Surface, red, w.Size), w.Surface.Commit(),
		f.WaitBarrier(), fullDamage(w.Surface, green, w.Size), w.Surface.Commit(),
		green.Destroy(), pc.c.Display.Flush(),
	); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the queued update of the destroyed buffer", showing(2, 2, [3]byte{0, 0xff, 0}))
	pc.c.Sync()
}

func TestASynchronizedSubsurfaceCacheWhoseBufferWasDestroyedStillShowsIt(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("parent", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	child := c.Surface()
	sub, err := c.Subcompositor.GetSubsurface(child, w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	size := image.Pt(16, 16)
	green := c.Buffer(size, 0, testclient.Fill(0, 0xff, 0))
	if err := errors.Join(
		sub.SetPosition(10, 10), fullDamage(child, green, size), child.Commit(),
		green.Destroy(), w.Surface.Commit(), c.Display.Flush(),
	); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the cached subsurface update of the destroyed buffer", showing(18, 18, [3]byte{0, 0xff, 0}))
	c.Sync()
}

func TestABufferDestroyedAfterItsCommitAppliedStaysOnScreen(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("destroyed after", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	green := c.Buffer(w.Size, 0, testclient.Fill(0, 0xff, 0))
	if err := errors.Join(fullDamage(w.Surface, green, w.Size), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the green buffer", showing(2, 2, [3]byte{0, 0xff, 0}))
	if err := green.Destroy(); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	settle(t, h)
	h.Eventually("the destroyed buffer's contents", showing(2, 2, [3]byte{0, 0xff, 0}))
	blue := c.Buffer(w.Size, 0, testclient.Fill(0xff, 0, 0))
	if err := errors.Join(fullDamage(w.Surface, blue, w.Size), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the next buffer", showing(2, 2, [3]byte{0, 0, 0xff}))
	c.Sync()
}
