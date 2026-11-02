// Package frametrace records what each composited frame cost and when it
// reached the screen, as JSON lines, for measuring smoothness.
package frametrace

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
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
}

type Sync interface {
	NativeFences() bool
	SyncFile() (int, error)
	Finish() error
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
	queue      []pending
}

const (
	chunkFrames  = 512
	writerChunks = 4
	closeFenceMS = 1000
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
		clock: clock, sync: sync, report: report, file: f, tracks: map[string]*track{},
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

func (r *Recorder) End(output string, animating bool) {
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
	t.queue = append(t.queue, t.frame)
	r.settle(t)
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
	return <-r.done
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
