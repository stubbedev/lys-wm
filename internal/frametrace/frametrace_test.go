package frametrace

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render"
)

const refresh = 10 * time.Millisecond

type fakeSync struct {
	native   bool
	cost     time.Duration
	fds      []int
	now      *time.Duration
	failed   error
	finishes int
	waits    uint64
}

func (s *fakeSync) Counts() render.Counts { return render.Counts{GPUWaits: s.waits} }

func (s *fakeSync) NativeFences() bool { return s.native }

func (s *fakeSync) SyncFile() (int, error) {
	if s.failed != nil {
		return -1, s.failed
	}
	*s.now += s.cost
	if s.cost > 0 {
		s.waits++
		return -1, nil
	}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		return -1, err
	}
	s.fds = append(s.fds, p[0])
	return p[1], nil
}

func (s *fakeSync) Finish() error {
	s.finishes++
	s.waits++
	*s.now += s.cost
	return s.failed
}

type rig struct {
	t      *testing.T
	now    time.Duration
	path   string
	r      *Recorder
	signal map[int]time.Duration
	last   int
	errs   []error
}

func newRig(t *testing.T, sync func(r *rig) Sync) *rig {
	t.Helper()
	g := &rig{t: t, now: time.Second, path: filepath.Join(t.TempDir(), "trace"), signal: map[int]time.Duration{}, last: -1}
	var s Sync
	if sync != nil {
		s = sync(g)
	}
	r, err := Create(g.path, Header{Backend: "headless", Renderer: "vulkan", Device: "test"}, s, Clock{
		Now: func() time.Duration { return g.now },
		Signal: func(fd int) (time.Duration, bool, error) {
			g.last = fd
			at, ok := g.signal[0]
			return at, ok, nil
		},
	}, func(err error) { g.errs = append(g.errs, err) })
	if err != nil {
		t.Fatal(err)
	}
	g.r = r
	return g
}

func (g *rig) frames() []Frame {
	g.t.Helper()
	if err := g.r.Close(); err != nil {
		g.t.Fatal(err)
	}
	f, err := os.Open(g.path)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	h, frames, err := Read(f)
	if err != nil {
		g.t.Fatal(err)
	}
	if h.Renderer != "vulkan" {
		g.t.Fatalf("header = %+v", h)
	}
	return frames
}

func (g *rig) frame(act func()) {
	g.r.Begin("A")
	g.now += time.Millisecond
	act()
	g.now += time.Millisecond
	g.r.End("A", true, Cost{})
}

func (g *rig) flip(at time.Duration) {
	g.now = at
	g.r.Presented("A", backend.Presentation{Time: at, Refresh: refresh})
}

func TestANilRecorderDoesNothing(t *testing.T) {
	var r *Recorder
	r.Begin("A")
	r.Submit("A")
	r.Scanout("A")
	r.Drop("A")
	r.End("A", true, Cost{})
	r.Presented("A", backend.Presentation{Time: 1, Refresh: 1})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAFenceTimesTheGPUFromSubmitToSignal(t *testing.T) {
	g := newRig(t, func(g *rig) Sync { return &fakeSync{native: true, now: &g.now} })
	g.frame(func() { g.r.Submit("A") })
	submitted := g.now - time.Millisecond
	g.flip(g.now + 5*time.Millisecond)
	g.signal[0] = submitted + 3*time.Millisecond
	g.r.Begin("A")
	g.r.End("A", false, Cost{})
	frames := g.frames()
	if len(frames) != 2 {
		t.Fatalf("%d frames, want 2", len(frames))
	}
	f := frames[0]
	if f.Kind != Composited || f.GPUSource != GPUFence || f.GPU != 3*time.Millisecond || f.GPUDone != submitted+3*time.Millisecond {
		t.Fatalf("frame = %+v", f)
	}
	if f.CPU != 2*time.Millisecond || f.Present == 0 || f.Refresh != refresh || !f.Animating {
		t.Fatalf("frame = %+v", f)
	}
	if frames[1].Kind != Empty {
		t.Fatalf("second frame = %+v", frames[1])
	}
}

func TestAFrameWaitsInOrderForItsFence(t *testing.T) {
	g := newRig(t, func(g *rig) Sync { return &fakeSync{native: true, now: &g.now} })
	g.frame(func() { g.r.Submit("A") })
	g.flip(g.now + refresh)
	if len(g.r.chunk) != 0 {
		t.Fatalf("emitted %d frames before the fence signalled", len(g.r.chunk))
	}
	g.frame(func() {})
	if len(g.r.chunk) != 0 {
		t.Fatal("the empty frame overtook the one waiting for its fence")
	}
	g.signal[0] = g.now
	g.r.Presented("A", backend.Presentation{Time: g.now, Refresh: refresh})
	if len(g.r.chunk) != 2 || g.r.chunk[0].Kind != Composited || g.r.chunk[1].Kind != Empty {
		t.Fatalf("chunk = %+v", g.r.chunk)
	}
	g.frames()
}

func TestAStackWithoutFencesIsWaitedForOutsideTheCPUTime(t *testing.T) {
	sync := &fakeSync{cost: 7 * time.Millisecond}
	g := newRig(t, func(g *rig) Sync {
		sync.now = &g.now
		return sync
	})
	g.frame(func() { g.r.Submit("A") })
	g.flip(g.now + refresh)
	f := g.frames()[0]
	if sync.finishes != 1 || f.GPUSource != GPUWait || f.GPU != 7*time.Millisecond || f.CPU != 2*time.Millisecond {
		t.Fatalf("frame = %+v after %d finishes", f, sync.finishes)
	}
}

func TestASyncFileAlreadyFinishedIsATimedWait(t *testing.T) {
	g := newRig(t, func(g *rig) Sync { return &fakeSync{native: true, cost: 4 * time.Millisecond, now: &g.now} })
	g.frame(func() { g.r.Submit("A") })
	g.flip(g.now + refresh)
	if f := g.frames()[0]; f.GPUSource != GPUWait || f.GPU != 4*time.Millisecond || f.CPU != 2*time.Millisecond {
		t.Fatalf("frame = %+v", f)
	}
}

func TestAFailedFenceIsReportedAndLeavesTheGPUUnknown(t *testing.T) {
	wrong := errors.New("no fence")
	g := newRig(t, func(g *rig) Sync { return &fakeSync{native: true, failed: wrong, now: &g.now} })
	g.frame(func() { g.r.Submit("A") })
	g.flip(g.now + refresh)
	if f := g.frames()[0]; f.GPUSource != GPUNone {
		t.Fatalf("frame = %+v", f)
	}
	if len(g.errs) != 1 || !errors.Is(g.errs[0], wrong) {
		t.Fatalf("reported %v", g.errs)
	}
}

func TestAFrameAimsAtTheVblankAfterItsStart(t *testing.T) {
	g := newRig(t, nil)
	g.frame(func() { g.r.Submit("A") })
	g.flip(g.now + refresh)
	flip := g.now
	g.now += 2 * time.Millisecond
	g.frame(func() { g.r.Submit("A") })
	g.flip(flip + 2*refresh)
	frames := g.frames()
	if frames[0].Aim != 0 {
		t.Fatalf("the first frame aims at %v with no vblank known", frames[0].Aim)
	}
	if frames[1].Aim != flip+refresh || frames[1].SkippedVblanks() != 1 {
		t.Fatalf("frame = %+v, %d skipped", frames[1], frames[1].SkippedVblanks())
	}
	if frames[0].GPUSource != GPUNone {
		t.Fatalf("frame without a GPU = %+v", frames[0])
	}
}

func TestAFrameThatNeverFlipsCountsAsDropped(t *testing.T) {
	g := newRig(t, nil)
	g.frame(func() { g.r.Submit("A") })
	g.frame(func() { g.r.Drop("A") })
	g.frame(func() { g.r.Scanout("A") })
	g.flip(g.now + refresh)
	frames := g.frames()
	if len(frames) != 3 || frames[0].Kind != Dropped || frames[1].Kind != Dropped || frames[2].Kind != Scanout || frames[2].Present == 0 {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestIdleVblanksAreNotPresentations(t *testing.T) {
	g := newRig(t, nil)
	g.frame(func() { g.r.Submit("A") })
	g.r.Presented("A", backend.Presentation{Time: g.now, Refresh: refresh, Idle: true})
	if len(g.r.chunk) != 0 {
		t.Fatal("an idle vblank presented a frame")
	}
	g.frames()
}

func TestFramesBeyondAChunkReachTheFile(t *testing.T) {
	g := newRig(t, nil)
	const n = chunkFrames*3 + 7
	for range n {
		g.frame(func() {})
	}
	if got := len(g.frames()); got != n {
		t.Fatalf("%d frames read back, want %d", got, n)
	}
}

func TestStatIsNearestRank(t *testing.T) {
	var samples []time.Duration
	for i := 100; i >= 1; i-- {
		samples = append(samples, time.Duration(i))
	}
	s := NewStat(samples)
	if s.Samples != 100 || s.P50 != 50 || s.P95 != 95 || s.P99 != 99 || s.Worst != 100 {
		t.Fatalf("stat = %+v", s)
	}
	if samples[0] != 100 {
		t.Fatal("NewStat reordered its input")
	}
	if s := NewStat([]time.Duration{7}); s.P50 != 7 || s.P99 != 7 || s.Worst != 7 {
		t.Fatalf("one sample = %+v", s)
	}
	if s := NewStat(nil); s != (Stat{}) {
		t.Fatalf("no samples = %+v", s)
	}
}

func TestAFrameShowsAtTheVblankAfterItsGPUFinished(t *testing.T) {
	early := Frame{Present: 100, GPUDone: 95, Refresh: 10}
	late := Frame{Present: 100, GPUDone: 113, Refresh: 10}
	exact := Frame{Present: 100, GPUDone: 120, Refresh: 10}
	if early.Shown() != 100 || late.Shown() != 120 || exact.Shown() != 120 {
		t.Fatalf("shown = %d, %d, %d", early.Shown(), late.Shown(), exact.Shown())
	}
}

func TestSummaryCountsEachRunOnItsOwn(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	frame := func(start, present int, animating bool) Frame {
		return Frame{Output: "A", Kind: Composited, Start: ms(start), CPU: ms(1), GPU: ms(2), GPUSource: GPUWait, Aim: ms(present), Present: ms(present), Refresh: ms(10), Animating: animating}
	}
	first := frame(0, 10, true)
	first.Aim = 0
	second := frame(10, 20, true)
	late := frame(20, 40, false)
	late.Aim = ms(30)
	settled := frame(1000, 1010, false)
	runs := [][]Frame{{first, second, late, settled, {Output: "A", Kind: Empty, Start: ms(1010)}, {Output: "A", Kind: Dropped, Start: ms(1020)}}, {frame(5000, 5010, true)}}
	s := Summarize(runs, false)
	if s.Frames != 7 || s.Presented != 5 || s.Empty != 1 || s.Dropped != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.Late != 1 || s.SkippedVblank != 1 || s.LatePercent != 20 {
		t.Fatalf("summary = %+v", s)
	}
	if s.Interval.Samples != 2 || s.Interval.P50 != ms(10) || s.Interval.Worst != ms(20) {
		t.Fatalf("intervals = %+v", s.Interval)
	}
	if s.Latency.Samples != 5 || s.CPU.Samples != 7 || s.GPU.Samples != 5 {
		t.Fatalf("summary = %+v", s)
	}
	if c := Summarize([][]Frame{{settled, settled}}, true); c.Interval.Samples != 1 {
		t.Fatalf("a continuous step counted %d intervals", c.Interval.Samples)
	}
}

func TestTheRecordersOwnWaitIsNotAFrameCost(t *testing.T) {
	sync := &fakeSync{cost: time.Millisecond}
	g := newRig(t, func(g *rig) Sync {
		sync.now = &g.now
		return sync
	})
	g.r.Begin("A")
	g.r.Submit("A")
	g.r.End("A", false, Cost{GPUWaits: 1})
	g.r.Begin("A")
	g.r.Submit("A")
	g.r.End("A", false, Cost{GPUWaits: 3})
	g.flip(time.Second)
	frames := g.frames()
	if len(frames) != 2 || frames[0].Cost.GPUWaits != 0 || frames[1].Cost.GPUWaits != 2 {
		t.Fatalf("frames %+v, want the recorder's one wait per frame left out", frames)
	}
}

// A renderer that answers the recorder's fence request by waiting for
// the queue, lavapipe without a device node, made that wait for the
// recorder: it is not the frame's.
func TestAFenceRequestThatWaitedIsNotAFrameCost(t *testing.T) {
	sync := &fakeSync{native: true, cost: time.Millisecond}
	g := newRig(t, func(g *rig) Sync {
		sync.now = &g.now
		return sync
	})
	g.r.Begin("A")
	g.r.Submit("A")
	g.r.End("A", false, Cost{GPUWaits: 1})
	g.flip(time.Second)
	if frames := g.frames(); len(frames) != 1 || frames[0].Cost.GPUWaits != 0 || sync.waits != 1 {
		t.Fatalf("frames %+v after %d waits, want the recorder's wait left out", frames, sync.waits)
	}
}

func TestAFencedFrameKeepsEveryGPUWait(t *testing.T) {
	sync := &fakeSync{native: true}
	g := newRig(t, func(g *rig) Sync {
		sync.now = &g.now
		return sync
	})
	g.r.Begin("A")
	g.r.Submit("A")
	g.r.End("A", false, Cost{GPUWaits: 1})
	g.flip(time.Second)
	if frames := g.frames(); len(frames) != 1 || frames[0].Cost.GPUWaits != 1 {
		t.Fatalf("frames %+v, want the renderer's wait counted", frames)
	}
}

func TestCountsFromFramesThatDrewNothingReachTheNextOneThatDid(t *testing.T) {
	g := newRig(t, nil)
	g.r.Begin("A")
	g.r.End("A", false, Cost{Uploads: 3, Thumbs: 1, Passes: 2, Filters: 4, Draws: 9, DamageRects: 9})
	g.r.Begin("A")
	g.r.Submit("A")
	g.r.End("A", false, Cost{Uploads: 1, Draws: 5, DamageRects: 2})
	g.flip(time.Second)
	frames := g.frames()
	if len(frames) != 2 {
		t.Fatalf("frames = %+v", frames)
	}
	c := frames[1].Cost
	if c.Uploads != 4 || c.Thumbs != 1 || c.Passes != 2 || c.Filters != 4 || c.Draws != 5 || c.DamageRects != 2 {
		t.Fatalf("cost = %+v", c)
	}
}

func TestAFrameDoingNoFileIOReportsNone(t *testing.T) {
	g := newRig(t, nil)
	for i := range 20 {
		g.frame(func() {})
		g.flip(time.Duration(i+1) * time.Second)
	}
	for _, f := range g.frames() {
		if f.Cost.FileIO != 0 {
			t.Fatalf("a frame that read nothing reports %d I/O calls", f.Cost.FileIO)
		}
	}
}

func TestAFrameAllocatingOnlyItsOwnBuffersReportsThem(t *testing.T) {
	g := newRig(t, nil)
	var sink [][]byte
	const held = 64
	g.frame(func() {
		for range held {
			sink = append(sink, make([]byte, 1<<10))
		}
	})
	g.flip(time.Second)
	frames := g.frames()
	if got := frames[0].Cost.Allocs; got < held {
		t.Fatalf("a frame allocating %d buffers reports %d allocations", held, got)
	}
	if len(sink) != held {
		t.Fatal("the buffers were dropped")
	}
}

func TestIOCallsAddReadsAndWrites(t *testing.T) {
	stat := []byte("rchar: 9\nwchar: 7\nsyscr: 12\nsyscw: 30\nread_bytes: 5\n")
	if got := ioCalls(stat); got != 42 {
		t.Fatalf("ioCalls = %d, want 42", got)
	}
}

func TestWhatOneRunAloneCostsIsNotRepeated(t *testing.T) {
	frame := func(allocs uint64) Frame { return Frame{Kind: Composited, Cost: Cost{Allocs: allocs}} }
	s := summarizeCost([][]Frame{{frame(337), frame(4)}, {frame(5), frame(4)}, {frame(6)}})
	if s.Allocs.Worst != 337 || s.Allocs.Repeated != 5 {
		t.Fatalf("worst %d, repeated %d: want 337 and 5", s.Allocs.Worst, s.Allocs.Repeated)
	}
}

func TestNewCountsFromFramesThatDrewNothingReachTheNextOneThatDid(t *testing.T) {
	g := newRig(t, nil)
	g.r.Begin("A")
	g.r.End("A", false, Cost{Submits: 1, Targets: 2, FilterPx: 300, RepaintPx: 40, Copied: 5000})
	g.r.Begin("A")
	g.r.Submit("A")
	g.r.End("A", false, Cost{Submits: 1, RepaintPx: 60})
	g.flip(time.Second)
	frames := g.frames()
	if len(frames) != 2 {
		t.Fatalf("frames = %+v", frames)
	}
	c := frames[1].Cost
	if c.Submits != 2 || c.Targets != 2 || c.FilterPx != 300 || c.RepaintPx != 100 || c.Copied != 5000 {
		t.Fatalf("cost = %+v", c)
	}
}

func TestAGCDuringAFrameIsItsPause(t *testing.T) {
	g := newRig(t, nil)
	g.frame(func() {})
	g.flip(time.Second)
	g.frame(runtime.GC)
	g.flip(2 * time.Second)
	frames := g.frames()
	if frames[1].GCPause <= 0 {
		t.Fatalf("a frame that collected garbage reports a pause of %v", frames[1].GCPause)
	}
}

func TestGoroutinesWaitingToRunAreTheFramesSchedulerLatency(t *testing.T) {
	g := newRig(t, nil)
	g.frame(func() {
		for range 64 {
			var wg sync.WaitGroup
			for range 4 * runtime.GOMAXPROCS(0) {
				wg.Go(func() {
					for range 1000 {
						runtime.Gosched()
					}
				})
			}
			wg.Wait()
		}
	})
	g.flip(time.Second)
	if f := g.frames()[0]; f.Sched <= 0 {
		t.Fatalf("a frame whose goroutines queued reports a scheduler latency of %v", f.Sched)
	}
}

func TestReadingRuntimeStatsAllocatesNothing(t *testing.T) {
	s := newRuntimeStats()
	s.read()
	if n := testing.AllocsPerRun(100, func() { s.read() }); n != 0 {
		t.Fatalf("reading the runtime stats allocates %v times", n)
	}
}

func TestTheWorstNewSampleIsTheUpperEdgeOfItsBucket(t *testing.T) {
	if got := bucketTime([]float64{0, 0.001, 0.002}, 1); got != 2*time.Millisecond {
		t.Fatalf("bucketTime = %v", got)
	}
	if got := bucketTime([]float64{0, 0.001, math.Inf(1)}, 1); got != time.Millisecond {
		t.Fatalf("bucketTime of the open bucket = %v", got)
	}
}

func TestSummaryReportsSchedulerLatencyAndGCPauses(t *testing.T) {
	s := Summarize([][]Frame{{{Kind: Empty, Sched: time.Millisecond, GCPause: 2 * time.Millisecond}, {Kind: Composited, Sched: 3 * time.Millisecond}}}, false)
	if s.Sched.Worst != 3*time.Millisecond || s.GCPause.Worst != 2*time.Millisecond || s.Sched.Samples != 2 {
		t.Fatalf("sched %+v, gc %+v", s.Sched, s.GCPause)
	}
}
