package drm

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"maps"
	"runtime"
	"slices"
	"strings"
	syncatomic "sync/atomic"
	"testing"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

const (
	propType = 100 + iota
	propFB
	propPlaneCRTC
	propSrcX
	propSrcY
	propSrcW
	propSrcH
	propCRTCX
	propCRTCY
	propCRTCW
	propCRTCH
	propInFormats
	propActive
	propModeID
	propConnCRTC
	propInFence
	propDamageClips
	propZpos
	propHotspotX
	propHotspotY
	propGammaLUT
	propGammaSize
	propCTM
	propEDID
	propHDRMeta
	propColorspaceID
	propMaxBPCID
	propNonDesktop
	propLinkStatusID
	propDegammaID
	propDegammaSizeID
	propInFormatsAsync
	propContentTypeID
	propColorEncodingID
	propColorRangeID
	propVRRCapableID
	propVRREnabledID
	propTileID
	inFormatsBlob    = 500
	edidBlob         = 501
	litModeBlob      = 502
	asyncFormatsBlob = 503
	tileBlobs        = 520
	splashFB         = 650
	fakeNumber       = 0xe200
)

type commit struct {
	flags    uint32
	userData uint64
	values   map[[2]uint32]uint64
}

type splash struct {
	crtc, plane uint32
	mode        kdrm.Mode
}

type fakeDevice struct {
	events, write     int
	splash            *splash
	sharedPrimary     bool
	connected         bool
	unplugged         bool
	gone              bool
	removed           bool
	badEDID           bool
	hdrConnector      bool
	noBT2020Enum      bool
	sinkEDID          []byte
	staleHDRBlob      uint64
	refuseCursor      bool
	cursorPlaneFB     uint64
	refuseGamma       bool
	noCTM             bool
	deep              []fourcc.Format
	refuseFrames      func(fb uint64) bool
	refuseCursorAt    func(at image.Point) error
	refuseAsync       bool
	refusePowerOff    bool
	busyNonblock      bool
	busyUntilFlip     bool
	damageClips       bool
	overlayZpos       uint64
	refuseOverlay     bool
	probeGate         chan struct{}
	probeGateOpen     bool
	probesOnLoop      syncatomic.Int64
	loopGoroutine     string
	refuseModesets    bool
	modes             []kdrm.Mode
	connectors        []uint32
	nonDesktop        map[uint32]bool
	links             bool
	linkBad           bool
	foundBPC          uint64
	refuseBPC         bool
	staleDegamma      uint64
	staleColorspace   uint64
	asyncFormats      bool
	imports           int
	contentTypes      bool
	vrr               bool
	tiles             map[uint32]string
	overlayYUV        bool
	refuseLone        bool
	refuseScaling     bool
	secondOverlay     bool
	degammaSize       uint64
	overlayBudget     int
	noMaster          bool
	refuseMode        func(m kdrm.Mode) bool
	active            map[uint32]bool
	asyncFlips        bool
	failCommits       error
	commits           []commit
	blobs             uint32
	blobData          map[uint32][]byte
	destroyed         []uint32
	dumbs, fbs        uint32
	live              map[uint32]bool
	pix               map[uint32][]byte
	scanouts          map[uint32]uint32
	removedFBs        []uint32
	openHandles       map[uint32]bool
	refuseCloseHandle bool
	refuseAddFB       bool
	refuseRemoveFB    bool
	closedFBs         []uint32
	unmapped          map[uint32]bool
	now               func() time.Duration
	vblank            func(crtc uint32) (kdrm.Vblank, error)
	vblankReads       int
}

var modes = []kdrm.Mode{
	{Clock: 148500, HDisplay: 1920, HTotal: 2200, VDisplay: 1080, VTotal: 1125, Type: kdrm.ModeTypePreferred},
	{Clock: 74250, HDisplay: 1280, HTotal: 1650, VDisplay: 720, VTotal: 750},
}

func (f *fakeDevice) FD() int { return f.events }

func (f *fakeDevice) Path() string { return "/dev/dri/card0" }

func (f *fakeDevice) CRTCSequence(crtc uint32) (kdrm.Vblank, error) {
	f.vblankReads++
	if f.vblank == nil {
		return kdrm.Vblank{}, kdrm.ErrNoVblankSequence
	}
	return f.vblank(crtc)
}

func (f *fakeDevice) Number() (uint64, error) { return fakeNumber, nil }

func (f *fakeDevice) Cap(c uint64) (uint64, error) {
	if f.removed {
		return 0, unix.ENODEV
	}
	switch c {
	case kdrm.CapCursorWidth, kdrm.CapCursorHeight:
		return 64, nil
	case kdrm.CapAddFB2Modifiers:
		return 1, nil
	case kdrm.CapAtomicAsyncFlip:
		if f.asyncFlips {
			return 1, nil
		}
		return 0, unix.EINVAL
	}
	return 0, fmt.Errorf("unknown cap %#x", c)
}

func (f *fakeDevice) PrimeFDToHandle(fd int) (uint32, error) {
	h := uint32(primeHandles + fd)
	if f.openHandles == nil {
		f.openHandles = map[uint32]bool{}
	}
	f.openHandles[h] = true
	return h, nil
}

func (f *fakeDevice) CloseHandle(h uint32) error {
	if !f.openHandles[h] {
		return fmt.Errorf("handle %d is not open: %w", h, unix.EINVAL)
	}
	if f.refuseCloseHandle {
		return fmt.Errorf("close handle %d: %w", h, unix.EIO)
	}
	delete(f.openHandles, h)
	return nil
}

const primeHandles = 1000

func (f *fakeDevice) Resources() (kdrm.Resources, error) {
	res := kdrm.Resources{CRTCs: []uint32{10, 11}, Connectors: []uint32{20}, Encoders: []uint32{30}}
	if f.connectors != nil {
		res.Connectors = f.connectors
	}
	if f.unplugged {
		res.Connectors = nil
	}
	return res, nil
}

func (f *fakeDevice) probeGated() bool {
	return f.probeGate != nil && !f.probeGateOpen
}

func (f *fakeDevice) openProbeGate() {
	f.probeGateOpen = true
	close(f.probeGate)
}

func (f *fakeDevice) Connector(id uint32) (kdrm.Connector, error) {
	if f.probeGate != nil {
		f.probesOnLoop.Add(probeOn(f.loopGoroutine))
		<-f.probeGate
	}
	if f.unplugged {
		return kdrm.Connector{}, unix.ENOENT
	}
	c := kdrm.Connector{ID: id, Type: 10, TypeID: id - 19, Encoders: []uint32{30}, Connection: kdrm.Disconnected}
	if f.connected {
		c.Connection, c.Modes = kdrm.Connected, modes
		if f.modes != nil {
			c.Modes = f.modes
		}
	}
	return c, nil
}

func (f *fakeDevice) Encoder(id uint32) (kdrm.Encoder, error) {
	return kdrm.Encoder{ID: id, PossibleCRTCs: 0b11}, nil
}

func (f *fakeDevice) Planes() ([]uint32, error) {
	if f.secondOverlay {
		return []uint32{40, 41, 42, 43, 44}, nil
	}
	return []uint32{40, 41, 42, 43}, nil
}

func (f *fakeDevice) Plane(id uint32) (kdrm.Plane, error) {
	formats := []fourcc.Format{scanoutFormat}
	switch id {
	case 43:
		formats = []fourcc.Format{cursorFormat}
	default:
		formats = append(formats, f.deep...)
	}
	return kdrm.Plane{ID: id, PossibleCRTCs: map[uint32]uint32{40: 0b01, 41: 0b10, 42: 0b11, 43: 0b01, 44: 0b11}[id], Formats: formats}, nil
}

func (f *fakeDevice) Properties(object, kind uint32) (map[string]kdrm.Property, error) {
	switch kind {
	case kdrm.ObjectPlane:
		typ, formats := map[uint32]uint64{42: 0, 43: 2}[object], uint64(0)
		if object == 40 || object == 41 || object == 42 && f.sharedPrimary {
			typ, formats = 1, inFormatsBlob
		}
		var fb, on uint64
		if f.splash != nil && f.splash.plane == object {
			fb, on = splashFB, uint64(f.splash.crtc)
		}
		props := map[string]kdrm.Property{
			"type": {ID: propType, Value: typ}, "FB_ID": {ID: propFB, Value: fb}, "CRTC_ID": {ID: propPlaneCRTC, Value: on},
			"SRC_X": {ID: propSrcX}, "SRC_Y": {ID: propSrcY}, "SRC_W": {ID: propSrcW}, "SRC_H": {ID: propSrcH},
			"CRTC_X": {ID: propCRTCX}, "CRTC_Y": {ID: propCRTCY}, "CRTC_W": {ID: propCRTCW}, "CRTC_H": {ID: propCRTCH},
			"IN_FORMATS": {ID: propInFormats, Value: formats}, "IN_FENCE_FD": {ID: propInFence},
		}
		if f.asyncFormats && formats != 0 {
			props["IN_FORMATS_ASYNC"] = kdrm.Property{ID: propInFormatsAsync, Value: asyncFormatsBlob}
		}
		if f.damageClips {
			props["FB_DAMAGE_CLIPS"] = kdrm.Property{ID: propDamageClips}
		}
		if f.overlayYUV && (object == 42 || object == 44) {
			props[propColorEncoding], props[propColorRange] = kdrm.Property{ID: propColorEncodingID}, kdrm.Property{ID: propColorRangeID}
		}
		if f.overlayZpos != 0 {
			props["zpos"] = kdrm.Property{ID: propZpos, Value: map[uint32]uint64{40: 0, 41: 0, 42: f.overlayZpos, 44: f.overlayZpos + 1}[object]}
		}
		if object == 43 {
			props["HOTSPOT_X"], props["HOTSPOT_Y"] = kdrm.Property{ID: propHotspotX}, kdrm.Property{ID: propHotspotY}
		}
		return props, nil
	case kdrm.ObjectCRTC:
		var active, mode uint64
		if f.splash != nil && f.splash.crtc == object {
			active, mode = 1, litModeBlob
		}
		props := map[string]kdrm.Property{"ACTIVE": {ID: propActive, Value: active}, "MODE_ID": {ID: propModeID, Value: mode}, "GAMMA_LUT": {ID: propGammaLUT}, "GAMMA_LUT_SIZE": {ID: propGammaSize, Value: 4}, propDegammaLUT: {ID: propDegammaID, Value: f.staleDegamma}}
		if !f.noCTM {
			props["CTM"] = kdrm.Property{ID: propCTM}
		}
		if f.degammaSize > 0 {
			props["DEGAMMA_LUT_SIZE"] = kdrm.Property{ID: propDegammaSizeID, Value: f.degammaSize}
		}
		if f.vrr {
			props[propVRREnabled] = kdrm.Property{ID: propVRREnabledID}
		}
		return props, nil
	case kdrm.ObjectConnector:
		var routed uint64
		if f.splash != nil {
			routed = uint64(f.splash.crtc)
		}
		props := map[string]kdrm.Property{"CRTC_ID": {ID: propConnCRTC, Value: routed}}
		if f.links {
			props[propLinkStatus] = kdrm.Property{ID: propLinkStatusID, Value: map[bool]uint64{false: linkStatusGood, true: linkStatusBad}[f.linkBad]}
		}
		if f.contentTypes {
			props[propContentType] = kdrm.Property{ID: propContentTypeID}
		}
		if f.vrr {
			props[propVRRCapable] = kdrm.Property{ID: propVRRCapableID, Value: 1}
		}
		if _, ok := f.tiles[object]; ok {
			props[propTile] = kdrm.Property{ID: propTileID, Value: uint64(tileBlobs + object)}
		}
		if f.nonDesktop[object] {
			props["non-desktop"] = kdrm.Property{ID: propNonDesktop, Value: 1}
		}
		if f.badEDID || f.sinkEDID != nil {
			props["EDID"] = kdrm.Property{ID: propEDID, Value: edidBlob}
		}
		if f.hdrConnector {
			props[propHDRMetadata] = kdrm.Property{ID: propHDRMeta, Value: f.staleHDRBlob}
			props[propColorspace] = kdrm.Property{ID: propColorspaceID, Value: f.staleColorspace}
			props[propMaxBPC] = kdrm.Property{ID: propMaxBPCID, Value: cmp.Or(f.foundBPC, 12)}
		}
		return props, nil
	}
	return nil, fmt.Errorf("unknown object type %#x", kind)
}

func (f *fakeDevice) PropertyInfo(id uint32) (kdrm.PropertyInfo, error) {
	switch id {
	case propColorEncodingID:
		return kdrm.PropertyInfo{Name: propColorEncoding, Enums: map[string]uint64{"ITU-R BT.601 YCbCr": 0, "ITU-R BT.709 YCbCr": 1, "ITU-R BT.2020 YCbCr": 2}}, nil
	case propColorRangeID:
		return kdrm.PropertyInfo{Name: propColorRange, Enums: map[string]uint64{"YCbCr limited range": 0, "YCbCr full range": 1}}, nil
	}
	if id == propContentTypeID {
		return kdrm.PropertyInfo{Name: propContentType, Enums: map[string]uint64{"No Data": 0, "Graphics": 1, "Photo": 2, "Cinema": 3, "Game": 4}}, nil
	}
	if id == propColorspaceID {
		enums := map[string]uint64{"Default": 0, "opRGB": 7}
		if !f.noBT2020Enum {
			enums[colorspaceHDR] = 9
		}
		return kdrm.PropertyInfo{Name: propColorspace, Enums: enums}, nil
	}
	return kdrm.PropertyInfo{Name: "type", Enums: map[string]uint64{"Overlay": 0, "Primary": 1, "Cursor": 2}}, nil
}

func (f *fakeDevice) Blob(id uint32) ([]byte, error) {
	if tile, ok := f.tiles[id-tileBlobs]; ok && id > tileBlobs {
		return append([]byte(tile), 0), nil
	}
	if id == edidBlob {
		if f.sinkEDID != nil {
			return f.sinkEDID, nil
		}
		return []byte("not an EDID"), nil
	}
	if id == litModeBlob && f.splash != nil {
		return f.splash.mode.Bytes(), nil
	}
	if id != inFormatsBlob && id != asyncFormatsBlob {
		return nil, fmt.Errorf("no blob %d", id)
	}
	formats := []uint32{uint32(scanoutFormat)}
	for _, format := range f.deep {
		formats = append(formats, uint32(format))
	}
	modifiers := []uint64{0, 7}
	if id == asyncFormatsBlob {
		modifiers = []uint64{7}
	}
	mask := uint64(1)<<len(formats) - 1
	modifiersAt := uint32(24 + (len(formats)*4+7)/8*8)
	b := binary.NativeEndian.AppendUint32(nil, 1)
	for _, v := range []uint32{0, uint32(len(formats)), 24, uint32(len(modifiers)), modifiersAt} {
		b = binary.NativeEndian.AppendUint32(b, v)
	}
	for _, v := range formats {
		b = binary.NativeEndian.AppendUint32(b, v)
	}
	b = append(b, make([]byte, int(modifiersAt)-len(b))...)
	for _, m := range modifiers {
		b = binary.NativeEndian.AppendUint64(b, mask)
		b = binary.NativeEndian.AppendUint64(b, 0)
		b = binary.NativeEndian.AppendUint64(b, m)
	}
	return b, nil
}

func (f *fakeDevice) CreateBlob(data []byte) (uint32, error) {
	f.blobs++
	if f.blobData == nil {
		f.blobData = map[uint32][]byte{}
	}
	f.blobData[600+f.blobs] = slices.Clone(data)
	return 600 + f.blobs, nil
}

func (f *fakeDevice) DestroyBlob(id uint32) error {
	if !f.gone {
		f.destroyed = append(f.destroyed, id)
	}
	return nil
}

func (f *fakeDevice) CreateDumb(size image.Point) (kdrm.Dumb, error) {
	f.dumbs++
	if f.live == nil {
		f.live = map[uint32]bool{}
	}
	f.live[f.dumbs] = true
	pix := make([]byte, size.X*size.Y*4)
	if f.pix == nil {
		f.pix = map[uint32][]byte{}
	}
	f.pix[f.dumbs] = pix
	return kdrm.Dumb{Handle: f.dumbs, Pitch: uint32(size.X * 4), Pix: pix}, nil
}

func (f *fakeDevice) DestroyDumb(b kdrm.Dumb) error {
	if !f.live[b.Handle] {
		return fmt.Errorf("dumb buffer %d destroyed twice", b.Handle)
	}
	if !f.gone {
		delete(f.live, b.Handle)
	}
	return f.unmap(b)
}

func (f *fakeDevice) unmap(b kdrm.Dumb) error {
	if f.unmapped == nil {
		f.unmapped = map[uint32]bool{}
	}
	if f.unmapped[b.Handle] {
		return fmt.Errorf("dumb buffer %d unmapped twice", b.Handle)
	}
	f.unmapped[b.Handle] = true
	return nil
}

func (f *fakeDevice) AddFB2(fb kdrm.FB) (uint32, error) {
	if fb.Handles[0] >= primeHandles {
		f.imports++
	}
	if f.refuseAddFB {
		return 0, fmt.Errorf("add a framebuffer: %w", unix.EINVAL)
	}
	prime := fb.Handles[0] >= primeHandles && (fb.Format == scanoutFormat || fb.Format == fourcc.NV12)
	if !prime && ((fb.Format != cursorFormat && fb.Format != scanoutFormat) || fb.WithModifier || !f.live[fb.Handles[0]]) {
		return 0, fmt.Errorf("unexpected framebuffer %+v", fb)
	}
	f.fbs++
	if f.scanouts == nil {
		f.scanouts = map[uint32]uint32{}
	}
	f.scanouts[700+f.fbs] = fb.Handles[0]
	return 700 + f.fbs, nil
}

func (f *fakeDevice) RemoveFB(id uint32) error {
	if f.refuseRemoveFB {
		return fmt.Errorf("remove framebuffer %d: %w", id, unix.EBUSY)
	}
	if !f.gone {
		f.removedFBs = append(f.removedFBs, id)
	}
	return nil
}

func (f *fakeDevice) CloseFB(id uint32) error {
	if !f.gone {
		f.closedFBs = append(f.closedFBs, id)
	}
	return nil
}

func (f *fakeDevice) Commit(r *kdrm.Request, flags uint32, userData uint64) error {
	c := commit{flags: flags, userData: userData, values: map[[2]uint32]uint64{}}
	r.Each(func(object, property uint32, value uint64) { c.values[[2]uint32{object, property}] = value })
	f.commits = append(f.commits, c)
	if f.removed {
		return unix.ENODEV
	}
	if f.noMaster {
		return unix.EACCES
	}
	if (f.busyNonblock || f.busyUntilFlip) && flags&kdrm.AtomicNonblock != 0 && flags&kdrm.AtomicAllowModeset != 0 {
		return unix.EBUSY
	}
	if f.refuseOverlay && c.values[[2]uint32{42, propFB}] != 0 {
		return unix.EINVAL
	}
	if f.refuseScaling && scales(c) {
		return unix.EINVAL
	}
	if f.overlayBudget > 0 && overlaysOn(c) > f.overlayBudget {
		return unix.EINVAL
	}
	if f.refusesModeset(c) {
		return unix.EINVAL
	}
	if _, bpc := c.values[[2]uint32{20, propMaxBPCID}]; bpc && f.refuseBPC {
		return unix.EINVAL
	}
	active, setsActive := c.values[[2]uint32{10, propActive}]
	cursorFB, setsCursor := c.values[[2]uint32{43, propFB}]
	if !setsCursor {
		cursorFB = f.cursorPlaneFB
	}
	touchesCursor := slices.ContainsFunc(slices.Collect(maps.Keys(c.values)), func(k [2]uint32) bool { return k[0] == 43 })
	if f.refuseCursor && touchesCursor && cursorFB != 0 || f.refuseGamma && c.values[[2]uint32{10, propGammaLUT}] != 0 || f.refuseAsync && flags&kdrm.AtomicPageFlipAsync != 0 || f.refusePowerOff && setsActive && active == 0 || f.refuseModesets && flags&kdrm.AtomicAllowModeset != 0 || f.refuseFrames != nil && f.refuseFrames(c.values[[2]uint32{40, propFB}]) {
		return unix.EINVAL
	}
	if _, moves := c.values[[2]uint32{43, propCRTCX}]; moves && f.refuseCursorAt != nil {
		_, at, _ := cursorAt(c)
		if err := f.refuseCursorAt(at); err != nil {
			return err
		}
	}
	if flags&kdrm.AtomicTestOnly == 0 && f.failCommits != nil {
		return f.failCommits
	}
	if flags&kdrm.AtomicTestOnly == 0 {
		f.cursorPlaneFB = cursorFB
		if v, ok := c.values[[2]uint32{20, propLinkStatusID}]; ok && v == linkStatusGood {
			f.linkBad = false
		}
		for _, crtc := range []uint32{10, 11} {
			if v, ok := c.values[[2]uint32{crtc, propActive}]; ok {
				if f.active == nil {
					f.active = map[uint32]bool{}
				}
				f.active[crtc] = v == 1
			}
		}
	}
	return nil
}

func overlaysOn(c commit) int {
	n := 0
	for _, plane := range []uint32{42, 44} {
		if c.values[[2]uint32{plane, propFB}] != 0 {
			n++
		}
	}
	return n
}

func scales(c commit) bool {
	for _, plane := range []uint32{40, 41, 42, 44} {
		srcW, ok := c.values[[2]uint32{plane, propSrcW}]
		if ok && (srcW>>16 != c.values[[2]uint32{plane, propCRTCW}] || c.values[[2]uint32{plane, propSrcH}]>>16 != c.values[[2]uint32{plane, propCRTCH}]) {
			return true
		}
	}
	return false
}

func (f *fakeDevice) refusesModeset(c commit) bool {
	stated, lit := 0, 0
	for _, crtc := range []uint32{10, 11} {
		blob, ok := c.values[[2]uint32{crtc, propModeID}]
		if ok {
			stated++
			if m, err := kdrm.ParseMode(f.blobData[uint32(blob)]); err == nil && f.refuseMode != nil && f.refuseMode(m) {
				return true
			}
		}
		if ok || f.active[crtc] {
			lit++
		}
	}
	return f.refuseLone && c.flags&kdrm.AtomicAllowModeset != 0 && stated > 0 && stated < lit
}

func (f *fakeDevice) flip(t *testing.T, crtc uint32, seq uint32) {
	t.Helper()
	f.busyUntilFlip = false
	ev := make([]byte, 32)
	binary.NativeEndian.PutUint32(ev, kdrm.EventFlipComplete)
	binary.NativeEndian.PutUint32(ev[4:], 32)
	binary.NativeEndian.PutUint64(ev[8:], uint64(crtc))
	now := f.now()
	binary.NativeEndian.PutUint32(ev[16:], uint32(now/time.Second))
	binary.NativeEndian.PutUint32(ev[20:], uint32(now%time.Second/time.Microsecond))
	binary.NativeEndian.PutUint32(ev[24:], seq)
	binary.NativeEndian.PutUint32(ev[28:], crtc)
	if _, err := unix.Write(f.write, ev); err != nil {
		t.Fatal(err)
	}
}

type fakeAlloc struct {
	formats     []fourcc.Format
	next        uint32
	freed       int
	soft        *soft.Renderer
	fences      []int
	buffersFor  map[fourcc.Format]int
	fbFormats   map[uint64]fourcc.Format
	fbModifiers map[uint64]fourcc.Modifier
	renders     []fourcc.Modifier
	offered     [][]fourcc.Modifier
}

func (a *fakeAlloc) renderer() render.Renderer { return a.soft }

func (a *fakeAlloc) renderable(fourcc.Format) ([]fourcc.Modifier, error) {
	if a.renders != nil {
		return a.renders, nil
	}
	return []fourcc.Modifier{0, 7, 9}, nil
}

func (a *fakeAlloc) allocate(size image.Point, l layout) (*scanout, error) {
	format, modifiers := l.format, l.modifiers
	if len(modifiers) == 0 || slices.ContainsFunc(modifiers, func(m fourcc.Modifier) bool { return m != 0 && m != 7 }) {
		return nil, fmt.Errorf("allocated with modifiers %v", modifiers)
	}
	a.offered = append(a.offered, slices.Clone(modifiers))
	if limit, limited := a.buffersFor[format]; limited && limit == 0 {
		return nil, fmt.Errorf("the allocator refuses %v", format)
	} else if limited {
		a.buffersFor[format] = limit - 1
	}
	a.formats = append(a.formats, format)
	a.next++
	fb := 900 + a.next
	if a.fbFormats != nil {
		a.fbFormats[uint64(fb)] = format
	}
	if a.fbModifiers != nil {
		a.fbModifiers[uint64(fb)] = modifiers[0]
	}
	return &scanout{target: soft.NewBuffer(size.X, size.Y), fb: fb, modifier: modifiers[0], fence: a.fence, release: func() error { a.freed++; return nil }}, nil
}

func (a *fakeAlloc) fence() (int, error) {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return -1, err
	}
	a.fences = append(a.fences, fd)
	return fd, nil
}
func (a *fakeAlloc) close() error { return nil }

type recorder struct {
	during   func(o backend.Output) error
	idle     bool
	log      []string
	outputs  []backend.Output
	presents []backend.Presentation
}

func (r *recorder) OutputAdded(o backend.Output) error {
	r.outputs = append(r.outputs, o)
	r.log = append(r.log, "added "+o.Name())
	return nil
}

func (r *recorder) OutputRemoved(o backend.Output) error {
	r.log = append(r.log, "removed "+o.Name())
	return nil
}

func (r *recorder) OutputChanged(backend.Output) error { return nil }

func (r *recorder) OutputDamaged(o backend.Output) error {
	r.log = append(r.log, "damaged "+o.Name())
	return o.ScheduleFrame()
}

func (r *recorder) Frame(o backend.Output) error {
	r.log = append(r.log, "frame")
	if r.during != nil {
		if err := r.during(o); err != nil {
			return err
		}
	}
	if r.idle {
		return nil
	}
	f, err := o.BeginFrame()
	if err != nil {
		return err
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rect(0, 0, 16, 16)})
	if err != nil {
		return err
	}
	p.FillRect(image.Rect(0, 0, 16, 16), render.Color{A: 0xff}, render.Mask{})
	return errors.Join(p.End(), o.Present(nil))
}

func (r *recorder) Presented(_ backend.Output, p backend.Presentation) error {
	r.presents = append(r.presents, p)
	r.log = append(r.log, fmt.Sprintf("presented %d", p.Seq))
	return nil
}

func (r *recorder) DeviceAdded(backend.Device) error          { return nil }
func (r *recorder) DeviceRemoved(backend.Device) error        { return nil }
func (r *recorder) Input(backend.Device, backend.Event) error { return nil }

type rig struct {
	t        *testing.T
	l        *loop.Loop
	clock    *loop.Manual
	dev      *fakeDevice
	alloc    *fakeAlloc
	b        *Backend
	rec      *recorder
	reports  []error
	awaited  func() bool
	waits    uint64
	met      bool
	stuck    bool
	overdue  bool
	offLoops []func() bool
}

const rigEpoch = time.Hour

func start(t *testing.T) *rig {
	t.Helper()
	return boot(t, func(*fakeDevice) allocator { return &fakeAlloc{soft: soft.New()} })
}

func boot(t *testing.T, alloc func(*fakeDevice) allocator) *rig {
	t.Helper()
	r := rigged(t, alloc, HoldsMaster)
	if err := r.b.Start(r.rec); err != nil {
		t.Fatal(err)
	}
	r.dev.commits = nil
	return r
}

func rigged(t *testing.T, alloc func(*fakeDevice) allocator, master Master) *rig {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = unix.Close(p[0]), unix.Close(p[1]) })
	l, clock, err := loop.NewManual(rigEpoch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	r := &rig{t: t, l: l, clock: clock, dev: &fakeDevice{events: p[0], write: p[1], connected: true}, rec: &recorder{}}
	l.BeforeWait(r.ask)
	clock.WhenDry(r.dry)
	r.offLoops = []func() bool{func() bool { return r.b.probing && !r.dev.probeGated() }}
	r.dev.now = r.now
	a := alloc(r.dev)
	r.alloc, _ = a.(*fakeAlloc)
	if r.b, err = newBackend(l, device{kernel: r.dev}, a, Options{Report: func(err error) { r.reports = append(r.reports, err) }, Master: master}); err != nil {
		t.Fatal(err)
	}
	r.b.fenceTime = func(int) (time.Duration, bool, error) { return 0, false, nil }
	r.b.now = r.now
	return r
}

func (r *rig) hotplug() error {
	r.t.Helper()
	if err := r.b.Hotplug(); err != nil {
		return err
	}
	r.until("the connector probe", func() bool { return !r.b.probing })
	return nil
}

func (r *rig) settle() {
	r.t.Helper()
	r.elapse(20 * time.Millisecond)
}

func (r *rig) elapse(d time.Duration) {
	r.t.Helper()
	end := r.now() + d
	timer, err := r.l.AddTimer(func() error { return nil })
	if err != nil {
		r.t.Fatal(err)
	}
	defer timer.Remove()
	if err := timer.Arm(d); err != nil {
		r.t.Fatal(err)
	}
	r.until(fmt.Sprintf("%v to pass", d), func() bool { return r.now() >= end })
}

func (r *rig) now() time.Duration {
	return r.clock.Now()
}

func (r *rig) at(now time.Duration) {
	r.t.Helper()
	if now < r.now() {
		r.t.Fatalf("the rig's clock is at %v and does not run back to %v", r.now(), now)
	}
	r.clock.Set(now)
}

func (r *rig) advance(d time.Duration) {
	r.t.Helper()
	r.at(r.now() + d)
}

func (r *rig) ask() error {
	if r.awaited != nil && r.awaited() {
		r.awaited, r.met = nil, true
		r.l.Stop()
	}
	return nil
}

func (r *rig) offLoop() bool {
	return slices.ContainsFunc(r.offLoops, func(running func() bool) bool { return running() })
}

func (r *rig) dry() {
	if r.offLoop() {
		return
	}
	next, armed := r.clock.NextTimer()
	if !armed {
		r.stuck = true
		r.l.Stop()
		return
	}
	r.clock.Set(next)
}

func (r *rig) until(what string, done func() bool) {
	r.t.Helper()
	if err := r.runUntil(what, done); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) runUntil(what string, done func() bool) error {
	r.t.Helper()
	r.awaited, r.met, r.stuck, r.overdue = done, false, false, false
	r.waits++
	wait := r.waits
	watchdog := time.AfterFunc(loopWait, func() {
		_ = r.l.Post(func() error {
			if wait == r.waits && r.awaited != nil {
				r.overdue = true
				r.l.Stop()
			}
			return nil
		})
	})
	err := r.l.Run()
	watchdog.Stop()
	r.awaited = nil
	if err != nil {
		return err
	}
	state := fmt.Sprintf("%d commits, %d presents, reports %v, log %v", len(r.dev.commits), len(r.rec.presents), r.reports, r.rec.log)
	switch {
	case r.met:
		return nil
	case r.stuck:
		r.t.Fatalf("%s never came: at %v the loop has no source ready, no timer armed and nothing running off it: %s", what, r.now()-rigEpoch, state)
	case r.overdue:
		r.t.Fatalf("waited %v for %s, with work off the loop that never returned: %s", loopWait, what, state)
	}
	r.t.Fatalf("the loop stopped before %s: %s", what, state)
	return nil
}

const loopWait = 10 * time.Second

func loopUntil(l *loop.Loop, done func() bool) (bool, error) {
	deadline := time.Now().Add(loopWait)
	met := false
	var check *loop.Timer
	check, err := l.AddTimer(func() error {
		if met = done(); met || time.Now().After(deadline) {
			l.Stop()
			return nil
		}
		return check.Arm(time.Millisecond)
	})
	if err != nil {
		return false, err
	}
	defer check.Remove()
	if err := check.Arm(time.Millisecond); err != nil {
		return false, err
	}
	err = l.Run()
	return met, err
}

func TestTheRigTakesTheAnswerAConditionGaveOnce(t *testing.T) {
	r := start(t)
	var tick *loop.Timer
	tick, err := r.l.AddTimer(func() error { return tick.Arm(time.Millisecond) })
	if err != nil {
		t.Fatal(err)
	}
	defer tick.Remove()
	if err := tick.Arm(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	asked := 0
	r.until("a condition that holds for one instant", func() bool {
		asked++
		return asked == 3
	})
	if asked != 3 {
		t.Fatalf("the condition was asked %d times; a condition on the clock can be true only once", asked)
	}
}

func TestTheRigsClockPassesOnlyAsItsTimersFallDue(t *testing.T) {
	r := start(t)
	began := r.now()
	r.settle()
	if passed := r.now() - began; passed != 20*time.Millisecond {
		t.Fatalf("settling took %v on the rig's clock, want 20ms", passed)
	}
	var fired time.Duration
	timer, err := r.l.AddTimer(func() error {
		fired = r.now()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer timer.Remove()
	if err := timer.Arm(time.Hour); err != nil {
		t.Fatal(err)
	}
	r.until("a timer an hour away", func() bool { return fired != 0 })
	if want := began + 20*time.Millisecond + time.Hour; fired != want || r.now() != want {
		t.Fatalf("a timer an hour away fired %v after the rig started, with its clock at %v, want both at %v", fired-began, r.now()-began, want-began)
	}
}

func TestOutputsPickACRTCAndPrimaryPlane(t *testing.T) {
	r := start(t)
	if len(r.rec.outputs) != 1 {
		t.Fatalf("outputs %v", r.rec.log)
	}
	o := r.b.Outputs()[0]
	if o.Name() != "DP-1" || o.crtc.id != 10 || o.primary.id != 40 || len(o.buffers) != buffersPerOut || !slices.Equal(o.primary.modifiers, []fourcc.Modifier{0, 7}) {
		t.Fatalf("%s on CRTC %d plane %d, %d buffers, modifiers %v", o.Name(), o.crtc.id, o.primary.id, len(o.buffers), o.primary.modifiers)
	}
	if s := o.State(); !s.Enabled() || s.Mode.Size != image.Pt(1920, 1080) || s.Mode.RefreshMHz != 60000 || len(o.Modes()) != 2 {
		t.Fatalf("state %+v, modes %v", s, o.Modes())
	}
}

func TestFramesCommitAndFlip(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	if len(r.dev.commits) != 2 {
		t.Fatalf("%d commits after the first frame", len(r.dev.commits))
	}
	fences := r.alloc.fences
	if len(fences) != 1 || r.dev.commits[0].values[[2]uint32{40, propInFence}] != uint64(fences[0]) || r.dev.commits[1].values[[2]uint32{40, propInFence}] != uint64(fences[0]) {
		t.Fatalf("fences %v, committed %+v", fences, r.dev.commits[1].values)
	}
	if _, err := unix.FcntlInt(uintptr(fences[0]), unix.F_GETFD, 0); err != nil {
		t.Fatalf("the fence fd was closed before its flip could be timed: %v", err)
	}
	test, real := r.dev.commits[0], r.dev.commits[1]
	if test.flags != kdrm.AtomicTestOnly|kdrm.AtomicAllowModeset || real.flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent|kdrm.AtomicAllowModeset || real.userData != 10 {
		t.Fatalf("flags %#x then %#x, user data %d", test.flags, real.flags, real.userData)
	}
	want := map[[2]uint32]uint64{
		{20, propConnCRTC}: 10, {10, propActive}: 1, {10, propModeID}: 601,
		{40, propFB}: 901, {40, propPlaneCRTC}: 10, {40, propSrcW}: 1920 << 16, {40, propSrcH}: 1080 << 16,
		{40, propCRTCW}: 1920, {40, propCRTCH}: 1080, {40, propSrcX}: 0, {40, propSrcY}: 0, {40, propCRTCX}: 0, {40, propCRTCY}: 0,
	}
	for k, v := range want {
		if got, ok := real.values[k]; !ok || got != v {
			t.Errorf("object %d property %d = %d (%v), want %d", k[0], k[1], got, ok, v)
		}
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != 2 {
		t.Fatal("a frame was drawn while the previous flip was pending")
	}
	r.dev.flip(t, 10, 77)
	r.committedSince("the queued frame after the flip", o, 2)
	if len(r.rec.presents) != 1 || r.rec.presents[0].Seq != 77 || r.rec.presents[0].Time == 0 {
		t.Fatalf("presents %+v", r.rec.presents)
	}
	_, err := unix.FcntlInt(uintptr(fences[0]), unix.F_GETFD, 0)
	if reused := slices.Contains(r.alloc.fences[1:], fences[0]); !errors.Is(err, unix.EBADF) && !reused {
		t.Fatalf("the fence fd stayed open after its flip: %v", err)
	}
	if len(r.dev.commits) != 4 {
		t.Fatalf("%d commits after the flip released the queued frame", len(r.dev.commits))
	}
	second := r.dev.commits[3]
	if second.flags&kdrm.AtomicAllowModeset != 0 || second.values[[2]uint32{40, propFB}] != 902 {
		t.Fatalf("the second frame: flags %#x fb %d", second.flags, second.values[[2]uint32{40, propFB}])
	}
	if _, ok := second.values[[2]uint32{10, propModeID}]; ok {
		t.Fatal("the second frame repeated the modeset")
	}
}

func TestFramesThatPresentNothingAreBoundedPerRefresh(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	limit := (int(20*time.Millisecond/o.State().Mode.Interval()) + 1) * backend.EmptyFramesPerRefresh
	frames := 0
	r.rec.idle = true
	r.rec.during = func(o backend.Output) error {
		if frames++; frames > limit {
			return nil
		}
		return o.ScheduleFrame()
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if frames > limit {
		t.Fatalf("more than %d frames that presented nothing in 20 ms", limit)
	}
	if len(r.dev.commits) != 0 {
		t.Fatalf("%d commits for frames that drew nothing", len(r.dev.commits))
	}
}

func TestAnEmptyFrameLetsTheNextRunAndReportsAnIdleRefresh(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	frames := 0
	r.rec.idle = true
	r.rec.during = func(o backend.Output) error {
		frames++
		if frames == 1 {
			return o.ScheduleFrame()
		}
		return nil
	}
	r.rec.log = nil
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("the idle refresh after both frames", func() bool { return len(r.rec.presents) > 0 })
	if len(r.rec.log) < 3 || !slices.Equal(r.rec.log[:3], []string{"frame", "frame", "presented 0"}) {
		t.Fatalf("log %v, want both frames before one idle presentation", r.rec.log)
	}
	if p := r.rec.presents[len(r.rec.presents)-1]; !p.Idle {
		t.Fatalf("the refresh after empty frames was presented as %+v, want idle", p)
	}
}

func TestTenBitFramesNeedAPlaneThatScansThemOut(t *testing.T) {
	deep := backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[0]), Depth: backend.Depth10}
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.Commit(deep); !errors.Is(err, backend.ErrDepthUnsupported) {
		t.Fatalf("10-bit frames on an XRGB8888-only plane: %v", err)
	}

	for _, format := range []fourcc.Format{fourcc.XRGB2101010, fourcc.XBGR2101010} {
		r = boot(t, func(d *fakeDevice) allocator {
			d.deep = []fourcc.Format{format}
			return &fakeAlloc{soft: soft.New()}
		})
		o = r.b.Outputs()[0]
		allocated := len(r.alloc.formats)
		if err := o.Commit(deep); err != nil {
			t.Fatalf("a 10-bit commit on a plane that scans out %v: %v", format, err)
		}
		if got := r.alloc.formats[allocated:]; !slices.Equal(got, []fourcc.Format{format, format}) || o.State() != deep {
			t.Fatalf("a 10-bit commit on a plane that scans out %v allocated %v and left state %+v", format, got, o.State())
		}
	}
	if test := r.dev.commits[len(r.dev.commits)-1]; test.flags&kdrm.AtomicTestOnly == 0 || test.flags&kdrm.AtomicAllowModeset == 0 {
		t.Fatalf("the 10-bit modeset was not tested first: %+v", test)
	}
	r.dev.refuseModesets = true
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[1]), Depth: backend.Depth10}); !errors.Is(err, backend.ErrDepthUnsupported) {
		t.Fatalf("a 10-bit modeset the kernel refuses: %v", err)
	}
}

func bootDeep(t *testing.T, alloc *fakeAlloc) (*rig, *Output, backend.OutputState) {
	t.Helper()
	alloc.soft = soft.New()
	r := boot(t, func(d *fakeDevice) allocator {
		d.deep = deepFormats
		return alloc
	})
	o := r.b.Outputs()[0]
	r.alloc.formats = nil
	return r, o, backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[0]), Depth: backend.Depth10}
}

func TestTenBitFramesMoveToTheNextFormatWhenTheAllocatorRefuses(t *testing.T) {
	r, o, deep := bootDeep(t, &fakeAlloc{buffersFor: map[fourcc.Format]int{fourcc.XRGB2101010: 1}})
	freed := r.alloc.freed
	if err := o.Commit(deep); err != nil {
		t.Fatalf("a plane with a second 10-bit format: %v", err)
	}
	want := []fourcc.Format{fourcc.XRGB2101010, fourcc.XBGR2101010, fourcc.XBGR2101010}
	if !slices.Equal(r.alloc.formats, want) || o.State() != deep {
		t.Fatalf("allocated %v and left state %+v, want %v", r.alloc.formats, o.State(), want)
	}
	if len(o.buffers) != buffersPerOut || r.alloc.freed != freed+buffersPerOut+1 {
		t.Fatalf("%d buffers held and %d freed after the refused format", len(o.buffers), r.alloc.freed-freed)
	}
	if len(r.reports) != 1 || !strings.Contains(r.reports[0].Error(), "the allocator refuses "+fourcc.XRGB2101010.String()) {
		t.Fatalf("reports %v, want the refused format named once", r.reports)
	}
}

func TestTenBitFramesMoveToTheNextFormatWhenTheKernelRefuses(t *testing.T) {
	alloc := &fakeAlloc{fbFormats: map[uint64]fourcc.Format{}}
	r, o, deep := bootDeep(t, alloc)
	r.dev.refuseFrames = func(fb uint64) bool { return alloc.fbFormats[fb] == fourcc.XRGB2101010 }
	before := len(r.dev.commits)
	if err := o.Commit(deep); err != nil {
		t.Fatalf("a plane with a second 10-bit format: %v", err)
	}
	want := []fourcc.Format{fourcc.XRGB2101010, fourcc.XRGB2101010, fourcc.XRGB2101010, fourcc.XRGB2101010, fourcc.XBGR2101010, fourcc.XBGR2101010}
	if !slices.Equal(r.alloc.formats, want) || o.State() != deep {
		t.Fatalf("allocated %v and left state %+v, want %v", r.alloc.formats, o.State(), want)
	}
	tests := 0
	for _, c := range r.dev.commits[before:] {
		if c.flags&kdrm.AtomicTestOnly != 0 && c.flags&kdrm.AtomicAllowModeset != 0 {
			tests++
		}
	}
	if tests != 3 {
		t.Fatalf("%d modesets tested, want one per modifier of the refused format and one for the next format", tests)
	}
	if len(r.reports) != 1 {
		t.Fatalf("reports %v, want the refused format named once", r.reports)
	}
}

func TestTenBitFramesRefusedEverywhereKeepTheFramesOnScreen(t *testing.T) {
	for _, tc := range []struct {
		name     string
		alloc    *fakeAlloc
		refusal  string
		byKernel bool
	}{
		{name: "by the allocator", alloc: &fakeAlloc{buffersFor: map[fourcc.Format]int{fourcc.XRGB2101010: 0, fourcc.XBGR2101010: 0}}, refusal: "the allocator refuses "},
		{name: "by the kernel", alloc: &fakeAlloc{fbFormats: map[uint64]fourcc.Format{}}, refusal: "", byKernel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, o, deep := bootDeep(t, tc.alloc)
			if tc.byKernel {
				r.dev.refuseFrames = func(fb uint64) bool { return slices.Contains(deepFormats, tc.alloc.fbFormats[fb]) }
			}
			depth8, buffers, modeBlob := o.State(), slices.Clone(o.buffers), o.modeBlob
			blobs, destroyed := r.dev.blobs, len(r.dev.destroyed)
			err := o.Commit(deep)
			if !errors.Is(err, backend.ErrDepthUnsupported) {
				t.Fatalf("no 10-bit format works: %v", err)
			}
			for _, format := range deepFormats {
				if !strings.Contains(err.Error(), tc.refusal+format.String()) {
					t.Fatalf("the error does not name %v: %v", format, err)
				}
			}
			if r.dev.blobs != blobs+1 || len(r.dev.destroyed) != destroyed+1 || o.modeBlob != modeBlob {
				t.Fatalf("the refused mode's blob: %d created, %d destroyed, current %d was %d", r.dev.blobs-blobs, len(r.dev.destroyed)-destroyed, o.modeBlob, modeBlob)
			}
			if o.State() != depth8 || !slices.Equal(o.buffers, buffers) {
				t.Fatalf("state %+v and buffers %v after the refusal, want the 8-bit frames untouched", o.State(), o.buffers)
			}
			allocated := len(r.alloc.formats)
			if err := o.Commit(depth8); err != nil {
				t.Fatalf("staying at 8 bits: %v", err)
			}
			if len(r.alloc.formats) != allocated {
				t.Fatalf("staying at 8 bits allocated %v", r.alloc.formats[allocated:])
			}
		})
	}
}

func TestEightBitFramesTryOnlyTheirOneFormat(t *testing.T) {
	r, o, _ := bootDeep(t, &fakeAlloc{buffersFor: map[fourcc.Format]int{scanoutFormat: 2}})
	state := o.State()
	err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[1])})
	if err == nil || errors.Is(err, backend.ErrDepthUnsupported) {
		t.Fatalf("no 8-bit buffers: %v", err)
	}
	if len(r.alloc.formats) != 0 || o.State() != state || len(o.buffers) != buffersPerOut {
		t.Fatalf("allocated %v and left state %+v with %d buffers after the 8-bit refusal", r.alloc.formats, o.State(), len(o.buffers))
	}
}

func TestModeChangesAndDisabling(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: modeOf(modes[1])}); err != nil {
		t.Fatal(err)
	}
	r.until("the mode change", func() bool {
		n := len(r.dev.commits)
		return n > 0 && r.dev.commits[n-1].flags&kdrm.AtomicAllowModeset != 0 && r.dev.commits[n-1].values[[2]uint32{40, propCRTCW}] == 1280
	})
	last := r.dev.commits[len(r.dev.commits)-1]
	if last.flags&kdrm.AtomicAllowModeset == 0 || last.values[[2]uint32{40, propCRTCW}] != 1280 || !slices.Contains(r.dev.destroyed, 601) {
		t.Fatalf("mode change commit %+v, destroyed blobs %v", last, r.dev.destroyed)
	}
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: backend.Mode{Size: image.Pt(1, 1), RefreshMHz: 1}}); err == nil {
		t.Fatal("a mode the connector lacks was accepted")
	}
	if err := o.Commit(backend.OutputState{}); err != nil {
		t.Fatal(err)
	}
	off := r.dev.commits[len(r.dev.commits)-1]
	if off.flags != kdrm.AtomicAllowModeset|kdrm.AtomicNonblock || off.values[[2]uint32{10, propActive}] != 0 || off.values[[2]uint32{40, propFB}] != 0 || o.State().Enabled() || len(o.buffers) != 0 {
		t.Fatalf("disable commit %+v, state %+v", off, o.State())
	}
}

func TestAConnectorThatVanishesIsAnUnplug(t *testing.T) {
	r := start(t)
	r.dev.unplugged = true
	if err := r.hotplug(); err != nil {
		t.Fatalf("an MST connector going away: %v", err)
	}
	if !slices.Equal(r.rec.log, []string{"added DP-1", "removed DP-1"}) {
		t.Fatalf("log %v", r.rec.log)
	}
}

func TestAnUnreadableEDIDStillLightsTheOutput(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.badEDID = true
		return &fakeAlloc{soft: soft.New()}
	})
	if len(r.b.Outputs()) != 1 || r.b.Outputs()[0].Description() != "DP-1" {
		t.Fatalf("outputs %v", r.rec.log)
	}
	if len(r.reports) != 1 || !strings.Contains(r.reports[0].Error(), "EDID") {
		t.Fatalf("reports %v, want the unreadable EDID", r.reports)
	}
}

func TestHotplugAndSessionPause(t *testing.T) {
	r := start(t)
	r.dev.connected = false
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	r.dev.connected = true
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.rec.log, []string{"added DP-1", "removed DP-1", "added DP-1"}) {
		t.Fatalf("log %v", r.rec.log)
	}
	o := r.b.Outputs()[0]
	r.dev.commits = nil
	r.b.Pause()
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != 0 {
		t.Fatalf("%d commits while paused", len(r.dev.commits))
	}
	if err := r.b.Resume(); err != nil {
		t.Fatal(err)
	}
	r.until("the modeset and the held frame after resume", func() bool { return len(r.dev.commits) >= 2 })
	if len(r.dev.commits) != 2 || r.dev.commits[1].flags&kdrm.AtomicAllowModeset == 0 {
		t.Fatalf("after resume: %+v", r.dev.commits)
	}
}

func TestPowerAndWake(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	r.dev.flip(t, 10, 1)
	r.until("the first frame's flip", func() bool { return !o.flipping })
	state := o.State()
	state.Power = backend.PoweredOff
	if err := o.Commit(state); err != nil {
		t.Fatal(err)
	}
	off := r.dev.commits[len(r.dev.commits)-1]
	if off.flags != kdrm.AtomicAllowModeset|kdrm.AtomicNonblock || off.values[[2]uint32{10, propActive}] != 0 || o.State().Power != backend.PoweredOff || len(o.buffers) != 0 || r.alloc.freed != buffersPerOut {
		t.Fatalf("power off commit %+v, state %+v, %d buffers kept, %d freed", off, o.State(), len(o.buffers), r.alloc.freed)
	}
	before := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != before {
		t.Fatal("an output that is off drew a frame")
	}
	state.Power = backend.PoweredOn
	if err := o.Commit(state); err != nil {
		t.Fatal(err)
	}
	r.until("the power-on modeset", func() bool {
		return r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{10, propActive}] == 1
	})
	on := r.dev.commits[len(r.dev.commits)-1]
	if on.flags&kdrm.AtomicAllowModeset == 0 || on.values[[2]uint32{10, propActive}] != 1 || o.State().Power == backend.PoweredOff || len(o.buffers) != buffersPerOut {
		t.Fatalf("power on commit %+v with %d buffers", on, len(o.buffers))
	}
	r.dev.flip(t, 10, 5)
	r.until("the flip", func() bool { return !o.flipping })
	woken := len(r.dev.commits)
	if err := r.b.Wake(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the modeset on wake", o, woken)
	woke := r.dev.commits[len(r.dev.commits)-1]
	if woke.flags&kdrm.AtomicAllowModeset == 0 || woke.values[[2]uint32{10, propModeID}] == 0 {
		t.Fatalf("waking did not recommit the mode: %+v", woke)
	}
}

func (r *rig) lit() *Output {
	r.t.Helper()
	o := r.flipFirstFrame()
	r.elapse(o.planesWait())
	return o
}

func (r *rig) refreshStart(o *Output) (vblank time.Duration) {
	r.t.Helper()
	interval := modeOf(o.mode).Interval()
	last := o.lastFlip
	if r.now() > last+time.Millisecond {
		last, _ = backend.NextVblank(last, r.now(), interval)
	}
	r.at(last + time.Millisecond)
	return last + interval
}

func (r *rig) flipFirstFrame() *Output {
	r.t.Helper()
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	r.dev.flip(r.t, 10, 1)
	r.until("the first frame's flip", func() bool { return !o.flipping })
	r.dev.commits = nil
	return o
}

func (r *rig) committedSince(what string, o *Output, n int) {
	r.t.Helper()
	r.until(what, func() bool { return len(r.dev.commits) > n && o.flipping })
}

func (r *rig) setCursor(o *Output, hotspot image.Point) error {
	r.t.Helper()
	cp, err := o.CursorPlane()
	if err != nil {
		r.t.Fatal(err)
	}
	f, err := cp.BeginCursor()
	if err != nil {
		return err
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rectangle{Max: cp.CursorSize()}})
	if err != nil {
		r.t.Fatal(err)
	}
	p.Clear(render.Color{})
	p.FillRect(image.Rect(0, 0, 1, 1), render.Color{R: 0xff, A: 0xff}, render.Mask{})
	if err := p.End(); err != nil {
		r.t.Fatal(err)
	}
	return cp.SetCursor(hotspot)
}

func cursorAt(c commit) (uint64, image.Point, bool) {
	fb, ok := c.values[[2]uint32{43, propFB}]
	return fb, image.Pt(int(int64(c.values[[2]uint32{43, propCRTCX}])), int(int64(c.values[[2]uint32{43, propCRTCY}]))), ok
}

func TestCursorPlaneMovesWithoutFrames(t *testing.T) {
	r := start(t)
	o := r.lit()
	if err := r.setCursor(o, image.Pt(4, 6)); err != nil {
		t.Fatal(err)
	}
	r.until("the cursor's first commit", func() bool { return o.flipping })
	if len(r.dev.commits) != 2 || r.dev.commits[0].flags != kdrm.AtomicTestOnly || r.dev.commits[1].flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent {
		t.Fatalf("setting the cursor committed %+v", r.dev.commits)
	}
	shown := r.dev.commits[1]
	if _, ok := shown.values[[2]uint32{40, propFB}]; ok {
		t.Fatal("a cursor update touched the primary plane")
	}
	if fb, at, _ := cursorAt(shown); fb == 0 || at != image.Pt(-4, -6) || shown.values[[2]uint32{43, propCRTCW}] != 64 || shown.values[[2]uint32{43, propPlaneCRTC}] != 10 {
		t.Fatalf("cursor fb %d at %v in %+v", fb, at, shown.values)
	}
	if shown.values[[2]uint32{43, propHotspotX}] != 4 || shown.values[[2]uint32{43, propHotspotY}] != 6 {
		t.Fatalf("the cursor plane got hotspot %d,%d, not 4,6", shown.values[[2]uint32{43, propHotspotX}], shown.values[[2]uint32{43, propHotspotY}])
	}
	cp, _ := o.CursorPlane()
	for _, at := range []image.Point{image.Pt(100, 50), image.Pt(120, 70)} {
		if err := cp.MoveCursor(at); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.dev.commits) != 2 {
		t.Fatal("the cursor moved while its last update was still flipping")
	}
	r.dev.flip(t, 10, 2)
	r.until("the moved cursor's commit after the flip", func() bool { return len(r.dev.commits) >= 3 })
	if len(r.dev.commits) != 3 || len(r.rec.presents) != 1 {
		t.Fatalf("%d commits and %d presents after the flip", len(r.dev.commits), len(r.rec.presents))
	}
	if _, at, _ := cursorAt(r.dev.commits[2]); at != image.Pt(116, 64) {
		t.Fatalf("the cursor landed at %v", at)
	}
	r.dev.flip(t, 10, 3)
	r.until("the moved cursor's flip", func() bool { return !o.flipping })
	sent := len(r.dev.commits)
	if err := cp.HideCursor(); err != nil {
		t.Fatal(err)
	}
	r.until("the hidden cursor's commit", func() bool { return len(r.dev.commits) > sent && o.flipping })
	if fb, _, ok := cursorAt(r.dev.commits[len(r.dev.commits)-1]); !ok || fb != 0 {
		t.Fatal("hiding the cursor did not clear its plane")
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	sent = len(r.dev.commits)
	r.dev.flip(t, 10, 4)
	r.until("the frame after the hidden cursor's flip", func() bool { return len(r.dev.commits) > sent && o.flipping })
	if fb, _, ok := cursorAt(r.dev.commits[len(r.dev.commits)-1]); ok && fb != 0 {
		t.Fatal("a frame brought a hidden cursor back")
	}
}

func TestFramesCarryTheCursor(t *testing.T) {
	r := start(t)
	o := r.lit()
	fences := len(r.alloc.fences)
	if err := r.setCursor(o, image.Point{}); err != nil {
		t.Fatal(err)
	}
	cp, _ := o.CursorPlane()
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	if err := cp.MoveCursor(image.Pt(30, 40)); err != nil {
		t.Fatal(err)
	}
	n := len(r.dev.commits)
	r.dev.flip(t, 10, 2)
	r.committedSince("the frame after the flip", o, n)
	last := r.dev.commits[len(r.dev.commits)-1]
	if _, at, _ := cursorAt(last); at != image.Pt(30, 40) || last.values[[2]uint32{40, propFB}] == 0 {
		t.Fatalf("the frame after the move: %+v", last.values)
	}
	if n := len(r.alloc.fences) - fences; n != 1 {
		t.Fatalf("%d fences for one frame", n)
	}
	if pix := o.cursor.pending.dumb.Pix; [4]byte(pix[:4]) != [4]byte{0, 0, 0xff, 0xff} || pix[4] != 0 {
		t.Fatalf("the cursor buffer starts %v", pix[:8])
	}
}

func TestCursorChangesDuringAFrameJoinIt(t *testing.T) {
	r := start(t)
	o := r.lit()
	if err := r.setCursor(o, image.Point{}); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the cursor", o, 0)
	r.dev.flip(t, 10, 2)
	r.until("the cursor's flip", func() bool { return !o.flipping })
	r.dev.commits = nil
	cp, _ := o.CursorPlane()
	r.rec.during = func(backend.Output) error { return cp.MoveCursor(image.Pt(9, 9)) }
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the frame", o, 0)
	if len(r.dev.commits) != 2 || r.dev.commits[1].values[[2]uint32{40, propFB}] == 0 {
		t.Fatalf("a move inside a frame committed %+v", r.dev.commits)
	}
	if _, at, _ := cursorAt(r.dev.commits[1]); at != image.Pt(9, 9) {
		t.Fatalf("the frame put the cursor at %v", at)
	}
}

func TestACursorMoveWaitsForTheLatchSoAFrameCarriesIt(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	half := modeOf(o.mode).Interval() / 2
	r.advance(2*half - backend.PlaneLead)
	if err := r.setCursor(o, image.Point{}); err != nil {
		t.Fatal(err)
	}
	if !o.flipping {
		t.Fatalf("a cursor set at the latch did not commit: %+v", r.dev.commits)
	}
	r.dev.flip(t, 10, 2)
	r.until("the cursor flip", func() bool { return !o.flipping })
	r.dev.commits = nil
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(5, 5)); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 0 {
		t.Fatalf("a cursor move right after a flip committed at once: %+v", r.dev.commits)
	}
	drawn := r.frames()
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.elapse(half - time.Millisecond)
	if len(r.dev.commits) != 0 || r.frames() != drawn {
		t.Fatalf("a frame requested before the latch drew %d frames and committed: %+v", r.frames()-drawn, r.dev.commits)
	}
	r.at(o.lastFlip + 2*half - backend.PlaneLead)
	r.until("the frame after the latch", func() bool { return o.flipping && o.late == nil })
	var flips []commit
	for _, c := range r.dev.commits {
		if c.flags&kdrm.AtomicTestOnly == 0 {
			flips = append(flips, c)
		}
	}
	if len(flips) != 1 || flips[0].values[[2]uint32{40, propFB}] == 0 {
		t.Fatalf("a frame requested before the latch committed %d flips, want one frame carrying the cursor: %+v", len(flips), flips)
	}
	if _, at, _ := cursorAt(flips[0]); at != image.Pt(5, 5) {
		t.Fatalf("the frame put the cursor at %v", at)
	}
}

func TestARefusedCursorFallsBack(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.dev.refuseCursor = true
	if err := r.setCursor(o, image.Point{}); err == nil {
		t.Fatal("a refused cursor image was accepted")
	}
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(1, 1)); err == nil {
		t.Fatal("a refused cursor plane moved")
	}
	if _, err := cp.BeginCursor(); err == nil {
		t.Fatal("a refused cursor plane took another image")
	}
}

func TestAFrameDropsACursorItsTestRefuses(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := r.setCursor(o, image.Point{}); err != nil {
		t.Fatalf("an untested cursor before the first frame: %v", err)
	}
	r.dev.refuseCursor = true
	before := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, before)
	last := r.dev.commits[len(r.dev.commits)-1]
	if fb, _, _ := cursorAt(last); fb != 0 || last.values[[2]uint32{40, propFB}] == 0 || last.flags&kdrm.AtomicTestOnly != 0 {
		t.Fatalf("the first frame: %+v", last)
	}
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(1, 1)); err == nil {
		t.Fatal("the cursor plane stayed in use after the frame dropped it")
	}
}

func TestCursorPlanesBelongToOneCRTC(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if o.cursor == nil || o.cursor.plane.id != 43 || r.b.cursor != image.Pt(64, 64) {
		t.Fatalf("cursor %+v, size %v", o.cursor, r.b.cursor)
	}
	if err := r.setCursor(o, image.Point{}); err != nil || len(r.dev.live) != cursorBuffers {
		t.Fatalf("%d dumb buffers for the cursor: %v", len(r.dev.live), err)
	}
	r.dev.connected = false
	if err := r.hotplug(); err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(r.b.planes, func(p *plane) bool { return p.id == 43 }); r.b.planes[i].taken {
		t.Fatal("the cursor plane stayed taken after its output left")
	}
	if len(r.dev.live) != 0 {
		t.Fatalf("dumb buffers %v outlived their output", r.dev.live)
	}
}

func TestFramebuffersCarryModifiersOnlyWhereTheDeviceTakesThem(t *testing.T) {
	const tiled = 0x0100000000000002
	size := image.Pt(64, 32)
	cases := []struct {
		name          string
		modifier      fourcc.Modifier
		withModifiers bool
		want          kdrm.FB
		fails         bool
	}{
		{"explicit modifier on a modifier device", tiled, true, kdrm.FB{Width: 64, Height: 32, Format: scanoutFormat, Modifier: tiled, WithModifier: true}, false},
		{"implicit layout on a modifier device", fourcc.Invalid, true, kdrm.FB{Width: 64, Height: 32, Format: scanoutFormat}, false},
		{"linear on a device without modifiers", fourcc.Linear, false, kdrm.FB{Width: 64, Height: 32, Format: scanoutFormat}, false},
		{"implicit layout on a device without modifiers", fourcc.Invalid, false, kdrm.FB{Width: 64, Height: 32, Format: scanoutFormat}, false},
		{"tiled on a device without modifiers", tiled, false, kdrm.FB{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fb, err := framebuffer(size, scanoutFormat, tc.modifier, tc.withModifiers)
			if (err != nil) != tc.fails || fb != tc.want {
				t.Fatalf("framebuffer = %+v, %v", fb, err)
			}
		})
	}
}

func TestSecondaryDevicesShowFramesThePrimaryRendered(t *testing.T) {
	primary := soft.New()
	r := boot(t, func(dev *fakeDevice) allocator { return &copyAllocator{dev: device{kernel: dev}, primary: primary} })
	o := r.rec.outputs[0]
	f, err := o.BeginFrame()
	if err != nil {
		t.Fatal(err)
	}
	if f.Renderer != render.Renderer(primary) {
		t.Fatalf("a secondary device's frame renders with %T, not the primary renderer", f.Renderer)
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rect(0, 0, 4, 4)})
	if err != nil {
		t.Fatal(err)
	}
	p.FillRect(image.Rect(0, 0, 4, 4), render.Color{R: 0xff, A: 0xff}, render.Mask{})
	if err := errors.Join(p.End(), o.Present(nil)); err != nil {
		t.Fatal(err)
	}
	fb := r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{40, propFB}]
	pix := r.dev.pix[r.dev.scanouts[uint32(fb)]]
	if pix == nil || pix[2] != 0xff || pix[1] != 0 || pix[0] != 0 {
		t.Fatalf("the scanout buffer behind framebuffer %d starts with %v, not the red the primary drew", fb, pix[:4])
	}
}

func TestUnpluggedDevicesDropTheirOutputsWithoutTouchingTheDevice(t *testing.T) {
	r := boot(t, func(dev *fakeDevice) allocator { return &copyAllocator{dev: device{kernel: dev}, primary: soft.New()} })
	r.settle()
	commits, dumbs, fbs := len(r.dev.commits), len(r.dev.live), len(r.dev.removedFBs)
	r.dev.gone = true
	if err := r.b.Unplug(); err != nil {
		t.Fatal(err)
	}
	if r.rec.log[len(r.rec.log)-1] != "removed "+r.rec.outputs[0].Name() {
		t.Fatalf("log %v: unplugging did not remove the output", r.rec.log)
	}
	if len(r.dev.commits) != commits || len(r.dev.live) != dumbs || len(r.dev.removedFBs) != fbs {
		t.Fatalf("unplugging touched the gone device: %d commits, %d dumb buffers, %d framebuffers removed", len(r.dev.commits)-commits, dumbs-len(r.dev.live), len(r.dev.removedFBs)-fbs)
	}
	for handle := range r.dev.live {
		if !r.dev.unmapped[handle] {
			t.Errorf("dumb buffer %d of the gone device is still mapped", handle)
		}
	}
	if err := r.b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOutputsOfferThePrimaryPlaneFormatsForScanout(t *testing.T) {
	r := start(t)
	got := r.lit().ScanoutFormats()
	want := gpu.ScanoutTarget{Device: fakeNumber, Formats: []gpu.FormatModifier{{Format: scanoutFormat, Modifier: 0}, {Format: scanoutFormat, Modifier: 7}}}
	if got.Device != want.Device || !slices.Equal(got.Formats, want.Formats) {
		t.Fatalf("scanout formats %+v, want %+v", got, want)
	}
}

type fakeBuffer struct{ d gpu.DMABUF }

func (b *fakeBuffer) DMABUF() gpu.DMABUF { return b.d }

func TestClientBuffersScanOutUntilTheNextFlip(t *testing.T) {
	r := start(t)
	o := r.lit()
	released := 0
	client := gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: 7, Stride: 1920 * 4}}}
	key := &fakeBuffer{client}
	ok, err := o.Scanout(key, client, backend.Whole(client.Size), func() error { released++; return nil }, false)
	if err != nil || !ok {
		t.Fatalf("Scanout = %t, %v", ok, err)
	}
	shown := r.dev.commits[len(r.dev.commits)-1]
	fb := shown.values[[2]uint32{40, propFB}]
	if shown.flags&kdrm.AtomicTestOnly != 0 || r.dev.scanouts[uint32(fb)] != primeHandles+7 {
		t.Fatalf("the client buffer went out as framebuffer %d with flags %#x", fb, shown.flags)
	}
	if again, err := o.Scanout(key, client, backend.Whole(client.Size), func() error { return nil }, false); again || err != nil {
		t.Fatalf("a second scanout during the flip = %t, %v", again, err)
	}
	r.dev.flip(t, 10, 2)
	r.until("the client buffer's flip", func() bool { return !o.flipping })
	if released != 0 {
		t.Fatal("the client buffer was released while on screen")
	}
	n := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the frame replacing the client buffer", o, n)
	r.dev.flip(t, 10, 3)
	r.until("the frame's flip", func() bool { return !o.flipping })
	if released != 1 {
		t.Fatalf("the client buffer was released %d times after a frame replaced it", released)
	}
	small := gpu.DMABUF{Size: image.Pt(640, 480), Format: scanoutFormat}
	if ok, err := o.Scanout(&fakeBuffer{small}, small, backend.Whole(modeOf(o.mode).Size), func() error { return nil }, false); ok || err != nil {
		t.Fatalf("a placement reading past the buffer scanned out: %t, %v", ok, err)
	}
}

func TestAClientBufferKeepsOneFramebufferUntilItIsForgotten(t *testing.T) {
	r := start(t)
	o := r.lit()
	client := gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: 7, Stride: 1920 * 4}}}
	key := &fakeBuffer{client}
	var fbs []uint64
	for seq := uint32(2); seq < 5; seq++ {
		if ok, err := o.Scanout(key, client, backend.Whole(client.Size), func() error { return nil }, false); err != nil || !ok {
			t.Fatalf("Scanout = %t, %v", ok, err)
		}
		fbs = append(fbs, r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{40, propFB}])
		r.dev.flip(t, 10, seq)
		r.until("the client buffer's flip", func() bool { return !o.flipping })
	}
	if fbs[0] == 0 || fbs[1] != fbs[0] || fbs[2] != fbs[0] {
		t.Fatalf("three scanouts of one buffer used framebuffers %v, want one framebuffer reused", fbs)
	}
	removed := len(r.dev.removedFBs)
	if err := o.ForgetScanout(key); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.removedFBs) != removed {
		t.Fatal("a framebuffer still on screen was removed when its buffer was forgotten")
	}
	n := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the frame replacing the forgotten buffer", o, n)
	r.dev.flip(t, 10, 5)
	r.until("the frame's flip", func() bool { return !o.flipping })
	if len(r.dev.removedFBs) != removed+1 {
		t.Fatalf("%d framebuffers removed once the forgotten buffer left the screen, want 1", len(r.dev.removedFBs)-removed)
	}
}

func TestClientBuffersAreReleasedWhenTheOutputStopsShowingThem(t *testing.T) {
	for name, stop := range map[string]func(r *rig, o *Output) error{
		"disabled": func(_ *rig, o *Output) error { return o.Commit(backend.OutputState{}) },
		"powered off": func(r *rig, o *Output) error {
			st := o.State()
			st.Power = backend.PoweredOff
			if err := o.Commit(st); err != nil {
				return err
			}
			r.dev.flip(r.t, 10, 2)
			r.until("the power off after the flip", func() bool { return !o.offDue })
			return nil
		},
		"paused and resumed": func(r *rig, _ *Output) error {
			r.b.Pause()
			return r.b.Resume()
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := start(t)
			o := r.lit()
			released := 0
			client := gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: 7, Stride: 1920 * 4}}}
			key := &fakeBuffer{client}
			if ok, err := o.Scanout(key, client, backend.Whole(client.Size), func() error { released++; return nil }, false); err != nil || !ok {
				t.Fatalf("Scanout = %t, %v", ok, err)
			}
			fb := r.b.clientFBs[key].fb
			removals := func() int {
				n := 0
				for _, id := range r.dev.removedFBs {
					if id == fb {
						n++
					}
				}
				return n
			}
			if err := stop(r, o); err != nil {
				t.Fatal(err)
			}
			if released != 1 || removals() != 0 {
				t.Fatalf("released %d times and framebuffer %d removed %d times; want the client buffer let go once and its framebuffer kept for the next scanout", released, fb, removals())
			}
			if err := o.ForgetScanout(key); err != nil || removals() != 1 {
				t.Fatalf("forgetting the buffer removed its framebuffer %d times, %v", removals(), err)
			}
		})
	}
}

func TestAFailedPowerOffLeavesTheOutputOn(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.dev.refusePowerOff = true
	st := o.State()
	st.Power = backend.PoweredOff
	if err := o.Commit(st); err == nil {
		t.Fatal("a refused power off succeeded")
	}
	if o.State().Power == backend.PoweredOff {
		t.Fatal("a refused power off left the output marked off")
	}
}

func gammaOf(c commit) (uint64, bool) {
	v, ok := c.values[[2]uint32{10, propGammaLUT}]
	return v, ok
}

func TestGammaLUTsReachTheCRTC(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 0)
	if lut, ok := gammaOf(r.dev.commits[1]); !ok || lut != 0 {
		t.Fatal("the first modeset kept whatever LUT the CRTC had")
	}
	r.dev.flip(t, 10, 1)
	r.until("the first frame's flip", func() bool { return !o.flipping })
	r.dev.commits = nil
	if n := o.GammaSize(); n != 4 {
		t.Fatalf("gamma size %d, want the CRTC's GAMMA_LUT_SIZE 4", n)
	}

	if err := o.SetGamma([]uint16{0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23}); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 2 || r.dev.commits[0].flags != kdrm.AtomicTestOnly || r.dev.commits[1].flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent {
		t.Fatalf("a LUT on an idle output committed %+v, want a test and a commit of its own", r.dev.commits)
	}
	first, ok := gammaOf(r.dev.commits[1])
	if _, primary := r.dev.commits[1].values[[2]uint32{40, propFB}]; !ok || first == 0 || primary {
		t.Fatalf("the gamma commit set GAMMA_LUT %d (%v) and touched the primary plane: %v", first, ok, primary)
	}
	want := []uint16{0, 10, 20, 0, 1, 11, 21, 0, 2, 12, 22, 0, 3, 13, 23, 0}
	got := make([]uint16, len(r.dev.blobData[uint32(first)])/2)
	for i := range got {
		got[i] = binary.NativeEndian.Uint16(r.dev.blobData[uint32(first)][2*i:])
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the LUT blob holds %v, want drm_color_lut entries %v", got, want)
	}

	if err := o.SetGamma([]uint16{3, 2, 1, 0, 3, 2, 1, 0, 3, 2, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 3 {
		t.Fatal("a LUT was committed while the previous commit was flipping")
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	n := len(r.dev.commits)
	r.dev.flip(t, 10, 2)
	r.committedSince("the frame after the flip", o, n)
	frame := r.dev.commits[len(r.dev.commits)-1]
	second, ok := gammaOf(frame)
	if _, primary := frame.values[[2]uint32{40, propFB}]; !primary || !ok || second == 0 || second == first {
		t.Fatalf("the next frame carried GAMMA_LUT %d (%v), want the new LUT with the frame", second, ok)
	}
	if !slices.Contains(r.dev.destroyed, uint32(first)) {
		t.Fatal("the replaced LUT blob was not destroyed")
	}

	r.dev.flip(t, 10, 3)
	r.until("the frame's flip", func() bool { return !o.flipping })
	if err := o.SetGamma(nil); err != nil {
		t.Fatal(err)
	}
	if lut, ok := gammaOf(r.dev.commits[len(r.dev.commits)-1]); !ok || lut != 0 {
		t.Fatalf("resetting committed GAMMA_LUT %d (%v), want the identity 0", lut, ok)
	}

	r.dev.flip(t, 10, 4)
	r.until("the identity LUT's flip", func() bool { return !o.flipping })
	r.dev.refuseGamma = true
	before := len(r.dev.commits)
	if err := o.SetGamma([]uint16{0, 1, 2, 3, 0, 1, 2, 3, 0, 1, 2, 3}); err == nil {
		t.Fatal("a LUT the driver refuses was accepted")
	}
	if len(r.dev.commits) != before+1 || !slices.Contains(r.dev.destroyed, 600+r.dev.blobs) {
		t.Fatal("a refused LUT was committed or its blob kept")
	}
	if err := o.SetGamma([]uint16{1, 2, 3}); err == nil {
		t.Fatal("ramps of the wrong size were accepted")
	}
}

func ctmOf(c commit) (uint64, bool) {
	v, ok := c.values[[2]uint32{10, propCTM}]
	return v, ok
}

func TestColorTransformsReachTheCRTC(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the first frame", o, 1)
	if m, ok := ctmOf(r.dev.commits[1]); !ok || m != 0 {
		t.Fatal("the first modeset kept whatever CTM the CRTC had")
	}
	r.dev.flip(t, 10, 1)
	r.until("the flip", func() bool { return !o.flipping })
	r.dev.commits = nil
	if !o.HasColorTransform() {
		t.Fatal("a CRTC with a CTM property reports no colour transform")
	}

	if err := o.SetColorTransform(backend.Matrix{1, 0, 0, 0, 0.5, 0, 0, -0.25, 0.75}); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 2 || r.dev.commits[1].flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent {
		t.Fatalf("a CTM on an idle output committed %+v, want a test and a commit of its own", r.dev.commits)
	}
	blob, ok := ctmOf(r.dev.commits[1])
	if !ok || blob == 0 {
		t.Fatalf("the commit set CTM %d (%v)", blob, ok)
	}
	want := []uint64{1 << 32, 0, 0, 0, 1 << 31, 0, 0, 1<<63 | 1<<30, 3 << 30}
	data := r.dev.blobData[uint32(blob)]
	got := make([]uint64, len(data)/8)
	for i := range got {
		got[i] = binary.NativeEndian.Uint64(data[8*i:])
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the CTM blob holds %#x, want S31.32 sign-magnitude %#x", got, want)
	}

	r.dev.flip(t, 10, 2)
	r.until("the flip", func() bool { return !o.flipping })
	if err := o.SetColorTransform(backend.Identity); err != nil {
		t.Fatal(err)
	}
	if m, ok := ctmOf(r.dev.commits[len(r.dev.commits)-1]); !ok || m != 0 {
		t.Fatalf("the identity committed CTM %d (%v), want no blob", m, ok)
	}
	r.dev.flip(t, 10, 3)
	r.until("the flip", func() bool { return !o.flipping })
	if !slices.Contains(r.dev.destroyed, uint32(blob)) {
		t.Fatal("the replaced CTM blob was not destroyed")
	}
}

func TestACRTCWithoutACTMRefusesOne(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.noCTM = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.b.Outputs()[0]
	if o.HasColorTransform() {
		t.Fatal("a CRTC without a CTM property reports a colour transform")
	}
	if err := o.SetColorTransform(backend.Matrix{1, 0, 0, 0, 1, 0, 0, 0, 0.5}); err == nil {
		t.Fatal("a CTM was accepted for a CRTC without the property")
	}
}

func TestTearingScanoutsFlipAsync(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.asyncFlips = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	keys := map[int]*fakeBuffer{}
	client := func(fd int) *fakeBuffer {
		if k, ok := keys[fd]; ok {
			return k
		}
		keys[fd] = &fakeBuffer{gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: fd, Stride: 1920 * 4}}}}
		return keys[fd]
	}
	scanout := func(fd int, tear bool) commit {
		t.Helper()
		k := client(fd)
		if ok, err := o.Scanout(k, k.d, backend.Whole(k.d.Size), func() error { return nil }, tear); err != nil || !ok {
			t.Fatalf("Scanout = %t, %v", ok, err)
		}
		return r.dev.commits[len(r.dev.commits)-1]
	}
	torn := scanout(7, true)
	if torn.flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent|kdrm.AtomicPageFlipAsync {
		t.Fatalf("a tearing scanout committed with flags %#x", torn.flags)
	}
	if len(torn.values) != 1 || r.dev.scanouts[uint32(torn.values[[2]uint32{40, propFB}])] != primeHandles+7 {
		t.Fatalf("an async flip carried %v, want the primary plane's FB_ID alone", torn.values)
	}
	presents := len(r.rec.presents)
	r.dev.flip(t, 10, 2)
	r.until("the flip's presentation", func() bool { return len(r.rec.presents) > presents })
	if p := r.rec.presents[len(r.rec.presents)-1]; !p.Torn {
		t.Fatalf("the async flip was presented as %+v, not torn", p)
	}

	if vsync := scanout(8, false); vsync.flags&kdrm.AtomicPageFlipAsync != 0 {
		t.Fatal("a scanout that does not tear flipped async")
	}
	presents = len(r.rec.presents)
	r.dev.flip(t, 10, 3)
	r.until("the flip's presentation", func() bool { return len(r.rec.presents) > presents })
	if p := r.rec.presents[len(r.rec.presents)-1]; p.Torn {
		t.Fatal("a vblank flip was presented as torn")
	}

	r.dev.refuseAsync = true
	if refused := scanout(9, true); refused.flags&kdrm.AtomicPageFlipAsync != 0 || refused.values[[2]uint32{40, propFB}] == 0 {
		t.Fatalf("an async flip the driver refuses did not fall back to a full vblank flip: %+v", refused)
	}
}

func TestCursorAndGammaWaitingOnAFlipShareOneCommit(t *testing.T) {
	r := start(t)
	o := r.lit()
	if err := r.setCursor(o, image.Pt(4, 6)); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the cursor", o, 0)
	cp, _ := o.CursorPlane()
	if err := errors.Join(cp.MoveCursor(image.Pt(30, 20)), o.SetGamma([]uint16{0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23})); err != nil {
		t.Fatal(err)
	}
	before := len(r.dev.commits)
	r.dev.flip(t, 10, 2)
	r.committedSince("the commit after the flip", o, before)
	var shown []commit
	for _, c := range r.dev.commits[before:] {
		if c.flags&kdrm.AtomicTestOnly == 0 {
			shown = append(shown, c)
		}
	}
	if len(shown) != 1 {
		t.Fatalf("%d commits after the flip, want the cursor and the LUT in one", len(shown))
	}
	_, at, _ := cursorAt(shown[0])
	if lut, ok := gammaOf(shown[0]); !ok || lut == 0 || at != image.Pt(26, 14) {
		t.Fatalf("the commit after the flip put the cursor at %v and GAMMA_LUT %d (%v)", at, lut, ok)
	}
}

func TestACursorRefusedAfterAFlipLetsTheLUTThrough(t *testing.T) {
	r := start(t)
	o := r.lit()
	if err := r.setCursor(o, image.Pt(4, 6)); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the cursor", o, 0)
	cp, _ := o.CursorPlane()
	if err := errors.Join(cp.MoveCursor(image.Pt(30, 20)), o.SetGamma([]uint16{0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23})); err != nil {
		t.Fatal(err)
	}
	r.dev.refuseCursor = true
	before := len(r.dev.commits)
	r.dev.flip(t, 10, 2)
	r.committedSince("the commit after the flip", o, before)
	last := r.dev.commits[len(r.dev.commits)-1]
	if lut, ok := gammaOf(last); len(r.dev.commits) == before || last.flags&kdrm.AtomicTestOnly != 0 || !ok || lut == 0 {
		t.Fatalf("the LUT did not go out after the cursor was refused: %+v", last)
	}
	if _, _, touched := cursorAt(last); touched {
		t.Fatal("the commit after the refusal still carried the cursor")
	}
	if err := cp.MoveCursor(image.Pt(40, 30)); err == nil {
		t.Fatal("the cursor plane kept moving after the kernel refused it")
	}
}

func (r *rig) planesAfterAFlip(fail func(*fakeDevice)) error {
	r.t.Helper()
	o := r.lit()
	if err := r.setCursor(o, image.Pt(4, 6)); err != nil {
		r.t.Fatal(err)
	}
	r.until("the cursor's first commit", func() bool { return o.flipping })
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(30, 20)); err != nil {
		r.t.Fatal(err)
	}
	fail(r.dev)
	r.dev.flip(r.t, 10, 2)
	return r.runUntil("the rejected plane commit's report", func() bool { return len(r.reports) > 0 })
}

func failingCommits(err error) func(*fakeDevice) {
	return func(d *fakeDevice) { d.failCommits = err }
}

func TestAPlaneCommitTheKernelRejectsIsReportedAndRetried(t *testing.T) {
	r := start(t)
	if err := r.planesAfterAFlip(failingCommits(unix.EBUSY)); err != nil {
		t.Fatalf("a busy plane commit ended the session: %v", err)
	}
	if len(r.reports) != 1 || !errors.Is(r.reports[0], unix.EBUSY) {
		t.Fatalf("reported %v, want the busy commit once", r.reports)
	}
	o := r.b.Outputs()[0]
	cp, _ := o.CursorPlane()
	r.at(o.lastFlip + modeOf(o.mode).Interval() - backend.PlaneLead)
	if err := errors.Join(cp.MoveCursor(image.Pt(31, 20)), cp.MoveCursor(image.Pt(32, 20))); err != nil {
		t.Fatal(err)
	}
	if len(r.reports) != 1 {
		t.Fatalf("reported %v, want one report while the commits keep failing", r.reports)
	}
	r.dev.failCommits = nil
	if err := cp.MoveCursor(image.Pt(33, 20)); err != nil {
		t.Fatal(err)
	}
	last := r.dev.commits[len(r.dev.commits)-1]
	if _, at, _ := cursorAt(last); at != image.Pt(29, 14) || last.flags&kdrm.AtomicTestOnly != 0 {
		t.Fatalf("the cursor did not go out once the kernel took commits again: %+v", last)
	}
}

func TestAPlaneCommitOnAGoneDeviceEndsTheSession(t *testing.T) {
	r := start(t)
	if err := r.planesAfterAFlip(func(d *fakeDevice) { d.removed = true }); !errors.Is(err, backend.ErrDeviceLost) || !errors.Is(err, unix.ENODEV) {
		t.Fatalf("the loop ended with %v, want the card's ENODEV as a lost device", err)
	}
}

func TestANoDeviceErrorFromACommitTheCardStillAnswersAfterIsReportedNotFatal(t *testing.T) {
	r := start(t)
	if err := r.planesAfterAFlip(failingCommits(unix.ENODEV)); err != nil {
		t.Fatalf("an ENODEV the card itself does not confirm ended the session: %v", err)
	}
	if len(r.reports) != 1 || !errors.Is(r.reports[0], unix.ENODEV) || backend.Fatal(r.reports[0]) {
		t.Fatalf("reported %v, want the ENODEV once, not as a lost device", r.reports)
	}
}

func TestAFrameCommitOnAGoneDeviceStopsTheCompositorWithTheDeviceLost(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.dev.removed = true
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	err := r.runUntil("the frame on the gone card", func() bool { return false })
	if !errors.Is(err, backend.ErrDeviceLost) || !errors.Is(err, unix.ENODEV) || !strings.Contains(err.Error(), "/dev/dri/card0") {
		t.Fatalf("the loop ended with %v, want the frame's ENODEV as /dev/dri/card0 lost", err)
	}
}

func TestAFailedEventReadIsFatalAndAnEmptyOneIsNot(t *testing.T) {
	r := start(t)
	_, err := device{kernel: &fakeDevice{events: -1}}.readEvents(make([]byte, 8))
	if !errors.Is(err, backend.ErrBroken) || !errors.Is(err, unix.EBADF) {
		t.Fatalf("reading a closed fd = %v, want a broken invariant", err)
	}
	if err := r.b.readEvents(0); err != nil {
		t.Fatalf("reading no events = %v, want nil on EAGAIN", err)
	}
}

func TestTheLatchFollowsMeasuredRenderTimeAndGrowsAMarginAfterAMiss(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	interval := modeOf(o.mode).Interval()
	o.lastFlip = r.now()
	half := func() time.Duration {
		next, _ := backend.NextVblank(o.lastFlip, r.now(), interval)
		return next - o.budget.Latch(o.lastFlip, r.now(), interval)
	}
	if got := half(); got != interval/2 {
		t.Fatalf("an output with no measurements latches %v before the vblank, want half a refresh %v", got, interval/2)
	}
	for range 32 {
		o.budget.Sample(time.Millisecond)
	}
	if got, want := half(), 4*time.Millisecond; got != want {
		t.Fatalf("after 1 ms renders the latch is %v before the vblank, want %v", got, want)
	}
	o.aimedAt = o.lastFlip
	if err := o.timeFrame(o.lastFlip + interval); err != nil {
		t.Fatal(err)
	}
	if got, want := half(), 7*time.Millisecond; got != want {
		t.Fatalf("after a missed vblank the latch is %v before the vblank, want a margin more, %v", got, want)
	}
}

func TestSteadyFramesSkipTheTestCommitUntilThePlanesChange(t *testing.T) {
	r := start(t)
	o := r.lit()
	frame := func(seq uint32) {
		t.Helper()
		n := len(r.dev.commits)
		if err := o.ScheduleFrame(); err != nil {
			t.Fatal(err)
		}
		r.committedSince("the frame", o, n)
		r.dev.flip(t, 10, seq)
		r.until("the frame's flip", func() bool { return !o.flipping })
	}
	tests := func() int {
		n := 0
		for _, c := range r.dev.commits {
			if c.flags&kdrm.AtomicTestOnly != 0 {
				n++
			}
		}
		return n
	}
	for seq := uint32(2); seq < 8; seq++ {
		frame(seq)
	}
	r.dev.commits = nil
	for seq := uint32(8); seq < 14; seq++ {
		frame(seq)
	}
	if n := tests(); n != 0 {
		t.Fatalf("%d test commits for frames on buffers that already passed, want none", n)
	}
	n := len(r.dev.commits)
	if err := r.setCursor(o, image.Pt(4, 6)); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the cursor", o, n)
	r.dev.flip(t, 10, 14)
	r.until("the cursor's flip", func() bool { return !o.flipping })
	r.dev.commits = nil
	frame(15)
	if n := tests(); n == 0 {
		t.Fatal("a frame with a newly shown cursor went out without a test commit")
	}
}

func TestAnEmptyFramesCallbacksSurviveACursorFlip(t *testing.T) {
	r := start(t)
	o := r.flipFirstFrame()
	r.rec.idle = true
	interval := modeOf(o.mode).Interval()
	r.at(o.lastFlip + interval - backend.PlaneLead)
	r.cursorCommit("the cursor", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	r.at(o.lastFlip + interval/2)
	frames := r.frames()
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("the empty frame", func() bool { return r.frames() > frames })
	refresh := o.lastFlip + interval
	if o.idleAt != refresh {
		t.Fatalf("the empty frame reports its refresh at %v after the flip, want the next vblank %v after it", o.idleAt-o.lastFlip, interval)
	}
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(40, 30)); err != nil {
		t.Fatal(err)
	}
	if o.flipping {
		t.Fatalf("a cursor move half a refresh before the vblank committed at once: %+v", r.dev.commits)
	}
	r.at(refresh - backend.PlaneLead)
	if err := o.planesDue(); err != nil {
		t.Fatal(err)
	}
	if !o.flipping || o.queued != nil {
		t.Fatalf("want a cursor-only flip pending at the refresh: flipping %v, queued %v", o.flipping, o.queued != nil)
	}
	presents := len(r.rec.presents)
	if err := o.paceElapsed(); err != nil {
		t.Fatal(err)
	}
	if got := r.rec.presents[presents:]; len(got) != 1 || !got[0].Idle || got[0].Time != refresh {
		t.Fatalf("the empty frame's refresh behind a cursor flip presented %+v, want one idle presentation at the vblank", got)
	}
}

func bootLit(t *testing.T, lit *splash, sharedPrimary bool) *rig {
	t.Helper()
	return boot(t, func(d *fakeDevice) allocator {
		d.splash, d.sharedPrimary = lit, sharedPrimary
		return &fakeAlloc{soft: soft.New()}
	})
}

func (r *rig) firstFrame(o *Output) []commit {
	r.t.Helper()
	before := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.committedSince("the first frame", o, before)
	frame := r.dev.commits[before:]
	if len(frame) == 0 || frame[len(frame)-1].flags&kdrm.AtomicTestOnly != 0 {
		r.t.Fatalf("the first frame committed %+v", frame)
	}
	return frame
}

func TestAnOutputLitInItsModeFlipsOverWhatIsOnScreen(t *testing.T) {
	lit := modes[0]
	lit.Type, lit.Name = 0, [32]byte{'s', 'p', 'l', 'a', 's', 'h'}
	r := bootLit(t, &splash{crtc: 10, plane: 40, mode: lit}, false)
	o := r.b.Outputs()[0]
	if len(r.reports) != 0 {
		t.Fatalf("taking over a lit output reported %v", r.reports)
	}
	if err := o.SetGamma([]uint16{0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23}); err != nil {
		t.Fatal(err)
	}
	if err := r.setCursor(o, image.Pt(1, 1)); err != nil {
		t.Fatal(err)
	}
	cp, err := o.CursorPlane()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.MoveCursor(image.Pt(30, 40)); err != nil {
		t.Fatal(err)
	}
	r.settle()
	for i, c := range r.dev.commits {
		if c.flags != kdrm.AtomicTestOnly {
			t.Fatalf("commit %d replaced what is on screen before the first frame: %+v", i, c)
		}
	}
	if !o.Inherited() {
		t.Fatal("an output lit in its mode does not report its inherited frame")
	}
	frame := r.firstFrame(o)
	if o.Inherited() {
		t.Fatal("the output still reports an inherited frame after its first flip")
	}
	for i, c := range frame {
		if c.flags&kdrm.AtomicAllowModeset != 0 {
			t.Fatalf("commit %d of the first frame allows a modeset: %+v", i, c)
		}
	}
	flip := frame[len(frame)-1]
	if flip.flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent || flip.values[[2]uint32{40, propFB}] != 901 || flip.values[[2]uint32{40, propPlaneCRTC}] != 10 {
		t.Fatalf("the first frame committed %+v", flip)
	}
	for _, k := range [][2]uint32{{10, propModeID}, {10, propActive}, {20, propConnCRTC}} {
		if v, ok := flip.values[k]; ok {
			t.Fatalf("the first frame set object %d property %d to %d", k[0], k[1], v)
		}
	}
	if lut, ok := gammaOf(flip); !ok || lut == 0 {
		t.Fatalf("the first frame set GAMMA_LUT %d (%v), not the LUT set before it", lut, ok)
	}
	if fb, at, ok := cursorAt(flip); !ok || fb == 0 || at != image.Pt(29, 39) {
		t.Fatalf("the first frame put cursor fb %d at %v (%v)", fb, at, ok)
	}
}

func TestAnOutputLitInItsModeTurnsOffBeforeItsFirstFrame(t *testing.T) {
	r := bootLit(t, &splash{crtc: 10, plane: 40, mode: modes[0]}, false)
	o := r.b.Outputs()[0]
	state := o.State()
	state.Power = backend.PoweredOff
	if err := o.Commit(state); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 1 || r.dev.commits[0].flags != kdrm.AtomicAllowModeset|kdrm.AtomicNonblock || r.dev.commits[0].values[[2]uint32{10, propActive}] != 0 {
		t.Fatalf("powering off what is on screen committed %+v", r.dev.commits)
	}
}

func TestOutputsNotLitInTheirModeAreModeset(t *testing.T) {
	for _, tc := range []struct {
		name string
		lit  *splash
	}{
		{"an unlit card", nil},
		{"a CRTC lit in another mode", &splash{crtc: 10, plane: 40, mode: modes[1]}},
		{"a lit CRTC without a framebuffer", &splash{crtc: 10, mode: modes[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bootLit(t, tc.lit, false)
			o := r.b.Outputs()[0]
			if len(r.reports) != 0 || o.Inherited() {
				t.Fatalf("reports %v, inherited %v", r.reports, o.Inherited())
			}
			frame := r.firstFrame(o)
			flip := frame[len(frame)-1]
			if flip.flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent|kdrm.AtomicAllowModeset {
				t.Fatalf("the first frame committed with flags %#x", flip.flags)
			}
			for k, v := range map[[2]uint32]uint64{{20, propConnCRTC}: 10, {10, propActive}: 1, {10, propModeID}: 601, {40, propFB}: 901} {
				if got, ok := flip.values[k]; !ok || got != v {
					t.Errorf("object %d property %d = %d (%v), want %d", k[0], k[1], got, ok, v)
				}
			}
		})
	}
}

func TestAnOutputKeepsTheCRTCAndPrimaryPlaneItIsLitOn(t *testing.T) {
	for _, tc := range []struct {
		name          string
		lit           splash
		sharedPrimary bool
	}{
		{"the second CRTC", splash{crtc: 11, plane: 41, mode: modes[0]}, false},
		{"the second primary plane of a CRTC", splash{crtc: 10, plane: 42, mode: modes[0]}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bootLit(t, &tc.lit, tc.sharedPrimary)
			o := r.b.Outputs()[0]
			if o.crtc.id != tc.lit.crtc || o.primary.id != tc.lit.plane || len(r.reports) != 0 {
				t.Fatalf("CRTC %d plane %d, reports %v", o.crtc.id, o.primary.id, r.reports)
			}
			flip := r.firstFrame(o)
			if got := flip[len(flip)-1]; got.flags&kdrm.AtomicAllowModeset != 0 || got.values[[2]uint32{tc.lit.plane, propPlaneCRTC}] != uint64(tc.lit.crtc) {
				t.Fatalf("the first frame committed %+v", got)
			}
		})
	}
}

func TestAPausedBackendClosesWithoutCommitting(t *testing.T) {
	r := bootLit(t, &splash{crtc: 10, plane: 40, mode: modes[0]}, false)
	r.b.Pause()
	before := len(r.dev.commits)
	if err := r.b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := r.dev.commits[before:]; len(got) != 0 {
		t.Fatalf("closing without DRM master committed %+v", got)
	}
}

func TestClosingLeavesTheFrameOnScreenForTheNextMaster(t *testing.T) {
	r := start(t)
	o := r.lit()
	front := o.front.fb
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the second frame", o, 0)
	queued := o.queued.fb
	others := len(o.buffers) - 2
	before, removed := len(r.dev.commits), len(r.dev.removedFBs)
	if err := r.b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := r.dev.commits[before:]; len(got) != 0 {
		t.Fatalf("closing committed %+v over the frame on screen", got)
	}
	if !slices.Equal(r.dev.closedFBs, []uint32{front, queued}) {
		t.Fatalf("closed framebuffers %v, want the one on screen %d and the queued %d", r.dev.closedFBs, front, queued)
	}
	if got := r.dev.removedFBs[removed:]; len(got) != others || slices.Contains(got, front) || slices.Contains(got, queued) {
		t.Fatalf("removed framebuffers %v, want the %d not on screen", got, others)
	}
}

func TestClosingOverAnInheritedFrameLeavesItAlone(t *testing.T) {
	r := bootLit(t, &splash{crtc: 10, plane: 40, mode: modes[0]}, false)
	before := len(r.dev.commits)
	if err := r.b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := r.dev.commits[before:]; len(got) != 0 || len(r.dev.closedFBs) != 0 {
		t.Fatalf("closing over the inherited frame committed %+v and closed %v", got, r.dev.closedFBs)
	}
}

func TestASteadyFrameTouchesOnlyThePrimaryPlanesBuffer(t *testing.T) {
	r := start(t)
	o := r.lit()
	for seq := uint32(2); seq < 6; seq++ {
		n := len(r.dev.commits)
		if err := o.ScheduleFrame(); err != nil {
			t.Fatal(err)
		}
		r.committedSince("the frame", o, n)
		r.dev.flip(t, 10, seq)
		r.until("the frame's flip", func() bool { return !o.flipping })
	}
	last := r.dev.commits[len(r.dev.commits)-1]
	if last.flags&kdrm.AtomicTestOnly != 0 {
		t.Fatal("the last commit is a test")
	}
	for key := range last.values {
		if object, prop := key[0], key[1]; object != o.primary.id || prop != propFB && prop != propInFence {
			t.Errorf("a steady frame set property %d of object %d", prop, object)
		}
	}
	if last.values[[2]uint32{o.primary.id, propFB}] == 0 {
		t.Fatal("a steady frame set no buffer")
	}
}

func (r *rig) cursorCommit(what string, o *Output, act func() error) commit {
	r.t.Helper()
	n := len(r.dev.commits)
	if err := act(); err != nil {
		r.t.Fatal(err)
	}
	r.until(what, func() bool { return o.flipping && len(r.dev.commits) > n })
	last := r.dev.commits[len(r.dev.commits)-1]
	if last.flags&kdrm.AtomicTestOnly != 0 {
		r.t.Fatalf("%s ended in a test commit", what)
	}
	r.dev.flip(r.t, 10, uint32(len(r.dev.commits)))
	r.until(what+"'s flip", func() bool { return !o.flipping })
	return last
}

func cursorProps(c commit) []uint32 {
	var props []uint32
	for key := range c.values {
		if key[0] == 43 {
			props = append(props, key[1])
		}
	}
	slices.Sort(props)
	return props
}

func sortedProps(props ...uint32) []uint32 {
	slices.Sort(props)
	return props
}

func TestACursorMoveJustBeforeTheVblankStillCommits(t *testing.T) {
	r := start(t)
	o := r.lit()
	interval := modeOf(o.mode).Interval()
	r.cursorCommit("the cursor's first commit", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	o.lastFlip = o.b.now() - interval + time.Millisecond
	if o.planesWait() > 0 {
		t.Fatalf("1 ms before the vblank a cursor commit would wait %v", o.planesWait())
	}
	cp, _ := o.CursorPlane()
	n := len(r.dev.commits)
	if err := cp.MoveCursor(image.Pt(20, 30)); err != nil {
		t.Fatal(err)
	}
	r.until("the move", func() bool { return len(r.dev.commits) > n })
	if o.frameWait() <= 0 {
		t.Fatal("the cursor's commit took the deferral of the frame after it")
	}
}

func TestACursorMoveCommitsAheadOfAFrameDeferredPastTheVblank(t *testing.T) {
	r := start(t)
	o := r.lit()
	interval := modeOf(o.mode).Interval()
	r.cursorCommit("the cursor's first commit", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	o.lastFlip = o.b.now() - interval + time.Millisecond
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	if !o.clock.Waiting() {
		t.Fatal("a frame requested 1 ms before the vblank was not deferred")
	}
	cp, _ := o.CursorPlane()
	n := len(r.dev.commits)
	if err := cp.MoveCursor(image.Pt(20, 30)); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != n+1 || r.dev.commits[n].flags&kdrm.AtomicTestOnly != 0 {
		t.Fatalf("a move ahead of a deferred frame sent %+v, want one real commit at once", r.dev.commits[n:])
	}
	if _, at, _ := cursorAt(r.dev.commits[n]); at != image.Pt(20, 30).Sub(image.Pt(4, 6)) {
		t.Fatalf("the move put the cursor plane at %v", at)
	}
	if !o.clock.Waiting() {
		t.Fatal("the cursor's commit took the deferred frame with it")
	}
}

func TestACursorCommitCarriesOnlyWhatChanged(t *testing.T) {
	r := start(t)
	o := r.lit()
	first := r.cursorCommit("the cursor's first commit", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	if len(cursorProps(first)) != 12 {
		t.Fatalf("the first image set %v, want every plane property", cursorProps(first))
	}
	cp, _ := o.CursorPlane()
	position := sortedProps(propCRTCX, propCRTCY)

	moved := r.cursorCommit("the move", o, func() error { return cp.MoveCursor(image.Pt(20, 30)) })
	if got := cursorProps(moved); !slices.Equal(got, position) {
		t.Fatalf("a move set %v, want only the position %v", got, position)
	}
	if _, at, _ := cursorAt(moved); at != image.Pt(16, 24) {
		t.Fatalf("the move put the plane at %v", at)
	}

	again := r.cursorCommit("a new image on the same hotspot", o, func() error { return r.setCursor(o, image.Pt(4, 6)) })
	if got, want := cursorProps(again), []uint32{propFB}; !slices.Equal(got, want) {
		t.Fatalf("a new image set %v, want only the buffer", got)
	}

	hotspot := r.cursorCommit("a new hotspot", o, func() error { return r.setCursor(o, image.Pt(1, 2)) })
	if got, want := cursorProps(hotspot), sortedProps(propFB, propCRTCX, propCRTCY, propHotspotX, propHotspotY); !slices.Equal(got, want) {
		t.Fatalf("a new hotspot set %v, want %v", got, want)
	}
	if _, at, _ := cursorAt(hotspot); at != image.Pt(19, 28) || hotspot.values[[2]uint32{43, propHotspotX}] != 1 || hotspot.values[[2]uint32{43, propHotspotY}] != 2 {
		t.Fatalf("the new hotspot put the plane at %v with %+v", at, hotspot.values)
	}

	hidden := r.cursorCommit("hiding", o, cp.HideCursor)
	if got, want := cursorProps(hidden), sortedProps(propFB, propPlaneCRTC); !slices.Equal(got, want) || hidden.values[[2]uint32{43, propFB}] != 0 {
		t.Fatalf("hiding set %v: %+v", got, hidden.values)
	}

	if err := cp.MoveCursor(image.Pt(40, 40)); err != nil {
		t.Fatal(err)
	}
	if o.flipping {
		t.Fatal("moving a hidden cursor committed")
	}

	back := r.cursorCommit("showing again", o, func() error { return r.setCursor(o, image.Pt(1, 2)) })
	if len(cursorProps(back)) != 12 {
		t.Fatalf("a cursor coming back set %v, want every plane property", cursorProps(back))
	}
	if _, at, _ := cursorAt(back); at != image.Pt(39, 38) || back.values[[2]uint32{43, propHotspotX}] != 1 {
		t.Fatalf("the cursor came back at %v with %+v", at, back.values)
	}
}

func TestACursorThatMovesBackBeforeItsCommitSendsNothing(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.cursorCommit("the cursor", o, func() error { return r.setCursor(o, image.Point{}) })
	cp, _ := o.CursorPlane()
	n := len(r.dev.commits)
	if err := cp.MoveCursor(image.Pt(5, 5)); err != nil {
		t.Fatal(err)
	}
	r.until("the move's commit", func() bool { return o.flipping })
	if err := errors.Join(cp.MoveCursor(image.Pt(6, 6)), cp.MoveCursor(image.Pt(5, 5))); err != nil {
		t.Fatal(err)
	}
	sent := len(r.dev.commits)
	if sent <= n {
		t.Fatal("the first move committed nothing")
	}
	r.dev.flip(t, 10, 99)
	r.until("the flip", func() bool { return !o.flipping })
	if len(r.dev.commits) != sent || o.cursor.dirty {
		t.Fatalf("a cursor back where the plane holds it committed %+v, dirty %v", r.dev.commits[sent:], o.cursor.dirty)
	}
}

func TestAFailedCursorCommitKeepsItsChangesForTheNext(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.cursorCommit("the cursor", o, func() error { return r.setCursor(o, image.Point{}) })
	cp, _ := o.CursorPlane()
	r.dev.failCommits = unix.EBUSY
	if err := cp.MoveCursor(image.Pt(7, 7)); err != nil {
		t.Fatal(err)
	}
	r.until("the refused move", func() bool {
		last := r.dev.commits[len(r.dev.commits)-1]
		_, at, _ := cursorAt(last)
		return at == image.Pt(7, 7) && last.flags&kdrm.AtomicTestOnly == 0
	})
	if o.cursor.onPlane.at == image.Pt(7, 7) {
		t.Fatal("a refused commit counted as on the plane")
	}
	r.dev.failCommits = nil
	last := r.cursorCommit("the retry", o, o.flushPlanes)
	if _, at, _ := cursorAt(last); at != image.Pt(7, 7) {
		t.Fatalf("the retry left out the position: %+v", last.values)
	}
}

func TestAResumedOutputSetsTheWholeCursorPlaneAgain(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.cursorCommit("the cursor", o, func() error { return r.setCursor(o, image.Pt(3, 3)) })
	r.b.Pause()
	if err := r.b.Resume(); err != nil {
		t.Fatal(err)
	}
	n := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("the frame after resuming", o, n)
	last := r.dev.commits[len(r.dev.commits)-1]
	if len(cursorProps(last)) != 12 || last.values[[2]uint32{43, propHotspotX}] != 3 {
		t.Fatalf("the frame after resuming set %v of the cursor plane", cursorProps(last))
	}
}

func TestScanoutFramesReuseTheirRecords(t *testing.T) {
	r := start(t)
	o := r.lit()
	client := gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: 7, Stride: 1920 * 4}}}
	keys := []*fakeBuffer{{client}, {client}}
	release := func() error { return nil }
	records := map[*scanout]struct{}{}
	for i := range 20 {
		ok, err := o.Scanout(keys[i%2], client, backend.Whole(client.Size), release, false)
		if err != nil || !ok {
			t.Fatalf("frame %d: Scanout = %t, %v", i, ok, err)
		}
		records[o.queued] = struct{}{}
		if err := o.flipped(kdrm.FlipEvent{Sec: uint32(i + 10)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(records) > 2 {
		t.Fatalf("20 scanout frames used %d records, want the on-screen and the queued one reused", len(records))
	}
}

func TestAPowerOffDuringAFlipWaitsForItsEventInsteadOfTheKernel(t *testing.T) {
	r := start(t)
	o := r.lit()
	frames := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.committedSince("a frame in flight", o, frames)
	state := o.State()
	state.Power = backend.PoweredOff
	n := len(r.dev.commits)
	if err := o.Commit(state); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != n {
		t.Fatalf("a power off during a flip committed at once: %+v", r.dev.commits[n:])
	}
	if o.State().Power != backend.PoweredOff {
		t.Fatal("the output does not count as off while its commit waits for the flip")
	}
	r.dev.flip(t, 10, 2)
	r.until("the power off after the flip", func() bool { return len(r.dev.commits) > n })
	off := r.dev.commits[n]
	if off.flags != kdrm.AtomicAllowModeset|kdrm.AtomicNonblock || off.values[[2]uint32{10, propActive}] != 0 {
		t.Fatalf("power off commit %+v", off)
	}
}

func TestDisableFallsBackToWaitingOnlyWhenTheKernelIsBusy(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.dev.busyNonblock = true
	n := len(r.dev.commits)
	if err := o.Commit(backend.OutputState{}); err != nil {
		t.Fatal(err)
	}
	if got := r.dev.commits[n:]; len(got) != 2 || got[0].flags != kdrm.AtomicAllowModeset|kdrm.AtomicNonblock || got[1].flags != kdrm.AtomicAllowModeset {
		t.Fatalf("disable during a flip committed %+v", got)
	}
}

func goroutineID() string {
	var buf [64]byte
	return string(bytes.Fields(buf[:runtime.Stack(buf[:], false)])[1])
}

func probeOn(loopGoroutine string) int64 {
	if goroutineID() == loopGoroutine {
		return 1
	}
	return 0
}

func TestAHotplugProbesOffTheLoopAndAppliesTheResultOnIt(t *testing.T) {
	r := start(t)
	r.dev.connected = false
	gate := make(chan struct{})
	r.dev.probeGate, r.dev.loopGoroutine = gate, goroutineID()
	if err := r.b.Hotplug(); err != nil {
		t.Fatal(err)
	}
	ticks := 0
	if err := r.l.Post(func() error { ticks++; return nil }); err != nil {
		t.Fatal(err)
	}
	r.until("a posted tick while the probe is stuck in the kernel", func() bool { return ticks == 1 })
	if !slices.Equal(r.rec.log, []string{"added DP-1"}) {
		t.Fatalf("the output changed before its probe returned: %v", r.rec.log)
	}
	if err := r.b.Hotplug(); err != nil {
		t.Fatal(err)
	}
	r.dev.openProbeGate()
	r.until("the second probe a hotplug during the first asks for", func() bool { return !r.b.probing && !r.b.probeAgain })
	if n := r.dev.probesOnLoop.Load(); n != 0 {
		t.Fatalf("%d connector probes ran on the loop goroutine", n)
	}
	if !slices.Equal(r.rec.log, []string{"added DP-1", "removed DP-1"}) {
		t.Fatalf("log %v", r.rec.log)
	}
}

type asyncPrimary struct {
	*soft.Renderer
	reads    int
	released int
	finished bool
	waiting  chan struct{}
}

func asyncPrimaryOf(r *soft.Renderer) *asyncPrimary {
	return &asyncPrimary{Renderer: r, waiting: make(chan struct{})}
}

func (p *asyncPrimary) finish() {
	p.finished = true
	close(p.waiting)
}

func (p *asyncPrimary) copying() bool {
	return p.finished && p.reads > p.released
}

func (r *rig) copiesOffLoop(p *asyncPrimary) {
	r.offLoops = append(r.offLoops, p.copying)
}

func (p *asyncPrimary) AsyncReads() bool { return true }

func (p *asyncPrimary) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	if t, ok := target.(*asyncTarget); ok {
		target = t.ReadTarget
	}
	return p.Renderer.Begin(target, damage)
}

func (p *asyncPrimary) NewAsyncTarget(size image.Point) (render.AsyncReadTarget, error) {
	t, err := p.NewTarget(size)
	if err != nil {
		return nil, err
	}
	return &asyncTarget{ReadTarget: t, p: p}, nil
}

type asyncTarget struct {
	render.ReadTarget
	p *asyncPrimary
}

func (t *asyncTarget) ReadAsync() (render.PendingRead, error) {
	t.p.reads++
	size := t.Size()
	pix := make([]byte, size.X*size.Y*4)
	if err := t.Read(pix, size.X*4); err != nil {
		return nil, err
	}
	return &pendingCopy{pix: pix, stride: size.X * 4, p: t.p}, nil
}

type pendingCopy struct {
	pix    []byte
	stride int
	p      *asyncPrimary
}

func (p *pendingCopy) Wait() error {
	<-p.p.waiting
	return nil
}

func (p *pendingCopy) Map() ([]byte, int, error) { return p.pix, p.stride, nil }

func (p *pendingCopy) Release() error {
	p.p.released++
	return nil
}

func TestACopiedFrameCommitsOnlyOnceTheReadFinishedOffTheLoop(t *testing.T) {
	primary := asyncPrimaryOf(soft.New())
	r := boot(t, func(dev *fakeDevice) allocator { return &copyAllocator{dev: device{kernel: dev}, primary: primary} })
	r.copiesOffLoop(primary)
	o := r.b.Outputs()[0]
	n := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.until("the frame's read to start", func() bool { return primary.reads > 0 })
	r.settle()
	if len(r.dev.commits) != n {
		t.Fatalf("%d commits while the copy was still waiting on the GPU", len(r.dev.commits)-n)
	}
	primary.finish()
	r.committedSince("the copied frame", o, n)
	if got := r.b.SyncReads(); got != 0 {
		t.Fatalf("%d synchronous reads on the loop", got)
	}
}

func TestAFlipCarriesDamageClipsReusingTheBlobWhileTheyMatch(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.damageClips = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	present := func(clip render.Region) uint64 {
		t.Helper()
		if _, err := o.BeginFrame(); err != nil {
			t.Fatal(err)
		}
		if err := o.Present(clip); err != nil {
			t.Fatal(err)
		}
		c := r.dev.commits[len(r.dev.commits)-1]
		r.dev.flip(t, 10, uint32(len(r.dev.commits)))
		r.until("the flip", func() bool { return !o.flipping })
		return c.values[[2]uint32{40, propDamageClips}]
	}
	clip := render.Region{image.Rect(10, 20, 30, 40)}
	blob := present(clip)
	if blob == 0 || len(r.dev.blobData[uint32(blob)]) != 16 {
		t.Fatalf("a frame with one rect of damage set clips %d: %v", blob, r.dev.blobData[uint32(blob)])
	}
	created := r.dev.blobs
	if again := present(clip); again != blob || r.dev.blobs != created {
		t.Fatalf("the same damage made blob %d from %d, %d blobs from %d", again, blob, r.dev.blobs, created)
	}
	if whole := present(render.Region{image.Rect(0, 0, 1920, 1080)}); whole != 0 {
		t.Fatalf("a whole repaint carried clips %d", whole)
	}
}

func overlayRig(t *testing.T, zpos uint64) (*rig, *Output, gpu.DMABUF) {
	t.Helper()
	r := boot(t, func(d *fakeDevice) allocator {
		d.overlayZpos = zpos
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	if len(o.overlays) != 0 {
		r.b.planes[slices.IndexFunc(r.b.planes, func(p *plane) bool { return p.id == 42 })].clients = []gpu.FormatModifier{{Format: scanoutFormat, Modifier: fourcc.Linear}}
	}
	return r, o, gpu.DMABUF{Size: image.Pt(640, 360), Format: scanoutFormat, Modifier: fourcc.Linear, Planes: []gpu.Plane{{FD: 9, Stride: 640 * 4}}}
}

func TestAnOverlayBelowThePrimaryPlaneIsNotUsed(t *testing.T) {
	_, o, _ := overlayRig(t, 0)
	if len(o.overlays) != 0 {
		t.Fatal("an overlay with no zpos above the primary plane was claimed")
	}
}

func TestAnOverlayIsTestedThenFlippedWithoutThePrimaryPlane(t *testing.T) {
	r, o, client := overlayRig(t, 2)
	if len(o.overlays) == 0 {
		t.Fatal("no overlay plane claimed")
	}
	key := &fakeBuffer{client}
	dst := image.Rect(100, 100, 740, 460)
	r.dev.refuseOverlay = true
	n := len(r.dev.commits)
	if testOverlay(o, key, client, dst) {
		t.Fatal("a TEST_ONLY commit the kernel refuses passed")
	}
	if got := r.dev.commits[n:]; len(got) != 1 || got[0].flags&kdrm.AtomicTestOnly == 0 {
		t.Fatalf("testing the overlay committed %+v", got)
	}
	r.dev.refuseOverlay = false
	clear(o.overlayTested)
	if !testOverlay(o, key, client, dst) {
		t.Fatal("the overlay was refused")
	}
	tests := len(r.dev.commits)
	if !testOverlay(o, key, client, dst) || len(r.dev.commits) != tests {
		t.Fatal("the same overlay was tested twice")
	}
	released := 0
	if err := o.SetOverlays([]backend.Overlay{overlayLayer(key, client, dst, func() error { released++; return nil })}); err != nil {
		t.Fatal(err)
	}
	flip, err := o.FlipOverlays()
	if err != nil || flip != backend.OverlaysFlipped {
		t.Fatalf("FlipOverlays = %d, %v", flip, err)
	}
	c := r.dev.commits[len(r.dev.commits)-1]
	if c.flags&kdrm.AtomicTestOnly != 0 || c.values[[2]uint32{42, propFB}] == 0 || c.values[[2]uint32{42, propCRTCW}] != 640 {
		t.Fatalf("the overlay flip %+v", c)
	}
	if _, touched := c.values[[2]uint32{40, propFB}]; touched {
		t.Fatalf("an overlay flip touched the primary plane: %+v", c)
	}
	r.dev.flip(t, 10, 2)
	r.until("the overlay's flip", func() bool { return !o.flipping })
	if err := o.SetOverlays(nil); err != nil {
		t.Fatal(err)
	}
	if flip, err := o.FlipOverlays(); err != nil || flip != backend.OverlaysFlipped {
		t.Fatalf("clearing the overlay: %d, %v", flip, err)
	}
	if off := r.dev.commits[len(r.dev.commits)-1]; off.values[[2]uint32{42, propFB}] != 0 {
		t.Fatalf("clearing left the overlay on: %+v", off)
	}
	r.dev.flip(t, 10, 3)
	r.until("the flip that takes it off", func() bool { return !o.flipping })
	if released != 1 {
		t.Fatalf("the overlay's buffer was released %d times", released)
	}
}

func TestEveryOverlayUseIsReleasedEvenIfItNeverReachedThePlane(t *testing.T) {
	r, o, client := overlayRig(t, 2)
	key := &fakeBuffer{client}
	dst := image.Rect(100, 100, 740, 460)
	if !testOverlay(o, key, client, dst) {
		t.Fatal("the overlay was refused")
	}
	set, released := 0, 0
	layer := func() []backend.Overlay {
		set++
		return []backend.Overlay{overlayLayer(key, client, dst, func() error { released++; return nil })}
	}
	if err := o.SetOverlays(layer()); err != nil {
		t.Fatal(err)
	}
	if err := o.SetOverlays(layer()); err != nil {
		t.Fatal(err)
	}
	r.dev.refuseOverlay = true
	if flip, err := o.FlipOverlays(); err != nil || flip != backend.OverlaysRefused {
		t.Fatalf("a refused overlay flip: %d, %v", flip, err)
	}
	r.dev.refuseOverlay = false
	if err := o.SetOverlays(layer()); err != nil {
		t.Fatal(err)
	}
	if flip, err := o.FlipOverlays(); err != nil || flip != backend.OverlaysFlipped {
		t.Fatalf("FlipOverlays = %d, %v", flip, err)
	}
	r.dev.flip(t, 10, 2)
	r.until("the overlay's flip", func() bool { return !o.flipping })
	if err := o.SetOverlays(nil); err != nil {
		t.Fatal(err)
	}
	if flip, err := o.FlipOverlays(); err != nil || flip != backend.OverlaysFlipped {
		t.Fatalf("clearing the overlay: %d, %v", flip, err)
	}
	r.dev.flip(t, 10, 3)
	r.until("the flip that takes it off", func() bool { return !o.flipping })
	if released != set {
		t.Fatalf("%d overlay uses were handed to the backend and %d released", set, released)
	}
}
