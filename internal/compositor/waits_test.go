package compositor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	cld "github.com/stubbedev/lys-wm/internal/protocol/client/linuxdmabuf"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type importingRenderer struct{ *spyRenderer }

func (importingRenderer) DMABUFFormats() ([]gpu.FormatModifier, error) {
	return []gpu.FormatModifier{{Format: fourcc.XRGB8888, Modifier: fourcc.Linear}}, nil
}

func (importingRenderer) MainDevice() (uint64, error) { return 0, gpu.ErrUnsupported }

func (importingRenderer) CheckDMABUF(gpu.DMABUF) error { return nil }

func (r importingRenderer) ImportTexture(b gpu.DMABUF) (render.Texture, error) {
	return r.NewBlankTexture(render.XRGB8888, b.Size)
}

type reports struct {
	mu   sync.Mutex
	errs []string
}

func (r *reports) add(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err.Error())
}

func (r *reports) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	got := r.errs
	r.errs = nil
	return got
}

type noDmabufEvents struct{}

func (noDmabufEvents) Format(*cld.ZwpLinuxDmabufV1, uint32) error                   { return nil }
func (noDmabufEvents) Modifier(*cld.ZwpLinuxDmabufV1, uint32, uint32, uint32) error { return nil }

type noParams struct{}

func (noParams) Created(*cld.ZwpLinuxBufferParamsV1, *cw.WlBuffer) (cw.WlBufferHandler, error) {
	return nil, errors.New("unexpected created")
}

func (noParams) Failed(*cld.ZwpLinuxBufferParamsV1) error { return errors.New("unexpected failed") }

type waitRig struct {
	*rig
	reports *reports
	dmabufs *cld.ZwpLinuxDmabufV1
}

func setupWaits(t *testing.T) *waitRig {
	t.Helper()
	rep := &reports{}
	r := setupWith(t, func(_ *loop.Loop, s *spyRenderer) backend.Backend { return rendererBackend{importingRenderer{s}} }, func(cfg *Config) { cfg.Report = rep.add })
	w := &waitRig{rig: r, reports: rep}
	r.on(func() {
		must(t, r.comp.waits.timer.Remove())
		var err error
		r.comp.waits.timer, err = r.loop.AddTimer(func() error { return nil })
		must(t, err)
	})
	var err error
	w.dmabufs, err = cld.NewZwpLinuxDmabufV1(r.client, 3, noDmabufEvents{})
	must(t, err, r.registry.Bind(r.globals["zwp_linux_dmabuf_v1"], w.dmabufs))
	r.roundtrip()
	return w
}

func (w *waitRig) buffer(plane int, rel cw.WlBufferHandler) *cw.WlBuffer {
	w.t.Helper()
	p, err := w.dmabufs.CreateParams(noParams{})
	must(w.t, err, p.Add(plane, 0, 0, 64, 0, 0))
	b, err := p.CreateImmed(rel, 16, 16, uint32(fourcc.XRGB8888), 0)
	must(w.t, err)
	return b
}

func (w *waitRig) memfdBuffer(rel cw.WlBufferHandler) *cw.WlBuffer {
	w.t.Helper()
	fd, err := unix.MemfdCreate("plane", unix.MFD_CLOEXEC)
	must(w.t, err)
	defer unix.Close(fd)
	must(w.t, unix.Ftruncate(fd, 16*16*4))
	return w.buffer(fd, rel)
}

func (w *waitRig) sweep() []string {
	w.t.Helper()
	w.on(func() { must(w.t, w.comp.sweepWaits()) })
	return w.reports.take()
}

func (w *waitRig) quietSweeps(what string) {
	w.t.Helper()
	for i := range waitStalled - 1 {
		if got := w.sweep(); len(got) != 0 {
			w.t.Fatalf("reported %q after %d sweeps, before %s could have stalled", got, i+1, what)
		}
	}
}

func onlyReport(t *testing.T, got []string, wants ...string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("reports %q, want one naming %q", got, wants)
	}
	for _, want := range wants {
		if !strings.Contains(got[0], want) {
			t.Fatalf("report %q does not name %q", got[0], want)
		}
	}
}

type heldCommit struct {
	surface *cw.WlSurface
	buffer  *cw.WlBuffer
	writer  int
}

func (w *waitRig) holdCommit() heldCommit {
	w.t.Helper()
	var p [2]int
	must(w.t, unix.Pipe2(p[:], unix.O_CLOEXEC))
	w.t.Cleanup(func() {
		_ = unix.Close(p[0])
		_ = unix.Close(p[1])
	})
	b := w.buffer(p[0], &releases{})
	s, err := w.wlComp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	must(w.t, err, s.Attach(b, 0, 0), s.Commit())
	w.roundtrip()
	return heldCommit{surface: s, buffer: b, writer: p[1]}
}

func (h heldCommit) String() string {
	return fmt.Sprintf("wl_buffer@%d of pid %d on wl_surface@%d of pid %d", h.buffer.Base().ID(), os.Getpid(), h.surface.Base().ID(), os.Getpid())
}

func TestACommitHeldByItsWritersFenceIsReportedOnceAndWhenItClears(t *testing.T) {
	w := setupWaits(t)
	held := w.holdCommit()
	w.on(func() {
		if !w.comp.waits.armed {
			t.Error("a commit held by its writer did not start the sweeps")
		}
	})
	w.quietSweeps("the writer")
	onlyReport(t, w.sweep(), "a commit of "+held.String()+" has waited", "implicit fence")
	for range 3 {
		if got := w.sweep(); len(got) != 0 {
			t.Fatalf("the same stall reported again: %q", got)
		}
	}
	_, err := unix.Write(held.writer, []byte{0})
	must(t, err)
	w.waitApplied(1)
	onlyReport(t, w.sweep(), held.String()+" no longer waits for its writer's implicit fence")
	w.on(func() {
		if w.comp.waits.armed {
			t.Error("the sweeps went on with nothing left to wait for")
		}
	})
}

func (w *waitRig) dumps() (count uint64, prefix string) {
	w.t.Helper()
	w.on(func() {
		w.comp.recorders.dumps.Wait()
		count = w.comp.waits.dump.count
		prefix = filepath.Join(filepath.Dir(w.comp.display.Path()), "lys-wm", w.comp.display.Name()+".stall-")
	})
	return count, prefix
}

const dumpNote = "; the protocol recorded up to this moment is in "

func TestAStallWritesTheProtocolRecordedUpToItOnlyWhileProtocolIsRecorded(t *testing.T) {
	w := setupWaits(t)
	unrecorded := w.holdCommit()
	w.quietSweeps("the writer")
	got := w.sweep()
	onlyReport(t, got, unrecorded.String())
	if n, _ := w.dumps(); n != 0 || strings.Contains(got[0], dumpNote) {
		t.Fatalf("with no protocol recorded, %d dumps were written and the report says %q", n, got[0])
	}

	w.on(func() { w.comp.display.RecordProtocol(true) })
	both := []heldCommit{w.holdCommit(), w.holdCommit()}
	_, prefix := w.dumps()
	must(t, os.Mkdir(filepath.Dir(prefix), 0o700))
	w.quietSweeps("the writers")
	got = w.sweep()
	n, _ := w.dumps()
	if len(got) != len(both) || n != 1 {
		t.Fatalf("two stalls found by one sweep made %d reports and %d dumps, want 2 and 1: %q", len(got), n, got)
	}
	path := prefix + "0.log"
	record, err := os.ReadFile(path)
	must(t, err)
	for i, held := range both {
		if !strings.Contains(got[i], held.String()) || !strings.HasSuffix(got[i], dumpNote+path) {
			t.Errorf("report %q does not name %s and the dump %s", got[i], held, path)
		}
		for _, sent := range []string{
			fmt.Sprintf("{%d/1} wl_surface@%d.attach(wl_buffer@%d, 0, 0)\n", os.Getpid(), held.surface.Base().ID(), held.buffer.Base().ID()),
			fmt.Sprintf("{%d/1} wl_surface@%d.commit()\n", os.Getpid(), held.surface.Base().ID()),
		} {
			if !strings.Contains(string(record), sent) {
				t.Errorf("the dump lacks %q:\n%s", sent, record)
			}
		}
	}
	if early := fmt.Sprintf("wl_surface@%d.commit()", unrecorded.surface.Base().ID()); strings.Contains(string(record), early) {
		t.Errorf("the dump holds %q, sent before the protocol was recorded", early)
	}
	for range 3 {
		if got := w.sweep(); len(got) != 0 {
			t.Fatalf("the same stalls reported again: %q", got)
		}
	}
	if n, _ := w.dumps(); n != 1 {
		t.Fatalf("%d dumps for one stall episode, want 1", n)
	}

	named := map[string]bool{path: true}
	for range stallDumps {
		held := w.holdCommit()
		w.quietSweeps("the writer")
		got := w.sweep()
		onlyReport(t, got, held.String(), dumpNote+prefix)
		_, file, _ := strings.Cut(got[0], dumpNote)
		named[file] = true
	}
	n, _ = w.dumps()
	files, err := filepath.Glob(prefix + "*")
	must(t, err)
	if n != stallDumps+1 || len(named) != stallDumps || len(files) != stallDumps {
		t.Fatalf("%d dumps named %d files and left %d, want %d dumps kept in %d files", n, len(named), len(files), stallDumps+1, stallDumps)
	}
}

func (w *waitRig) waitApplied(n int) {
	w.t.Helper()
	for range 5000 {
		applied := 0
		w.on(func() { applied = len(w.comp.textures) })
		if applied == n {
			return
		}
		w.roundtrip()
		time.Sleep(time.Millisecond)
	}
	w.t.Fatalf("the commit never applied")
}

func TestABufferReplacedButStillOnAPlaneIsReportedUntilItLeaves(t *testing.T) {
	w := setupWaits(t)
	rel := &releases{}
	first, second := w.memfdBuffer(rel), w.memfdBuffer(&releases{})
	s, err := w.wlComp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	must(t, err, s.Attach(first, 0, 0), s.Commit())
	w.roundtrip()
	var shown *dmabuf.Buffer
	var on *surface.Surface
	w.on(func() {
		for owner, tex := range w.comp.textures {
			shown, on = tex.(imported).buffer, owner
		}
		w.comp.holdScannedOut(shown, on)
	})
	must(t, s.Attach(second, 0, 0), s.Commit())
	w.roundtrip()
	if rel.n != 0 {
		t.Fatal("a buffer still on a plane was released")
	}
	w.quietSweeps("the release")
	onlyReport(t, w.sweep(), fmt.Sprintf("wl_buffer@%d of pid %d, last shown on wl_surface@%d", first.Base().ID(), os.Getpid(), s.Base().ID()), "wl_buffer.release waits for a display plane")
	if got := w.sweep(); len(got) != 0 {
		t.Fatalf("the same withheld buffer reported again: %q", got)
	}
	w.on(func() { must(t, w.comp.leftScreen(shown)) })
	w.roundtrip()
	if rel.n != 1 {
		t.Fatalf("%d releases once the buffer left the plane, want 1", rel.n)
	}
	onlyReport(t, w.sweep(), "no longer waits for a display plane to retire it")
}

func TestABufferStillOnScreenAndOwedNothingIsNotReported(t *testing.T) {
	w := setupWaits(t)
	s, err := w.wlComp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	must(t, err, s.Attach(w.memfdBuffer(&releases{}), 0, 0), s.Commit())
	w.roundtrip()
	w.on(func() {
		for owner, tex := range w.comp.textures {
			w.comp.holdScannedOut(tex.(imported).buffer, owner)
		}
		if w.comp.waits.armed {
			t.Error("a buffer on screen that is owed nothing started the sweeps")
		}
	})
	for range 4 {
		if got := w.sweep(); len(got) != 0 {
			t.Fatalf("reported %q for the buffer its surface still shows", got)
		}
	}
}
