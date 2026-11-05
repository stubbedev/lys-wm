// Package tabletinput carries drawing tablets and the events of their
// tools from the input backend to the compositor.
package tabletinput

import (
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
)

type ToolType uint8

const (
	Pen ToolType = iota + 1
	Eraser
	Brush
	Pencil
	Airbrush
	Mouse
	Lens
	Totem
)

type Capability uint8

const (
	CanTilt Capability = 1 << iota
	CanPressure
	CanDistance
	CanRotation
	CanSlider
	CanWheel
)

func (c Capability) Has(flag Capability) bool {
	return c&flag == flag
}

type Tool struct {
	Type       ToolType
	Serial     uint64
	HardwareID uint64
	Caps       Capability
}

type ID struct {
	Vendor, Product uint32
}

type Tablet interface {
	backend.Device
	Tablet() (ID, bool)
}

type Axis uint16

const (
	AxisX Axis = 1 << iota
	AxisY
	AxisPressure
	AxisDistance
	AxisTiltX
	AxisTiltY
	AxisRotation
	AxisSlider
	AxisWheel
)

func (a Axis) Any(flags Axis) bool {
	return a&flags != 0
}

type Axes struct {
	Changed            Axis
	X, Y               float64
	Pressure, Distance float64
	TiltX, TiltY       float64
	Rotation           float64
	Slider             float64
	Wheel              float64
	WheelClicks        int32
}

type Proximity struct {
	Time time.Duration
	Tool *Tool
	In   bool
	Axes Axes
}

type Motion struct {
	Time time.Duration
	Tool *Tool
	Axes Axes
}

type Tip struct {
	Time time.Duration
	Tool *Tool
	Down bool
	Axes Axes
}

type Button struct {
	Time    time.Duration
	Tool    *Tool
	Button  uint32
	Pressed bool
}

func (e Proximity) Timestamp() time.Duration { return e.Time }
func (e Motion) Timestamp() time.Duration    { return e.Time }
func (e Tip) Timestamp() time.Duration       { return e.Time }
func (e Button) Timestamp() time.Duration    { return e.Time }
