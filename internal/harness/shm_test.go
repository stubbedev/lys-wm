package harness

import (
	"errors"
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
