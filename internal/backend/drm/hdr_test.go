package drm

import (
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func sealBlock(b []byte) {
	var sum byte
	for _, c := range b[:127] {
		sum += c
	}
	b[127] = -sum
}

func hdrEDID(st2084, bt2020 bool, peakCode byte) []byte {
	base := make([]byte, 128)
	copy(base, []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0})
	base[8], base[9] = 0x10, 0x01
	copy(base[27:], []byte{174, 82, 68, 176, 38, 15, 80, 84})
	base[126] = 1
	sealBlock(base)
	ext := make([]byte, 128)
	ext[0], ext[1] = 0x02, 3
	eotf, colorimetry := byte(1), byte(0)
	if st2084 {
		eotf |= 1 << 2
	}
	if bt2020 {
		colorimetry = 0x80
	}
	blocks := []byte{7<<5 | 3, 0x05, colorimetry, 0, 7<<5 | 6, 0x06, eotf, 1, peakCode, 96, 40}
	ext[2] = byte(4 + copy(ext[4:], blocks))
	sealBlock(ext)
	return append(base, ext...)
}

func hdrMeta() backend.HDRMetadata {
	return backend.HDRMetadata{
		Active: true,
		Red:    edid.Chromaticity{X: 0.68, Y: 0.32}, Green: edid.Chromaticity{X: 0.265, Y: 0.69}, Blue: edid.Chromaticity{X: 0.15, Y: 0.06}, White: edid.Chromaticity{X: 0.3127, Y: 0.329},
		MaxLuminance: 1000, MinLuminance: 0.005, MaxCLL: 1000, MaxFALL: 400,
	}
}

func bootHDR(t *testing.T, setup func(*fakeDevice)) (*rig, *Output) {
	t.Helper()
	r := boot(t, func(d *fakeDevice) allocator {
		d.deep, d.hdrConnector, d.sinkEDID = deepFormats, true, hdrEDID(true, true, 124)
		if setup != nil {
			setup(d)
		}
		return &fakeAlloc{soft: soft.New()}
	})
	return r, r.b.Outputs()[0]
}

func connectorValue(c commit, prop uint32) (uint64, bool) {
	v, ok := c.values[[2]uint32{20, prop}]
	return v, ok
}

func TestTheMetadataBlobIsTheKernelsStruct(t *testing.T) {
	b := metadataBytes(hdrMeta())
	if len(b) != 32 {
		t.Fatalf("%d bytes, want sizeof(struct hdr_output_metadata)", len(b))
	}
	if typ := binary.NativeEndian.Uint32(b); typ != 0 {
		t.Fatalf("metadata type %d", typ)
	}
	if b[4] != 2 || b[5] != 0 {
		t.Fatalf("EOTF %d and descriptor %d, want ST 2084 and static type 1", b[4], b[5])
	}
	want := []uint16{34000, 16000, 13250, 34500, 7500, 3000, 15635, 16450, 1000, 50, 1000, 400}
	for i, v := range want {
		if got := binary.NativeEndian.Uint16(b[6+2*i:]); got != v {
			t.Errorf("field %d is %d, want %d", i, got, v)
		}
	}
}

func TestAnHDRCommitProgramsTheConnectorAndSDRPutsItBack(t *testing.T) {
	r, o := bootHDR(t, nil)
	sdr := o.State()
	caps, err := o.HDRCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	if !caps.ST2084 || caps.MaxLuminance < 700 {
		t.Fatalf("capabilities %+v", caps)
	}
	on := sdr
	on.Depth, on.HDR = backend.Depth10, hdrMeta()
	if err := o.Commit(on); err != nil {
		t.Fatal(err)
	}
	if o.State() != on {
		t.Fatalf("state %+v, want %+v", o.State(), on)
	}
	test := r.dev.commits[len(r.dev.commits)-1]
	if test.flags&kdrm.AtomicTestOnly == 0 {
		t.Fatalf("the HDR modeset was not tested first: %+v", test)
	}
	if v, ok := connectorValue(test, propColorspaceID); !ok || v != 9 {
		t.Fatalf("the test set Colorspace to %d (%v), want BT2020_RGB", v, ok)
	}
	if v, ok := connectorValue(test, propMaxBPCID); !ok || v != hdrBPC {
		t.Fatalf("the test set max bpc to %d (%v)", v, ok)
	}
	blob, ok := connectorValue(test, propHDRMeta)
	if !ok || blob == 0 {
		t.Fatalf("the test set the metadata blob to %d (%v)", blob, ok)
	}
	if got := r.dev.blobData[uint32(blob)]; string(got) != string(metadataBytes(hdrMeta())) {
		t.Fatalf("the blob holds %v", got)
	}
	r.dev.commits = nil
	r.committedSince("the first HDR frame", func() *Output { _ = o.ScheduleFrame(); return o }(), 0)
	first := r.dev.commits[len(r.dev.commits)-1]
	if v, ok := connectorValue(first, propHDRMeta); !ok || v != blob {
		t.Fatalf("the modeset's metadata blob is %d (%v), want %d", v, ok, blob)
	}
	r.dev.flip(t, 10, 1)
	r.until("the flip", func() bool { return !o.flipping })

	r.dev.commits = nil
	if err := o.Commit(sdr); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.dev.destroyed, uint32(blob)) {
		t.Fatalf("the metadata blob %d was not destroyed: %v", blob, r.dev.destroyed)
	}
	r.committedSince("the first SDR frame", func() *Output { _ = o.ScheduleFrame(); return o }(), 0)
	back := r.dev.commits[len(r.dev.commits)-1]
	for prop, want := range map[uint32]uint64{propColorspaceID: 0, propHDRMeta: 0, propMaxBPCID: 12} {
		if v, ok := connectorValue(back, prop); !ok || v != want {
			t.Errorf("back on SDR property %d is %d (%v), want %d", prop, v, ok, want)
		}
	}
}

func TestSDRSessionsLeaveTheConnectorAlone(t *testing.T) {
	r, o := bootHDR(t, nil)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	for _, c := range r.dev.commits {
		for _, prop := range []uint32{propColorspaceID, propHDRMeta, propMaxBPCID} {
			if _, ok := connectorValue(c, prop); ok {
				t.Fatalf("an SDR session set connector property %d: %+v", prop, c)
			}
		}
	}
}

func TestAnotherSessionsSignalIsCleared(t *testing.T) {
	r, o := bootHDR(t, func(d *fakeDevice) { d.staleHDRBlob = 77 })
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	var cleared bool
	for _, c := range r.dev.commits {
		if v, ok := connectorValue(c, propHDRMeta); ok && v == 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("the metadata another session left stays on the connector")
	}
}

func TestDisplaysAndKernelsThatCannotCarryHDRSaySo(t *testing.T) {
	for name, setup := range map[string]func(*fakeDevice){
		"no properties":      func(d *fakeDevice) { d.hdrConnector = false },
		"no BT2020_RGB":      func(d *fakeDevice) { d.noBT2020Enum = true },
		"no EDID":            func(d *fakeDevice) { d.sinkEDID = nil },
		"an EDID without PQ": func(d *fakeDevice) { d.sinkEDID = hdrEDID(false, true, 124) },
		"no BT.2020":         func(d *fakeDevice) { d.sinkEDID = hdrEDID(true, false, 124) },
		"an unreadable EDID": func(d *fakeDevice) { d.sinkEDID = []byte("not an EDID") },
	} {
		t.Run(name, func(t *testing.T) {
			_, o := bootHDR(t, setup)
			if _, err := o.HDRCapabilities(); !errors.Is(err, backend.ErrHDRUnsupported) {
				t.Fatalf("capabilities: %v", err)
			}
			state := o.State()
			on := state
			on.Depth, on.HDR = backend.Depth10, hdrMeta()
			if err := o.Commit(on); !errors.Is(err, backend.ErrHDRUnsupported) {
				t.Fatalf("commit: %v", err)
			}
			if o.State() != state {
				t.Fatalf("state %+v after a refused commit", o.State())
			}
		})
	}
}

func TestAnHDRModesetTheKernelRefusesLeavesTheOutputAlone(t *testing.T) {
	r, o := bootHDR(t, nil)
	state := o.State()
	before := len(r.dev.destroyed)
	r.dev.refuseModesets = true
	on := state
	on.Depth, on.HDR = backend.Depth10, hdrMeta()
	if err := o.Commit(on); !errors.Is(err, backend.ErrHDRUnsupported) {
		t.Fatalf("commit: %v", err)
	}
	if o.State() != state {
		t.Fatalf("state %+v", o.State())
	}
	if len(r.dev.destroyed) == before {
		t.Fatal("the refused modeset's blobs live on")
	}
}

func TestAnHDROutputReleasesItsBlob(t *testing.T) {
	r, o := bootHDR(t, nil)
	on := o.State()
	on.Depth, on.HDR = backend.Depth10, hdrMeta()
	if err := o.Commit(on); err != nil {
		t.Fatal(err)
	}
	blob := o.signal.blob
	if err := o.release(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.dev.destroyed, blob) {
		t.Fatalf("blob %d was not destroyed: %v", blob, r.dev.destroyed)
	}
}
