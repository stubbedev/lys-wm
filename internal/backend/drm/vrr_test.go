package drm

import (
	"image"
	"testing"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"

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
	o := r.flipFirstFrame()
	interval := modeOf(o.mode).Interval()
	r.at(o.lastFlip + time.Millisecond)
	if o.frameWait() <= 0 || o.planesWait() <= 0 {
		t.Fatalf("at a fixed refresh, right after a flip the frame waits %v and the planes %v, want both held for the next vblank", o.frameWait(), o.planesWait())
	}
	if err := o.SetAdaptiveSync(true); err != nil {
		t.Fatal(err)
	}
	r.nextFrame(o)
	r.at(o.lastFlip + time.Millisecond)
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

func vrrEDID(minHz, maxHz byte) []byte {
	b := make([]byte, 128)
	copy(b, []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0})
	b[8], b[9], b[18], b[19] = 0x10, 0x01, 1, 4
	copy(b[54:], []byte{0, 0, 0, 0xfd, 0, minHz, maxHz, 30, 160, 60, 1, 0x0a})
	sealBlock(b)
	return b
}

type vrrCursorRun struct {
	r                            *rig
	o                            *Output
	c                            *cursor
	frames, cursorOnly           int
	cursorAt                     []time.Duration
	stalePlanes, immediateMisses int
}

func bootVRRCursor(t *testing.T) *vrrCursorRun {
	t.Helper()
	r := boot(t, func(d *fakeDevice) allocator {
		d.vrr, d.asyncFlips, d.sinkEDID = true, true, vrrEDID(48, 144)
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	c := r.shownCursor(o, image.Pt(4, 6))
	if err := o.SetAdaptiveSync(true); err != nil {
		t.Fatal(err)
	}
	r.nextFrame(o)
	if !o.vrrSent {
		t.Fatal("adaptive sync did not reach the CRTC")
	}
	if want := time.Second / 48; o.vrrHold != want {
		t.Fatalf("an EDID ranging 48-144 Hz holds the cursor %v, want %v", o.vrrHold, want)
	}
	r.dev.commits = nil
	return &vrrCursorRun{r: r, o: o, c: c}
}

func (v *vrrCursorRun) play(ms int, frameEvery int) {
	r, o, c := v.r, v.o, v.c
	buffers := [2]*fakeBuffer{clientBuffer(7, fourcc.Linear), clientBuffer(8, fourcc.Linear)}
	for tick := 1; tick <= ms; tick++ {
		r.advance(time.Millisecond)
		if o.flipping {
			r.dev.flip(r.t, 10, uint32(tick))
			r.until("the flip", func() bool { return !o.flipping })
		}
		n := len(r.dev.commits)
		if frameEvery > 0 && tick%frameEvery == 0 {
			if !r.scanout(o, buffers[tick/frameEvery%2], false) {
				r.t.Fatalf("the game's frame at %d ms did not scan out", tick)
			}
			if c.onPlane != c.wanted() {
				v.stalePlanes++
			}
		}
		flipping := o.flipping
		if err := c.MoveCursor(image.Pt(100+tick%500, 300)); err != nil {
			r.t.Fatal(err)
		}
		if !flipping && !o.filled && !o.flipping {
			v.immediateMisses++
		}
		if o.planesWaiting && r.now() >= o.lastFlip+o.vrrHold {
			if err := o.planesDue(); err != nil {
				r.t.Fatal(err)
			}
		}
		for _, cm := range r.dev.commits[n:] {
			switch {
			case cm.flags&kdrm.AtomicTestOnly != 0:
			case hasPrimaryFB(cm):
				v.frames++
			default:
				v.cursorOnly++
				v.cursorAt = append(v.cursorAt, r.now())
			}
		}
	}
}

func hasPrimaryFB(c commit) bool {
	_, ok := c.values[[2]uint32{40, propFB}]
	return ok
}

func TestUnderVRRAFullscreenGameCarriesTheCursorInItsFrames(t *testing.T) {
	v := bootVRRCursor(t)
	v.o.FillFrame(true)
	v.play(2000, 20)
	if v.cursorOnly != 0 {
		t.Fatalf("a 50 Hz fullscreen game under VRR with 1000 cursor moves a second saw %d cursor-only commits, want 0", v.cursorOnly)
	}
	if v.frames != 100 || v.stalePlanes != 0 {
		t.Fatalf("%d frames, %d of them without the latest cursor, want 100 frames all carrying it", v.frames, v.stalePlanes)
	}
}

func TestUnderVRRAStalledFullscreenGameLetsTheCursorCommitOncePerMinimumRefresh(t *testing.T) {
	v := bootVRRCursor(t)
	v.o.FillFrame(true)
	v.play(2000, 0)
	hold := v.o.vrrHold
	if len(v.cursorAt) < int(2*time.Second/(hold+2*time.Millisecond)) {
		t.Fatalf("a stalled game saw %d cursor commits in 2 s, want one every %v", len(v.cursorAt), hold)
	}
	for i := 1; i < len(v.cursorAt); i++ {
		if gap := v.cursorAt[i] - v.cursorAt[i-1]; gap < hold || gap > hold+2*time.Millisecond {
			t.Fatalf("cursor commits %d and %d came %v apart, want between %v and %v", i-1, i, gap, hold, hold+2*time.Millisecond)
		}
	}
}

func TestUnderVRROnTheDesktopTheCursorCommitsAtOnce(t *testing.T) {
	v := bootVRRCursor(t)
	v.o.FillFrame(false)
	v.play(2000, 0)
	if v.cursorOnly != 2000 || v.immediateMisses != 0 {
		t.Fatalf("2000 moves on a VRR desktop, each after the last flip, made %d cursor commits and %d waited, want each at once", v.cursorOnly, v.immediateMisses)
	}
}
