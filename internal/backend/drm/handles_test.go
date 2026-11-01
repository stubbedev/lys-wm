package drm

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
)

func planesOn(fds ...int) gpu.DMABUF {
	d := gpu.DMABUF{Size: image.Pt(16, 16), Format: scanoutFormat, Modifier: fourcc.Linear}
	for _, fd := range fds {
		d.Planes = append(d.Planes, gpu.Plane{FD: fd, Stride: 64})
	}
	return d
}

func TestAFramebufferKeepsNoGEMHandlesOpen(t *testing.T) {
	dev := &fakeDevice{}
	id, err := importFB(dev, planesOn(7, 7, 8), true)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("no framebuffer id")
	}
	if len(dev.openHandles) != 0 {
		t.Fatalf("handles left open: %v", dev.openHandles)
	}
}

func TestARefusedFramebufferLeavesNoHandlesOpen(t *testing.T) {
	dev := &fakeDevice{refuseAddFB: true}
	if _, err := importFB(dev, planesOn(7), true); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("importFB = %v, want the refusal", err)
	}
	if len(dev.openHandles) != 0 {
		t.Fatalf("handles left open: %v", dev.openHandles)
	}
}

func TestAHandleThatWillNotCloseUndoesTheFramebuffer(t *testing.T) {
	dev := &fakeDevice{refuseCloseHandle: true}
	if _, err := importFB(dev, planesOn(7), true); !errors.Is(err, unix.EIO) {
		t.Fatalf("importFB = %v, want the close failure", err)
	}
	if len(dev.removedFBs) != 1 {
		t.Fatalf("framebuffers removed: %v", dev.removedFBs)
	}
}

func TestAClientFramebufferThatWillNotGoIsReportedNotFatal(t *testing.T) {
	var reported []error
	dev := &fakeDevice{refuseRemoveFB: true}
	cf := &clientFB{dev: dev, report: func(err error) { reported = append(reported, err) }, fb: 701}
	cf.remove()
	if len(reported) != 1 || !errors.Is(reported[0], unix.EBUSY) {
		t.Fatalf("reported %v", reported)
	}
	if slices.Contains(dev.removedFBs, 701) {
		t.Fatal("the refused framebuffer counted as removed")
	}
}
