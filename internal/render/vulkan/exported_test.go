package vulkan_test

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/rendertest"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
)

const sharedFormat = fourcc.XRGB8888

var sharedSize = image.Pt(8, 8)

type sharedTarget struct {
	t      *testing.T
	r      *vulkan.Renderer
	target render.Target
	held   *gpu.Buffer
	copy   func() error
}

func exporting(t *testing.T) *vulkan.Renderer {
	t.Helper()
	r, _ := hardware(t)
	if !r.Device().Exports() {
		t.Skipf("%s shares no dmabufs", r.Device().Name())
	}
	return r
}

func hold(t *testing.T, r *vulkan.Renderer, target render.Target, shared gpu.DMABUF, copy func() error) *sharedTarget {
	t.Helper()
	held, err := shared.Dup()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := held.Close(); err != nil {
			t.Error(err)
		}
	})
	return &sharedTarget{t: t, r: r, target: target, held: held, copy: copy}
}

func (s *sharedTarget) paint(c render.Color) {
	s.t.Helper()
	rendertest.Paint(s.t, s.r, s.target, render.Region{{Max: sharedSize}}, func(p render.Pass) { p.Clear(c) })
	if err := s.copy(); err != nil {
		s.t.Fatal(err)
	}
	if err := s.r.Finish(); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sharedTarget) shown() render.Color {
	s.t.Helper()
	tex, err := s.r.ImportTexture(s.held.DMABUF)
	if err != nil {
		s.t.Fatal(err)
	}
	seen, err := s.r.NewTarget(sharedSize)
	if err != nil {
		s.t.Fatal(err)
	}
	whole := image.Rectangle{Max: sharedSize}
	rendertest.Paint(s.t, s.r, seen, render.Region{whole}, func(p render.Pass) {
		p.Clear(render.Color{})
		p.DrawTexture(tex, render.FRect{W: float64(sharedSize.X), H: float64(sharedSize.Y)}, whole, render.Normal, 1, render.Mask{})
	})
	pix := make([]byte, sharedSize.X*sharedSize.Y*4)
	if err := seen.Read(pix, sharedSize.X*4); err != nil {
		s.t.Fatal(err)
	}
	if err := seen.Destroy(); err != nil {
		s.t.Fatal(err)
	}
	if err := tex.Destroy(); err != nil {
		s.t.Fatal(err)
	}
	return render.ARGB8888.Decode(pix)
}

func file(t *testing.T, fd int) (unix.Stat_t, error) {
	t.Helper()
	var st unix.Stat_t
	err := unix.Fstat(fd, &st)
	return st, err
}

func (s *sharedTarget) holdsTheSameMemory(fd int) bool {
	s.t.Helper()
	got, err := file(s.t, fd)
	if err != nil {
		return false
	}
	for _, p := range s.held.Planes {
		if want, err := file(s.t, p.FD); err != nil {
			s.t.Fatal(err)
		} else if got.Dev == want.Dev && got.Ino == want.Ino {
			return true
		}
	}
	return false
}

func (s *sharedTarget) survivesALostDevice(free func() error) {
	s.t.Helper()
	red, blue := render.Color{R: 0xff, A: 0xff}, render.Color{B: 0xff, A: 0xff}
	s.paint(red)
	if got := s.shown(); got != red {
		s.t.Fatalf("before the loss a holder of the dmabuf sees %v, want %v", got, red)
	}
	if kept := vulkan.KeptPlanes(s.r); len(kept) != 0 {
		s.t.Fatalf("the renderer duplicated %d planes before any device was lost", len(kept))
	}

	s.r.LoseDevice()
	if err := s.r.Recover(); err != nil {
		s.t.Fatal(err)
	}
	kept := vulkan.KeptPlanes(s.r)
	if len(kept) != len(s.held.Planes) {
		s.t.Fatalf("the renderer kept %d planes for the new device, want the dmabuf's %d", len(kept), len(s.held.Planes))
	}
	for _, p := range kept {
		if !s.holdsTheSameMemory(p.FD) {
			s.t.Fatalf("plane fd %d kept for the new device is not the dmabuf the holder has", p.FD)
		}
	}
	s.paint(blue)
	if got := s.shown(); got != blue {
		s.t.Fatalf("after the loss a holder of the dmabuf sees %v, want %v: the new device draws into other memory", got, blue)
	}

	if err := free(); err != nil {
		s.t.Fatal(err)
	}
	for _, p := range kept {
		if s.holdsTheSameMemory(p.FD) {
			s.t.Fatalf("plane fd %d is still open after its target was destroyed", p.FD)
		}
	}
	if left := vulkan.KeptPlanes(s.r); len(left) != 0 {
		s.t.Fatalf("%d planes are still kept after their target was destroyed", len(left))
	}
}

func TestAnExportedTargetDrawsIntoTheSameDMABUFAfterALostDeviceOnHardware(t *testing.T) {
	r := exporting(t)
	modifiers, err := r.ExportModifiers(sharedFormat)
	if err != nil {
		t.Fatal(err)
	}
	target, shared, err := r.NewExportedTarget(sharedSize, sharedFormat, modifiers)
	if err != nil {
		t.Fatal(err)
	}
	hold(t, r, target, shared, func() error { return nil }).survivesALostDevice(target.Destroy)
}

func TestALinearCopyTargetCopiesIntoTheSameDMABUFAfterALostDeviceOnHardware(t *testing.T) {
	r := exporting(t)
	c, err := r.NewLinearCopyTarget(sharedSize, sharedFormat)
	if err != nil {
		t.Fatal(err)
	}
	hold(t, r, c.Target, c.DMABUF, c.Copy).survivesALostDevice(c.Free)
}
