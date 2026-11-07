package drm

import (
	"image"
	"slices"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

var (
	tileHalf = kdrm.Mode{Clock: 297000, HDisplay: 1920, HSyncStart: 2008, HSyncEnd: 2052, HTotal: 2200, VDisplay: 2160, VSyncStart: 2168, VSyncEnd: 2178, VTotal: 2250, Type: kdrm.ModeTypePreferred}
	tileLone = kdrm.Mode{Clock: 148500, HDisplay: 1920, HTotal: 2200, VDisplay: 1080, VTotal: 1125}
)

func bootTiled(t *testing.T, tiles map[uint32]string) *rig {
	t.Helper()
	return boot(t, func(d *fakeDevice) allocator {
		d.connectors, d.modes, d.tiles = []uint32{20, 21}, []kdrm.Mode{tileHalf, tileLone}, tiles
		return &fakeAlloc{soft: soft.New()}
	})
}

var halves = map[uint32]string{20: "7:1:2:1:0:0:1920:2160", 21: "7:1:2:1:1:0:1920:2160"}

func TestATileBlobPlacesItsTile(t *testing.T) {
	got, err := parseTile([]byte("7:1:2:1:1:0:1920:2160\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (tileInfo{group: 7, cols: 2, rows: 1, col: 1, size: image.Pt(1920, 2160)}); got != want {
		t.Fatalf("parsed %+v, want %+v", got, want)
	}
	if got.at() != image.Pt(1920, 0) || got.full() != image.Pt(3840, 2160) {
		t.Fatalf("tile at %v of %v, want (1920,0) of 3840x2160", got.at(), got.full())
	}
	for _, bad := range []string{"", "7:1:2:1:0:0:1920", "0:1:2:1:0:0:1920:2160", "7:1:2:1:2:0:1920:2160", "7:1:2:1:0:0:x:2160", "7:1:2:1:0:0:-1:2160"} {
		if _, err := parseTile([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func modeIDOf(r *rig, c commit, crtc uint32) kdrm.Mode {
	r.t.Helper()
	blob, ok := c.values[[2]uint32{crtc, propModeID}]
	if !ok {
		r.t.Fatalf("CRTC %d has no MODE_ID in %+v", crtc, c.values)
	}
	m, err := kdrm.ParseMode(r.dev.blobData[uint32(blob)])
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func TestTiledConnectorsLightAsOneOutputAcrossTheirCRTCs(t *testing.T) {
	r := bootTiled(t, halves)
	outs := r.b.Outputs()
	if len(outs) != 1 {
		t.Fatalf("%d outputs for a monitor of two tiles", len(outs))
	}
	o := outs[0]
	full := image.Pt(3840, 2160)
	if modes := o.Modes(); len(modes) != 1 || modes[0].Size != full || modes[0].RefreshMHz != modeOf(tileHalf).RefreshMHz || !modes[0].Preferred {
		t.Fatalf("modes %+v, want the one %v mode of the tiles' rate", modes, full)
	}
	if s := o.State(); s.Mode.Size != full || !s.Enabled() {
		t.Fatalf("state %+v", s)
	}
	if _, err := o.CursorPlane(); err == nil {
		t.Fatal("a tiled output took a cursor plane that shows on one tile only")
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	c := r.dev.commits[len(r.dev.commits)-1]
	fb := c.values[[2]uint32{40, propFB}]
	want := map[[2]uint32]uint64{
		{20, propConnCRTC}: 10, {21, propConnCRTC}: 11, {10, propActive}: 1, {11, propActive}: 1,
		{40, propPlaneCRTC}: 10, {40, propSrcX}: 0, {40, propSrcW}: 1920 << 16, {40, propSrcH}: 2160 << 16, {40, propCRTCX}: 0, {40, propCRTCW}: 1920, {40, propCRTCH}: 2160,
		{41, propFB}: fb, {41, propPlaneCRTC}: 11, {41, propSrcX}: 1920 << 16, {41, propSrcY}: 0, {41, propSrcW}: 1920 << 16, {41, propSrcH}: 2160 << 16, {41, propCRTCX}: 0, {41, propCRTCW}: 1920, {41, propCRTCH}: 2160,
	}
	for k, v := range want {
		if got, ok := c.values[k]; !ok || got != v {
			t.Errorf("object %d property %d = %d (%v), want %d", k[0], k[1], got, ok, v)
		}
	}
	for _, crtc := range []uint32{10, 11} {
		if m := modeIDOf(r, c, crtc); m != tileHalf {
			t.Errorf("CRTC %d runs %+v, want the tile's own mode", crtc, m)
		}
	}
	if size := o.buffers[0].target.Size(); size != full {
		t.Fatalf("frames of %v, want one %v frame both tiles scan out of", size, full)
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.dev.flip(t, 10, 1)
	r.settle()
	if !o.flipping || len(r.rec.presents) != 0 {
		t.Fatalf("one of two tiles flipping finished the frame: flipping %v, presents %+v", o.flipping, r.rec.presents)
	}
	r.dev.flip(t, 10, 1)
	r.committedSince("the frame after both tiles flipped", o, len(r.dev.commits))
	if len(r.rec.presents) != 1 {
		t.Fatalf("presents %+v after both tiles flipped", r.rec.presents)
	}
	next := r.dev.commits[len(r.dev.commits)-1]
	if next.flags&kdrm.AtomicAllowModeset != 0 || next.values[[2]uint32{40, propFB}] == fb || next.values[[2]uint32{41, propFB}] != next.values[[2]uint32{40, propFB}] {
		t.Fatalf("the next frame: flags %#x, planes %d and %d, want both on the new frame %d without a modeset", next.flags, next.values[[2]uint32{40, propFB}], next.values[[2]uint32{41, propFB}], fb)
	}
}

func (r *rig) tiledFrame(o *Output) commit {
	r.t.Helper()
	r.dev.commits = nil
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.committedSince("a frame", o, 0)
	c := r.dev.commits[len(r.dev.commits)-1]
	r.dev.flip(r.t, 10, 1)
	r.dev.flip(r.t, 10, 1)
	r.until("both tiles' flips", func() bool { return !o.flipping })
	return c
}

func TestATiledOutputSetsItsColourOnEveryCRTC(t *testing.T) {
	r := bootTiled(t, halves)
	o := r.b.Outputs()[0]
	r.tiledFrame(o)
	r.dev.commits = nil
	if err := o.SetGamma(make([]uint16, 3*o.GammaSize())); err != nil {
		t.Fatal(err)
	}
	r.until("the gamma commit", func() bool { return o.flipping })
	c := r.dev.commits[len(r.dev.commits)-1]
	r.dev.flip(t, 10, 1)
	r.settle()
	if !o.flipping {
		t.Fatal("one CRTC flipping finished a commit that changed both")
	}
	r.dev.flip(t, 10, 1)
	r.until("both CRTCs' flips", func() bool { return !o.flipping })
	lead, ok := c.values[[2]uint32{10, propGammaLUT}]
	if !ok || lead == 0 || c.values[[2]uint32{11, propGammaLUT}] != lead {
		t.Fatalf("gamma LUT %d on CRTC 10 and %d on CRTC 11, want the one blob on both", lead, c.values[[2]uint32{11, propGammaLUT}])
	}
}

func TestATileThatGoesAwayLeavesItsPartnerAlone(t *testing.T) {
	r := bootTiled(t, halves)
	r.dev.connectors = []uint32{20}
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	outs := r.b.Outputs()
	if len(outs) != 1 || outs[0].tiled() || outs[0].conn.ID != 20 {
		t.Fatalf("outputs %v after a tile left, want connector 20 alone", outs)
	}
	if modes := outs[0].Modes(); !slices.Equal(modes, []backend.Mode{modeOf(tileHalf), modeOf(tileLone)}) {
		t.Fatalf("the lone tile offers %+v, want its own modes", modes)
	}
	r.dev.connectors = []uint32{20, 21}
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	if outs := r.b.Outputs(); len(outs) != 1 || !outs[0].tiled() || outs[0].State().Mode.Size != image.Pt(3840, 2160) {
		t.Fatalf("outputs %v after the tile came back, want one tiled output", outs)
	}
}

func TestATileGroupTheDriverDrivesWholeLightsOneConnector(t *testing.T) {
	whole := kdrm.Mode{Clock: 594000, HDisplay: 3840, HTotal: 4400, VDisplay: 2160, VTotal: 2250, Type: kdrm.ModeTypePreferred}
	r := boot(t, func(d *fakeDevice) allocator {
		d.connectors, d.modes, d.tiles = []uint32{20, 21}, []kdrm.Mode{whole, tileHalf}, halves
		return &fakeAlloc{soft: soft.New()}
	})
	outs := r.b.Outputs()
	if len(outs) != 1 || outs[0].tiled() || outs[0].State().Mode.Size != image.Pt(3840, 2160) {
		t.Fatalf("outputs %v, want the connector that carries the whole picture lit alone", outs)
	}
}

func TestAnIncompleteTileGroupLightsEachConnector(t *testing.T) {
	r := bootTiled(t, map[uint32]string{20: "7:1:3:1:0:0:1920:2160", 21: "7:1:3:1:1:0:1920:2160"})
	if outs := r.b.Outputs(); len(outs) != 2 || outs[0].tiled() || outs[1].tiled() {
		t.Fatalf("outputs %v for two of three tiles, want two plain outputs", outs)
	}
}
