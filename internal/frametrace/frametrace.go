// Package frametrace records what each composited frame cost and when it
// reached the screen, as JSON lines, for measuring smoothness.
package frametrace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render"
)

type Header struct {
	Backend  string `json:"backend"`
	Renderer string `json:"renderer"`
	Device   string `json:"device"`
	Software bool   `json:"software"`
	Vsync    bool   `json:"vsync"`
}

type Kind string

const (
	Composited Kind = "composited"
	Scanout    Kind = "scanout"
	Empty      Kind = "empty"
	Dropped    Kind = "dropped"
)

type GPUSource string

const (
	GPUNone  GPUSource = "none"
	GPUFence GPUSource = "fence"
	GPUWait  GPUSource = "wait"
)

type Frame struct {
	Output    string        `json:"output"`
	Kind      Kind          `json:"kind"`
	Start     time.Duration `json:"start_ns"`
	CPU       time.Duration `json:"cpu_ns"`
	GPU       time.Duration `json:"gpu_ns"`
	GPUDone   time.Duration `json:"gpu_done_ns"`
	GPUSource GPUSource     `json:"gpu_source"`
	Aim       time.Duration `json:"aim_ns"`
	Present   time.Duration `json:"present_ns"`
	Refresh   time.Duration `json:"refresh_ns"`
	Torn      bool          `json:"torn"`
	Animating bool          `json:"animating"`
	Sched     time.Duration `json:"sched_latency_ns"`
	GCPause   time.Duration `json:"gc_pause_ns"`
	Cost      Cost          `json:"cost"`
}

type Cost struct {
	Draws       uint64  `json:"draws"`
	Passes      uint64  `json:"passes"`
	Filters     uint64  `json:"filters"`
	Uploads     uint64  `json:"uploads"`
	GPUWaits    uint64  `json:"gpu_waits"`
	Thumbs      uint64  `json:"thumbs"`
	DamageRects uint64  `json:"damage_rects"`
	Allocs      uint64  `json:"allocs"`
	FileIO      uint64  `json:"file_io"`
	Submits     uint64  `json:"submits"`
	Targets     uint64  `json:"targets"`
	FilterPx    uint64  `json:"filter_pixels"`
	RepaintPx   uint64  `json:"repaint_pixels"`
	Copied      uint64  `json:"copied_bytes"`
	Overview    float64 `json:"overview"`
}

type Sync interface {
	NativeFences() bool
	SyncFile() (int, error)
	Finish() error
	Counts() render.Counts
}

type Clock struct {
	Now    func() time.Duration
	Signal func(fd int) (time.Duration, bool, error)
}

type pending struct {
	Frame
	fd        int
	submitted time.Duration
	wait      time.Duration
}

type track struct {
	lastVblank time.Duration
	refresh    time.Duration
	open       bool
	frame      pending
	allocs     uint64
	fileIO     uint64
	ioTid      int
	carried    Cost
	queue      []pending
}

const (
	chunkFrames  = 512
	writerChunks = 4
	closeFenceMS = 1000
	ioStatPath   = "/proc/thread-self/io"
	ioStatSize   = 256
	ownIOReads   = 2
)

type Recorder struct {
	clock  Clock
	sync   Sync
	report func(error)
	tracks map[string]*track
	chunk  []Frame
	full   chan []Frame
	free   chan []Frame
	done   chan error
	file   *os.File
	ioStat *os.File
	ioTid  int
	ioBuf  [ioStatSize]byte
	mem    runtime.MemStats
	stats  *runtimeStats
	// ownWaits counts the GPU waits the recorder made itself, timing a
	// frame where the renderer has no fence to hand, not yet taken out of
	// a frame's cost.
	ownWaits uint64
}

func Create(path string, h Header, sync Sync, clock Clock, report func(error)) (*Recorder, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the path comes from the command line
	if err != nil {
		return nil, fmt.Errorf("frametrace: create the trace: %w", err)
	}
	line, err := json.Marshal(h)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("frametrace: encode the header of %s: %w", path, err), f.Close())
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return nil, errors.Join(fmt.Errorf("frametrace: write the header of %s: %w", path, err), f.Close())
	}
	r := &Recorder{
		clock: clock, sync: sync, report: report, file: f, tracks: map[string]*track{}, stats: newRuntimeStats(),
		chunk: make([]Frame, 0, chunkFrames), full: make(chan []Frame, writerChunks), free: make(chan []Frame, writerChunks), done: make(chan error, 1),
	}
	go r.write()
	return r, nil
}

func (r *Recorder) write() {
	w := bufio.NewWriter(r.file)
	enc := json.NewEncoder(w)
	var failed error
	for chunk := range r.full {
		for i := range chunk {
			if failed == nil {
				failed = enc.Encode(&chunk[i])
			}
		}
		if failed == nil {
			failed = w.Flush()
		}
		select {
		case r.free <- chunk[:0]:
		default:
		}
	}
	if failed != nil {
		failed = fmt.Errorf("frametrace: write the trace: %w", failed)
	}
	r.done <- errors.Join(failed, r.file.Close())
}

func (r *Recorder) usage(output string) (allocs, fileIO uint64) {
	runtime.ReadMemStats(&r.mem)
	if err := r.followThread(output); err != nil {
		r.report(err)
	}
	if r.ioStat == nil {
		return r.mem.Mallocs, 0
	}
	n, err := r.ioStat.ReadAt(r.ioBuf[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		r.report(fmt.Errorf("frametrace: read the I/O counters for %s: %w", output, err))
		return r.mem.Mallocs, 0
	}
	return r.mem.Mallocs, ioCalls(r.ioBuf[:n])
}

func (r *Recorder) followThread(output string) error {
	tid := unix.Gettid()
	if r.ioStat != nil && tid == r.ioTid {
		return nil
	}
	var moved error
	if r.ioStat != nil {
		moved = fmt.Errorf("frametrace: %s drew a frame on thread %d after thread %d, so its file I/O is not comparable; frames must stay on the pinned loop thread", output, tid, r.ioTid)
		moved = errors.Join(moved, r.ioStat.Close())
		r.ioStat = nil
	}
	ioStat, err := os.Open(ioStatPath)
	if err != nil {
		return errors.Join(moved, fmt.Errorf("frametrace: open the I/O counters of thread %d for %s: %w", tid, output, err))
	}
	r.ioStat, r.ioTid = ioStat, tid
	return moved
}

func ioCalls(stat []byte) uint64 {
	var total uint64
	for line := range bytes.Lines(stat) {
		if !bytes.HasPrefix(line, []byte("syscr: ")) && !bytes.HasPrefix(line, []byte("syscw: ")) {
			continue
		}
		var n uint64
		for _, b := range line[len("syscr: "):] {
			if b < '0' || b > '9' {
				break
			}
			n = n*10 + uint64(b-'0')
		}
		total += n
	}
	return total
}

func (r *Recorder) track(output string) *track {
	t, ok := r.tracks[output]
	if !ok {
		t = &track{}
		r.tracks[output] = t
	}
	return t
}

func (r *Recorder) Begin(output string) {
	if r == nil {
		return
	}
	t := r.track(output)
	for i := range t.queue {
		if t.queue[i].awaitsPresent() {
			t.queue[i].Kind = Dropped
		}
	}
	r.settle(t)
	t.allocs, t.fileIO = r.usage(output)
	t.ioTid = r.ioTid
	now := r.clock.Now()
	t.open = true
	t.frame = pending{Output: output, Kind: Empty, Start: now, GPUSource: GPUNone, Refresh: t.refresh, fd: -1}
	if t.refresh > 0 {
		t.frame.Aim, _ = backend.NextVblank(t.lastVblank, now, t.refresh)
	}
}

func (p *pending) awaitsPresent() bool {
	return (p.Kind == Composited || p.Kind == Scanout) && p.Present == 0
}

func (r *Recorder) Submit(output string) {
	if r == nil {
		return
	}
	t := r.track(output)
	if !t.open {
		return
	}
	t.frame.Kind = Composited
	if r.sync == nil {
		return
	}
	t.frame.submitted = r.clock.Now()
	waitsBefore := r.sync.Counts().GPUWaits
	defer func() { r.ownWaits += r.sync.Counts().GPUWaits - waitsBefore }()
	if !r.sync.NativeFences() {
		if err := r.sync.Finish(); err != nil {
			r.report(fmt.Errorf("frametrace: wait for the frame on %s: %w", output, err))
			return
		}
		r.waited(t)
		return
	}
	fd, err := r.sync.SyncFile()
	if err != nil {
		r.report(fmt.Errorf("frametrace: fence the frame on %s: %w", output, err))
		return
	}
	if fd < 0 {
		r.waited(t)
		return
	}
	t.frame.fd, t.frame.GPUSource = fd, GPUFence
}

func (r *Recorder) waited(t *track) {
	done := r.clock.Now()
	t.frame.GPUDone, t.frame.GPU, t.frame.GPUSource = done, done-t.frame.submitted, GPUWait
	t.frame.wait = t.frame.GPU
}

func (r *Recorder) Scanout(output string) {
	if r == nil {
		return
	}
	if t := r.track(output); t.open {
		t.frame.Kind = Scanout
	}
}

func (r *Recorder) Drop(output string) {
	if r == nil {
		return
	}
	if t := r.track(output); t.open {
		t.frame.Kind = Dropped
	}
}

func (r *Recorder) End(output string, animating bool, cost Cost) {
	if r == nil {
		return
	}
	t := r.track(output)
	if !t.open {
		return
	}
	t.open = false
	t.frame.CPU = r.clock.Now() - t.frame.Start - t.frame.wait
	t.frame.Animating = animating
	t.frame.Sched, t.frame.GCPause = r.stats.read()
	allocs, fileIO := r.usage(output)
	cost.Allocs = allocs - t.allocs
	if r.ioTid == t.ioTid {
		cost.FileIO = fileIO - t.fileIO - ownIOReads
	}
	cost.GPUWaits -= min(cost.GPUWaits, r.ownWaits)
	r.ownWaits = 0
	t.frame.Cost = t.carry(cost)
	t.queue = append(t.queue, t.frame)
	r.settle(t)
}

func (t *track) carry(cost Cost) Cost {
	cost.Passes += t.carried.Passes
	cost.Filters += t.carried.Filters
	cost.Uploads += t.carried.Uploads
	cost.GPUWaits += t.carried.GPUWaits
	cost.Thumbs += t.carried.Thumbs
	cost.Submits += t.carried.Submits
	cost.Targets += t.carried.Targets
	cost.FilterPx += t.carried.FilterPx
	cost.RepaintPx += t.carried.RepaintPx
	cost.Copied += t.carried.Copied
	if t.frame.Kind == Composited {
		t.carried = Cost{}
	} else {
		t.carried = cost
	}
	return cost
}

func (r *Recorder) Presented(output string, p backend.Presentation) {
	if r == nil || p.Idle {
		return
	}
	t := r.track(output)
	t.lastVblank, t.refresh = p.Time, p.Refresh
	for i := range t.queue {
		if t.queue[i].awaitsPresent() {
			t.queue[i].Present, t.queue[i].Refresh, t.queue[i].Torn = p.Time, p.Refresh, p.Torn
			break
		}
	}
	r.settle(t)
}

func (r *Recorder) settle(t *track) {
	n := 0
	for n < len(t.queue) && !t.queue[n].awaitsPresent() && r.fenced(&t.queue[n]) {
		r.emit(t.queue[n].Frame)
		n++
	}
	if n > 0 {
		t.queue = append(t.queue[:0], t.queue[n:]...)
	}
}

func (r *Recorder) fenced(p *pending) bool {
	if p.fd < 0 {
		return true
	}
	done, ok, err := r.clock.Signal(p.fd)
	if err != nil {
		r.report(fmt.Errorf("frametrace: read the fence of the frame on %s: %w", p.Output, err))
		p.GPUSource = GPUNone
	} else if !ok {
		return false
	} else {
		p.GPUDone, p.GPU = done, max(done-p.submitted, 0)
	}
	if err := unix.Close(p.fd); err != nil {
		r.report(fmt.Errorf("frametrace: close the fence of the frame on %s: %w", p.Output, err))
	}
	p.fd = -1
	return true
}

func (r *Recorder) emit(f Frame) {
	r.chunk = append(r.chunk, f)
	if len(r.chunk) < chunkFrames {
		return
	}
	r.full <- r.chunk
	select {
	case r.chunk = <-r.free:
	default:
		r.chunk = make([]Frame, 0, chunkFrames)
	}
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	for _, t := range r.tracks {
		for i := range t.queue {
			p := &t.queue[i]
			if p.fd >= 0 {
				fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}} //nolint:gosec // file descriptors fit in int32
				if _, err := unix.Poll(fds, closeFenceMS); err != nil && !errors.Is(err, unix.EINTR) {
					r.report(fmt.Errorf("frametrace: wait for the last fence on %s: %w", p.Output, err))
				}
				if !r.fenced(p) {
					if err := unix.Close(p.fd); err != nil {
						r.report(fmt.Errorf("frametrace: close the last fence on %s: %w", p.Output, err))
					}
					p.fd, p.GPUSource = -1, GPUNone
				}
			}
			r.emit(p.Frame)
		}
		t.queue = nil
	}
	if len(r.chunk) > 0 {
		r.full <- r.chunk
	}
	r.chunk = nil
	close(r.full)
	written := <-r.done
	if r.ioStat == nil {
		return written
	}
	return errors.Join(written, r.ioStat.Close())
}

func Read(src io.Reader) (Header, []Frame, error) {
	dec := json.NewDecoder(src)
	dec.DisallowUnknownFields()
	var h Header
	if err := dec.Decode(&h); err != nil {
		return Header{}, nil, fmt.Errorf("frametrace: read the header: %w", err)
	}
	var frames []Frame
	for {
		var f Frame
		err := dec.Decode(&f)
		if errors.Is(err, io.EOF) {
			return h, frames, nil
		}
		if err != nil {
			return h, frames, fmt.Errorf("frametrace: read frame %d: %w", len(frames), err)
		}
		frames = append(frames, f)
	}
}
