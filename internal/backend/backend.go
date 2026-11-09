// Package backend abstracts outputs and input devices so the compositor runs
// headless, nested or on DRM.
package backend

import (
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/stubbedev/lys-lib/edid"
	lgpu "github.com/stubbedev/lys-lib/gpu"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

func NextVblank(last, now, interval time.Duration) (at time.Duration, ticks uint64) {
	if last == 0 || last > now {
		last = now
	}
	ticks = uint64((now-last)/interval) + 1            //nolint:gosec // last is never after now
	return last + time.Duration(ticks)*interval, ticks //nolint:gosec // a tick count fits a duration
}

type Backend interface {
	Start(h Handler) error
	Renderer() render.Renderer
	Close() error
}

type Handler interface {
	OutputAdded(o Output) error
	OutputRemoved(o Output) error
	OutputChanged(o Output) error
	OutputDamaged(o Output) error
	Frame(o Output) error
	Presented(o Output, p Presentation) error
	DeviceAdded(d Device) error
	DeviceRemoved(d Device) error
	Input(d Device, e Event) error
}

type Mode struct {
	Size       image.Point
	RefreshMHz int32
	Preferred  bool
}

func (m Mode) Valid() error {
	if m.Size.X <= 0 || m.Size.Y <= 0 || m.RefreshMHz <= 0 {
		return fmt.Errorf("backend: invalid mode %dx%d@%dmHz", m.Size.X, m.Size.Y, m.RefreshMHz)
	}
	return nil
}

func (m Mode) Interval() time.Duration {
	return time.Duration(int64(time.Second) * 1000 / int64(m.RefreshMHz))
}

func (m Mode) PacedInterval(capHz int, variable bool) time.Duration {
	interval := m.Interval()
	switch {
	case capHz <= 0:
		return interval
	case variable:
		return max(interval, time.Second/time.Duration(capHz))
	}
	capMHz := int64(capHz) * 1000
	return time.Duration((int64(m.RefreshMHz)+capMHz-1)/capMHz) * interval
}

type Power uint8

const (
	Disabled Power = iota
	PoweredOn
	PoweredOff
)

type BitDepth uint8

const (
	Depth8 BitDepth = iota
	Depth10
)

type HDRMetadata struct {
	Active                  bool
	Red, Green, Blue, White edid.Chromaticity
	MaxLuminance            float64
	MinLuminance            float64
	MaxCLL                  float64
	MaxFALL                 float64
}

type OutputState struct {
	Power Power
	Mode  Mode
	Depth BitDepth
	HDR   HDRMetadata
}

func (s OutputState) Enabled() bool {
	return s.Power != Disabled
}

func (s OutputState) Lit() bool {
	return s.Power == PoweredOn
}

type Frame struct {
	Renderer render.Renderer
	Target   render.Target
	Age      int
}

type Presentation struct {
	Time     time.Duration
	Refresh  time.Duration
	Seq      uint64
	Torn     bool
	Idle     bool
	Hardware bool
	Variable bool
}

const EmptyFramesPerRefresh = 2

var ErrNoCursorPlanes = errors.New("backend: this backend draws the cursor in the frame")

var ErrCursorReadback = errors.New("backend: drawing this cursor image on the plane would read it back from the GPU synchronously")

var ErrDepthUnsupported = errors.New("backend: the output cannot show frames at this bit depth")

var ErrHDRUnsupported = errors.New("backend: the output cannot carry an HDR signal")

var (
	ErrDeviceLost = errors.New("backend: the device is gone")
	ErrBroken     = errors.New("backend: broken invariant")
)

func Fatal(err error) bool {
	return errors.Is(err, ErrDeviceLost) || errors.Is(err, ErrBroken) || errors.Is(err, unix.ENODEV)
}

func GPULost(err error) bool {
	return errors.Is(err, gpu.ErrContextLost) || errors.Is(err, lgpu.ErrDeviceLost)
}

func Broken(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBroken}, args...)...)
}

type ScanoutKey interface {
	DMABUF() gpu.DMABUF
}

type Placement struct {
	Src render.FRect
	Dst image.Rectangle
}

func Whole(size image.Point) Placement {
	return Placement{Src: render.FRect{W: float64(size.X), H: float64(size.Y)}, Dst: image.Rectangle{Max: size}}
}

func (p Placement) Valid(buffer, output image.Point) bool {
	s := p.Src
	return !p.Dst.Empty() && p.Dst.In(image.Rectangle{Max: output}) && s.W > 0 && s.H > 0 && s.X >= 0 && s.Y >= 0 && s.X+s.W <= float64(buffer.X) && s.Y+s.H <= float64(buffer.Y)
}

type Scanner interface {
	Scanout(key ScanoutKey, d gpu.DMABUF, at Placement, release func() error, tear bool) (bool, error)
	ForgetScanout(key ScanoutKey) error
	ScanoutFormats() gpu.ScanoutTarget
}

const MaxOverlayPlanes = 3

type Overlay struct {
	Key     ScanoutKey
	Buffer  gpu.DMABUF
	At      Placement
	Release func() error
}

type OverlayPlanes interface {
	OverlayPlanes() int
	OverlayTarget() gpu.ScanoutTarget
	TestOverlays(layers []Overlay) uint
	SetOverlays(layers []Overlay) error
	FlipOverlays() (OverlayFlip, error)
}

type OverlayFlip uint8

const (
	OverlaysNotFlipped OverlayFlip = iota
	OverlaysFlipped
	OverlaysRefused
)

type CursorPlane interface {
	CursorSize() image.Point
	BeginCursor() (Frame, error)
	SetCursor(hotspot image.Point) error
	LoadCursor(pix []byte, stride int, size, hotspot image.Point) error
	MoveCursor(at image.Point) error
	HideCursor() error
}

type Output interface {
	Name() string
	Description() string
	Identity() edid.Info
	PhysicalSize() image.Point
	Modes() []Mode
	State() OutputState
	Commit(s OutputState) error
	ScheduleFrame() error
	BeginFrame() (Frame, error)
	AbortFrame()
	Present(damage render.Region) error
	CursorPlane() (CursorPlane, error)
}

type Gamma interface {
	GammaSize() int
	SetGamma(ramps []uint16) error
}

type Matrix [9]float64

var Identity = Matrix{1, 0, 0, 0, 1, 0, 0, 0, 1}

type Degamma interface {
	DegammaSize() int
	SetDegamma(ramps []uint16) error
}

type ColorTransform interface {
	HasColorTransform() bool
	SetColorTransform(m Matrix) error
}

type HDRDisplay interface {
	HDRCapabilities() (edid.HDR, error)
}

type Inheriting interface {
	Inherited() bool
}

type ContentTyped interface {
	SetContentType(t wm.ContentType)
}

type FrameTearing interface {
	TearFrame(tear bool)
}

type FrameFilling interface {
	FillFrame(filled bool)
}

type FrameRateCap interface {
	CapFrameRate(hz int)
}

type AdaptiveSync interface {
	AdaptiveSyncCapable() bool
	AdaptiveSync() bool
	SetAdaptiveSync(on bool) error
}

func AdaptiveSyncCapable(o Output) bool {
	a, ok := o.(AdaptiveSync)
	return ok && a.AdaptiveSyncCapable()
}

type Capability uint8

const (
	Keyboard Capability = 1 << iota
	Pointer
	Touch
)

func (c Capability) Has(flag Capability) bool {
	return c&flag == flag
}

type Device interface {
	Name() string
	Capabilities() Capability
}

type KeyboardLEDs interface {
	SetLEDs(l xkb.Locks)
}

type Event interface {
	Timestamp() time.Duration
}

type Key struct {
	Time    time.Duration
	Code    uint32
	Pressed bool
}

type PointerMotion struct {
	Time                 time.Duration
	DX, DY               float64
	UnaccelDX, UnaccelDY float64
}

type PointerMotionAbsolute struct {
	Time   time.Duration
	Output Output
	X, Y   float64
}

type PointerButton struct {
	Time    time.Duration
	Button  uint32
	Pressed bool
}

type Orientation uint8

const (
	Vertical Orientation = iota
	Horizontal
)

type AxisSource uint8

const (
	Wheel AxisSource = iota
	Finger
	Continuous
	WheelTilt
)

type PointerAxis struct {
	Time        time.Duration
	Orientation Orientation
	Source      AxisSource
	Value       float64
	Value120    int32
}

type PointerFrame struct {
	Time time.Duration
}

type TouchDown struct {
	Time   time.Duration
	ID     int32
	Output Output
	X, Y   float64
}

type TouchMotion struct {
	Time time.Duration
	ID   int32
	X, Y float64
}

type TouchUp struct {
	Time time.Duration
	ID   int32
}

type TouchCancel struct {
	Time time.Duration
	ID   int32
}

type TouchFrame struct {
	Time time.Duration
}

type Gesture uint8

const (
	Swipe Gesture = iota + 1
	Pinch
	Hold
)

type GestureBegin struct {
	Time    time.Duration
	Gesture Gesture
	Fingers uint32
}

type SwipeUpdate struct {
	Time                 time.Duration
	DX, DY               float64
	UnaccelDX, UnaccelDY float64
}

type PinchUpdate struct {
	Time     time.Duration
	DX, DY   float64
	Scale    float64
	Rotation float64
}

type GestureEnd struct {
	Time      time.Duration
	Gesture   Gesture
	Cancelled bool
}

type SwitchKind uint8

const (
	SwitchLid SwitchKind = iota + 1
	SwitchTabletMode
)

type Switch struct {
	Time time.Duration
	Kind SwitchKind
	On   bool
}

func (e Switch) Timestamp() time.Duration { return e.Time }

func (e Key) Timestamp() time.Duration                   { return e.Time }
func (e *PointerMotion) Timestamp() time.Duration        { return e.Time }
func (e PointerMotionAbsolute) Timestamp() time.Duration { return e.Time }
func (e PointerButton) Timestamp() time.Duration         { return e.Time }
func (e PointerAxis) Timestamp() time.Duration           { return e.Time }
func (e *PointerFrame) Timestamp() time.Duration         { return e.Time }
func (e TouchDown) Timestamp() time.Duration             { return e.Time }
func (e TouchMotion) Timestamp() time.Duration           { return e.Time }
func (e TouchUp) Timestamp() time.Duration               { return e.Time }
func (e TouchCancel) Timestamp() time.Duration           { return e.Time }
func (e TouchFrame) Timestamp() time.Duration            { return e.Time }
func (e GestureBegin) Timestamp() time.Duration          { return e.Time }
func (e *SwipeUpdate) Timestamp() time.Duration          { return e.Time }
func (e *PinchUpdate) Timestamp() time.Duration          { return e.Time }
func (e GestureEnd) Timestamp() time.Duration            { return e.Time }
