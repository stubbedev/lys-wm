package drm

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const seatVar = "LYS_WM_SEAT"

type seatDevice struct {
	*kdrm.Device
	commits []commit
	errs    []error
}

func (d *seatDevice) Commit(r *kdrm.Request, flags uint32, userData uint64) error {
	c := commit{flags: flags, userData: userData, values: map[[2]uint32]uint64{}}
	r.Each(func(object, property uint32, value uint64) { c.values[[2]uint32{object, property}] = value })
	err := d.Device.Commit(r, flags, userData)
	d.commits, d.errs = append(d.commits, c), append(d.errs, err)
	return err
}

func (d *seatDevice) since(n int) (real, tests int) {
	for _, c := range d.commits[n:] {
		if c.flags&kdrm.AtomicTestOnly != 0 {
			tests++
		} else {
			real++
		}
	}
	return real, tests
}

type seatHandler struct {
	damage   render.Region
	frames   int
	presents int
	then     func() error
}

func (h *seatHandler) OutputAdded(o backend.Output) error   { return o.ScheduleFrame() }
func (h *seatHandler) OutputRemoved(backend.Output) error   { return nil }
func (h *seatHandler) OutputChanged(backend.Output) error   { return nil }
func (h *seatHandler) OutputDamaged(o backend.Output) error { return o.ScheduleFrame() }
func (h *seatHandler) DeviceAdded(backend.Device) error     { return nil }
func (h *seatHandler) DeviceRemoved(backend.Device) error   { return nil }

func (h *seatHandler) Input(backend.Device, backend.Event) error { return nil }

func (h *seatHandler) Presented(backend.Output, backend.Presentation) error {
	h.presents++
	return nil
}

func (h *seatHandler) Frame(o backend.Output) error {
	f, err := o.BeginFrame()
	if err != nil {
		return err
	}
	whole := image.Rectangle{Max: f.Target.Size()}
	region := render.Region{whole}
	if h.frames > 0 && h.damage != nil {
		region = h.damage
	}
	p, err := f.Renderer.Begin(f.Target, region)
	if err != nil {
		return err
	}
	shade := uint8(0x20 * (h.frames % 8))
	for _, r := range region {
		p.FillRect(r, render.Color{R: shade, G: 0x40, B: 0x80, A: 0xff}, render.Mask{})
	}
	h.frames++
	damage := h.damage
	if h.frames == 1 {
		damage = nil
	}
	if err := errors.Join(p.End(), o.Present(damage)); err != nil {
		return err
	}
	then := h.then
	h.then = nil
	if then == nil {
		return nil
	}
	return then()
}

type seat struct {
	t       *testing.T
	l       *loop.Loop
	dev     *seatDevice
	b       *Backend
	h       *seatHandler
	reports []error
}

type drmVersion struct {
	major, minor, patch int32
	_                   int32
	nameLen             uint64
	name                *byte
	dateLen             uint64
	date                *byte
	descLen             uint64
	desc                *byte
}

const drmIoctlVersion = 0xc0406400

func driverName(dev *kdrm.Device) (string, error) {
	name := make([]byte, 64)
	v := drmVersion{nameLen: uint64(len(name)), name: &name[0]}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(dev.FD()), drmIoctlVersion, uintptr(unsafe.Pointer(&v))); errno != 0 {
		return "", fmt.Errorf("DRM_IOCTL_VERSION on %s: %w", dev.Path(), errno)
	}
	return string(name[:min(v.nameLen, uint64(len(name)))]), nil
}

func cardDriven(t *testing.T, driver string) *kdrm.Device {
	t.Helper()
	cards, err := filepath.Glob("/dev/dri/card[0-9]")
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, card := range cards {
		dev, err := kdrm.Open(card)
		if err != nil {
			t.Fatal(err)
		}
		name, err := driverName(dev)
		if err != nil {
			t.Fatal(errors.Join(err, dev.Close()))
		}
		if name == driver {
			t.Cleanup(func() { _ = dev.Close() })
			return dev
		}
		seen = append(seen, card+" "+name)
		if err := dev.Close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("no card is driven by %s: %v", driver, seen)
	return nil
}

func onASeat(t *testing.T, driver string) (*seat, *Output) {
	t.Helper()
	if os.Getenv(seatVar) != "1" {
		t.Skipf("%s=1 lets this take DRM master and commit on the machine's cards: set it only in a VM, never beside a session", seatVar)
	}
	dev := cardDriven(t, driver)
	if err := atomic(dev); err != nil {
		t.Fatal(err)
	}
	paths := gpu.PathsFromEnv(os.Getenv)
	vendors, err := edid.LoadVendors(edid.PNPPath(os.Getenv))
	if err != nil {
		t.Fatal(err)
	}
	alloc, err := newGLESAllocator(device{kernel: dev}, paths)
	if err != nil {
		t.Fatal(err)
	}
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s := &seat{t: t, l: l, dev: &seatDevice{Device: dev}, h: &seatHandler{}}
	if s.b, err = newBackend(l, device{kernel: s.dev}, alloc, Options{Paths: paths, Vendors: vendors, Report: func(err error) { s.reports = append(s.reports, err) }, Master: HoldsMaster}); err != nil {
		t.Fatal(errors.Join(err, alloc.close()))
	}
	t.Cleanup(func() {
		if err := s.b.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := s.b.Start(s.h); err != nil {
		t.Fatal(err)
	}
	if len(s.b.Outputs()) == 0 {
		t.Fatalf("%s lights no output", dev.Path())
	}
	o := s.b.Outputs()[0]
	s.until("the first frame on screen", func() bool { return s.h.presents > 0 && !o.flipping })
	t.Logf("%s (%s): %s on CRTC %d, %v", dev.Path(), driver, o.Name(), o.crtc.id, modeOf(o.mode))
	return s, o
}

func (s *seat) until(what string, done func() bool) {
	s.t.Helper()
	met, err := loopUntil(s.l, done)
	if err != nil {
		s.t.Fatal(err)
	}
	if !met {
		s.t.Fatalf("waited %v for %s: %d commits, last errors %v, reports %v", loopWait, what, len(s.dev.commits), s.dev.errs[max(0, len(s.dev.errs)-4):], s.reports)
	}
	if len(s.reports) > 0 {
		s.t.Fatalf("%s: the backend reported %v", what, s.reports)
	}
}

func (s *seat) property(object, kind uint32, name string) uint64 {
	s.t.Helper()
	props, err := s.dev.Properties(object, kind)
	if err != nil {
		s.t.Fatal(err)
	}
	p, ok := props[name]
	if !ok {
		s.t.Fatalf("object %d has no %s", object, name)
	}
	return p.Value
}

func (s *seat) frame(o *Output) {
	s.t.Helper()
	presents := s.h.presents
	if err := o.ScheduleFrame(); err != nil {
		s.t.Fatal(err)
	}
	s.until("a frame on screen", func() bool { return s.h.presents > presents && !o.flipping })
}

var seatDrivers = []string{"virtio_gpu", "vkms"}

func TestCursorMotionTakesOneRequestPerRefreshOnASeat(t *testing.T) {
	for _, driver := range seatDrivers {
		t.Run(driver, func(t *testing.T) {
			s, o := onASeat(t, driver)
			cp, err := o.CursorPlane()
			if err != nil {
				t.Fatal(err)
			}
			c := o.cursor
			white := make([]byte, 16*16*4)
			for i := range white {
				white[i] = 0xff
			}
			if err := errors.Join(cp.LoadCursor(white, 16*4, image.Pt(16, 16), image.Pt(1, 1)), cp.MoveCursor(image.Pt(40, 40))); err != nil {
				t.Fatal(err)
			}
			s.until("the cursor on its plane", func() bool { return !o.flipping && c.settled() })
			n := len(s.dev.commits)
			const moves = 8
			for i := range moves {
				if err := cp.MoveCursor(image.Pt(60+7*i, 50+3*i)); err != nil {
					t.Fatal(err)
				}
				s.until("the move's flip", func() bool { return !o.flipping && c.settled() })
			}
			real, tests := s.dev.since(n)
			t.Logf("%d moves over %d refreshes: %d atomic commits, %d test commits", moves, moves, real, tests)
			if real != moves || tests != 0 {
				t.Fatalf("%d moves took %d commits and %d tests, want one commit each and no test", moves, real, tests)
			}
			if x := int32(s.property(c.plane.id, kdrm.ObjectPlane, "CRTC_X")); x != int32(60+7*(moves-1)-1) {
				t.Fatalf("the cursor plane sits at x %d after the last move", x)
			}
			n = len(s.dev.commits)
			if err := cp.MoveCursor(image.Pt(math.MaxInt32-4, 50)); err != nil && !errors.Is(err, unix.ERANGE) {
				t.Fatal(err)
			}
			s.until("the refused move's commit", func() bool { return c.refused != nil })
			err = cp.MoveCursor(image.Pt(60, 50))
			if !errors.Is(err, unix.ERANGE) || !errors.Is(s.dev.errs[len(s.dev.errs)-1], unix.ERANGE) {
				t.Fatalf("a cursor position past INT_MAX: %v after %v, want the kernel's ERANGE as the refusal", err, s.dev.errs[len(s.dev.errs)-1])
			}
			if real, tests := s.dev.since(n); real != 1 || tests != 0 {
				t.Fatalf("the refused move took %d commits and %d tests, want the real commit alone", real, tests)
			}
			t.Logf("the kernel refused the cursor: %v", err)
			s.frame(o)
			if fb := s.property(c.plane.id, kdrm.ObjectPlane, "FB_ID"); fb != 0 {
				t.Fatalf("a refused cursor stayed on its plane with framebuffer %d after the next frame", fb)
			}
			if _, err := cp.BeginCursor(); !errors.Is(err, unix.ERANGE) {
				t.Fatalf("a refused cursor plane still takes images: %v", err)
			}
		})
	}
}

func TestDamageClipsReachTheDriverOnASeat(t *testing.T) {
	s, o := onASeat(t, "virtio_gpu")
	prop, ok := o.primary.props["FB_DAMAGE_CLIPS"]
	if !ok {
		t.Fatalf("the primary plane of %s has no FB_DAMAGE_CLIPS", o.Name())
	}
	damage := image.Rect(32, 32, 96, 96)
	s.h.damage = render.Region{damage}
	const frames = 6
	n := len(s.dev.commits)
	for range frames {
		s.frame(o)
	}
	clipped := 0
	for _, c := range s.dev.commits[n:] {
		if c.flags&kdrm.AtomicTestOnly == 0 && c.values[[2]uint32{o.primary.id, prop.ID}] != 0 {
			clipped++
		}
	}
	t.Logf("damage-clips: %d of %d frames carried FB_DAMAGE_CLIPS for %v", clipped, frames, damage)
	if clipped != frames {
		t.Fatalf("%d of %d frames damaging %v carried damage clips", clipped, frames, damage)
	}
}

func TestAnOverlayTakesAClientBufferOnASeat(t *testing.T) {
	s, o := onASeat(t, "vkms")
	if len(o.overlays) == 0 {
		o.overlays = []*plane{s.unstackedOverlay(o)}
	}
	size := image.Pt(256, 144)
	d, free, err := testclient.UDMABUF(size, testclient.Fill(0x10, 0x80, 0xf0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := free(); err != nil {
			t.Error(err)
		}
	})
	if d.Format != scanoutFormat || d.Modifier != fourcc.Linear {
		t.Fatalf("the client buffer is %v with %v", d.Format, d.Modifier)
	}
	key := &fakeBuffer{d}
	dst := image.Rectangle{Min: image.Pt(100, 60), Max: image.Pt(100, 60).Add(size)}
	n := len(s.dev.commits)
	if !testOverlay(o, key, d, dst) {
		t.Fatalf("vkms refused a 1:1 opaque %v overlay at %v: %v", d.Format, dst, s.dev.errs[len(s.dev.errs)-1])
	}
	if real, tests := s.dev.since(n); real != 0 || tests != 1 {
		t.Fatalf("testing the overlay took %d commits and %d tests", real, tests)
	}
	released := false
	if err := o.SetOverlays([]backend.Overlay{overlayLayer(key, d, dst, func() error { released = true; return nil })}); err != nil {
		t.Fatal(err)
	}
	if flip, err := o.FlipOverlays(); err != nil || flip != backend.OverlaysFlipped {
		t.Fatalf("FlipOverlay = %d, %v", flip, err)
	}
	s.until("the overlay's flip", func() bool { return !o.flipping })
	p := o.overlays[0].id
	if fb, crtc, x := s.property(p, kdrm.ObjectPlane, "FB_ID"), s.property(p, kdrm.ObjectPlane, "CRTC_ID"), s.property(p, kdrm.ObjectPlane, "CRTC_X"); fb == 0 || crtc != uint64(o.crtc.id) || x != uint64(dst.Min.X) {
		t.Fatalf("after the flip overlay plane %d shows framebuffer %d on CRTC %d at x %d", p, fb, crtc, x)
	}
	t.Logf("overlay plane %d scans out the client buffer at %v", p, dst)
	if err := o.SetOverlays(nil); err != nil {
		t.Fatal(err)
	}
	if flip, err := o.FlipOverlays(); err != nil || flip != backend.OverlaysFlipped {
		t.Fatalf("clearing the overlay: FlipOverlay = %d, %v", flip, err)
	}
	s.until("the cleared overlay's flip", func() bool { return !o.flipping && released })
	if fb := s.property(p, kdrm.ObjectPlane, "FB_ID"); fb != 0 {
		t.Fatalf("a cleared overlay still shows framebuffer %d", fb)
	}
}

func TestPowerOffAndDisableDuringAFlipOnASeat(t *testing.T) {
	for _, driver := range seatDrivers {
		t.Run(driver, func(t *testing.T) {
			s, o := onASeat(t, driver)
			mode := modeOf(o.mode)
			n := len(s.dev.commits)
			s.duringAFlip(o, func() error { return o.Commit(backend.OutputState{Power: backend.PoweredOff, Mode: mode}) })
			s.until("the output off", func() bool { return o.off && !o.flipping && !o.offDue })
			if active := s.property(o.crtc.id, kdrm.ObjectCRTC, "ACTIVE"); active != 0 {
				t.Fatalf("CRTC %d is still active after power-off", o.crtc.id)
			}
			off := s.dev.commits[len(s.dev.commits)-1]
			if off.flags&kdrm.AtomicNonblock == 0 || s.dev.errs[len(s.dev.errs)-1] != nil {
				t.Fatalf("the power-off committed with flags %#x: %v", off.flags, s.dev.errs[len(s.dev.errs)-1])
			}
			real, tests := s.dev.since(n)
			t.Logf("power-off during a flip: %d commits, %d tests, the last nonblocking", real, tests)
			if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			s.until("a frame after power-on", func() bool {
				return !o.off && s.h.presents > 0 && !o.flipping && s.property(o.crtc.id, kdrm.ObjectCRTC, "ACTIVE") == 1
			})
			n = len(s.dev.commits)
			s.duringAFlip(o, func() error { return o.Commit(backend.OutputState{Power: backend.Disabled, Mode: mode}) })
			if o.enabled {
				t.Fatal("the output is still enabled")
			}
			if active := s.property(o.crtc.id, kdrm.ObjectCRTC, "ACTIVE"); active != 0 {
				t.Fatalf("CRTC %d is still active after disable", o.crtc.id)
			}
			flags := make([]uint32, 0, 2)
			for i, c := range s.dev.commits[n:] {
				flags = append(flags, c.flags)
				if err := s.dev.errs[n+i]; err != nil && !errors.Is(err, unix.EBUSY) {
					t.Fatalf("disabling during a flip: %v", err)
				}
			}
			t.Logf("disable during a flip: commits with flags %#x, errors %v", flags, s.dev.errs[n:])
			if flags[0]&kdrm.AtomicNonblock == 0 {
				t.Fatalf("the disable did not try a nonblocking commit first: %#x", flags)
			}
		})
	}
}

func (s *seat) duringAFlip(o *Output, act func() error) {
	s.t.Helper()
	ran, flipping := false, false
	s.h.then = func() error {
		ran, flipping = true, o.flipping
		return act()
	}
	if err := o.ScheduleFrame(); err != nil {
		s.t.Fatal(err)
	}
	s.until("a frame to act behind", func() bool { return ran })
	if !flipping {
		s.t.Fatal("the frame was not flipping when the test acted behind it")
	}
}

func (s *seat) unstackedOverlay(o *Output) *plane {
	s.t.Helper()
	ids, err := s.dev.Planes()
	if err != nil {
		s.t.Fatal(err)
	}
	for _, id := range ids {
		info, err := s.dev.Plane(id)
		if err != nil {
			s.t.Fatal(err)
		}
		props, err := s.dev.Properties(id, kdrm.ObjectPlane)
		if err != nil {
			s.t.Fatal(err)
		}
		kind, err := s.dev.PropertyInfo(props["type"].ID)
		if err != nil {
			s.t.Fatal(err)
		}
		if props["type"].Value != kind.Enums["Overlay"] || info.PossibleCRTCs&(1<<o.crtc.index) == 0 {
			continue
		}
		if _, stacked := props["zpos"]; stacked {
			s.t.Fatalf("overlay plane %d has a zpos, yet %s took no overlay", id, o.Name())
		}
		var blob []byte
		if in, ok := props["IN_FORMATS"]; ok && in.Value != 0 {
			if blob, err = s.dev.Blob(uint32(in.Value)); err != nil {
				s.t.Fatal(err)
			}
		}
		p := &plane{kmsObject: kmsObject{id, props}, kind: overlayPlane, possible: info.PossibleCRTCs, taken: true}
		if p.clients, err = s.b.clientFormats(info.Formats, blob); err != nil {
			s.t.Fatal(err)
		}
		s.t.Logf("overlay plane %d has no zpos, which lys-wm needs to stack it, so the test gives it to %s", id, o.Name())
		return p
	}
	s.t.Fatalf("%s has no overlay plane; load vkms with enable_overlay=1", o.Name())
	return nil
}
