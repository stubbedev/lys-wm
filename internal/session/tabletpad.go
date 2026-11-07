package session

import (
	"fmt"
	"math"

	"github.com/ebitengine/purego"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
)

const (
	evTabletPadButton = 700
	evTabletPadRing   = 701
	evTabletPadStrip  = 702
	evTabletPadDial   = 704
	capTabletPad      = 4
	padSourceFinger   = 2
)

type padFuncs struct {
	event        func(ev uintptr) uintptr
	time         func(pev uintptr) uint64
	button       func(pev uintptr) uint32
	buttonState  func(pev uintptr) int32
	ringPosition func(pev uintptr) float64
	ringNumber   func(pev uintptr) uint32
	ringSource   func(pev uintptr) int32
	stripPos     func(pev uintptr) float64
	stripNumber  func(pev uintptr) uint32
	stripSource  func(pev uintptr) int32
	dialDelta    func(pev uintptr) float64
	dialNumber   func(pev uintptr) uint32
	mode         func(pev uintptr) uint32
	modeGroup    func(pev uintptr) uintptr
	groupIndex   func(group uintptr) uint32
	numButtons   func(dev uintptr) int32
	numRings     func(dev uintptr) int32
	numStrips    func(dev uintptr) int32
	numDials     func(dev uintptr) int32
	numGroups    func(dev uintptr) int32
	group        func(dev uintptr, index uint32) uintptr
	groupModes   func(group uintptr) uint32
	groupMode    func(group uintptr) uint32
	hasButton    func(group uintptr, n uint32) int32
	hasRing      func(group uintptr, n uint32) int32
	hasStrip     func(group uintptr, n uint32) int32
	hasDial      func(group uintptr, n uint32) int32
	deviceGroup  func(dev uintptr) uintptr
}

type padEvents struct {
	button tabletinput.PadButton
	ring   tabletinput.PadRing
	strip  tabletinput.PadStrip
	dial   tabletinput.PadDial
}

func (in *Libinput) bindPad(path string) error {
	f := &in.fn.pad
	syms := []libSymbol{
		{&f.event, "libinput_event_get_tablet_pad_event"},
		{&f.time, "libinput_event_tablet_pad_get_time_usec"},
		{&f.button, "libinput_event_tablet_pad_get_button_number"},
		{&f.buttonState, "libinput_event_tablet_pad_get_button_state"},
		{&f.ringPosition, "libinput_event_tablet_pad_get_ring_position"},
		{&f.ringNumber, "libinput_event_tablet_pad_get_ring_number"},
		{&f.ringSource, "libinput_event_tablet_pad_get_ring_source"},
		{&f.stripPos, "libinput_event_tablet_pad_get_strip_position"},
		{&f.stripNumber, "libinput_event_tablet_pad_get_strip_number"},
		{&f.stripSource, "libinput_event_tablet_pad_get_strip_source"},
		{&f.dialDelta, "libinput_event_tablet_pad_get_dial_delta_v120"},
		{&f.dialNumber, "libinput_event_tablet_pad_get_dial_number"},
		{&f.mode, "libinput_event_tablet_pad_get_mode"},
		{&f.modeGroup, "libinput_event_tablet_pad_get_mode_group"},
		{&f.groupIndex, "libinput_tablet_pad_mode_group_get_index"},
		{&f.numButtons, "libinput_device_tablet_pad_get_num_buttons"},
		{&f.numRings, "libinput_device_tablet_pad_get_num_rings"},
		{&f.numStrips, "libinput_device_tablet_pad_get_num_strips"},
		{&f.numDials, "libinput_device_tablet_pad_get_num_dials"},
		{&f.numGroups, "libinput_device_tablet_pad_get_num_mode_groups"},
		{&f.group, "libinput_device_tablet_pad_get_mode_group"},
		{&f.groupModes, "libinput_tablet_pad_mode_group_get_num_modes"},
		{&f.groupMode, "libinput_tablet_pad_mode_group_get_mode"},
		{&f.hasButton, "libinput_tablet_pad_mode_group_has_button"},
		{&f.hasRing, "libinput_tablet_pad_mode_group_has_ring"},
		{&f.hasStrip, "libinput_tablet_pad_mode_group_has_strip"},
		{&f.hasDial, "libinput_tablet_pad_mode_group_has_dial"},
		{&f.deviceGroup, "libinput_device_get_device_group"},
	}
	for _, s := range syms {
		if _, err := purego.Dlsym(in.lib, s.name); err != nil {
			return fmt.Errorf("session: %s lacks %s: %w", path, s.name, err)
		}
		purego.RegisterLibFunc(s.fn, in.lib, s.name)
	}
	return nil
}

func count(n int32) uint32 {
	return uint32(max(n, 0))
}

func members(n uint32, has func(uint32) bool) []uint32 {
	var out []uint32
	for i := range n {
		if has(i) {
			out = append(out, i)
		}
	}
	return out
}

func (in *Libinput) padLayout(h uintptr) tabletinput.PadLayout {
	f := &in.fn.pad
	l := tabletinput.PadLayout{Buttons: count(f.numButtons(h))}
	rings, strips, dials := count(f.numRings(h)), count(f.numStrips(h)), count(f.numDials(h))
	for i := range count(f.numGroups(h)) {
		g := f.group(h, i)
		l.Groups = append(l.Groups, tabletinput.PadGroup{
			Buttons: members(l.Buttons, func(n uint32) bool { return f.hasButton(g, n) != 0 }),
			Rings:   members(rings, func(n uint32) bool { return f.hasRing(g, n) != 0 }),
			Strips:  members(strips, func(n uint32) bool { return f.hasStrip(g, n) != 0 }),
			Dials:   members(dials, func(n uint32) bool { return f.hasDial(g, n) != 0 }),
			Modes:   f.groupModes(g),
			Mode:    f.groupMode(g),
		})
	}
	return l
}

func (in *Libinput) translatePad(out []backend.Event, ev uintptr, typ int32) []backend.Event {
	f := &in.fn.pad
	pev := f.event(ev)
	at, group, mode := usec(f.time(pev)), f.groupIndex(f.modeGroup(pev)), f.mode(pev)
	p := &in.pad
	switch typ {
	case evTabletPadButton:
		p.button = tabletinput.PadButton{Time: at, Button: f.button(pev), Pressed: f.buttonState(pev) == 1, Group: group, Mode: mode}
		return append(out, &p.button)
	case evTabletPadRing:
		v := f.ringPosition(pev)
		p.ring = tabletinput.PadRing{Time: at, Ring: f.ringNumber(pev), Degrees: max(v, 0), Stopped: v < 0, Finger: f.ringSource(pev) == padSourceFinger, Group: group, Mode: mode}
		return append(out, &p.ring)
	case evTabletPadStrip:
		v := f.stripPos(pev)
		p.strip = tabletinput.PadStrip{Time: at, Strip: f.stripNumber(pev), Position: max(v, 0), Stopped: v < 0, Finger: f.stripSource(pev) == padSourceFinger, Group: group, Mode: mode}
		return append(out, &p.strip)
	case evTabletPadDial:
		p.dial = tabletinput.PadDial{Time: at, Dial: f.dialNumber(pev), Delta: int32(math.Round(f.dialDelta(pev))), Group: group, Mode: mode}
		return append(out, &p.dial)
	}
	return out
}

func (d *InputDevice) Pad() (tabletinput.PadLayout, bool) {
	return d.padLayout, d.pad
}

func (d *InputDevice) Group() uintptr {
	return d.group
}
