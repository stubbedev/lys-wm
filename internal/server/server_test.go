package server

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wlclient"
)

type harness struct {
	t       *testing.T
	loop    *loop.Loop
	disp    *Display
	dir     string
	trace   *bytes.Buffer
	report  func(error)
	stopped chan struct{}
	ran     error
	ending  bool
}

func start(t *testing.T) *harness {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, loop: l, dir: testenv.SocketDir(t), trace: &bytes.Buffer{}, report: func(err error) { t.Error(err) }, stopped: make(chan struct{})}
	if h.disp, err = New(l, Options{RuntimeDir: h.dir, Trace: h.trace, Report: func(err error) { h.report(err) }}); err != nil {
		t.Fatal(err)
	}
	go func() {
		h.ran = l.Run()
		close(h.stopped)
	}()
	t.Cleanup(func() {
		if err := l.Post(func() error {
			l.Stop()
			return nil
		}); err != nil {
			t.Error(err)
		}
		<-h.stopped
		if h.ran != nil && !h.ending {
			t.Errorf("Run: %v", h.ran)
		}
		if err := h.disp.Close(); err != nil {
			t.Errorf("Display.Close: %v", err)
		}
		if err := l.Close(); err != nil {
			t.Errorf("Loop.Close: %v", err)
		}
	})
	return h
}

func (h *harness) on(f func()) {
	ran := make(chan struct{})
	if err := h.loop.Post(func() error {
		f()
		close(ran)
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
	select {
	case <-ran:
	case <-h.stopped:
		h.t.Fatalf("the loop stopped: %v", h.ran)
	}
}

func (h *harness) ended() error {
	h.t.Helper()
	select {
	case <-h.stopped:
		h.ending = true
		return h.ran
	case <-time.After(5 * time.Second):
		h.t.Fatal("the loop is still running")
		return nil
	}
}

func (h *harness) reports() func() []error {
	var got []error
	h.on(func() { h.report = func(err error) { got = append(got, err) } })
	return func() []error {
		if h.ending {
			return slices.Clone(got)
		}
		var out []error
		h.on(func() { out = slices.Clone(got) })
		return out
	}
}

func (h *harness) connect() *wlclient.Display {
	h.t.Helper()
	d, err := wlclient.Connect(filepath.Join(h.dir, h.disp.Name()))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = d.Close() })
	return d
}

func (h *harness) clients() int {
	var n int
	h.on(func() { n = len(h.disp.clients) })
	return n
}

func (h *harness) waitClients(n int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.clients() != n {
		if time.Now().After(deadline) {
			h.t.Fatalf("server has %d clients, want %d", h.clients(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type registry struct {
	globals map[uint32]string
	removed []uint32
}

func (r *registry) Global(_ *cw.WlRegistry, name uint32, iface string, version uint32) error {
	r.globals[name] = iface + "/" + string(rune('0'+version))
	return nil
}

func (r *registry) GlobalRemove(_ *cw.WlRegistry, name uint32) error {
	r.removed = append(r.removed, name)
	return nil
}

func (h *harness) registry(d *wlclient.Display) (*cw.WlRegistry, *registry) {
	h.t.Helper()
	r := &registry{globals: map[uint32]string{}}
	reg, err := d.WlDisplay().GetRegistry(r)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := d.Roundtrip(); err != nil {
		h.t.Fatal(err)
	}
	return reg, r
}

func compositorGlobal(bound *[]uint32) BindFunc {
	return func(c *Client, id wire.ObjectID, version uint32) error {
		*bound = append(*bound, version)
		_, err := sw.NewWlCompositor(c, id, version, struct{ sw.WlCompositorHandler }{})
		return err
	}
}

func TestSocketLockAndCleanup(t *testing.T) {
	dir := testenv.SocketDir(t)
	if err := os.WriteFile(filepath.Join(dir, "wayland-0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	first, err := New(l, Options{RuntimeDir: dir, Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(l, Options{RuntimeDir: dir, Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	if first.Name() != "wayland-0" || second.Name() != "wayland-1" {
		t.Fatalf("names = %s, %s; want wayland-0 replacing the stale file, then wayland-1", first.Name(), second.Name())
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(dir, "wayland-0"), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		t.Fatalf("wayland-0 is not a socket: %v", err)
	}
	for _, d := range []*Display{first, second} {
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
	left, err := os.ReadDir(dir)
	if err != nil || len(left) != 0 {
		t.Fatalf("Close left %v behind (%v)", left, err)
	}
	if _, err := New(l, Options{Report: func(error) {}}); err == nil || !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") {
		t.Fatalf("New without a runtime dir = %v", err)
	}
	if _, err := New(l, Options{RuntimeDir: dir}); err == nil || !strings.Contains(err.Error(), "Report is required") {
		t.Fatalf("New without a report hook = %v", err)
	}
	long := filepath.Join(dir, strings.Repeat("x", 100))
	if _, err := New(l, Options{RuntimeDir: long, Report: func(error) {}}); err == nil || !strings.Contains(err.Error(), "a Unix socket allows 107") {
		t.Fatalf("New with an overlong path = %v", err)
	}
}

func TestRegistryAnnouncesAddsAndRemovesGlobals(t *testing.T) {
	h := start(t)
	var bound []uint32
	h.on(func() {
		if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, compositorGlobal(&bound)); err != nil {
			t.Error(err)
		}
	})
	d := h.connect()
	reg, r := h.registry(d)
	if r.globals[1] != "wl_compositor/6" {
		t.Fatalf("globals = %v", r.globals)
	}
	comp, err := cw.NewWlCompositor(d, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Bind(1, comp); err != nil {
		t.Fatal(err)
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.on(func() {
		if len(bound) != 1 || bound[0] != 5 {
			t.Errorf("bind called with %v, want [5]", bound)
		}
	})
	var g *Global
	h.on(func() {
		var err error
		if g, err = h.disp.AddGlobal(sw.WlShmInterface, 2, func(*Client, wire.ObjectID, uint32) error { return nil }); err != nil {
			t.Error(err)
		}
	})
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if r.globals[2] != "wl_shm/2" {
		t.Fatalf("runtime global not announced: %v", r.globals)
	}
	h.on(func() {
		if err := g.Remove(); err != nil {
			t.Error(err)
		}
	})
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(r.removed) != 1 || r.removed[0] != 2 {
		t.Fatalf("removed = %v, want [2]", r.removed)
	}
	if _, late := h.registry(h.connect()); late.globals[2] != "" {
		t.Fatal("a removed global was announced to a new registry")
	}
}

func TestBindErrors(t *testing.T) {
	cases := []struct {
		name string
		bind func(d *wlclient.Display, reg *cw.WlRegistry) error
		want string
	}{
		{"version too high", func(d *wlclient.Display, reg *cw.WlRegistry) error {
			comp, err := cw.NewWlCompositor(d, 7)
			if err != nil {
				return err
			}
			return reg.Bind(1, comp)
		}, "invalid version"},
		{"wrong interface", func(d *wlclient.Display, reg *cw.WlRegistry) error {
			shm, err := cw.NewWlShm(d, 1, struct{ cw.WlShmHandler }{})
			if err != nil {
				return err
			}
			return reg.Bind(1, shm)
		}, "invalid interface"},
		{"unknown global", func(d *wlclient.Display, reg *cw.WlRegistry) error {
			comp, err := cw.NewWlCompositor(d, 1)
			if err != nil {
				return err
			}
			return reg.Bind(99, comp)
		}, "invalid global"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t)
			var bound []uint32
			h.on(func() {
				if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, compositorGlobal(&bound)); err != nil {
					t.Error(err)
				}
			})
			d := h.connect()
			reg, _ := h.registry(d)
			if err := c.bind(d, reg); err != nil {
				t.Fatal(err)
			}
			var pe *wire.ProtocolError
			err := d.Roundtrip()
			if !errors.As(err, &pe) || pe.Object != reg.Base().ID() || pe.Code != uint32(sw.WlDisplayErrorInvalidObject) || !strings.Contains(pe.Message, c.want) {
				t.Fatalf("Roundtrip = %v, want invalid_object on the registry mentioning %q", err, c.want)
			}
			h.waitClients(0)
		})
	}
}

func TestProtocolErrorDisconnectsOnlyThatClient(t *testing.T) {
	h := start(t)
	bad, good := h.connect(), h.connect()
	reg, _ := h.registry(bad)
	h.registry(good)
	if err := bad.Conn().Begin(reg.Base().ID(), 7).End(); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := bad.Roundtrip(); !errors.As(err, &pe) || pe.Object != reg.Base().ID() || pe.Code != uint32(sw.WlDisplayErrorInvalidMethod) {
		t.Fatalf("Roundtrip = %v, want invalid_method on the registry", err)
	}
	if err := bad.Dispatch(); err == nil {
		t.Fatal("the failed client is still connected")
	}
	if err := good.Roundtrip(); err != nil {
		t.Fatalf("the other client was affected: %v", err)
	}
	h.waitClients(1)
}

func TestRequestToUnknownObject(t *testing.T) {
	h := start(t)
	d := h.connect()
	if err := d.Conn().Begin(99, 0).End(); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := d.Roundtrip(); !errors.As(err, &pe) || pe.Object != 1 || pe.Code != uint32(sw.WlDisplayErrorInvalidObject) {
		t.Fatalf("Roundtrip = %v, want invalid_object on wl_display", err)
	}
}

func TestSyncFreesCallbackIDsAndTraces(t *testing.T) {
	h := start(t)
	d := h.connect()
	h.registry(d)
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var trace string
	h.on(func() { trace = h.trace.String() })
	for _, want := range []string{
		"wl_display@1.get_registry(new id wl_registry@2)",
		"wl_display@1.sync(new id wl_callback@3)",
		" -> wl_callback@3.done(",
		" -> wl_display@1.delete_id(3)",
	} {
		if !strings.Contains(trace, want) {
			t.Errorf("trace lacks %q:\n%s", want, trace)
		}
	}
	if n := strings.Count(trace, "sync(new id wl_callback@3)"); n != 2 {
		t.Errorf("callback id 3 used %d times, want 2: delete_id did not free it", n)
	}
}

func TestPeerCredentials(t *testing.T) {
	h := start(t)
	h.registry(h.connect())
	h.on(func() {
		creds := h.disp.clients[0].Creds()
		if int(creds.PID) != os.Getpid() || int(creds.UID) != os.Getuid() {
			t.Errorf("creds = %+v, want pid %d uid %d", creds, os.Getpid(), os.Getuid())
		}
	})
}

func TestClientThatStopsReadingIsDisconnected(t *testing.T) {
	h := start(t)
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrote := make(chan struct{})
	defer func() {
		_ = unix.Shutdown(fd, unix.SHUT_WR)
		<-wrote
		_ = unix.Close(fd)
	}()
	if err := unix.Connect(fd, &unix.SockaddrUnix{Name: filepath.Join(h.dir, h.disp.Name())}); err != nil {
		close(wrote)
		t.Fatal(err)
	}
	const syncs = 40000
	req := make([]byte, 0, 12*syncs)
	for i := range syncs {
		req = binary.NativeEndian.AppendUint32(req, 1)
		req = binary.NativeEndian.AppendUint32(req, 12<<16)
		req = binary.NativeEndian.AppendUint32(req, uint32(2+i))
	}
	go func() {
		defer close(wrote)
		for len(req) > 0 {
			n, err := unix.Write(fd, req)
			if err != nil {
				return
			}
			req = req[n:]
		}
	}()
	h.waitClients(0)
	var stream []byte
	buf := make([]byte, 1<<16)
	for {
		n, err := unix.Read(fd, buf)
		if n <= 0 || err != nil {
			break
		}
		stream = append(stream, buf[:n]...)
	}
	if len(stream) < 1<<16 {
		t.Fatalf("only %d bytes were queued before the disconnect, want more than the output bound", len(stream))
	}
	for len(stream) >= 8 {
		object, word := binary.NativeEndian.Uint32(stream), binary.NativeEndian.Uint32(stream[4:])
		if object == 1 && word&0xffff == 0 {
			t.Fatal("the slow client was sent a protocol error instead of being dropped for back-pressure")
		}
		stream = stream[word>>16:]
	}
}

func TestCloseDisconnectsClients(t *testing.T) {
	h := start(t)
	d := h.connect()
	h.registry(d)
	h.on(func() {
		if err := h.disp.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := d.Dispatch(); err == nil {
		t.Fatal("client still connected after Close")
	}
}

func TestDestroyHooksRunOnDestructorAndDisconnect(t *testing.T) {
	h := start(t)
	var ran []string
	var tracked []*sw.WlCompositor
	h.on(func() {
		if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, func(c *Client, id wire.ObjectID, version uint32) error {
			comp, err := sw.NewWlCompositor(c, id, version, releasable{})
			if err != nil {
				return err
			}
			Track(comp, &tracked, comp)
			OnDestroy(comp, func() error {
				ran = append(ran, "compositor")
				return nil
			})
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	d := h.connect()
	reg, _ := h.registry(d)
	first, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*cw.WlCompositor{first, second} {
		if err := reg.Bind(1, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err == nil {
		t.Fatal("release is a version 7 request and should not be sendable at version 6")
	}
	h.on(func() {
		c := h.disp.clients[0]
		if err := c.Remove(first.Base().ID()); err != nil {
			t.Error(err)
		}
		if len(ran) != 1 || len(tracked) != 1 || tracked[0].Base().ID() != second.Base().ID() {
			t.Errorf("hooks after a destructor: %v, tracked %v", ran, tracked)
		}
	})
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	h.waitClients(0)
	h.on(func() {
		if len(ran) != 2 || len(tracked) != 0 {
			t.Errorf("hooks after disconnect: %v, want both compositors, tracked %v", ran, tracked)
		}
	})
}

type releasable struct{ sw.WlCompositorHandler }

func TestFailingDestroyHooksAreReported(t *testing.T) {
	h := start(t)
	broken := errors.New("hook failed")
	h.on(func() {
		if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, func(c *Client, id wire.ObjectID, version uint32) error {
			comp, err := sw.NewWlCompositor(c, id, version, releasable{})
			if err != nil {
				return err
			}
			OnDestroy(comp, func() error { return broken })
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	d := h.connect()
	reg, _ := h.registry(d)
	first, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(1, first), reg.Bind(1, second), d.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	reported := h.reports()
	h.on(func() {
		if err := h.disp.clients[0].Remove(first.Base().ID()); !errors.Is(err, broken) {
			t.Errorf("Remove = %v, want the hook's error", err)
		}
		if err := h.disp.Close(); err != nil {
			t.Errorf("Close = %v, want the hook's error reported, not returned", err)
		}
	})
	if got := reported(); len(got) != 1 || !errors.Is(got[0], broken) || !strings.Contains(got[0].Error(), fmt.Sprintf("wl_compositor@%d", second.Base().ID())) {
		t.Errorf("reported %v, want the hook's error from tearing the client down", got)
	}
}

func hookedCompositors(h *harness, hooks ...func() error) {
	h.on(func() {
		if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, func(c *Client, id wire.ObjectID, version uint32) error {
			comp, err := sw.NewWlCompositor(c, id, version, releasable{})
			if err != nil {
				return err
			}
			for _, f := range hooks {
				OnDestroy(comp, f)
			}
			return nil
		}); err != nil {
			h.t.Error(err)
		}
	})
}

func bindCompositors(t *testing.T, h *harness, d *wlclient.Display, n int) []*cw.WlCompositor {
	t.Helper()
	reg, _ := h.registry(d)
	comps := make([]*cw.WlCompositor, n)
	for i := range comps {
		var err error
		if comps[i], err = cw.NewWlCompositor(d, 6); err != nil {
			t.Fatal(err)
		}
		if err := reg.Bind(1, comps[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return comps
}

func TestAFailingDestroyHookOfALeavingClientIsReportedAndOthersAreStillServed(t *testing.T) {
	h := start(t)
	broken := errors.New("hook failed")
	var after int
	hookedCompositors(h, func() error { return broken }, func() error { after++; return nil })
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.on(func() {
		if _, err := h.disp.addClient(fds[0], &SecurityContext{Engine: "test", AppID: "org.example.Leaving"}); err != nil {
			t.Error(err)
		}
	})
	leaving, err := wlclient.FromFD(fds[1])
	if err != nil {
		t.Fatal(err)
	}
	staying := h.connect()
	gone := bindCompositors(t, h, leaving, 2)
	bindCompositors(t, h, staying, 1)
	reported := h.reports()
	if err := leaving.Close(); err != nil {
		t.Fatal(err)
	}
	h.waitClients(1)
	got := reported()
	if len(got) != 1 || !errors.Is(got[0], broken) {
		t.Fatalf("reported %v, want the hook's error once", got)
	}
	for _, want := range []string{
		fmt.Sprintf("pid %d", os.Getpid()),
		"org.example.Leaving",
		fmt.Sprintf("wl_compositor@%d", gone[0].Base().ID()),
		fmt.Sprintf("wl_compositor@%d", gone[1].Base().ID()),
	} {
		if !strings.Contains(got[0].Error(), want) {
			t.Errorf("reported %q, want it to name %q", got[0], want)
		}
	}
	h.on(func() {
		if after != 2 {
			t.Errorf("%d of the hooks after the failing ones ran, want 2", after)
		}
	})
	bindCompositors(t, h, staying, 1)
	bindCompositors(t, h, h.connect(), 1)
}

func TestAFatalDestroyHookOfALeavingClientStopsTheCompositor(t *testing.T) {
	h := start(t)
	var after int
	hookedCompositors(h, func() error { return fmt.Errorf("%w: lost while a surface let go", backend.ErrDeviceLost) }, func() error { after++; return nil })
	d := h.connect()
	bindCompositors(t, h, d, 2)
	reported := h.reports()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	err := h.ended()
	if !errors.Is(err, backend.ErrDeviceLost) || !strings.Contains(err.Error(), fmt.Sprintf("tear down the client of pid %d", os.Getpid())) {
		t.Fatalf("Run = %v, want the device loss from tearing the client down", err)
	}
	if after != 2 {
		t.Errorf("%d of the hooks after the fatal ones ran, want 2", after)
	}
	if got := reported(); len(got) != 0 {
		t.Errorf("reported %v, want the fatal error returned, not reported", got)
	}
}

func TestPrivateGlobalsAndSocketpairClients(t *testing.T) {
	h := start(t)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var private *Client
	var bound []uint32
	h.on(func() {
		var err error
		if private, err = h.disp.AddClient(fds[0]); err != nil {
			t.Error(err)
			return
		}
		_, err = h.disp.AddGlobalFor(sw.WlCompositorInterface, 6, func(c *Client) bool { return c == private }, compositorGlobal(&bound))
		if err != nil {
			t.Error(err)
		}
	})
	mine, err := wlclient.FromFD(fds[1])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mine.Close() })
	if _, r := h.registry(mine); r.globals[1] != "wl_compositor/6" {
		t.Fatalf("the chosen client sees %v", r.globals)
	}
	other := h.connect()
	reg, r := h.registry(other)
	if len(r.globals) != 0 {
		t.Fatalf("another client sees %v", r.globals)
	}
	comp, err := cw.NewWlCompositor(other, 6)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Bind(1, comp); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := other.Roundtrip(); !errors.As(err, &pe) || !strings.Contains(pe.Message, "invalid global") {
		t.Fatalf("binding a hidden global: %v", err)
	}
	h.on(func() {
		if len(bound) != 0 {
			t.Errorf("a hidden global was bound: %v", bound)
		}
	})
}

func TestRunningOutOfFDsRefusesAClientAndKeepsServing(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var reported []error
	d, err := New(l, Options{RuntimeDir: testenv.SocketDir(t), Report: func(err error) { reported = append(reported, err) }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dial := func() int {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Connect(fd, &unix.SockaddrUnix{Name: filepath.Join(filepath.Dir(d.socketPath), d.Name())}); err != nil {
			t.Fatal(err)
		}
		return fd
	}
	refused := dial()
	defer unix.Close(refused)
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	var filler []int
	tight := limit
	tight.Cur = uint64(refused) + 64
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &tight); err != nil {
		t.Fatal(err)
	}
	for {
		fd, err := unix.Open(os.DevNull, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			break
		}
		filler = append(filler, fd)
	}
	acceptErr := d.accept(0)
	for _, fd := range filler {
		_ = unix.Close(fd)
	}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	if acceptErr != nil || len(reported) != 1 || !errors.Is(reported[0], unix.EMFILE) {
		t.Fatalf("accept = %v, reported %v; want nil and one EMFILE report", acceptErr, reported)
	}
	if n, err := unix.Read(refused, make([]byte, 1)); n != 0 || err != nil {
		t.Fatalf("the refused client read %d, %v; want EOF", n, err)
	}
	served := dial()
	defer unix.Close(served)
	if err := d.accept(0); err != nil || len(d.clients) != 1 {
		t.Fatalf("accept after the shortage = %v with %d clients", err, len(d.clients))
	}
}

func TestAClientCannotTakeAServerID(t *testing.T) {
	h := start(t)
	d := h.connect()
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var freed wire.ObjectID
	h.on(func() {
		c := h.disp.clients[0]
		id, err := c.NewID()
		if err != nil {
			t.Error(err)
			return
		}
		cb, err := sw.NewWlCallback(c, id, 1)
		if err != nil {
			t.Error(err)
			return
		}
		freed = cb.Base().ID()
		if err := c.Remove(freed); err != nil {
			t.Error(err)
		}
	})
	e := d.Conn().Begin(1, 1)
	e.Uint(uint32(freed))
	if err := e.End(); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := d.Roundtrip(); !errors.As(err, &pe) || pe.Code != uint32(sw.WlDisplayErrorInvalidObject) {
		t.Fatalf("a get_registry with the freed server id %#x: %v", freed, err)
	}
}

func TestEventsToARemovedObjectAreDropped(t *testing.T) {
	h := start(t)
	d := h.connect()
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.on(func() {
		c := h.disp.clients[0]
		id, err := c.NewID()
		if err != nil {
			t.Error(err)
			return
		}
		cb, err := sw.NewWlCallback(c, id, 1)
		if err != nil {
			t.Error(err)
			return
		}
		if err := c.Remove(id); err != nil {
			t.Error(err)
		}
		if err := cb.SendDone(7); err != nil {
			t.Errorf("an event to a removed object: %v", err)
		}
	})
	if err := d.Roundtrip(); err != nil {
		t.Fatalf("the client after an event to a removed object: %v", err)
	}
	var trace string
	h.on(func() { trace = h.trace.String() })
	if strings.Contains(trace, ".done(7)") {
		t.Fatalf("an event went out on a removed object:\n%s", trace)
	}
}

func TestOnCloseRunsAfterClientsAreGone(t *testing.T) {
	h := start(t)
	var ran []string
	h.on(func() {
		h.disp.OnClose(func() error {
			ran = append(ran, "close")
			return nil
		})
		if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, func(c *Client, id wire.ObjectID, version uint32) error {
			comp, err := sw.NewWlCompositor(c, id, version, releasable{})
			if err != nil {
				return err
			}
			OnDestroy(comp, func() error {
				ran = append(ran, "object")
				return nil
			})
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	d := h.connect()
	reg, globals := h.registry(d)
	comp, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	var name uint32
	for n, iface := range globals.globals {
		if strings.HasPrefix(iface, "wl_compositor/") {
			name = n
		}
	}
	if err := reg.Bind(name, comp); err != nil {
		t.Fatal(err)
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.on(func() {
		if err := h.disp.Close(); err != nil {
			t.Error(err)
		}
	})
	if !slices.Equal(ran, []string{"object", "close"}) {
		t.Fatalf("teardown ran %v", ran)
	}
}

func TestQueuedEventsReachTheSocketBeforeIdleWorkRuns(t *testing.T) {
	h := start(t)
	d := h.connect()
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	fd := d.Conn().FD()
	waiting := -1
	h.on(func() {
		if err := h.disp.clients[0].wlDisplay.SendDeleteID(0xff000000); err != nil {
			t.Error(err)
		}
		h.loop.Idle(func() error {
			var err error
			waiting, err = unix.IoctlGetInt(fd, unix.TIOCINQ)
			return err
		})
	})
	h.on(func() {})
	if waiting <= 0 {
		t.Fatalf("%d bytes on the client's socket when idle work ran, want the queued event", waiting)
	}
}

func TestOnlyProtocolErrorsOfALeavingClientGoUnreported(t *testing.T) {
	h := start(t)
	broken := errors.New("hook failed")
	hookedCompositors(h,
		func() error { return wire.NewProtocolError(1, 0, "addressed to the client alone") },
		func() error { return errors.Join(wire.NewProtocolError(1, 0, "joined"), broken) },
	)
	d := h.connect()
	bindCompositors(t, h, d, 1)
	reported := h.reports()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	h.waitClients(0)
	got := reported()
	if len(got) != 1 || !errors.Is(got[0], broken) || strings.Contains(got[0].Error(), "addressed to the client alone") {
		t.Fatalf("reported %v, want only the internal error joined to a protocol error", got)
	}
}

func failingGlobal(h *harness, err error) {
	h.on(func() {
		if _, e := h.disp.AddGlobal(sw.WlCompositorInterface, 6, func(*Client, wire.ObjectID, uint32) error { return err }); e != nil {
			h.t.Error(e)
		}
	})
}

func requestBind(t *testing.T, h *harness, d *wlclient.Display) {
	t.Helper()
	reg, _ := h.registry(d)
	comp, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(1, comp), d.Flush()); err != nil {
		t.Fatal(err)
	}
}

func TestARequestMeetingTheCompositorsOwnFatalConditionStopsTheCompositor(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{
		{"device lost", fmt.Errorf("%w: lost while importing a buffer", backend.ErrDeviceLost)},
		{"broken invariant", backend.Broken("a frame on an unknown output")},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := start(t)
			failingGlobal(h, c.err)
			reported := h.reports()
			requestBind(t, h, h.connect())
			err := h.ended()
			if !errors.Is(err, c.err) || !strings.Contains(err.Error(), fmt.Sprintf("serve the client of pid %d", os.Getpid())) {
				t.Fatalf("Run = %v, want %v from serving the client", err, c.err)
			}
			if got := reported(); len(got) != 0 {
				t.Errorf("reported %v, want the fatal error returned, not reported", got)
			}
		})
	}
}

func TestAPostedFailureMeetingTheCompositorsOwnFatalConditionStopsTheCompositor(t *testing.T) {
	h := start(t)
	h.connect()
	h.waitClients(1)
	lost := fmt.Errorf("%w: lost while retrying a commit", backend.ErrDeviceLost)
	h.on(func() {
		if err := h.disp.clients[0].Fail(lost); err != nil {
			t.Error(err)
		}
	})
	if err := h.ended(); !errors.Is(err, lost) {
		t.Fatalf("Run = %v, want the device loss", err)
	}
}

func TestARequestFailingOnAClientsOwnResourceKillsOnlyThatClient(t *testing.T) {
	h := start(t)
	failingGlobal(h, fmt.Errorf("dmabuf: poll plane fd 7 for its writer: %w", unix.ENODEV))
	reported := h.reports()
	bad, good := h.connect(), h.connect()
	h.registry(good)
	requestBind(t, h, bad)
	h.waitClients(1)
	var pe *wire.ProtocolError
	if err := bad.Roundtrip(); !errors.As(err, &pe) || pe.Code != uint32(sw.WlDisplayErrorImplementation) || !strings.Contains(pe.Message, "no such device") {
		t.Fatalf("Roundtrip = %v, want an implementation error naming the client's fd", err)
	}
	if err := good.Roundtrip(); err != nil {
		t.Fatalf("the other client was affected: %v", err)
	}
	if got := reported(); len(got) != 0 {
		t.Errorf("reported %v, want the error sent to the client alone", got)
	}
}

func TestANoDeviceErrorFromALeavingClientsResourceIsReportedAndOthersAreStillServed(t *testing.T) {
	h := start(t)
	enodev := fmt.Errorf("shm: map the pool for writing: %w", unix.ENODEV)
	hookedCompositors(h, func() error { return enodev })
	leaving, staying := h.connect(), h.connect()
	bindCompositors(t, h, leaving, 1)
	reported := h.reports()
	if err := leaving.Close(); err != nil {
		t.Fatal(err)
	}
	h.waitClients(1)
	if got := reported(); len(got) != 1 || !errors.Is(got[0], unix.ENODEV) {
		t.Fatalf("reported %v, want the client resource's error once", got)
	}
	bindCompositors(t, h, staying, 1)
}
