package gpu_test

import (
	"errors"
	"image"
	"os"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
)

func memfd(t *testing.T, content string) int {
	t.Helper()
	fd, err := unix.MemfdCreate("plane", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Pwrite(fd, []byte(content), 0); err != nil {
		t.Fatal(err)
	}
	return fd
}

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestADuplicatedDMABUFOutlivesItsSourceAndClosesOnlyItsOwnDescriptors(t *testing.T) {
	src := gpu.DMABUF{
		Size: image.Pt(4, 2), Format: fourcc.NV12, Modifier: fourcc.Linear, YUV: gpu.YUVEncoding{Coefficients: gpu.BT709},
		Planes: []gpu.Plane{{FD: memfd(t, "luma"), Stride: 4}, {FD: memfd(t, "chroma"), Offset: 8, Stride: 4}},
	}
	before := openFDs(t)
	dup, err := src.Dup()
	if err != nil {
		t.Fatal(err)
	}
	if dup.Size != src.Size || dup.Format != src.Format || dup.Modifier != src.Modifier || dup.YUV != src.YUV || len(dup.Planes) != len(src.Planes) {
		t.Fatalf("the duplicate describes %+v, want what %+v does", dup.DMABUF, src)
	}
	for i, p := range dup.Planes {
		s := src.Planes[i]
		if p.FD == s.FD || p.Offset != s.Offset || p.Stride != s.Stride {
			t.Fatalf("plane %d is %+v, want the layout of %+v behind a descriptor of its own", i, p, s)
		}
		flags, err := unix.FcntlInt(uintptr(p.FD), unix.F_GETFD, 0)
		if err != nil {
			t.Fatal(err)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			t.Fatalf("plane %d's descriptor %d would leak into a child", i, p.FD)
		}
	}
	for _, p := range src.Planes {
		if err := unix.Close(p.FD); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []string{"luma", "chroma"} {
		got := make([]byte, len(want))
		if _, err := unix.Pread(dup.Planes[i].FD, got, 0); err != nil || string(got) != want {
			t.Fatalf("plane %d reads %q, %v after its source closed, want %q", i, got, err, want)
		}
	}
	if err := dup.Close(); err != nil {
		t.Fatal(err)
	}
	if after := openFDs(t); after != before-len(src.Planes) {
		t.Fatalf("%d descriptors open after the source and its duplicate closed, want %d", after, before-len(src.Planes))
	}
}

func TestADuplicateThatFailsNamesThePlaneAndLeaksNoDescriptor(t *testing.T) {
	open := memfd(t, "kept")
	t.Cleanup(func() { _ = unix.Close(open) })
	src := gpu.DMABUF{Size: image.Pt(4, 2), Format: fourcc.NV12, Planes: []gpu.Plane{{FD: open, Stride: 4}, {FD: -1, Stride: 4}}}
	before := openFDs(t)
	dup, err := src.Dup()
	if dup != nil || !errors.Is(err, unix.EBADF) || !strings.Contains(err.Error(), "plane fd -1") {
		t.Fatalf("Dup = %v, %v; want EBADF naming plane fd -1", dup, err)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("%d descriptors open after a failed duplicate, %d before it", after, before)
	}
	if _, err := unix.FcntlInt(uintptr(open), unix.F_GETFD, 0); err != nil {
		t.Fatalf("the source's own descriptor: %v", err)
	}
}
