package drm

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func TestASecondaryHoldsItsRenderStackOnlyWhileItDrivesAnOutput(t *testing.T) {
	world := &blitWorld{memory: map[int]*soft.Buffer{}}
	primary := fakeStack(world, "primary", true)
	opened, closed := 0, 0
	var lazy *lazyBlit
	stack := func(dev *fakeDevice) *blitAllocator {
		local := fakeStack(world, "local", true)
		local.dev, local.closeGPU = dev, func() error { closed++; return nil }
		return &blitAllocator{primary: primary, local: local}
	}
	r := boot(t, func(dev *fakeDevice) allocator {
		var err error
		lazy, err = newLazyBlit(stack(dev), func() (*blitAllocator, error) {
			opened++
			return stack(dev), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return lazy
	})
	o := r.b.Outputs()[0]
	if opened != 1 || closed != 1 || lazy.blit == nil {
		t.Fatalf("after the first output lit: %d stacks opened, %d closed, open %v", opened, closed, lazy.blit != nil)
	}
	if err := o.Commit(backend.OutputState{}); err != nil {
		t.Fatal(err)
	}
	if lazy.blit != nil || closed != 2 {
		t.Fatalf("with every output off the render stack stays open: %d closed", closed)
	}
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[0])}); err != nil {
		t.Fatal(err)
	}
	if lazy.blit == nil || opened != 2 {
		t.Fatalf("enabling an output did not open the render stack: %d opened", opened)
	}
	r.drawRed(o)
	if !o.flipping {
		t.Fatal("the frame on the reopened stack did not commit")
	}
}

func bootRenderCapable(t *testing.T, refuseLinear bool) (*rig, *blitWorld, *int) {
	t.Helper()
	world := &blitWorld{memory: map[int]*soft.Buffer{}}
	primary := fakeStack(world, "primary", true)
	opened := 0
	r := boot(t, func(dev *fakeDevice) allocator {
		if refuseLinear {
			dev.refuseFrames = func(fb uint64) bool {
				h := dev.scanouts[uint32(fb)]
				return h >= primeHandles && !world.scanout[int(h-primeHandles)]
			}
		}
		stack := func() *blitAllocator {
			local := fakeStack(world, "local", true)
			local.dev = dev
			return &blitAllocator{primary: primary, local: local}
		}
		lazy, err := newLazyBlit(stack(), func() (*blitAllocator, error) {
			opened++
			return stack(), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return &linearFirst{dev: dev, primary: primary, own: lazy, ownHow: "a GPU blit"}
	})
	return r, world, &opened
}

func TestARenderCapableSecondaryScansOutThePrimarysLinearBufferWithoutOpeningItsStack(t *testing.T) {
	r, world, opened := bootRenderCapable(t, false)
	o := r.b.Outputs()[0]
	fb := r.drawRed(o)
	h := r.dev.scanouts[uint32(fb)]
	if *opened != 0 || h < primeHandles || world.scanout[int(h-primeHandles)] || o.layout.route != primaryBuffer {
		t.Fatalf("framebuffer %d is handle %d after %d stacks opened, want the primary's linear buffer and no stack", fb, h, *opened)
	}
	if len(r.reports) != 0 {
		t.Fatalf("reports %v", r.reports)
	}
}

func TestARenderCapableSecondaryThatRefusesTheLinearBufferBlits(t *testing.T) {
	r, world, opened := bootRenderCapable(t, true)
	o := r.b.Outputs()[0]
	fb := r.drawRed(o)
	h := r.dev.scanouts[uint32(fb)]
	if *opened != 1 || !world.scanout[int(h-primeHandles)] || o.layout.route != ownBuffer {
		t.Fatalf("framebuffer %d is handle %d after %d stacks opened, want a blit into the card's own buffer", fb, h, *opened)
	}
	if pix := world.memory[int(h-primeHandles)].Pix; pix[2] != 0xff {
		t.Fatalf("the blitted buffer starts with %v, not the red the primary drew", pix[:4])
	}
	if len(r.reports) != 1 {
		t.Fatalf("reports %v, want the fallback named once", r.reports)
	}
}
