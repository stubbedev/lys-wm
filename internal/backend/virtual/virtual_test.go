package virtual

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

var mode = backend.Mode{Size: image.Pt(64, 48), RefreshMHz: 100000}

const bound = 10 * time.Second

type recorder struct {
	l         *loop.Loop
	frames    int
	presented []backend.Presentation
	onFrame   func(backend.Output) error
	stopAfter int
}

func (r *recorder) Frame(o backend.Output) error {
	r.frames++
	if r.onFrame == nil {
		return nil
	}
	return r.onFrame(o)
}

func (r *recorder) Presented(_ backend.Output, p backend.Presentation) error {
	r.presented = append(r.presented, p)
	if len(r.presented) == r.stopAfter {
		r.l.Stop()
	}
	return nil
}

func setup(t *testing.T) (*loop.Loop, *Output, *recorder) {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	r := &recorder{l: l}
	o, err := New(l, soft.New(), r, "VIRT-1", mode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(o.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	})
	return l, o, r
}

func enable(t *testing.T, o *Output) {
	t.Helper()
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: o.Modes()[0]}); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, l *loop.Loop) {
	t.Helper()
	guard, err := l.AddTimer(func() error { return errors.New("the loop ran past its bound") })
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Remove()
	if err := guard.Arm(bound); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(); err != nil {
		t.Fatal(err)
	}
}

func paint(t *testing.T, o backend.Output, frames int) {
	t.Helper()
	f, err := o.BeginFrame()
	if err != nil {
		t.Fatal(err)
	}
	if want := min(frames-1, 1); f.Age != want {
		t.Fatalf("frame %d has age %d, want %d", frames, f.Age, want)
	}
	if f.Target.Size() != mode.Size {
		t.Fatalf("frame target is %v, want %v", f.Target.Size(), mode.Size)
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rectangle{Max: mode.Size}})
	if err != nil {
		t.Fatal(err)
	}
	p.FillRect(image.Rectangle{Max: mode.Size}, render.Color{G: byte(frames), A: 0xff}, render.Mask{})
	if err := errors.Join(p.End(), o.Present(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestFramesRenderOffscreenOnTheModesClock(t *testing.T) {
	l, o, r := setup(t)
	enable(t, o)
	const want = 6
	r.stopAfter = want
	r.onFrame = func(out backend.Output) error {
		paint(t, out, r.frames)
		if r.frames < want {
			return out.ScheduleFrame()
		}
		return nil
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	run(t, l)
	if r.frames != want || o.Frames() != want {
		t.Fatalf("%d frames (%d counted), want %d", r.frames, o.Frames(), want)
	}
	for i := 1; i < len(r.presented); i++ {
		prev, cur := r.presented[i-1], r.presented[i]
		if cur.Seq <= prev.Seq || cur.Idle || cur.Refresh != mode.Interval() {
			t.Fatalf("presentation %d = %+v after %+v: want a later tick of %v", i, cur, prev, mode.Interval())
		}
	}
}

func TestAnOutputNobodyAsksFramesOfRendersNone(t *testing.T) {
	l, o, r := setup(t)
	enable(t, o)
	tick, err := l.AddTimer(func() error {
		l.Stop()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tick.Remove()
	if err := tick.Arm(10 * mode.Interval()); err != nil {
		t.Fatal(err)
	}
	run(t, l)
	if r.frames != 0 || len(r.presented) != 0 || o.Frames() != 0 {
		t.Fatalf("%d frames and %d presentations without a request", r.frames, len(r.presented))
	}
}

func TestAFrameThatDrawsNothingReportsAnIdleRefresh(t *testing.T) {
	l, o, r := setup(t)
	enable(t, o)
	r.stopAfter = 1
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	run(t, l)
	if r.frames != 1 || len(r.presented) != 1 || !r.presented[0].Idle {
		t.Fatalf("%d frames, presentations %+v: want one idle refresh", r.frames, r.presented)
	}
}

func TestADisabledOrDarkOutputHoldsNoFrames(t *testing.T) {
	_, o, _ := setup(t)
	if _, err := o.BeginFrame(); err == nil {
		t.Fatal("a frame began before the output was enabled")
	}
	enable(t, o)
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOff, Mode: mode}); err != nil {
		t.Fatal(err)
	}
	if err := o.ScheduleFrame(); err != nil || o.clock.Requested() {
		t.Fatalf("a powered-off output took a frame request (%v)", err)
	}
	if err := o.Commit(backend.OutputState{Mode: mode}); err != nil {
		t.Fatal(err)
	}
	if o.target != nil {
		t.Fatal("a disabled output keeps its offscreen target")
	}
}

func TestCommitsOutsideTheOneModeAreRefused(t *testing.T) {
	_, o, _ := setup(t)
	for _, tc := range []struct {
		name  string
		state backend.OutputState
		want  error
	}{
		{"another size", backend.OutputState{Power: backend.PoweredOn, Mode: backend.Mode{Size: image.Pt(32, 32), RefreshMHz: mode.RefreshMHz}}, nil},
		{"another rate", backend.OutputState{Power: backend.PoweredOn, Mode: backend.Mode{Size: mode.Size, RefreshMHz: 60000}}, nil},
		{"10-bit", backend.OutputState{Power: backend.PoweredOn, Mode: mode, Depth: backend.Depth10}, backend.ErrDepthUnsupported},
		{"HDR", backend.OutputState{Power: backend.PoweredOn, Mode: mode, HDR: backend.HDRMetadata{Active: true}}, backend.ErrHDRUnsupported},
	} {
		err := o.Commit(tc.state)
		if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: commit gave %v, want %v", tc.name, err, tc.want)
		}
	}
	if o.State().Enabled() {
		t.Fatalf("a refused commit left the state %+v", o.State())
	}
}

func TestAnInvalidOrHugeModeMakesNoOutput(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := New(l, soft.New(), &recorder{l: l}, "VIRT-1", backend.Mode{Size: image.Pt(0, 48), RefreshMHz: 60000}); err == nil || !strings.Contains(err.Error(), "VIRT-1") {
		t.Fatalf("New with an empty size gave %v", err)
	}
	for _, m := range []backend.Mode{{Size: image.Pt(MaxEdge+1, 48), RefreshMHz: 60000}, {Size: image.Pt(64, MaxEdge+1), RefreshMHz: 60000}, {Size: image.Pt(64, 48), RefreshMHz: MaxRefreshMHz + 1}} {
		if _, err := New(l, soft.New(), &recorder{l: l}, "VIRT-1", m); err == nil || !strings.Contains(err.Error(), "beyond") {
			t.Errorf("New with mode %+v gave %v", m, err)
		}
	}
}

func TestARemovedOutputRefusesWorkAndClosesOnce(t *testing.T) {
	_, o, _ := setup(t)
	enable(t, o)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if err := o.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	if _, err := o.BeginFrame(); err == nil {
		t.Fatal("a frame began on a removed output")
	}
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: mode}); err == nil {
		t.Fatal("a removed output took a commit")
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
}

func TestRenewReplacesTheTargetAndRedrawsWhole(t *testing.T) {
	_, o, _ := setup(t)
	enable(t, o)
	paint(t, o, 1)
	before := o.target
	if err := o.Renew(); err != nil {
		t.Fatal(err)
	}
	f, err := o.BeginFrame()
	if err != nil {
		t.Fatal(err)
	}
	o.AbortFrame()
	if o.target == before || f.Age != 0 {
		t.Fatalf("Renew kept the target (%v) or its age (%d)", o.target == before, f.Age)
	}
}
