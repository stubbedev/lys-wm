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
