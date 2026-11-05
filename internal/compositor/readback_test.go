package compositor

import (
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/render"
)

type blockedWriter struct {
	entered, release chan struct{}
	rows             []byte
	closed           bool
}

func (w *blockedWriter) Write(f func(data []byte, stride int) error) error {
	close(w.entered)
	<-w.release
	return f(w.rows, 4)
}

func (w *blockedWriter) Close() error {
	w.closed = true
	return nil
}

func TestCancelReturnsWhileACopyIsStuckOnClientMemory(t *testing.T) {
	w := &blockedWriter{entered: make(chan struct{}), release: make(chan struct{}), rows: make([]byte, 16)}
	r := &asyncRead{c: &Compositor{}}
	pix := []byte{1, 2, 3, 4, 1, 2, 3, 4, 1, 2, 3, 4, 1, 2, 3, 4}
	copied := make(chan error, 1)
	go func() { copied <- r.copyRows(w, pix, 4, render.Region{image.Rect(0, 0, 1, 4)}) }()
	<-w.entered
	cancelled := make(chan struct{})
	go func() {
		r.cancel()
		close(cancelled)
	}()
	select {
	case <-cancelled:
	case <-time.After(30 * time.Second):
		t.Fatal("cancel waited for the copy stuck in Write")
	}
	close(w.release)
	if err := <-copied; err != nil {
		t.Fatal(err)
	}
	if !w.closed {
		t.Fatal("the mapping was not released")
	}
	for i, b := range w.rows {
		if b != 0 {
			t.Fatalf("byte %d was written after the read was cancelled", i)
		}
	}
}

func TestACaptureCopiesOnlyTheRegionItUpdates(t *testing.T) {
	src := make([]byte, 4*4*4)
	for i := range src {
		src[i] = byte(i + 1)
	}
	dst := make([]byte, 4*4*4)
	n := copyRegion(dst, 16, src, 16, render.Region{image.Rect(1, 1, 3, 2), image.Rect(0, 3, 1, 4)}, func() bool { return false })
	if n != 12 {
		t.Fatalf("copied %d bytes, want 12", n)
	}
	for y := range 4 {
		for x := range 4 {
			in := image.Pt(x, y).In(image.Rect(1, 1, 3, 2)) || image.Pt(x, y).In(image.Rect(0, 3, 1, 4))
			for b := range 4 {
				i := y*16 + x*4 + b
				if want := map[bool]byte{true: src[i], false: 0}[in]; dst[i] != want {
					t.Fatalf("byte %d of pixel %d,%d is %d, want %d", b, x, y, dst[i], want)
				}
			}
		}
	}
}
