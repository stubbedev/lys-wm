package syncobj

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	cld "github.com/stubbedev/lys-wm/internal/protocol/client/linuxdmabuf"
	cls "github.com/stubbedev/lys-wm/internal/protocol/client/linuxdrmsyncobj"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wlclient"
)

func device(t *testing.T) *drm.Device {
	t.Helper()
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil || len(nodes) == 0 {
		t.Skip("no DRM render node")
	}
	d, err := drm.Open(nodes[0])
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if v, err := d.Cap(drm.CapSyncobjTimeline); err != nil || v == 0 {
		t.Skipf("%s has no timeline syncobjs: %v", nodes[0], err)
	}
	return d
}

type importer struct{}

func (importer) DMABUFFormats() ([]gpu.FormatModifier, error) {
	return []gpu.FormatModifier{{Format: fourcc.XRGB8888, Modifier: fourcc.Linear}}, nil
}

func (importer) MainDevice() (uint64, error) { return 0, gpu.ErrUnsupported }

func (importer) CheckDMABUF(gpu.DMABUF) error { return nil }

func (importer) BufferDestroyed(*dmabuf.Buffer) error { return nil }

func (importer) SurfaceScanout(*surface.Surface) gpu.ScanoutTarget { return gpu.ScanoutTarget{} }

func (importer) Awaiting() error { return nil }

type fencer struct{ d *drm.Device }

func (f fencer) SyncFile() (int, error) {
	s, err := f.d.CreateSyncobj(true)
	if err != nil {
		return -1, err
	}
	fd, err := f.d.ExportSyncFile(s, 0)
	return fd, errors.Join(err, f.d.DestroySyncobj(s))
}

type screen struct {
	mu     sync.Mutex
	hold   bool
	held   []func() error
	awaits int
}

func (s *screen) Awaiting() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.awaits++
	return nil
}

func (s *screen) OffScreen(_ *dmabuf.Buffer, f func() error) error {
	s.mu.Lock()
	if !s.hold {
		s.mu.Unlock()
		return f()
	}
	defer s.mu.Unlock()
	s.held = append(s.held, f)
	return nil
}

func (s *screen) leave() error {
	s.mu.Lock()
	held := s.held
	s.held, s.hold = nil, false
	s.mu.Unlock()
	errs := make([]error, 0, len(held))
	for _, f := range held {
		errs = append(errs, f())
	}
	return errors.Join(errs...)
}

func (s *screen) holding() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.held)
}

type recorder struct {
	mu      sync.Mutex
	commits int
}

func (r *recorder) Committed(*surface.Surface) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commits++
	return nil
}

func (r *recorder) Destroyed(*surface.Surface) error { return nil }

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commits
}

type env struct {
	t       *testing.T
	l       *loop.Loop
	dev     *drm.Device
	rec     *recorder
	scr     *screen
	c       *wlclient.Display
	cmp     *cw.WlCompositor
	buf     *cld.ZwpLinuxDmabufV1
	mgr     *cls.WpLinuxDrmSyncobjManagerV1
	manager *Manager
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type noEvents struct{}

func (noEvents) Format(*cld.ZwpLinuxDmabufV1, uint32) error                   { return nil }
func (noEvents) Modifier(*cld.ZwpLinuxDmabufV1, uint32, uint32, uint32) error { return nil }

func setup(t *testing.T) *env {
	t.Helper()
	dev := device(t)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	dir := testenv.SocketDir(t)
	d, err := server.New(l, server.Options{RuntimeDir: dir, Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, l: l, dev: dev, rec: &recorder{}, scr: &screen{}}
	if _, err := surface.New(d, e.rec); err != nil {
		t.Fatal(err)
	}
	m, err := dmabuf.New(d, l, importer{}, importer{})
	if err != nil {
		t.Fatal(err)
	}
	if e.manager, err = New(d, l, dev, fencer{dev}, e.scr); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		if err := l.Post(func() error {
			l.Stop()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(<-done, m.Close(), d.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	})
	if e.c, err = wlclient.Connect(filepath.Join(dir, d.Name())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.c.Close() })
	globals := registry{}
	reg, err := e.c.WlDisplay().GetRegistry(globals)
	if err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
	var errs [3]error
	e.cmp, errs[0] = cw.NewWlCompositor(e.c, 6)
	e.buf, errs[1] = cld.NewZwpLinuxDmabufV1(e.c, 3, noEvents{})
	e.mgr, errs[2] = cls.NewWpLinuxDrmSyncobjManagerV1(e.c, 1)
	if err := errors.Join(errors.Join(errs[:]...), reg.Bind(globals["wl_compositor"], e.cmp), reg.Bind(globals["zwp_linux_dmabuf_v1"], e.buf), reg.Bind(globals["wp_linux_drm_syncobj_manager_v1"], e.mgr)); err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
	return e
}

func (e *env) roundtrip() {
	e.t.Helper()
	if err := e.c.Roundtrip(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) timeline() (drm.Syncobj, *cls.WpLinuxDrmSyncobjTimelineV1) {
	e.t.Helper()
	s, err := e.dev.CreateSyncobj(false)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = e.dev.DestroySyncobj(s) })
	fd, err := e.dev.ExportSyncobj(s)
	if err != nil {
		e.t.Fatal(err)
	}
	defer unix.Close(fd)
	tl, err := e.mgr.ImportTimeline(fd)
	if err != nil {
		e.t.Fatal(err)
	}
	return s, tl
}

type released struct{}

func (released) Release(*cw.WlBuffer) error { return nil }

type noParams struct{}

func (noParams) Created(*cld.ZwpLinuxBufferParamsV1, *cw.WlBuffer) (cw.WlBufferHandler, error) {
	return nil, errors.New("unexpected created")
}
func (noParams) Failed(*cld.ZwpLinuxBufferParamsV1) error { return errors.New("unexpected failed") }

func (e *env) dmabuf() *cw.WlBuffer {
	e.t.Helper()
	fd, err := unix.MemfdCreate("plane", unix.MFD_CLOEXEC)
	if err != nil {
		e.t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 16*16*4); err != nil {
		e.t.Fatal(err)
	}
	p, err := e.buf.CreateParams(noParams{})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := p.Add(fd, 0, 0, 64, 0, 0); err != nil {
		e.t.Fatal(err)
	}
	b, err := p.CreateImmed(released{}, 16, 16, uint32(fourcc.XRGB8888), 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *env) surface() (*cw.WlSurface, *cls.WpLinuxDrmSyncobjSurfaceV1) {
	e.t.Helper()
	s, err := e.cmp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		e.t.Fatal(err)
	}
	ss, err := e.mgr.GetSurface(s)
	if err != nil {
		e.t.Fatal(err)
	}
	return s, ss
}

func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
		e.roundtrip()
	}
}

func (e *env) signaledAt(s drm.Syncobj, point uint64) bool {
	e.t.Helper()
	v, err := e.dev.Query(s)
	if err != nil {
		e.t.Fatal(err)
	}
	return v >= point
}

func TestCommitsWaitForTheirAcquirePoint(t *testing.T) {
	e := setup(t)
	acquire, atl := e.timeline()
	release, rtl := e.timeline()
	s, ss := e.surface()
	first := e.dmabuf()
	for _, err := range []error{ss.SetAcquirePoint(atl, 0, 1), ss.SetReleasePoint(rtl, 0, 1), s.Attach(first, 0, 0), s.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	e.roundtrip()
	time.Sleep(20 * time.Millisecond)
	e.roundtrip()
	if n := e.rec.count(); n != 0 {
		t.Fatalf("a commit took effect before its acquire point: %d commits", n)
	}
	if err := e.dev.Signal(acquire, 1); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the commit after its acquire point", func() bool { return e.rec.count() == 1 })
	if e.signaledAt(release, 1) {
		t.Fatal("the shown buffer's release point signalled")
	}
	second := e.dmabuf()
	for _, err := range []error{ss.SetAcquirePoint(atl, 0, 2), ss.SetReleasePoint(rtl, 0, 2), s.Attach(second, 0, 0), s.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	third := e.dmabuf()
	for _, err := range []error{ss.SetAcquirePoint(atl, 0, 3), ss.SetReleasePoint(rtl, 0, 3), s.Attach(third, 0, 0), s.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	e.roundtrip()
	if err := e.dev.Signal(acquire, 3); err != nil {
		t.Fatal(err)
	}
	e.waitFor("both commits, each in turn", func() bool { return e.rec.count() == 3 })
	e.waitFor("the replaced and skipped buffers released", func() bool { return e.signaledAt(release, 2) })
	if e.signaledAt(release, 3) {
		t.Fatal("the buffer on screen was released")
	}
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the last buffer released with its surface", func() bool { return e.signaledAt(release, 3) })
}

func TestBuffersOnScreenReleaseWhenTheyLeaveIt(t *testing.T) {
	e := setup(t)
	acquire, atl := e.timeline()
	release, rtl := e.timeline()
	s, ss := e.surface()
	e.scr.hold = true
	for i, b := range []*cw.WlBuffer{e.dmabuf(), e.dmabuf()} {
		point := uint32(i + 1)
		for _, err := range []error{ss.SetAcquirePoint(atl, 0, point), ss.SetReleasePoint(rtl, 0, point), s.Attach(b, 0, 0), s.Commit()} {
			if err != nil {
				t.Fatal(err)
			}
		}
		e.roundtrip()
		if err := e.dev.Signal(acquire, uint64(point)); err != nil {
			t.Fatal(err)
		}
		e.waitFor("the commit after its acquire point", func() bool { return e.rec.count() == i+1 })
	}
	e.waitFor("the replaced buffer handed to the screen", func() bool { return e.scr.holding() == 1 })
	if e.signaledAt(release, 1) {
		t.Fatal("a replaced buffer still on screen was released")
	}
	if err := e.l.Post(e.scr.leave); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the buffer released once off screen", func() bool { return e.signaledAt(release, 1) })
}

func TestProtocolErrors(t *testing.T) {
	type step func(e *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error
	cases := map[string]struct {
		code      uint32
		onManager bool
		run       step
	}{
		"no buffer": {3, false, func(_ *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error {
			return errors.Join(ss.SetAcquirePoint(tl, 0, 1), ss.SetReleasePoint(tl, 0, 2), s.Commit())
		}},
		"no acquire point": {4, false, func(e *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error {
			return errors.Join(ss.SetReleasePoint(tl, 0, 2), s.Attach(e.dmabuf(), 0, 0), s.Commit())
		}},
		"no release point": {5, false, func(e *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error {
			return errors.Join(ss.SetAcquirePoint(tl, 0, 1), s.Attach(e.dmabuf(), 0, 0), s.Commit())
		}},
		"conflicting points": {6, false, func(e *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error {
			return errors.Join(ss.SetAcquirePoint(tl, 0, 4), ss.SetReleasePoint(tl, 0, 4), s.Attach(e.dmabuf(), 0, 0), s.Commit())
		}},
		"surface exists": {0, true, func(e *env, s *cw.WlSurface, _ *cls.WpLinuxDrmSyncobjSurfaceV1, _ *cls.WpLinuxDrmSyncobjTimelineV1) error {
			_, err := e.mgr.GetSurface(s)
			return err
		}},
		"invalid timeline": {1, true, func(e *env, _ *cw.WlSurface, _ *cls.WpLinuxDrmSyncobjSurfaceV1, _ *cls.WpLinuxDrmSyncobjTimelineV1) error {
			fd, err := unix.MemfdCreate("not-a-syncobj", unix.MFD_CLOEXEC)
			if err != nil {
				return err
			}
			defer unix.Close(fd)
			_, err = e.mgr.ImportTimeline(fd)
			return err
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			_, tl := e.timeline()
			s, ss := e.surface()
			if err := tc.run(e, s, ss, tl); err != nil {
				t.Fatal(err)
			}
			want := ss.Base().ID()
			if tc.onManager {
				want = e.mgr.Base().ID()
			}
			var pe *wire.ProtocolError
			if err := e.c.Roundtrip(); !errors.As(err, &pe) || pe.Code != tc.code || pe.Object != want {
				t.Fatalf("%v, want error %d on object %d", err, tc.code, want)
			}
		})
	}
}

type finishedGPU struct{}

func (finishedGPU) SyncFile() (int, error) { return -1, nil }

func TestAReleasePointSignalsAtOnceWhenTheGPUHasFinished(t *testing.T) {
	dev := device(t)
	s, err := dev.CreateSyncobj(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.DestroySyncobj(s) })
	tl := &timeline{m: &Manager{dev: dev, fencer: finishedGPU{}}, handle: s, refs: 2}
	how, err := (&point{t: tl, value: 5}).armAfterGPU()
	if err != nil {
		t.Fatal(err)
	}
	if v, err := dev.Query(s); err != nil || v != 5 || how != signalled {
		t.Fatalf("the timeline is at %d, %v, armed %d, want 5 signalled", v, err, how)
	}
}

func TestASurfaceReusesItsEventFDAcrossCommits(t *testing.T) {
	e := setup(t)
	acquire, atl := e.timeline()
	_, rtl := e.timeline()
	s, ss := e.surface()
	const commits = 8
	for i := range uint32(commits) {
		b := e.dmabuf()
		for _, err := range []error{ss.SetAcquirePoint(atl, 0, i+1), ss.SetReleasePoint(rtl, 0, i+1), s.Attach(b, 0, 0), s.Commit()} {
			if err != nil {
				t.Fatal(err)
			}
		}
		e.roundtrip()
		if err := e.dev.Signal(acquire, uint64(i+1)); err != nil {
			t.Fatal(err)
		}
		e.waitFor("the commit after its acquire point", func() bool { return e.rec.count() == int(i+1) })
	}
	made := make(chan uint64, 1)
	if err := e.l.Post(func() error {
		made <- e.manager.eventFDs
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := <-made; got != 1 {
		t.Fatalf("%d commits made %d eventfds", commits, got)
	}
}

type acquireWait struct {
	surface wire.ObjectID
	acquire uint64
	sweeps  uint64
}

type owedRelease struct {
	surface  wire.ObjectID
	buffer   string
	timeline wire.ObjectID
	point    uint64
	arming   arming
	sweeps   uint64
	refs     int
}

type swept struct {
	waits    []acquireWait
	releases []owedRelease
	busy     bool
}

func (e *env) sweepAll() swept {
	e.t.Helper()
	got := make(chan swept, 1)
	if err := e.l.Post(func() error {
		var s swept
		var err error
		s.busy, err = e.manager.Sweep(func(sf *surface.Surface, acquire, sweeps uint64) {
			s.waits = append(s.waits, acquireWait{sf.Resource().Base().ID(), acquire, sweeps})
		}, func(r Release, sweeps uint64) {
			s.releases = append(s.releases, owedRelease{r.Surface().Resource().Base().ID(), r.Buffer().String(), r.p.res.Base().ID(), r.p.value, r.arming, sweeps, r.p.t.refs})
		})
		got <- s
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return <-got
}

func (e *env) sweep() ([]acquireWait, bool) {
	e.t.Helper()
	s := e.sweepAll()
	if len(s.releases) != 0 {
		e.t.Fatalf("a sweep found release points owed: %v", s.releases)
	}
	return s.waits, s.busy
}

func TestASweepNamesTheSurfaceWaitingOnItsOldestAcquirePoint(t *testing.T) {
	e := setup(t)
	acquire, atl := e.timeline()
	_, rtl := e.timeline()
	s, ss := e.surface()
	if waits, busy := e.sweep(); busy || len(waits) != 0 {
		t.Fatalf("a sweep before any commit found %v", waits)
	}
	for _, point := range []uint32{1, 2} {
		for _, err := range []error{ss.SetAcquirePoint(atl, 0, point), ss.SetReleasePoint(rtl, 0, point), s.Attach(e.dmabuf(), 0, 0), s.Commit()} {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	e.roundtrip()
	for want := range uint64(3) {
		waits, busy := e.sweep()
		if !busy || !slices.Equal(waits, []acquireWait{{s.Base().ID(), 1, want + 1}}) {
			t.Fatalf("sweep %d found %v, want wl_surface@%d waiting %d sweeps on acquire point 1", want+1, waits, s.Base().ID(), want+1)
		}
	}
	e.scr.mu.Lock()
	awaits := e.scr.awaits
	e.scr.mu.Unlock()
	if awaits != 2 {
		t.Fatalf("the screen heard of %d acquire waits, want 2", awaits)
	}
	if err := e.dev.Signal(acquire, 2); err != nil {
		t.Fatal(err)
	}
	e.waitFor("both commits", func() bool { return e.rec.count() == 2 })
	if waits, busy := e.sweep(); busy || len(waits) != 0 {
		t.Fatalf("a sweep after the acquire points signalled found %v", waits)
	}
}

type brokenGPU struct{}

func (brokenGPU) SyncFile() (int, error) { return -1, errors.New("the GPU is gone") }

type notAFence struct{}

func (notAFence) SyncFile() (int, error) {
	return unix.MemfdCreate("not-a-sync-file", unix.MFD_CLOEXEC)
}

func TestAReleasePointSignalsEvenIfItsGPUFenceCannotBeHad(t *testing.T) {
	for name, f := range map[string]Fencer{"no fence": brokenGPU{}, "a fence the timeline refuses": notAFence{}} {
		t.Run(name, func(t *testing.T) {
			dev := device(t)
			s, err := dev.CreateSyncobj(false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = dev.DestroySyncobj(s) })
			tl := &timeline{m: &Manager{dev: dev, fencer: f}, handle: s, refs: 2}
			how, err := (&point{t: tl, value: 5}).armAfterGPU()
			if err == nil {
				t.Fatal("the lost GPU fence was not reported")
			}
			if v, err := dev.Query(s); err != nil || v != 5 || how != signalled {
				t.Fatalf("the timeline is at %d, %v, armed %d, want 5 signalled: the client would wait on its release for ever", v, err, how)
			}
		})
	}
}

type withheldFences struct{ *drm.Device }

func (withheldFences) ImportSyncFile(drm.Syncobj, uint64, int) error { return nil }

type pendingGPU struct{}

func (pendingGPU) SyncFile() (int, error) {
	return unix.MemfdCreate("pending-fence", unix.MFD_CLOEXEC)
}

func (e *env) show(s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, acquire drm.Syncobj, atl, rtl *cls.WpLinuxDrmSyncobjTimelineV1, point uint32) *cw.WlBuffer {
	e.t.Helper()
	b := e.dmabuf()
	if err := e.dev.Signal(acquire, uint64(point)); err != nil {
		e.t.Fatal(err)
	}
	want := e.rec.count() + 1
	for _, err := range []error{ss.SetAcquirePoint(atl, 0, point), ss.SetReleasePoint(rtl, 0, point), s.Attach(b, 0, 0), s.Commit()} {
		if err != nil {
			e.t.Fatal(err)
		}
	}
	e.waitFor("the commit after its acquire point", func() bool { return e.rec.count() == want })
	return b
}

func (e *env) arm(dev Device, f Fencer) {
	e.t.Helper()
	if err := e.l.Post(func() error {
		e.manager.dev, e.manager.fencer = dev, f
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

func TestASweepNamesAReleasePointOwedUntilTheKernelReachesIt(t *testing.T) {
	e := setup(t)
	e.arm(withheldFences{e.dev}, pendingGPU{})
	acquire, atl := e.timeline()
	release, rtl := e.timeline()
	s, ss := e.surface()
	first := e.show(s, ss, acquire, atl, rtl, 1)
	if got := e.sweepAll(); got.busy || len(got.releases) != 0 {
		t.Fatalf("a sweep with the buffer still on screen found %v", got.releases)
	}
	e.show(s, ss, acquire, atl, rtl, 2)
	pid := os.Getpid()
	for want := range uint64(3) {
		got := e.sweepAll()
		owed := []owedRelease{{s.Base().ID(), fmt.Sprintf("wl_buffer@%d of pid %d", first.Base().ID(), pid), rtl.Base().ID(), 1, fenced, want + 1, 3}}
		if !got.busy || len(got.waits) != 0 || !slices.Equal(got.releases, owed) {
			t.Fatalf("sweep %d found %+v, busy %v, want %+v", want+1, got.releases, got.busy, owed)
		}
	}
	if err := e.dev.Signal(release, 1); err != nil {
		t.Fatal(err)
	}
	if got := e.sweepAll(); got.busy || len(got.releases) != 0 {
		t.Fatalf("a sweep after the release point signalled found %v, busy %v", got.releases, got.busy)
	}
	refs := make(chan int, 1)
	if err := e.l.Post(func() error {
		for _, ss := range e.manager.surfaces {
			refs <- ss.current.release.t.refs
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := <-refs; n != 2 {
		t.Fatalf("the release timeline holds %d references once the owed point signalled, want 2: its object and the buffer on screen", n)
	}
}

func TestASweepFindsNoReleasePointOwedOnceItsGPUFenceSignalled(t *testing.T) {
	e := setup(t)
	acquire, atl := e.timeline()
	_, rtl := e.timeline()
	s, ss := e.surface()
	for point := range uint32(3) {
		e.show(s, ss, acquire, atl, rtl, point+1)
	}
	if got := e.sweepAll(); got.busy || len(got.releases) != 0 {
		t.Fatalf("a sweep found %v owed, busy %v, after their fences signalled", got.releases, got.busy)
	}
}
