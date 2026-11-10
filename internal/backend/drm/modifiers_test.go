package drm

import (
	"slices"
	"strings"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func TestARefusedModifierFallsDownToTheNextOneTheDriverAllocates(t *testing.T) {
	alloc := &fakeAlloc{soft: soft.New(), fbModifiers: map[uint64]fourcc.Modifier{}}
	r := boot(t, func(dev *fakeDevice) allocator {
		dev.refuseFrames = func(fb uint64) bool { m, ok := alloc.fbModifiers[fb]; return ok && m == fourcc.Linear }
		return alloc
	})
	o := r.b.Outputs()[0]
	want := [][]fourcc.Modifier{{0, 7}, {0, 7}, {7}, {7}}
	if !slices.EqualFunc(alloc.offered, want, slices.Equal) {
		t.Fatalf("offered the allocator %v, want %v", alloc.offered, want)
	}
	if !o.State().Enabled() || len(o.buffers) != buffersPerOut || slices.ContainsFunc(o.buffers, func(s *scanout) bool { return s.modifier != 7 }) {
		t.Fatalf("state %+v with %d buffers, want both in modifier 7", o.State(), len(o.buffers))
	}
	if !slices.Equal(o.layout.modifiers, []fourcc.Modifier{7}) {
		t.Fatalf("later buffers are offered %v, want only the modifier the kernel took", o.layout.modifiers)
	}
	if len(r.reports) != 1 || !strings.Contains(r.reports[0].Error(), "in modifier "+fourcc.Modifier(7).String()) || !strings.Contains(r.reports[0].Error(), scanoutFormat.String()+" frames in modifier linear") {
		t.Fatalf("reports %v, want the winning modifier and the refused one named once", r.reports)
	}
	tests := 0
	for _, c := range r.dev.commits {
		if c.flags&kdrm.AtomicTestOnly != 0 {
			tests++
		}
	}
	if tests != 0 {
		t.Fatalf("%d test commits after the modeset", tests)
	}
}

func TestAModeWhoseEveryModifierIsRefusedKeepsTheFramesOnScreen(t *testing.T) {
	alloc := &fakeAlloc{soft: soft.New(), fbModifiers: map[uint64]fourcc.Modifier{}}
	r := boot(t, func(*fakeDevice) allocator { return alloc })
	o := r.b.Outputs()[0]
	before, buffers := o.State(), slices.Clone(o.buffers)
	booted := uint64(900 + alloc.next)
	r.dev.refuseFrames = func(fb uint64) bool { return fb > booted }
	alloc.offered = nil
	err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[1])})
	if err == nil {
		t.Fatal("a mode the kernel refuses in every modifier was taken")
	}
	for _, m := range []fourcc.Modifier{0, 7} {
		if !strings.Contains(err.Error(), scanoutFormat.String()+" frames in modifier "+m.String()) {
			t.Fatalf("the error does not name modifier %v: %v", m, err)
		}
	}
	want := [][]fourcc.Modifier{{0, 7}, {0, 7}, {7}, {7}}
	if !slices.EqualFunc(alloc.offered, want, slices.Equal) {
		t.Fatalf("offered the allocator %v, want %v", alloc.offered, want)
	}
	if o.State() != before || !slices.Equal(o.buffers, buffers) {
		t.Fatalf("state %+v and buffers %v after the refusal, want the frames untouched", o.State(), o.buffers)
	}
}

func TestThePrimaryPlaneOffersOnlyModifiersTheRendererAllocates(t *testing.T) {
	r := boot(t, func(*fakeDevice) allocator { return &fakeAlloc{soft: soft.New(), renders: []fourcc.Modifier{9, 7}} })
	if got := r.b.Outputs()[0].primary.modifiers; !slices.Equal(got, []fourcc.Modifier{7}) {
		t.Fatalf("the primary plane offers %v, want the one modifier both scan out and render", got)
	}
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	_, err = newBackend(l, &fakeDevice{connected: true}, &fakeAlloc{soft: soft.New(), renders: []fourcc.Modifier{9}}, Options{Report: func(error) {}})
	if err == nil || !strings.Contains(err.Error(), "scans out no XRGB8888 modifier the renderer can draw") {
		t.Fatalf("a renderer sharing no modifier with the plane: %v", err)
	}
}

func TestWithoutModifierKeepsTheOrderOfTheRest(t *testing.T) {
	offered := []fourcc.Modifier{3, 7, 0, 9}
	if got := withoutModifier(offered, 7); !slices.Equal(got, []fourcc.Modifier{3, 0, 9}) {
		t.Fatalf("withoutModifier = %v", got)
	}
	if !slices.Equal(offered, []fourcc.Modifier{3, 7, 0, 9}) {
		t.Fatalf("withoutModifier changed its input to %v", offered)
	}
	if got := withoutModifier(offered, 5); !slices.Equal(got, offered) {
		t.Fatalf("withoutModifier of a missing modifier = %v", got)
	}
}
