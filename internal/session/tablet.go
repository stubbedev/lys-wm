package session

import (
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
)

const (
	evTabletToolAxis      = 600
	evTabletToolProximity = 601
	evTabletToolTip       = 602
	evTabletToolButton    = 603
	capTabletTool         = 3
)

type tabletFuncs struct {
	event        func(ev uintptr) uintptr
	tool         func(tev uintptr) uintptr
	time         func(tev uintptr) uint64
	changed      [9]func(tev uintptr) int32
	x, y         func(tev uintptr, size uint32) float64
	pressure     func(tev uintptr) float64
	distance     func(tev uintptr) float64
	tiltX, tiltY func(tev uintptr) float64
	rotation     func(tev uintptr) float64
	slider       func(tev uintptr) float64
	wheel        func(tev uintptr) float64
	wheelClicks  func(tev uintptr) int32
	proximity    func(tev uintptr) int32
	tip          func(tev uintptr) int32
	button       func(tev uintptr) uint32
	buttonState  func(tev uintptr) int32
	toolType     func(tool uintptr) int32
	toolSerial   func(tool uintptr) uint64
	toolID       func(tool uintptr) uint64
	toolHas      [6]func(tool uintptr) int32
	toolRef      func(tool uintptr) uintptr
	toolUnref    func(tool uintptr) uintptr
	vendor       func(dev uintptr) uint32
	product      func(dev uintptr) uint32
}

var changedAxes = [9]struct {
	axis tabletinput.Axis
	name string
}{
	{tabletinput.AxisX, "x"},
	{tabletinput.AxisY, "y"},
	{tabletinput.AxisPressure, "pressure"},
	{tabletinput.AxisDistance, "distance"},
	{tabletinput.AxisTiltX, "tilt_x"},
	{tabletinput.AxisTiltY, "tilt_y"},
	{tabletinput.AxisRotation, "rotation"},
	{tabletinput.AxisSlider, "slider"},
	{tabletinput.AxisWheel, "wheel"},
}

var toolCapabilities = [6]struct {
	capability tabletinput.Capability
	name       string
}{
	{tabletinput.CanTilt, "tilt"},
	{tabletinput.CanPressure, "pressure"},
	{tabletinput.CanDistance, "distance"},
	{tabletinput.CanRotation, "rotation"},
	{tabletinput.CanSlider, "slider"},
	{tabletinput.CanWheel, "wheel"},
}

var toolTypes = map[int32]tabletinput.ToolType{
	1: tabletinput.Pen, 2: tabletinput.Eraser, 3: tabletinput.Brush, 4: tabletinput.Pencil,
	5: tabletinput.Airbrush, 6: tabletinput.Mouse, 7: tabletinput.Lens, 8: tabletinput.Totem,
}

type libSymbol struct {
	fn   any
	name string
}

func (r *inputReader) bindTablet(path string) error {
	f := &r.fn.tablet
	syms := []libSymbol{
		{&f.event, "libinput_event_get_tablet_tool_event"},
		{&f.tool, "libinput_event_tablet_tool_get_tool"},
		{&f.time, "libinput_event_tablet_tool_get_time_usec"},
		{&f.x, "libinput_event_tablet_tool_get_x_transformed"},
		{&f.y, "libinput_event_tablet_tool_get_y_transformed"},
		{&f.pressure, "libinput_event_tablet_tool_get_pressure"},
		{&f.distance, "libinput_event_tablet_tool_get_distance"},
		{&f.tiltX, "libinput_event_tablet_tool_get_tilt_x"},
		{&f.tiltY, "libinput_event_tablet_tool_get_tilt_y"},
		{&f.rotation, "libinput_event_tablet_tool_get_rotation"},
		{&f.slider, "libinput_event_tablet_tool_get_slider_position"},
		{&f.wheel, "libinput_event_tablet_tool_get_wheel_delta"},
		{&f.wheelClicks, "libinput_event_tablet_tool_get_wheel_delta_discrete"},
		{&f.proximity, "libinput_event_tablet_tool_get_proximity_state"},
		{&f.tip, "libinput_event_tablet_tool_get_tip_state"},
		{&f.button, "libinput_event_tablet_tool_get_button"},
		{&f.buttonState, "libinput_event_tablet_tool_get_button_state"},
		{&f.toolType, "libinput_tablet_tool_get_type"},
		{&f.toolSerial, "libinput_tablet_tool_get_serial"},
		{&f.toolID, "libinput_tablet_tool_get_tool_id"},
		{&f.toolRef, "libinput_tablet_tool_ref"},
		{&f.toolUnref, "libinput_tablet_tool_unref"},
		{&f.vendor, "libinput_device_get_id_vendor"},
		{&f.product, "libinput_device_get_id_product"},
	}
	for i, a := range changedAxes {
		syms = append(syms, libSymbol{&f.changed[i], "libinput_event_tablet_tool_" + a.name + "_has_changed"})
	}
	for i, c := range toolCapabilities {
		syms = append(syms, libSymbol{&f.toolHas[i], "libinput_tablet_tool_has_" + c.name})
	}
	return r.register(path, syms)
}

func (r *inputReader) tabletTool(h uintptr) *tabletinput.Tool {
	f := &r.fn.tablet
	if t, ok := r.tools[h]; ok {
		return t
	}
	t := &tabletinput.Tool{Type: toolTypes[f.toolType(h)], Serial: f.toolSerial(h), HardwareID: f.toolID(h)}
	for i, c := range toolCapabilities {
		if f.toolHas[i](h) != 0 {
			t.Caps |= c.capability
		}
	}
	if r.tools == nil {
		r.tools = map[uintptr]*tabletinput.Tool{}
	}
	r.tools[f.toolRef(h)] = t
	return t
}

func (r *inputReader) dropTabletTools() {
	for h := range r.tools {
		r.fn.tablet.toolUnref(h)
		delete(r.tools, h)
	}
}

func (r *inputReader) tabletAxes(tev uintptr) tabletinput.Axes {
	f := &r.fn.tablet
	var a tabletinput.Axes
	for i, c := range changedAxes {
		if f.changed[i](tev) != 0 {
			a.Changed |= c.axis
		}
	}
	a.X, a.Y = f.x(tev, 1), f.y(tev, 1)
	a.Pressure, a.Distance = f.pressure(tev), f.distance(tev)
	a.TiltX, a.TiltY = f.tiltX(tev), f.tiltY(tev)
	a.Rotation, a.Slider = f.rotation(tev), f.slider(tev)
	a.Wheel, a.WheelClicks = f.wheel(tev), f.wheelClicks(tev)
	return a
}

func (r *inputReader) translateTablet(out []backend.Event, into *translated, ev uintptr, typ int32) []backend.Event {
	f := &r.fn.tablet
	tev := f.event(ev)
	at, tool := usec(f.time(tev)), r.tabletTool(f.tool(tev))
	switch typ {
	case evTabletToolProximity:
		return append(out, into.proximity.put(tabletinput.Proximity{Time: at, Tool: tool, In: f.proximity(tev) == 1, Axes: r.tabletAxes(tev)}))
	case evTabletToolAxis:
		return append(out, into.toolMotion.put(tabletinput.Motion{Time: at, Tool: tool, Axes: r.tabletAxes(tev)}))
	case evTabletToolTip:
		return append(out, into.tip.put(tabletinput.Tip{Time: at, Tool: tool, Down: f.tip(tev) == 1, Axes: r.tabletAxes(tev)}))
	case evTabletToolButton:
		return append(out, into.toolButton.put(tabletinput.Button{Time: at, Tool: tool, Button: f.button(tev), Pressed: f.buttonState(tev) == 1}))
	}
	return out
}

func (d *InputDevice) Tablet() (tabletinput.ID, bool) {
	return d.tabletID, d.tablet
}
