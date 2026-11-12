package dmabuf

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	cld "github.com/stubbedev/lys-wm/internal/protocol/client/linuxdmabuf"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wlclient"
	cfb "github.com/stubbedev/lys-wm/internal/wlclient/feedback"
)

var formats = []gpu.FormatModifier{
	{Format: fourcc.ARGB8888, Modifier: fourcc.Linear},
	{Format: fourcc.ARGB8888, Modifier: fourcc.Invalid},
	{Format: fourcc.XRGB8888, Modifier: fourcc.Linear},
	{Format: fourcc.NV12, Modifier: fourcc.Linear},
}

type importer struct {
	extra   []gpu.FormatModifier
	device  error
	mu      sync.Mutex
	checked []gpu.DMABUF
	reject  bool
	gone    []*Buffer
	plane   gpu.ScanoutTarget
	surface *surface.Surface
	awaits  int
}

func (i *importer) DMABUFFormats() ([]gpu.FormatModifier, error) {
	return append(slices.Clip(formats), i.extra...), nil
}

const mainDevice = 0xe280

func (i *importer) MainDevice() (uint64, error) { return mainDevice, i.device }

func (i *importer) CheckDMABUF(b gpu.DMABUF) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.checked = append(i.checked, b)
	if i.reject {
		return errors.New("the GPU refuses")
	}
	return nil
}

func (i *importer) SurfaceScanout(s *surface.Surface) gpu.ScanoutTarget {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.surface = s
	return i.plane
}

func (i *importer) Awaiting() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.awaits++
	return nil
}

func (*importer) Committed(*surface.Surface) error { return nil }

func (*importer) Destroyed(*surface.Surface) error { return nil }

func (e *env) scanout(t gpu.ScanoutTarget) {
	e.t.Helper()
	e.imp.mu.Lock()
	e.imp.plane = t
	s := e.imp.surface
	e.imp.mu.Unlock()
	if s == nil {
		e.t.Fatal("no surface asked for feedback")
	}
	if err := e.l.Post(func() error { return e.m.ScanoutChanged(s) }); err != nil {
		e.t.Fatal(err)
	}
}

func (i *importer) BufferDestroyed(b *Buffer) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.gone = append(i.gone, b)
	return nil
}

type env struct {
	t   *testing.T
	dir string
	l   *loop.Loop
	d   *server.Display
	imp *importer
	m   *Manager
}

func setup(t *testing.T, device error, extra ...gpu.FormatModifier) *env {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dir: testenv.SocketDir(t), l: l, imp: &importer{device: device, extra: extra}}
	if e.d, err = server.New(l, server.Options{RuntimeDir: e.dir, Report: func(err error) { t.Error(err) }}); err != nil {
		t.Fatal(err)
	}
	if _, err := surface.New(e.d, e.imp); err != nil {
		t.Fatal(err)
	}
	if e.m, err = New(e.d, l, e.imp, e.imp); err != nil {
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
		if err := errors.Join(<-done, e.m.Close(), e.d.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	})
	return e
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type legacy struct {
	formats   []fourcc.Format
	modifiers []gpu.FormatModifier
}

func (l *legacy) Format(_ *cld.ZwpLinuxDmabufV1, f uint32) error {
	l.formats = append(l.formats, fourcc.Format(f))
	return nil
}

func (l *legacy) Modifier(_ *cld.ZwpLinuxDmabufV1, f, hi, lo uint32) error {
	l.modifiers = append(l.modifiers, gpu.FormatModifier{Format: fourcc.Format(f), Modifier: fourcc.Modifier(wire.Join(hi, lo))})
	return nil
}

type client struct {
	t       *testing.T
	d       *wlclient.Display
	reg     *cw.WlRegistry
	globals registry
	linux   *cld.ZwpLinuxDmabufV1
	legacy  *legacy
}

func (c *client) surface() *cw.WlSurface {
	c.t.Helper()
	cmp, err := cw.NewWlCompositor(c.d, 6)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.reg.Bind(c.globals["wl_compositor"], cmp); err != nil {
		c.t.Fatal(err)
	}
	s, err := cmp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		c.t.Fatal(err)
	}
	return s
}

func (e *env) connect(version uint32) *client {
	e.t.Helper()
	d, err := wlclient.Connect(filepath.Join(e.dir, e.d.Name()))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = d.Close() })
	globals := registry{}
	reg, err := d.WlDisplay().GetRegistry(globals)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &client{t: e.t, d: d, reg: reg, globals: globals, legacy: &legacy{}}
	c.roundtrip()
	if c.linux, err = cld.NewZwpLinuxDmabufV1(d, version, c.legacy); err != nil {
		e.t.Fatal(err)
	}
	if err := reg.Bind(globals["zwp_linux_dmabuf_v1"], c.linux); err != nil {
		e.t.Fatal(err)
	}
	c.roundtrip()
	return c
}

func (c *client) roundtrip() {
	c.t.Helper()
	if err := c.d.Roundtrip(); err != nil {
		c.t.Fatal(err)
	}
}

type received struct{ batches []cfb.Feedback }

func (r *received) listener() *cfb.Listener {
	return cfb.NewListener(func(f cfb.Feedback) error {
		r.batches = append(r.batches, f)
		return nil
	})
}

func (r *received) last(n int) ([]cfb.Tranche, bool) {
	if len(r.batches) != n {
		return nil, false
	}
	return r.batches[n-1].Tranches, true
}

func TestFeedbackDescribesTheRenderer(t *testing.T) {
	e := setup(t, nil)
	if e.m.Version() != 5 {
		t.Fatalf("version %d with a main device", e.m.Version())
	}
	c := e.connect(5)
	fb := &received{}
	if _, err := c.linux.GetDefaultFeedback(fb.listener()); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	want := []cfb.Tranche{{Target: mainDevice, Formats: formats}}
	if got, ok := fb.last(1); !ok || fb.batches[0].Main != mainDevice || !slices.EqualFunc(got, want, sameTranche) {
		t.Fatalf("received %+v", fb.batches)
	}
	if len(c.legacy.formats)+len(c.legacy.modifiers) != 0 {
		t.Fatalf("a v5 client got legacy events %+v", c.legacy)
	}
}

func sameTranche(a, b cfb.Tranche) bool {
	return a.Target == b.Target && a.Flags == b.Flags && slices.Equal(a.Formats, b.Formats)
}

func TestASurfaceThatCanBeScannedOutLearnsThePlaneFormats(t *testing.T) {
	e := setup(t, nil)
	c := e.connect(5)
	s := c.surface()
	fb := &received{}
	if _, err := c.linux.GetSurfaceFeedback(fb.listener(), s); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	renderer := cfb.Tranche{Target: mainDevice, Formats: formats}
	if got, ok := fb.last(1); !ok || !slices.EqualFunc(got, []cfb.Tranche{renderer}, sameTranche) {
		t.Fatalf("feedback before any scanout: %+v", fb.batches)
	}
	plane := gpu.ScanoutTarget{Device: 0xe200, Formats: []gpu.FormatModifier{
		{Format: fourcc.XRGB8888, Modifier: fourcc.Linear},
		{Format: fourcc.XRGB8888, Modifier: 7},
		{Format: fourcc.ARGB8888, Modifier: fourcc.Invalid},
	}}
	e.scanout(plane)
	c.roundtrip()
	scan := cfb.Tranche{Target: plane.Device, Formats: []gpu.FormatModifier{plane.Formats[0], plane.Formats[2]}, Flags: cld.ZwpLinuxDmabufFeedbackV1TrancheFlagsScanout}
	if got, ok := fb.last(2); !ok || !slices.EqualFunc(got, []cfb.Tranche{scan, renderer}, sameTranche) {
		t.Fatalf("feedback once the surface can be scanned out: %+v", fb.batches)
	}
	e.scanout(gpu.ScanoutTarget{})
	c.roundtrip()
	if got, ok := fb.last(3); !ok || !slices.EqualFunc(got, []cfb.Tranche{renderer}, sameTranche) {
		t.Fatalf("feedback once the surface can no longer be scanned out: %+v", fb.batches)
	}
}

func TestLegacyEventsWithoutADevice(t *testing.T) {
	e := setup(t, gpu.ErrUnsupported)
	if e.m.Version() != 3 {
		t.Fatalf("version %d without a main device", e.m.Version())
	}
	if c := e.connect(3); !slices.Equal(c.legacy.modifiers, formats) {
		t.Fatalf("modifiers %+v", c.legacy.modifiers)
	}
	if c := e.connect(2); !slices.Equal(c.legacy.formats, []fourcc.Format{fourcc.ARGB8888, fourcc.XRGB8888, fourcc.NV12}) {
		t.Fatalf("formats %+v", c.legacy.formats)
	}
}

func memfd(t *testing.T, size int) int {
	t.Helper()
	fd, err := unix.MemfdCreate("plane", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

type created struct {
	buffer *cw.WlBuffer
	failed bool
}

func (c *created) Created(_ *cld.ZwpLinuxBufferParamsV1, b *cw.WlBuffer) (cw.WlBufferHandler, error) {
	c.buffer = b
	return releases{}, nil
}

func (c *created) Failed(*cld.ZwpLinuxBufferParamsV1) error {
	c.failed = true
	return nil
}

type releases struct{}

func (releases) Release(*cw.WlBuffer) error { return nil }

func TestBuffersAreCheckedAndCreated(t *testing.T) {
	e := setup(t, gpu.ErrUnsupported)
	c := e.connect(3)
	fd := memfd(t, 64*16*4)
	result := &created{}
	p, err := c.linux.CreateParams(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0)); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	if result.buffer == nil || result.failed {
		t.Fatalf("create gave %+v", result)
	}
	e.imp.mu.Lock()
	checked := e.imp.checked
	e.imp.mu.Unlock()
	if len(checked) != 1 || checked[0].Format != fourcc.XRGB8888 || checked[0].Planes[0].Stride != 256 {
		t.Fatalf("checked %+v", checked)
	}
	if err := result.buffer.Destroy(); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	e.imp.mu.Lock()
	gone := len(e.imp.gone)
	e.imp.mu.Unlock()
	if gone != 1 {
		t.Fatalf("%d buffers reported destroyed", gone)
	}

	e.imp.mu.Lock()
	e.imp.reject = true
	e.imp.mu.Unlock()
	refused := &created{}
	if p, err = c.linux.CreateParams(refused); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0)); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	if !refused.failed || refused.buffer != nil {
		t.Fatalf("a refused import gave %+v", refused)
	}
	if p, err = c.linux.CreateParams(&created{}); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(memfd(t, 64*16*4), 0, 0, 256, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateImmed(releases{}, 64, 16, uint32(fourcc.XRGB8888), 0); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := c.d.Roundtrip(); !errors.As(err, &pe) || pe.Code != uint32(cld.ZwpLinuxBufferParamsV1ErrorInvalidWlBuffer) || !strings.Contains(pe.Message, "format XR24 with modifier linear") {
		t.Fatalf("an immediate buffer the GPU refuses: %v", err)
	}
}

func TestTightlyPackedNV12IsInBounds(t *testing.T) {
	e := setup(t, gpu.ErrUnsupported)
	c := e.connect(3)
	const w, h = 64, 16
	fd := memfd(t, w*h*3/2)
	result := &created{}
	p, err := c.linux.CreateParams(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(p.Add(fd, 0, 0, w, 0, 0), p.Add(fd, 1, w*h, w, 0, 0), p.Create(w, h, uint32(fourcc.NV12), 0)); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	if result.buffer == nil || result.failed {
		t.Fatalf("a tightly packed linear NV12 buffer gave %+v", result)
	}
}

func TestParamsErrors(t *testing.T) {
	cases := map[string]struct {
		code  uint32
		build func(t *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error
	}{
		"plane index": {1, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return p.Add(fd, 4, 0, 256, 0, 0)
		}},
		"plane set": {2, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Add(fd, 0, 0, 256, 0, 0))
		}},
		"mixed modifiers": {4, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Add(fd, 1, 0, 256, 0, 1))
		}},
		"missing plane": {3, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 1, 0, 256, 0, 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0))
		}},
		"too few planes": {3, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 64, 0, 0), p.Create(64, 16, uint32(fourcc.NV12), 0))
		}},
		"unknown format": {4, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 16, 'R'|'G'<<8|'1'<<16|'6'<<24, 0))
		}},
		"unknown modifier": {4, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 1, 2), p.Create(64, 16, uint32(fourcc.XRGB8888), 0))
		}},
		"dimensions": {5, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(0, 16, uint32(fourcc.XRGB8888), 0))
		}},
		"out of bounds": {6, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 64, uint32(fourcc.XRGB8888), 0))
		}},
		"used twice": {0, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0))
		}},
		"refused immediate": {7, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			if err := p.Add(fd, 0, 0, 256, 0, 0); err != nil {
				return err
			}
			_, err := p.CreateImmed(releases{}, 64, 16, uint32(fourcc.XRGB8888), cld.ZwpLinuxBufferParamsV1FlagsYInvert)
			return err
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t, gpu.ErrUnsupported)
			c := e.connect(3)
			p, err := c.linux.CreateParams(&created{})
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.build(t, p, memfd(t, 64*16*4)); err != nil {
				t.Fatal(err)
			}
			var pe *wire.ProtocolError
			if err := c.d.Roundtrip(); !errors.As(err, &pe) || pe.Code != tc.code || pe.Object != p.Base().ID() {
				t.Fatalf("%v, want error %d on params", err, tc.code)
			}
			if strings.TrimSpace(pe.Message) == "" {
				t.Fatal("the error has no message")
			}
		})
	}
}

func TestTightlyPackedYUVIsInBoundsForEveryFormat(t *testing.T) {
	const w, h = 64, 16
	for _, tc := range []struct {
		format  fourcc.Format
		strides []uint32
	}{
		{fourcc.P012, []uint32{2 * w, 2 * w}},
		{fourcc.P016, []uint32{2 * w, 2 * w}},
		{fourcc.YUV422, []uint32{w, w / 2, w / 2}},
		{fourcc.NV61, []uint32{w, w}},
		{fourcc.YUYV, []uint32{2 * w}},
		{fourcc.Q410, []uint32{2 * w, 2 * w, 2 * w}},
	} {
		e := setup(t, gpu.ErrUnsupported, gpu.FormatModifier{Format: tc.format, Modifier: fourcc.Linear})
		c := e.connect(3)
		var size uint32
		offsets := make([]uint32, len(tc.strides))
		for i, stride := range tc.strides {
			offsets[i] = size
			size += stride * uint32(tc.format.PlaneRows(i, h))
		}
		fd := memfd(t, int(size))
		result := &created{}
		p, err := c.linux.CreateParams(result)
		if err != nil {
			t.Fatal(err)
		}
		for i, stride := range tc.strides {
			if err := p.Add(fd, uint32(i), offsets[i], stride, 0, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.Create(w, h, uint32(tc.format), 0); err != nil {
			t.Fatal(err)
		}
		c.roundtrip()
		if result.buffer == nil || result.failed {
			t.Errorf("a tightly packed %v buffer gave %+v", tc.format, result)
		}
	}
}

type awaiter func() func() error

func (awaiter) DamagedPixels() int { return 0 }

func (a awaiter) Await() func() error { return a() }

func (awaiter) Surface() *surface.Surface { return nil }

type committing struct {
	awaiter
	to *surface.Surface
}

func (c committing) Surface() *surface.Surface { return c.to }

// A commit of a buffer whose writer is still at work waits for it, as
// linux-dmabuf's implicit synchronization promises the client, and one whose
// writer has finished does not. A pipe's read end stands in for the dmabuf: both
// poll readable when the wait is over.
func TestAwaitWriterHoldsUntilThePlaneIsReadable(t *testing.T) {
	e := setup(t, nil)
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	b := &Buffer{m: e.m, dmabuf: gpu.DMABUF{Planes: []gpu.Plane{{FD: p[0]}}}}
	released := make(chan struct{})
	holds := make(chan int, 1)
	if err := e.l.Post(func() error {
		n := 0
		err := b.AwaitWriter(awaiter(func() func() error {
			n++
			return func() error {
				close(released)
				return nil
			}
		}))
		holds <- n
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := <-holds; n != 1 {
		t.Fatalf("%d holds for a plane with its writer at work", n)
	}
	select {
	case <-released:
		t.Fatal("released with the writer at work")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := unix.Write(p[1], []byte{0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("not released after the writer finished")
	}
	if err := e.l.Post(func() error {
		return b.AwaitWriter(awaiter(func() func() error {
			t.Error("a hold for a plane whose writer has finished")
			return func() error { return nil }
		}))
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAWaitedPlaneKeepsOneDuplicateAcrossCommits(t *testing.T) {
	e := setup(t, nil)
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	b := &Buffer{m: e.m, dmabuf: gpu.DMABUF{Planes: []gpu.Plane{{FD: p[0]}, {FD: p[0]}}}}
	const commits = 20
	for range commits {
		released, held := make(chan struct{}), make(chan int, 1)
		if err := e.l.Post(func() error {
			n := 0
			err := b.AwaitWriter(awaiter(func() func() error {
				n++
				return func() error {
					close(released)
					return nil
				}
			}))
			held <- n
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if n := <-held; n != 1 {
			t.Fatalf("%d holds for two planes of one dmabuf", n)
		}
		if _, err := unix.Write(p[1], []byte{0}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-released:
		case <-time.After(30 * time.Second):
			t.Fatal("not released after the writer finished")
		}
		var one [1]byte
		if _, err := unix.Read(p[0], one[:]); err != nil {
			t.Fatal(err)
		}
	}
	dups := make(chan uint64, 1)
	if err := e.l.Post(func() error {
		dups <- e.m.fenceDups
		return errors.Join(b.fences[0].close())
	}); err != nil {
		t.Fatal(err)
	}
	if got := <-dups; got != 1 {
		t.Fatalf("%d commits duplicated the plane %d times", commits, got)
	}
}

func TestASweepCountsHowLongAPlaneFenceHoldsItsCommit(t *testing.T) {
	e := setup(t, nil)
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	b := &Buffer{m: e.m, dmabuf: gpu.DMABUF{Planes: []gpu.Plane{{FD: p[0]}}}}
	released := make(chan struct{})
	first, second := &surface.Surface{}, &surface.Surface{}
	type seen struct {
		buffers  []*Buffer
		surfaces []*surface.Surface
		sweeps   []uint64
		busy     bool
	}
	sweep := func() seen {
		t.Helper()
		got := make(chan seen, 1)
		if err := e.l.Post(func() error {
			var s seen
			s.busy = e.m.Sweep(func(b *Buffer, on *surface.Surface, sweeps uint64) {
				s.buffers, s.surfaces, s.sweeps = append(s.buffers, b), append(s.surfaces, on), append(s.sweeps, sweeps)
			})
			got <- s
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return <-got
	}
	if s := sweep(); s.busy || len(s.buffers) != 0 {
		t.Fatalf("a sweep with no commit held found %v", s)
	}
	if err := e.l.Post(func() error {
		for _, to := range []*surface.Surface{first, second} {
			if err := b.AwaitWriter(committing{to: to, awaiter: func() func() error {
				return func() error {
					select {
					case <-released:
					default:
						close(released)
					}
					return nil
				}
			}}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for want := range uint64(3) {
		s := sweep()
		if !s.busy || !slices.Equal(s.buffers, []*Buffer{b}) || !slices.Equal(s.sweeps, []uint64{want + 1}) {
			t.Fatalf("sweep %d found %d buffers held for %v sweeps, busy %v; want the one buffer once, held for %d", want+1, len(s.buffers), s.sweeps, s.busy, want+1)
		}
		if !slices.Equal(s.surfaces, []*surface.Surface{first}) {
			t.Fatalf("sweep %d names %d surfaces, want only the one whose commit began the wait", want+1, len(s.surfaces))
		}
	}
	e.imp.mu.Lock()
	awaits := e.imp.awaits
	e.imp.mu.Unlock()
	if awaits != 1 {
		t.Fatalf("the listener heard of %d waits for one plane fence, want 1", awaits)
	}
	if _, err := unix.Write(p[1], []byte{0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("not released after the writer finished")
	}
	if s := sweep(); s.busy || len(s.buffers) != 0 {
		t.Fatalf("a sweep after the writer finished found %v", s)
	}
}
