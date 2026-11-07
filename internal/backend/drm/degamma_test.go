package drm

import (
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func TestADegammaLUTReachesTheCRTCInACommitOfItsOwn(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.degammaSize = 4
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.flipFirstFrame()
	if o.DegammaSize() != 4 {
		t.Fatalf("a CRTC with a 4-entry DEGAMMA_LUT reports %d entries", o.DegammaSize())
	}
	if err := o.SetDegamma(make([]uint16, 3*4)); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 2 || r.dev.commits[0].flags != kdrm.AtomicTestOnly {
		t.Fatalf("a degamma LUT on an idle output committed %+v, want a test and a commit of its own", r.dev.commits)
	}
	if blob := r.dev.commits[1].values[[2]uint32{10, propDegammaID}]; blob == 0 || len(r.dev.blobData[uint32(blob)]) != 4*8 {
		t.Fatalf("the commit set DEGAMMA_LUT to blob %d", blob)
	}
	if err := o.SetDegamma(make([]uint16, 5)); err == nil {
		t.Fatal("a degamma LUT of the wrong size was taken")
	}
}
