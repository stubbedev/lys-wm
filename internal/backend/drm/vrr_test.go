package drm

import (
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func vrrOf(c commit) (uint64, bool) {
	v, ok := c.values[[2]uint32{10, propVRREnabledID}]
	return v, ok
}

func bootVRR(t *testing.T, capable bool) *rig {
	t.Helper()
	return boot(t, func(d *fakeDevice) allocator {
		d.vrr, d.asyncFlips = capable, true
		return &fakeAlloc{soft: soft.New()}
	})
}

func TestAdaptiveSyncReachesTheCRTCOnlyWhenItChanges(t *testing.T) {
	r := bootVRR(t, true)
	o := r.b.Outputs()[0]
	if !o.AdaptiveSyncCapable() {
		t.Fatal("a vrr_capable connector on a CRTC with VRR_ENABLED is not capable")
	}
	if v, ok := vrrOf(r.nextFrame(o)); !ok || v != 0 {
		t.Fatalf("the modeset stated VRR_ENABLED %d (%v), want 0", v, ok)
	}
	if _, ok := vrrOf(r.nextFrame(o)); ok {
		t.Fatal("a flip restated an unchanged VRR_ENABLED")
	}
	if err := o.SetAdaptiveSync(true); err != nil {
		t.Fatal(err)
	}
	if !o.AdaptiveSync() || !o.vrrDirty() {
		t.Fatal("adaptive sync asked for is not pending")
	}
	if v, ok := vrrOf(r.nextFrame(o)); !ok || v != 1 {
		t.Fatalf("turning adaptive sync on set VRR_ENABLED %d (%v), want 1", v, ok)
	}
	if _, ok := vrrOf(r.nextFrame(o)); ok {
		t.Fatal("the next frame set VRR_ENABLED again")
	}
	if p := r.rec.presents[len(r.rec.presents)-1]; !p.Variable {
		t.Fatalf("a flip under adaptive sync presented %+v, want it variable", p)
	}
	if err := o.SetAdaptiveSync(false); err != nil {
		t.Fatal(err)
	}
	r.dev.commits = nil
	if !r.scanout(o, clientBuffer(7, fourcc.Linear), false) {
		t.Fatal("the client buffer did not scan out")
	}
	if v, ok := vrrOf(r.dev.commits[len(r.dev.commits)-1]); !ok || v != 0 {
		t.Fatalf("turning adaptive sync off set VRR_ENABLED %d (%v) on the scanout, want 0", v, ok)
	}
}

func TestAnOutputWithoutVRRCapableKeepsAFixedRefresh(t *testing.T) {
	r := bootVRR(t, false)
	o := r.b.Outputs()[0]
	if o.AdaptiveSyncCapable() {
		t.Fatal("an output without vrr_capable is capable")
	}
	if err := o.SetAdaptiveSync(true); err != nil {
		t.Fatal(err)
	}
	if o.AdaptiveSync() {
		t.Fatal("adaptive sync turned on where the connector cannot vary its refresh")
	}
	if _, ok := vrrOf(r.nextFrame(o)); ok {
		t.Fatal("a CRTC without VRR_ENABLED was sent one")
	}
}

func TestUnderAdaptiveSyncTheFrameClockKeepsNoFixedInterval(t *testing.T) {
	r := bootVRR(t, true)
	r.pinned = backend.Now()
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	r.pinned = o.lastFlip + time.Millisecond
	if o.frameWait() <= 0 || o.planesWait() <= 0 {
		t.Fatalf("at a fixed refresh, right after a flip the frame waits %v and the planes %v, want both held for the next vblank", o.frameWait(), o.planesWait())
	}
	if err := o.SetAdaptiveSync(true); err != nil {
		t.Fatal(err)
	}
	r.pinned = 0
	r.nextFrame(o)
	r.pinned = o.lastFlip + time.Millisecond
	if wait := o.frameWait(); wait != 0 {
		t.Fatalf("under adaptive sync, a millisecond after a flip the frame waits %v of a %v refresh, want none", wait, interval)
	}
	if wait := o.planesWait(); wait != 0 {
		t.Fatalf("under adaptive sync, a millisecond after a flip the planes wait %v, want none", wait)
	}
	if o.aimedAt != 0 {
		t.Fatalf("under adaptive sync the frame aimed at vblank %v, want none", o.aimedAt)
	}
}
