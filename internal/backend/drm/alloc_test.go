package drm

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func TestTheProbeTurnsAScanoutBufferIntoAFramebufferAndRemovesIt(t *testing.T) {
	a := fakeStack(&blitWorld{memory: map[int]*soft.Buffer{}}, "card", true)
	dev := a.dev.(*fakeDevice)
	if err := a.probe(); err != nil {
		t.Fatal(err)
	}
	if dev.fbs != 1 {
		t.Fatalf("the probe created %d framebuffers, want 1", dev.fbs)
	}
	if len(dev.removedFBs) != 1 {
		t.Fatalf("the probe left %d framebuffers removed, want the 1 it created", len(dev.removedFBs))
	}
}

func TestTheProbeFailsWhenTheCardTakesNoFramebufferFromAScanoutBuffer(t *testing.T) {
	a := fakeStack(&blitWorld{memory: map[int]*soft.Buffer{}}, "card", true)
	a.dev.(*fakeDevice).refuseAddFB = true
	if err := a.probe(); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("probe() = %v, want the card's EINVAL: an allocator whose buffers never reach the screen must not pass", err)
	}
}
