package compositor

import (
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
)

type linearSecond struct{}

func (linearSecond) At(elapsed time.Duration) float64 { return elapsed.Seconds() }

func (linearSecond) Duration() time.Duration { return time.Second }

func TestAFrameSamplesItsAnimationsAtTheVblankItAimsAt(t *testing.T) {
	const refresh = 16 * time.Millisecond
	now := 10 * time.Second
	c := &Compositor{scenes: map[*output.Output]*scene{}, clock: func() time.Duration { return now }}
	o := &output.Output{}
	c.sceneOf(o).last = backend.Presentation{Time: now - 100*time.Millisecond, Refresh: refresh}
	start := now - 50*time.Millisecond
	tween := anim.NewTween(0.0, anim.Lerp)
	tween.Set(1, start, linearSecond{})
	now += 5 * time.Millisecond
	aim, _ := backend.NextVblank(c.sceneOf(o).last.Time, now, refresh)
	frame := c.aimed(o, c.clock())
	if frame != aim || frame == now {
		t.Fatalf("the frame samples at %v, want the aimed vblank %v, not the clock's %v", frame, aim, now)
	}
	if got, want := tween.At(frame), (aim - start).Seconds(); got != want {
		t.Fatalf("the animation sampled %v of its travel, want %v: the aimed vblank minus its start", got, want)
	}
}

func TestAFrameWithoutARecentVblankSamplesAtTheClock(t *testing.T) {
	now := 10 * time.Second
	c := &Compositor{scenes: map[*output.Output]*scene{}}
	o := &output.Output{}
	if got := c.aimed(o, now); got != now {
		t.Fatalf("without a presentation the frame samples at %v, want %v", got, now)
	}
	c.sceneOf(o).last = backend.Presentation{Time: now - time.Minute, Refresh: 16 * time.Millisecond}
	if got := c.aimed(o, now); got != now {
		t.Fatalf("after a minute idle the frame samples at %v, want %v", got, now)
	}
	c.sceneOf(o).last = backend.Presentation{Time: now + time.Hour, Refresh: 16 * time.Millisecond}
	if got := c.aimed(o, now); got != now {
		t.Fatalf("with a vblank on another clock the frame samples at %v, want %v", got, now)
	}
}
