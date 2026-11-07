package drm

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func bootDisplayOnly(t *testing.T, setup func(*fakeDevice)) (*rig, *gbmAllocator) {
	t.Helper()
	linear := fakeStack(&blitWorld{memory: map[int]*soft.Buffer{}}, "primary", true)
	r := boot(t, func(dev *fakeDevice) allocator {
		if setup != nil {
			setup(dev)
		}
		return &linearFirst{dev: dev, primary: linear, own: &copyAllocator{dev: dev, primary: linear.gpu}, ownHow: "a CPU copy"}
	})
	return r, linear
}

func (r *rig) drawRed(o *Output) uint64 {
	r.t.Helper()
	f, err := o.BeginFrame()
	if err != nil {
		r.t.Fatal(err)
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rect(0, 0, 4, 4)})
	if err != nil {
		r.t.Fatal(err)
	}
	p.FillRect(image.Rect(0, 0, 4, 4), render.Color{R: 0xff, A: 0xff}, render.Mask{})
	if err := errors.Join(p.End(), o.Present(nil)); err != nil {
		r.t.Fatal(err)
	}
	return r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{40, propFB}]
}

func TestADisplayOnlyCardScansOutThePrimarysLinearBuffer(t *testing.T) {
	r, linear := bootDisplayOnly(t, nil)
	o := r.b.Outputs()[0]
	fb := r.drawRed(o)
	if r.dev.scanouts[uint32(fb)] < primeHandles || r.dev.dumbs != 0 || r.b.SyncReads() != 0 {
		t.Fatalf("framebuffer %d is handle %d with %d dumb buffers and %d CPU reads, want the primary's linear buffer imported", fb, r.dev.scanouts[uint32(fb)], r.dev.dumbs, r.b.SyncReads())
	}
	if o.buffers[0].prepare != nil || o.buffers[0].prepareLater != nil {
		t.Fatal("a shared buffer copies its frame")
	}
	if fence := r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{40, propInFence}]; fence == 0 || linear.gpu.(*fakeRenderer).world.log[0] != "primary fence" {
		t.Fatalf("the frame carries fence %d, want the primary GPU's", fence)
	}
}

func TestADisplayOnlyCardThatRefusesTheLinearBufferGetsTheCopy(t *testing.T) {
	var r *rig
	r, _ = bootDisplayOnly(t, func(dev *fakeDevice) {
		dev.refuseFrames = func(fb uint64) bool { return dev.scanouts[uint32(fb)] >= primeHandles }
	})
	if len(r.b.Outputs()) != 1 {
		t.Fatalf("outputs %v, reports %v", r.rec.log, r.reports)
	}
	o := r.b.Outputs()[0]
	fb := r.drawRed(o)
	pix := r.dev.pix[r.dev.scanouts[uint32(fb)]]
	if pix == nil || pix[2] != 0xff || r.b.SyncReads() != 1 {
		t.Fatalf("framebuffer %d after the refusal is no CPU copy of the red frame: %d reads", fb, r.b.SyncReads())
	}
	if len(r.reports) != 1 {
		t.Fatalf("reports %v, want the fallback named once", r.reports)
	}
}

func TestADisplayOnlyCardRefusedOnceForTheModeTakesTheLinearBufferAgainOnTheNextModeset(t *testing.T) {
	refusals := 1
	var r *rig
	r, _ = bootDisplayOnly(t, func(dev *fakeDevice) {
		dev.refuseFrames = func(fb uint64) bool {
			if refusals == 0 || dev.scanouts[uint32(fb)] < primeHandles {
				return false
			}
			refusals--
			return true
		}
	})
	o := r.b.Outputs()[0]
	if fb := r.drawRed(o); r.dev.scanouts[uint32(fb)] >= primeHandles || len(r.reports) != 1 {
		t.Fatalf("framebuffer %d after the refusal is handle %d with reports %v, want the CPU copy named once", fb, r.dev.scanouts[uint32(fb)], r.reports)
	}
	r.dev.flip(r.t, 10, uint32(len(r.dev.commits)))
	r.until("the copied frame's flip", func() bool { return !o.flipping })
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[1])}); err != nil {
		t.Fatal(err)
	}
	reads := r.b.SyncReads()
	if fb := r.drawRed(o); r.dev.scanouts[uint32(fb)] < primeHandles || r.b.SyncReads() != reads {
		t.Fatalf("framebuffer %d after the next modeset is handle %d with %d more CPU reads, want the primary's linear buffer again", fb, r.dev.scanouts[uint32(fb)], r.b.SyncReads()-reads)
	}
	if o.layout.route != primaryBuffer || len(r.reports) != 1 {
		t.Fatalf("route %d, reports %v", o.layout.route, r.reports)
	}
}

func TestDamagedRowsMergeIntoTheSpansACopyMoves(t *testing.T) {
	for _, c := range []struct {
		name   string
		stale  []rows
		damage render.Region
		want   []rows
	}{
		{"no damage is the whole frame", []rows{{2, 3}}, nil, []rows{{0, 10}}},
		{"apart", nil, render.Region{image.Rect(0, 6, 4, 8), image.Rect(0, 1, 4, 2)}, []rows{{1, 2}, {6, 8}}},
		{"overlapping and touching merge", []rows{{1, 3}}, render.Region{image.Rect(0, 2, 1, 5), image.Rect(3, 5, 4, 6)}, []rows{{1, 6}}},
		{"clipped to the frame", nil, render.Region{image.Rect(0, -3, 4, 2), image.Rect(0, 9, 4, 20), image.Rect(0, 12, 4, 14)}, []rows{{0, 2}, {9, 10}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := addRows(c.stale, c.damage, 10); !slices.Equal(got, c.want) {
				t.Fatalf("rows %v, want %v", got, c.want)
			}
		})
	}
}

func TestAnOffLoopCopyMovesOnlyTheRowsPaintedSinceItsBufferWasLastCopied(t *testing.T) {
	primary := &asyncPrimary{Renderer: soft.New(), waiting: make(chan struct{})}
	close(primary.waiting)
	r := boot(t, func(dev *fakeDevice) allocator { return &copyAllocator{dev: dev, primary: primary} })
	o := r.b.Outputs()[0]
	size := modeOf(o.mode).Size
	pitch := size.X * 4
	present := func(damage render.Region) []byte {
		t.Helper()
		f, err := o.BeginFrame()
		if err != nil {
			t.Fatal(err)
		}
		p, err := f.Renderer.Begin(f.Target, render.Region{{Max: size}})
		if err != nil {
			t.Fatal(err)
		}
		p.FillRect(image.Rectangle{Max: size}, render.Color{R: 0xff, A: 0xff}, render.Mask{})
		n := len(r.dev.commits)
		if err := errors.Join(p.End(), o.Present(damage)); err != nil {
			t.Fatal(err)
		}
		r.committedSince("the copied frame", o, n)
		fb := r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{40, propFB}]
		r.dev.flip(r.t, 10, uint32(len(r.dev.commits)))
		r.until("the flip", func() bool { return !o.flipping })
		return r.dev.pix[r.dev.scanouts[uint32(fb)]]
	}
	first := present(nil)
	present(nil)
	clear(first)
	again := present(render.Region{image.Rect(0, 5, 1, 7)})
	if &again[0] != &first[0] {
		t.Fatal("the third frame did not reuse the first frame's buffer")
	}
	for y := range size.Y {
		want := byte(0)
		if y >= 5 && y < 7 {
			want = 0xff
		}
		if got := again[y*pitch+2]; got != want {
			t.Fatalf("row %d holds red %#x, want %#x", y, got, want)
		}
	}
}
