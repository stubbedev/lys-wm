package harness

import (
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const (
	thumbRoughnessBound = 2.0
	thumbMatchTolerance = 8
	thumbFrameBound     = 10
	thumbFrameStep      = 16 * time.Millisecond
)

func fineDetail(pix []byte, stride int, size image.Point, frame int) {
	phase, band := frame&1, max(size.Y/3&^7, 8)
	for y := range size.Y {
		for x := range size.X {
			var on bool
			switch y / band {
			case 0:
				on = (y+phase)&1 == 0
			case 1:
				on = (x+y+phase)&1 == 0
			default:
				on = (x+phase)&1 == 0
			}
			v := byte(0xf0)
			if on {
				v = 0x10
			}
			at := y*stride + x*4
			pix[at], pix[at+1], pix[at+2], pix[at+3] = v, v, v, 0xff
		}
	}
}

func thumbRoughness(px *soft.Buffer, r image.Rectangle) float64 {
	var sum, n int
	for y := r.Min.Y; y < r.Max.Y-1; y++ {
		for x := r.Min.X; x < r.Max.X-1; x++ {
			at := pixel(px, x, y)[1]
			sum += absDiff(at, pixel(px, x+1, y)[1]) + absDiff(at, pixel(px, x, y+1)[1])
			n++
		}
	}
	return float64(sum) / float64(n)
}

func thumbMismatch(a, b *soft.Buffer, r image.Rectangle) int {
	worst := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			pa, pb := pixel(a, x, y), pixel(b, x, y)
			for i := range pa {
				worst = max(worst, absDiff(pa[i], pb[i]))
			}
		}
	}
	return worst
}

func absDiff(a, b byte) int {
	return max(int(a)-int(b), int(b)-int(a))
}

type thumbCycle struct {
	shown   *soft.Buffer
	redraws uint64
	counts  render.Counts
}

func (h *Harness) thumbCycle(now *time.Duration, change func()) thumbCycle {
	h.t.Helper()
	var (
		cycle   thumbCycle
		redraws uint64
		counts  render.Counts
	)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		redraws, counts = comp.OverviewThumbRedraws(), b.Renderer().Counts()
		return nil
	})
	change()
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		frames := 0
		for comp.OverviewThumbRedraws() == redraws || comp.OverviewThumbsStale() {
			if frames++; frames > thumbFrameBound {
				break
			}
			*now += thumbFrameStep
			if err := comp.Frame(o); err != nil {
				return err
			}
		}
		*now += thumbFrameStep
		if err := comp.Frame(o); err != nil {
			return err
		}
		after := b.Renderer().Counts()
		cycle = thumbCycle{
			shown:   snapshot(o),
			redraws: comp.OverviewThumbRedraws() - redraws,
			counts:  render.Counts{Passes: after.Passes - counts.Passes, Uploads: after.Uploads - counts.Uploads},
		}
		if comp.OverviewThumbsStale() {
			h.t.Errorf("after %d frames the thumbnail is still not redrawn through its filter", frames)
		}
		return nil
	})
	return cycle
}

func TestAThumbnailStaysFilteredAcrossItsWindowsUpdates(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		var now time.Duration
		h := start(t, func(cfg *compositor.Config) {
			cfg.Animations = springTable(t)
			cfg.Clock = func() time.Duration { return now }
			plainOverview(cfg)
		}, overviewMode)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("fine", fineDetail)
		c.DispatchUntil("the window to map", w.Mapped)
		h.dispatch("fullscreen")
		c.DispatchUntil("the window at the whole output", func() bool { return w.Logical == overviewMode.Size })
		w.Stop()
		h.Rendered(0)
		h.dispatch("overview show")
		h.frameAt(&now, time.Minute)
		thumb := planOf(h, 0).Strip[0].Inset(12)
		first := h.thumbCycle(&now, func() {})
		update := func() {
			if err := w.Redraw(); err != nil {
				t.Fatal(err)
			}
			c.Sync()
		}
		updated := h.thumbCycle(&now, update)
		again := h.thumbCycle(&now, update)
		refreshed := h.thumbCycle(&now, func() {
			h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
				comp.DirtyOverviewThumbs()
				return nil
			})
		})

		for _, cycle := range []struct {
			name string
			thumbCycle
		}{{"first shown", first}, {"after an update", updated}, {"after another update", again}} {
			if got := thumbRoughness(cycle.shown, thumb); got > thumbRoughnessBound {
				t.Errorf("the thumbnail %s has a high-frequency energy of %.2f, over %.2f: aliased", cycle.name, got, thumbRoughnessBound)
			}
		}
		if got := thumbMismatch(first.shown, updated.shown, thumb); got > thumbMatchTolerance {
			t.Errorf("the thumbnail after an update differs from the first by up to %d, over %d", got, thumbMatchTolerance)
		}
		if refreshed.redraws != 1 {
			t.Fatalf("a dirtied thumbnail was redrawn %d times, want once", refreshed.redraws)
		}
		want := render.Counts{Passes: refreshed.counts.Passes + 1, Uploads: refreshed.counts.Uploads + 1}
		for _, cycle := range []struct {
			name string
			thumbCycle
		}{{"an update", updated}, {"another update", again}} {
			if cycle.redraws != 1 {
				t.Errorf("%s redrew the thumbnail %d times, want once", cycle.name, cycle.redraws)
			}
			if cycle.counts != want {
				t.Errorf("%s cost %+v, want one refresh of the thumbnail and the client's own frame and upload, %+v", cycle.name, cycle.counts, want)
			}
		}
	})
}
