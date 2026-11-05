package drm

import (
	"errors"
	"image"
	"testing"

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
		return &copyAllocator{dev: dev, primary: linear.gpu, linear: linear}
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
