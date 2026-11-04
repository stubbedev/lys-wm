package compositor

import (
	"image"
	"testing"
	"time"
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
	r := &asyncRead{}
	pix := []byte{1, 2, 3, 4, 1, 2, 3, 4, 1, 2, 3, 4, 1, 2, 3, 4}
	copied := make(chan error, 1)
	go func() { copied <- r.copyRows(w, pix, 4, image.Pt(1, 4)) }()
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
