// Package headless is a backend with virtual outputs rendered in software and
// synthetic input devices, for tests and the compositor-in-the-loop harness.
package headless

import (
	"errors"
	"fmt"
	"image"
	"os"
	"slices"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

type Backend struct {
	shallow       bool
	sdrOnly       bool
	vrr           bool
	loop          *loop.Loop
	renderer      render.Renderer
	closeGPU      func() error
	handler       backend.Handler
	outputs       []*Output
	next          int
	keyboard      *Keyboard
	pointer       *Pointer
	touch         *Touch
	switches      *Switches
	plugged       []backend.Device
	cursorSize    image.Point
	overlayPlanes int
	overlayTarget gpu.ScanoutTarget
	inherit       bool
	closed        bool
	fenceTime     func(fd int) (time.Duration, bool, error)
	report        func(error)
}

type frameFencer interface {
	NativeFences() bool
	SyncFile() (int, error)
}

func New(l *loop.Loop) *Backend {
	b := &Backend{loop: l, renderer: soft.New(), fenceTime: kdrm.SyncFileSignalTime, report: func(err error) { fmt.Fprintln(os.Stderr, "lys-wm:", err) }}
	b.keyboard = &Keyboard{device: device{b, "headless-keyboard", backend.Keyboard}}
	b.pointer = &Pointer{device: device{b, "headless-pointer", backend.Pointer}}
	b.touch = &Touch{device{b, "headless-touch", backend.Touch}}
	b.switches = &Switches{device{b, "headless-switches", 0}}
	return b
}

func NewGLES(l *loop.Loop, paths gpu.Paths) (*Backend, error) {
	lib, err := gpu.Load(paths)
	if err != nil {
		return nil, err
	}
	d, err := lib.Surfaceless()
	if err != nil {
		return nil, fmt.Errorf("headless: %w", err)
	}
	r, err := gles.New(d)
	if err != nil {
		return nil, errors.Join(err, d.Close())
	}
	b := New(l)
	b.renderer, b.closeGPU = r, func() error { return errors.Join(r.Close(), d.Close()) }
	return b, nil
}

func NewVulkan(l *loop.Loop) (*Backend, error) {
	r, err := vulkan.OpenDefault()
	if err != nil {
		return nil, fmt.Errorf("headless: %w", err)
	}
	b := New(l)
	b.renderer, b.closeGPU = r, r.Close
	return b, nil
}

func (o *Output) resize(size image.Point) error {
	if err := o.freeTarget(); err != nil {
		return err
	}
	o.buffer = soft.NewBuffer(size.X, size.Y)
	o.drawn, o.unread = false, false
	if o.backend.closeGPU == nil {
		o.target = o.buffer
		return nil
	}
	t, err := o.backend.renderer.NewTarget(size)
	if err != nil {
		return fmt.Errorf("headless: %s: %w", o.name, err)
	}
	o.target = t
	return nil
}

func (o *Output) gpuTarget() (render.ReadTarget, bool) {
	t, ok := o.target.(render.ReadTarget)
	return t, ok && o.backend.closeGPU != nil
}

func (o *Output) freeTarget() error {
	t, ok := o.gpuTarget()
	o.target = nil
	if !ok {
		return nil
	}
	return t.Destroy()
}

func (b *Backend) Start(h backend.Handler) error {
	if b.handler != nil {
		return errors.New("headless: already started")
	}
	b.handler = h
	for _, d := range b.devices() {
		if err := h.DeviceAdded(d); err != nil {
			return err
		}
	}
	for _, o := range b.outputs {
		if err := h.OutputAdded(o); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) Renderer() render.Renderer {
	return b.renderer
}

func (b *Backend) devices() []backend.Device {
	return append([]backend.Device{b.keyboard, b.pointer, b.touch, b.switches}, b.plugged...)
}

func (b *Backend) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	var errs []error
	for len(b.outputs) > 0 {
		errs = append(errs, b.RemoveOutput(b.outputs[0]))
	}
	if b.handler != nil {
		for _, d := range b.devices() {
			errs = append(errs, b.handler.DeviceRemoved(d))
		}
	}
	if b.closeGPU != nil {
		errs = append(errs, b.closeGPU())
	}
	return errors.Join(errs...)
}

func (b *Backend) Outputs() []*Output {
	return slices.Clone(b.outputs)
}

func (b *Backend) InheritFrames() {
	b.inherit = true
}

func (b *Backend) AddOutput(modes ...backend.Mode) (*Output, error) {
	return b.AddNamedOutput(fmt.Sprintf("HEADLESS-%d", b.next+1), modes...)
}

func (b *Backend) AddNamedOutput(name string, modes ...backend.Mode) (*Output, error) {
	if len(modes) == 0 {
		return nil, errors.New("headless: an output needs at least one mode")
	}
	current := modes[0]
	for _, m := range modes {
		if err := m.Valid(); err != nil {
			return nil, err
		}
		if m.Preferred {
			current = m
		}
	}
	b.next++
	o := &Output{
		backend:     b,
		name:        name,
		modes:       slices.Clone(modes),
		state:       backend.OutputState{Power: backend.PoweredOn, Mode: current},
		ctm:         backend.Identity,
		inherited:   b.inherit,
		renderFence: -1,
	}
	o.clock = backend.NewFrameClock(b.loop.Idle, o.frameReady, o.runFrame, &backend.Latch{
		Wait: func() time.Duration {
			now := backend.Now()
			if o.vrr && o.frameCap > 0 && o.lastVblank != 0 {
				return max(o.lastVblank+o.paceInterval()-now, 0)
			}
			return backend.HoldToCap(o.budget.Deadline(o.lastVblank, now, o.paceInterval()), o.lastVblank, o.paceInterval(), o.state.Mode.Interval()) - now
		},
		Wake: func(after time.Duration) error { return o.latch.Arm(after) },
	})
	if err := o.resize(current.Size); err != nil {
		return nil, err
	}
	if b.cursorSize != (image.Point{}) {
		o.cursor = &Cursor{o: o}
	}
	var err error
	if o.timer, err = b.loop.AddTimer(o.vblank); err != nil {
		return nil, errors.Join(err, o.freeTarget())
	}
	if o.latch, err = b.loop.AddTimer(o.clock.Woke); err != nil {
		return nil, errors.Join(err, o.timer.Remove(), o.freeTarget())
	}
	b.outputs = append(b.outputs, o)
	if b.handler != nil {
		if err := b.handler.OutputAdded(o); err != nil {
			return nil, err
		}
	}
	return o, nil
}

func (b *Backend) RemoveOutput(o *Output) error {
	i := slices.Index(b.outputs, o)
	if i < 0 {
		return fmt.Errorf("headless: %s is not an output of this backend", o.name)
	}
	b.outputs = slices.Delete(b.outputs, i, i+1)
	o.removed = true
	var errs []error
	if b.handler != nil {
		errs = append(errs, b.handler.OutputRemoved(o))
	}
	errs = append(errs, o.releaseClientBuffers(), o.timer.Remove(), o.latch.Remove(), o.dropFence(), o.freeTarget())
	if o.cursor != nil {
		errs = append(errs, o.cursor.destroy())
	}
	return errors.Join(errs...)
}

var (
	_ backend.Inheriting     = (*Output)(nil)
	_ backend.Scanner        = (*Output)(nil)
	_ backend.Gamma          = (*Output)(nil)
	_ backend.ColorTransform = (*Output)(nil)
)

type Output struct {
	backend        *Backend
	name           string
	modes          []backend.Mode
	state          backend.OutputState
	buffer         *soft.Buffer
	target         render.Target
	drawn          bool
	unread         bool
	timer          *loop.Timer
	latch          *loop.Timer
	budget         backend.RenderBudget
	frameStart     time.Duration
	aimedAt        time.Duration
	renderFence    int
	fenceStart     time.Duration
	untimed        bool
	armed          bool
	clock          *backend.FrameClock
	presentPending bool
	empties        int
	inFrame        bool
	lastVblank     time.Duration
	nextVblank     time.Duration
	seq            uint64
	nextSeq        uint64
	presents       uint64
	frames         uint64
	damage         render.Region
	removed        bool
	cursor         *Cursor
	scanout        *gpu.ScanoutTarget
	scanouts       uint64
	shownRelease   func() error
	queuedRelease  func() error
	gammaSize      int
	gamma          []uint16
	hasCTM         bool
	vrr            bool
	ctm            backend.Matrix
	queuedTorn     bool
	torn           uint64
	tearFrame      bool
	filled         bool
	frameCap       int
	hdrCommits     uint64
	degammaSize    int
	degamma        []uint16
	colorSets      ColorSets
	scannedAt      backend.Placement
	overlayNext    overlayLayers
	overlayQueued  overlayLayers
	overlayShown   overlayLayers
	overlayLatched bool
	overlayFlips   uint64
	hardwareClock  bool
	contentType    wm.ContentType
	failPresents   error
	loseInFrame    bool
	inherited      bool
	takeover       *soft.Buffer
}

func (o *Output) Inherited() bool {
	return o.inherited
}

func (o *Output) Takeover() *soft.Buffer {
	return o.takeover
}

func (o *Output) SetGammaSize(n int) {
	o.gammaSize = n
}

func (o *Output) GammaSize() int {
	return o.gammaSize
}

func (o *Output) SetGamma(ramps []uint16) error {
	if ramps != nil && len(ramps) != 3*o.gammaSize {
		return fmt.Errorf("headless: %s takes 3 ramps of %d gamma entries, not %d entries", o.name, o.gammaSize, len(ramps))
	}
	o.gamma = slices.Clone(ramps)
	o.colorSets.Gamma++
	return nil
}

func (o *Output) Gamma() []uint16 {
	return slices.Clone(o.gamma)
}

type ColorSets struct {
	Gamma, Degamma, CTM int
}

func (o *Output) ColorSets() ColorSets {
	return o.colorSets
}

func (o *Output) SetDegammaSize(n int) {
	o.degammaSize = n
}

func (o *Output) DegammaSize() int {
	return o.degammaSize
}

func (o *Output) SetDegamma(ramps []uint16) error {
	if ramps != nil && len(ramps) != 3*o.degammaSize {
		return fmt.Errorf("headless: %s takes 3 ramps of %d degamma entries, not %d entries", o.name, o.degammaSize, len(ramps))
	}
	o.degamma = slices.Clone(ramps)
	o.colorSets.Degamma++
	return nil
}

func (o *Output) Degamma() []uint16 {
	return slices.Clone(o.degamma)
}

func (b *Backend) EnableAdaptiveSync() {
	b.vrr = true
}

func (o *Output) AdaptiveSyncCapable() bool {
	return o.backend.vrr
}

func (o *Output) AdaptiveSync() bool {
	return o.vrr
}

func (o *Output) SetAdaptiveSync(on bool) error {
	o.vrr = on && o.backend.vrr
	return nil
}

func (o *Output) EnableColorTransform() {
	o.hasCTM = true
}

func (o *Output) HasColorTransform() bool {
	return o.hasCTM
}

func (o *Output) SetColorTransform(m backend.Matrix) error {
	if !o.hasCTM {
		return fmt.Errorf("headless: %s has no colour transform", o.name)
	}
	o.ctm = m
	o.colorSets.CTM++
	return nil
}

func (o *Output) ColorTransform() backend.Matrix {
	return o.ctm
}

func (o *Output) Name() string {
	return o.name
}

func (o *Output) Description() string {
	return "Headless output " + o.name
}

func (*Output) Identity() edid.Info {
	return edid.Info{}
}

func (o *Output) PhysicalSize() image.Point {
	return image.Point{}
}

func (o *Output) Modes() []backend.Mode {
	return slices.Clone(o.modes)
}

func (b *Backend) RefuseDepth10() {
	b.shallow = true
}

func (o *Output) State() backend.OutputState {
	return o.state
}

type noWorkingSpace struct {
	render.Renderer
}

func (noWorkingSpace) NewWorkingTarget(image.Point) (render.TextureTarget, error) {
	return nil, errors.New("headless: this renderer has no float targets")
}

func (b *Backend) RefuseWorkingSpace() {
	b.renderer = noWorkingSpace{b.renderer}
}

func (b *Backend) RefuseHDR() {
	b.sdrOnly = true
}

func (o *Output) HDRCapabilities() (edid.HDR, error) {
	if o.backend.sdrOnly {
		return edid.HDR{}, fmt.Errorf("headless: %s: %w", o.name, backend.ErrHDRUnsupported)
	}
	return edid.HDR{
		Red: edid.Chromaticity{X: 0.68, Y: 0.32}, Green: edid.Chromaticity{X: 0.265, Y: 0.69}, Blue: edid.Chromaticity{X: 0.15, Y: 0.06}, White: edid.Chromaticity{X: 0.3127, Y: 0.329},
		ST2084: true, BT2020: true, StaticMetadata: true, MaxLuminance: 1000, MaxAverageLuminance: 400, MinLuminance: 0.005,
	}, nil
}

func (o *Output) Commit(s backend.OutputState) error {
	if o.removed {
		return fmt.Errorf("headless: commit to removed output %s", o.name)
	}
	if s.Enabled() && s.Depth == backend.Depth10 && o.backend.shallow {
		return fmt.Errorf("headless: %s: %w", o.name, backend.ErrDepthUnsupported)
	}
	if s.HDR.Active && o.backend.sdrOnly {
		return fmt.Errorf("headless: %s: %w", o.name, backend.ErrHDRUnsupported)
	}
	if s.Enabled() {
		if err := s.Mode.Valid(); err != nil {
			return fmt.Errorf("headless: %s: %w", o.name, err)
		}
		if s.Mode.Size != o.buffer.Size() {
			if err := o.resize(s.Mode.Size); err != nil {
				return err
			}
		}
	}
	if !s.Lit() || s.Mode != o.state.Mode {
		o.inherited = false
	}
	if s.HDR != o.state.HDR {
		o.hdrCommits++
	}
	var err error
	if !s.Lit() {
		o.clock.Cancel()
		o.presentPending = false
		err = o.releaseClientBuffers()
	}
	o.state = s
	return err
}

func (o *Output) releaseClientBuffers() error {
	var errs []error
	for _, release := range []func() error{o.shownRelease, o.queuedRelease} {
		if release != nil {
			errs = append(errs, release())
		}
	}
	o.shownRelease, o.queuedRelease, o.queuedTorn = nil, nil, false
	errs = append(errs, o.releaseOverlays())
	return errors.Join(errs...)
}

func (o *Output) ScheduleFrame() error {
	if !o.state.Lit() || o.removed {
		return nil
	}
	return o.clock.Request()
}

func (o *Output) frameReady() bool {
	return o.backend.handler != nil && !o.presentPending && o.empties < backend.EmptyFramesPerRefresh && !o.inFrame && !o.removed && o.state.Lit()
}

func (o *Output) runFrame() error {
	o.frames++
	o.budget.Started()
	o.frameStart = backend.Now()
	o.aimedAt, _ = backend.NextVblank(o.lastVblank, o.frameStart, o.paceInterval())
	if err := o.backend.handler.Frame(o); err != nil {
		return err
	}
	if !o.presentPending {
		o.empties++
	}
	return o.arm()
}

func (o *Output) BeginFrame() (backend.Frame, error) {
	switch {
	case o.removed:
		return backend.Frame{}, fmt.Errorf("headless: frame on removed output %s", o.name)
	case !o.state.Enabled():
		return backend.Frame{}, fmt.Errorf("headless: frame on disabled output %s", o.name)
	case o.inFrame:
		return backend.Frame{}, backend.Broken("headless: %s already has a frame in progress", o.name)
	}
	o.inFrame = true
	if o.loseInFrame {
		o.loseInFrame = false
		o.backend.renderer.LoseDevice()
	}
	age := 0
	if o.drawn {
		age = 1
	}
	return backend.Frame{Renderer: o.backend.renderer, Target: o.target, Age: age}, nil
}

func (o *Output) AbortFrame() {
	o.inFrame = false
}

func (o *Output) ExpectFrameWork(pixels int) {
	o.budget.Expect(pixels)
}

func (o *Output) Present(damage render.Region) error {
	o.budget.Painted(damage.Area())
	if !o.inFrame {
		return backend.Broken("headless: present on %s without BeginFrame", o.name)
	}
	o.inFrame = false
	if o.failPresents != nil {
		return o.failPresents
	}
	o.drawn = true
	o.damage = append(o.damage[:0], damage...)
	_, o.unread = o.gpuTarget()
	if o.inherited {
		snap, err := o.Primary()
		if err != nil {
			return err
		}
		o.takeover, o.inherited = snap, false
	}
	o.latchOverlays()
	o.presentPending, o.queuedTorn = true, false
	o.presents++
	o.fenceFrame()
	return o.arm()
}

func (o *Output) noTiming(cause error) {
	o.untimed = true
	o.backend.report(fmt.Errorf("headless: %s renders on the half-refresh latch, its frames report no time: %w", o.name, cause))
}

func (o *Output) dropFence() error {
	if o.renderFence < 0 {
		return nil
	}
	fd := o.renderFence
	o.renderFence = -1
	return unix.Close(fd)
}

func (o *Output) StopTiming() {
	o.untimed = true
}

func (o *Output) fenceFrame() {
	f, ok := o.backend.renderer.(frameFencer)
	switch {
	case !ok:
		o.budget.Sample(backend.Now() - o.frameStart)
		return
	case o.untimed:
		return
	case !f.NativeFences():
		o.noTiming(errors.New("the renderer has no native fences"))
		return
	}
	o.settleFence(backend.Now())
	if err := o.dropFence(); err != nil {
		o.backend.report(fmt.Errorf("headless: close the render fence of %s: %w", o.name, err))
	}
	if o.untimed {
		return
	}
	fd, err := f.SyncFile()
	switch {
	case err != nil:
		o.noTiming(err)
	case fd < 0:
		o.budget.Sample(backend.Now() - o.frameStart)
	default:
		o.renderFence, o.fenceStart = fd, o.frameStart
	}
}

func (o *Output) settleFence(now time.Duration) {
	if o.renderFence < 0 {
		return
	}
	done, ok, err := o.backend.fenceTime(o.renderFence)
	switch {
	case err != nil:
		o.noTiming(err)
	case !ok:
		o.budget.Sample(now - o.fenceStart)
	case done > o.fenceStart:
		o.budget.Sample(done - o.fenceStart)
	}
}

func (o *Output) timeFrame(flip time.Duration, flipped bool) error {
	interval := o.paceInterval()
	missed := flipped && o.aimedAt != 0 && flip > o.aimedAt+interval/2
	if flipped {
		o.aimedAt = 0
	}
	if o.renderFence < 0 || o.untimed {
		if missed {
			o.budget.Missed(interval)
		}
		return nil
	}
	done, ok, err := o.backend.fenceTime(o.renderFence)
	if err != nil {
		o.noTiming(err)
	}
	if missed || flipped && err == nil && (!ok || done > flip) {
		o.budget.Missed(interval)
	}
	if !ok && err == nil {
		return nil
	}
	if ok && done > o.fenceStart {
		o.budget.Sample(done - o.fenceStart)
	}
	if err := o.dropFence(); err != nil {
		return fmt.Errorf("headless: close the render fence of %s: %w", o.name, err)
	}
	return nil
}

func (o *Output) AllowScanout(t gpu.ScanoutTarget) {
	o.scanout = &t
}

func (o *Output) ScanoutFormats() gpu.ScanoutTarget {
	if o.scanout == nil {
		return gpu.ScanoutTarget{}
	}
	return *o.scanout
}

func (o *Output) Scanouts() uint64 {
	return o.scanouts
}

func (o *Output) ScannedAt() backend.Placement {
	return o.scannedAt
}

func (o *Output) ShowsClientBuffer() bool {
	return o.shownRelease != nil || o.queuedRelease != nil
}

func (o *Output) ForgetScanout(backend.ScanoutKey) error {
	return nil
}

func (o *Output) Scanout(_ backend.ScanoutKey, d gpu.DMABUF, at backend.Placement, release func() error, tear bool) (bool, error) {
	if o.scanout == nil || !slices.Contains(o.scanout.Formats, gpu.FormatModifier{Format: d.Format, Modifier: d.Modifier}) || o.removed || !o.state.Lit() || o.inFrame || o.presentPending || !at.Valid(d.Size, o.state.Mode.Size) {
		return false, nil
	}
	o.queuedRelease, o.presentPending, o.drawn, o.queuedTorn, o.inherited = release, true, false, tear, false
	o.presents++
	o.scanouts++
	o.scannedAt = at
	if tear {
		o.torn++
	}
	return true, o.arm()
}

func (o *Output) Torn() uint64 {
	return o.torn
}

func (o *Output) EnableHardwareClock() {
	o.hardwareClock = true
}

func (o *Output) TearFrame(tear bool) {
	o.tearFrame = tear
}

func (o *Output) TearsFrames() bool {
	return o.tearFrame
}

func (o *Output) FillFrame(filled bool) {
	o.filled = filled
}

func (o *Output) FillsFrame() bool {
	return o.filled
}

func (o *Output) HDRCommits() uint64 {
	return o.hdrCommits
}

func (o *Output) CapFrameRate(hz int) {
	o.frameCap = hz
}

func (o *Output) paceInterval() time.Duration {
	return o.state.Mode.PacedInterval(o.frameCap, o.vrr)
}

func (o *Output) SetContentType(t wm.ContentType) {
	o.contentType = t
}

func (o *Output) ContentType() wm.ContentType {
	return o.contentType
}

func (o *Output) FailPresents(err error) {
	o.failPresents = err
}

func (o *Output) LoseDeviceInFrame() {
	o.loseInFrame = true
}

func (o *Output) Presents() uint64 {
	return o.presents
}

func (o *Output) Frames() uint64 {
	return o.frames
}

func (o *Output) FrameOwed() bool {
	return o.clock.Requested() || o.inFrame || o.presentPending
}

func (o *Output) LastDamage() render.Region {
	return slices.Clone(o.damage)
}

func (o *Output) Primary() (*soft.Buffer, error) {
	if t, ok := o.gpuTarget(); ok && o.unread {
		if err := t.Read(o.buffer.Pix, o.buffer.Stride); err != nil {
			return nil, fmt.Errorf("headless: read back %s: %w", o.name, err)
		}
		o.unread = false
	}
	return &soft.Buffer{Pix: slices.Clone(o.buffer.Pix), Stride: o.buffer.Stride, W: o.buffer.W, H: o.buffer.H}, nil
}

func (o *Output) Snapshot() (*soft.Buffer, error) {
	snap, err := o.Primary()
	if err != nil {
		return nil, err
	}
	if o.cursor != nil {
		o.cursor.composite(snap)
	}
	return snap, nil
}

func (o *Output) arm() error {
	if o.armed {
		return nil
	}
	now := backend.Now()
	at, ticks := backend.NextVblank(o.lastVblank, now, o.state.Mode.Interval())
	o.nextVblank, o.nextSeq = at, o.seq+ticks
	if err := o.timer.Arm(o.nextVblank - now); err != nil {
		return fmt.Errorf("headless: arm frame timer for %s: %w", o.name, err)
	}
	o.armed = true
	return nil
}

func (o *Output) vblank() error {
	o.armed = false
	o.lastVblank, o.seq = o.nextVblank, o.nextSeq
	idle := o.empties > 0
	o.empties = 0
	if o.state.Enabled() {
		if err := o.timeFrame(o.lastVblank, o.presentPending); err != nil {
			return err
		}
	}
	switch {
	case o.presentPending:
		if err := o.present(); err != nil {
			return err
		}
	case idle && o.backend.handler != nil:
		if err := o.backend.handler.Presented(o, backend.Presentation{Time: o.lastVblank, Refresh: o.paceInterval(), Seq: o.seq, Idle: true}); err != nil {
			return err
		}
	}
	return o.clock.Kick()
}

func (o *Output) present() error {
	h := o.backend.handler
	if h == nil {
		return nil
	}
	if err := o.overlaysFlipped(); err != nil {
		return err
	}
	o.presentPending = false
	left := o.shownRelease
	o.shownRelease, o.queuedRelease = o.queuedRelease, nil
	if left != nil {
		if err := left(); err != nil {
			return err
		}
	}
	torn := o.queuedTorn
	o.queuedTorn = false
	return h.Presented(o, backend.Presentation{Time: o.lastVblank, Refresh: o.paceInterval(), Seq: o.seq, Torn: torn, Hardware: o.hardwareClock, Variable: o.vrr})
}

type device struct {
	backend *Backend
	name    string
	caps    backend.Capability
}

func (d *device) Name() string {
	return d.name
}

func (d *device) Capabilities() backend.Capability {
	return d.caps
}

func (b *Backend) emit(d backend.Device, e backend.Event) error {
	if b.handler == nil {
		return errors.New("headless: input injected before Start")
	}
	return b.handler.Input(d, e)
}

func now() time.Duration {
	return backend.Now()
}

type Keyboard struct {
	device
	leds xkb.Locks
}

var _ backend.KeyboardLEDs = (*Keyboard)(nil)

func (k *Keyboard) SetLEDs(l xkb.Locks) {
	k.leds = l
}

func (k *Keyboard) LEDs() xkb.Locks {
	return k.leds
}

func (b *Backend) Keyboard() *Keyboard {
	return b.keyboard
}

func (b *Backend) PlugKeyboard(name string) (*Keyboard, error) {
	if b.handler == nil {
		return nil, errors.New("headless: a keyboard plugged in before Start")
	}
	k := &Keyboard{device: device{b, name, backend.Keyboard}}
	b.plugged = append(b.plugged, k)
	return k, b.handler.DeviceAdded(k)
}

func (k *Keyboard) Key(code uint32, pressed bool) error {
	return k.backend.emit(k, backend.Key{Time: now(), Code: code, Pressed: pressed})
}

type Pointer struct {
	device
	swipe backend.SwipeUpdate
	pinch backend.PinchUpdate
}

func (b *Backend) Pointer() *Pointer {
	return b.pointer
}

func (p *Pointer) Move(dx, dy float64) error {
	return p.backend.emit(p, &backend.PointerMotion{Time: now(), DX: dx, DY: dy, UnaccelDX: dx, UnaccelDY: dy})
}

func (p *Pointer) MoveTo(o backend.Output, x, y float64) error {
	return p.backend.emit(p, backend.PointerMotionAbsolute{Time: now(), Output: o, X: x, Y: y})
}

func (p *Pointer) Button(button uint32, pressed bool) error {
	return p.backend.emit(p, backend.PointerButton{Time: now(), Button: button, Pressed: pressed})
}

func (p *Pointer) Axis(orientation backend.Orientation, source backend.AxisSource, value float64, value120 int32) error {
	return p.backend.emit(p, backend.PointerAxis{Time: now(), Orientation: orientation, Source: source, Value: value, Value120: value120})
}

func (p *Pointer) Frame() error {
	return p.backend.emit(p, &backend.PointerFrame{Time: now()})
}

func (p *Pointer) GestureBegin(g backend.Gesture, fingers uint32) error {
	return p.backend.emit(p, backend.GestureBegin{Time: now(), Gesture: g, Fingers: fingers})
}

func (p *Pointer) Swipe(dx, dy float64) error {
	p.swipe = backend.SwipeUpdate{Time: now(), DX: dx, DY: dy, UnaccelDX: dx, UnaccelDY: dy}
	return p.backend.emit(p, &p.swipe)
}

func (p *Pointer) SwipeAt(at time.Duration, dx, dy float64) error {
	p.swipe = backend.SwipeUpdate{Time: at, DX: dx, DY: dy, UnaccelDX: dx, UnaccelDY: dy}
	return p.backend.emit(p, &p.swipe)
}

func (p *Pointer) Pinch(dx, dy, scale, rotation float64) error {
	p.pinch = backend.PinchUpdate{Time: now(), DX: dx, DY: dy, Scale: scale, Rotation: rotation}
	return p.backend.emit(p, &p.pinch)
}

func (p *Pointer) GestureEnd(g backend.Gesture, cancelled bool) error {
	return p.backend.emit(p, backend.GestureEnd{Time: now(), Gesture: g, Cancelled: cancelled})
}

func (p *Pointer) GestureEndAt(at time.Duration, g backend.Gesture, cancelled bool) error {
	return p.backend.emit(p, backend.GestureEnd{Time: at, Gesture: g, Cancelled: cancelled})
}

type Switches struct{ device }

func (b *Backend) Switches() *Switches {
	return b.switches
}

func (s *Switches) Toggle(kind backend.SwitchKind, on bool) error {
	return s.backend.emit(s, backend.Switch{Time: now(), Kind: kind, On: on})
}

type Touch struct{ device }

func (b *Backend) Touch() *Touch {
	return b.touch
}

func (t *Touch) Down(id int32, o backend.Output, x, y float64) error {
	return t.backend.emit(t, backend.TouchDown{Time: now(), ID: id, Output: o, X: x, Y: y})
}

func (t *Touch) Motion(id int32, x, y float64) error {
	return t.backend.emit(t, backend.TouchMotion{Time: now(), ID: id, X: x, Y: y})
}

func (t *Touch) DownAt(at time.Duration, id int32, o backend.Output, x, y float64) error {
	return t.backend.emit(t, backend.TouchDown{Time: at, ID: id, Output: o, X: x, Y: y})
}

func (t *Touch) MotionAt(at time.Duration, id int32, x, y float64) error {
	return t.backend.emit(t, backend.TouchMotion{Time: at, ID: id, X: x, Y: y})
}

func (t *Touch) UpAt(at time.Duration, id int32) error {
	return t.backend.emit(t, backend.TouchUp{Time: at, ID: id})
}

func (t *Touch) Up(id int32) error {
	return t.backend.emit(t, backend.TouchUp{Time: now(), ID: id})
}

func (t *Touch) Cancel(id int32) error {
	return t.backend.emit(t, backend.TouchCancel{Time: now(), ID: id})
}

func (t *Touch) Frame() error {
	return t.backend.emit(t, backend.TouchFrame{Time: now()})
}
