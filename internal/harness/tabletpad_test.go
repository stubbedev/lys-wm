package harness

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	ctab "github.com/stubbedev/lys-wm/internal/protocol/client/tablet"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type fakePad struct{}

func (fakePad) Name() string                     { return "Wacom Intuos Pad" }
func (fakePad) Capabilities() backend.Capability { return 0 }
func (fakePad) Group() uintptr                   { return 1 }
func (fakePad) Pad() (tabletinput.PadLayout, bool) {
	return tabletinput.PadLayout{Buttons: 4, Groups: []tabletinput.PadGroup{
		{Buttons: []uint32{0, 1, 2, 3}, Rings: []uint32{0}, Strips: []uint32{0}, Dials: []uint32{0}, Modes: 2},
	}}, true
}

type padEvents struct{ l *tabletLog }

func (e padEvents) Group(*ctab.ZwpTabletPadV2, *ctab.ZwpTabletPadGroupV2) (ctab.ZwpTabletPadGroupV2Handler, error) {
	return padGroupEvents(e), e.l.log.Note("group")
}

func (padEvents) Path(*ctab.ZwpTabletPadV2, string) error { return nil }

func (e padEvents) Buttons(_ *ctab.ZwpTabletPadV2, n uint32) error {
	return e.l.log.Note("pad buttons %d", n)
}

func (e padEvents) Done(*ctab.ZwpTabletPadV2) error { return e.l.log.Note("pad_done") }

func (e padEvents) Button(_ *ctab.ZwpTabletPadV2, _, button uint32, state ctab.ZwpTabletPadV2ButtonState) error {
	return e.l.log.Note("pad button %d %d", button, state)
}

func (e padEvents) Enter(_ *ctab.ZwpTabletPadV2, _ uint32, tablet *ctab.ZwpTabletV2, s *cw.WlSurface) error {
	if tablet == nil {
		return e.l.log.Note("pad enter without a tablet")
	}
	return e.l.log.Note("pad enter %s", e.l.in.Surfaces[s.Base().ID()])
}

func (e padEvents) Leave(_ *ctab.ZwpTabletPadV2, _ uint32, s *cw.WlSurface) error {
	return e.l.log.Note("pad leave %s", e.l.in.Surfaces[s.Base().ID()])
}

func (e padEvents) Removed(*ctab.ZwpTabletPadV2) error { return e.l.log.Note("pad_removed") }

type padGroupEvents struct{ l *tabletLog }

func (e padGroupEvents) Buttons(_ *ctab.ZwpTabletPadGroupV2, buttons []byte) error {
	var list []uint32
	for at := 0; at+4 <= len(buttons); at += 4 {
		list = append(list, binary.LittleEndian.Uint32(buttons[at:]))
	}
	return e.l.log.Note("group buttons %v", list)
}

func (e padGroupEvents) Ring(*ctab.ZwpTabletPadGroupV2, *ctab.ZwpTabletPadRingV2) (ctab.ZwpTabletPadRingV2Handler, error) {
	return ringEvents(e), e.l.log.Note("ring")
}

func (e padGroupEvents) Strip(*ctab.ZwpTabletPadGroupV2, *ctab.ZwpTabletPadStripV2) (ctab.ZwpTabletPadStripV2Handler, error) {
	return stripEvents(e), e.l.log.Note("strip")
}

func (e padGroupEvents) Dial(*ctab.ZwpTabletPadGroupV2, *ctab.ZwpTabletPadDialV2) (ctab.ZwpTabletPadDialV2Handler, error) {
	return dialEvents(e), e.l.log.Note("dial")
}

func (e padGroupEvents) Modes(_ *ctab.ZwpTabletPadGroupV2, n uint32) error {
	return e.l.log.Note("modes %d", n)
}

func (e padGroupEvents) Done(*ctab.ZwpTabletPadGroupV2) error { return e.l.log.Note("group_done") }

func (e padGroupEvents) ModeSwitch(_ *ctab.ZwpTabletPadGroupV2, _, _, mode uint32) error {
	return e.l.log.Note("mode %d", mode)
}

type ringEvents struct{ l *tabletLog }

func (e ringEvents) Source(_ *ctab.ZwpTabletPadRingV2, s ctab.ZwpTabletPadRingV2Source) error {
	return e.l.log.Note("ring source %d", s)
}

func (e ringEvents) Angle(_ *ctab.ZwpTabletPadRingV2, d wire.Fixed) error {
	return e.l.log.Note("ring angle %g", d.Float())
}

func (e ringEvents) Stop(*ctab.ZwpTabletPadRingV2) error { return e.l.log.Note("ring stop") }

func (e ringEvents) Frame(*ctab.ZwpTabletPadRingV2, uint32) error { return e.l.log.Note("ring frame") }

type stripEvents struct{ l *tabletLog }

func (e stripEvents) Source(_ *ctab.ZwpTabletPadStripV2, s ctab.ZwpTabletPadStripV2Source) error {
	return e.l.log.Note("strip source %d", s)
}

func (e stripEvents) Position(_ *ctab.ZwpTabletPadStripV2, p uint32) error {
	return e.l.log.Note("strip position %d", p)
}

func (e stripEvents) Stop(*ctab.ZwpTabletPadStripV2) error { return e.l.log.Note("strip stop") }
func (e stripEvents) Frame(*ctab.ZwpTabletPadStripV2, uint32) error {
	return e.l.log.Note("strip frame")
}

type dialEvents struct{ l *tabletLog }

func (e dialEvents) Delta(_ *ctab.ZwpTabletPadDialV2, v int32) error {
	return e.l.log.Note("dial delta %d", v)
}

func (e dialEvents) Frame(*ctab.ZwpTabletPadDialV2, uint32) error { return e.l.log.Note("dial frame") }

func pad(h *Harness, events ...backend.Event) {
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		for _, e := range events {
			if err := c.Input(fakePad{}, e); err != nil {
				return err
			}
		}
		return nil
	})
}

var padAnnounced = []string{
	"group", "group buttons [0 1 2 3]", "ring", "strip", "dial", "modes 2", "group_done", "pad buttons 4", "pad_done",
}

func TestATabletPadReachesTheKeyboardFocus(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	left := join(t, h, "left")
	right := join(t, h, "right")
	ll, rl := tabletSeat(t, left), tabletSeat(t, right)
	addTablet(h)
	until(t, left.c, "the tablet on left", &ll.log, "tablet_added", "name Wacom Intuos", "id 56a:374", "tablet_done")
	until(t, right.c, "the tablet on right", &rl.log, "tablet_added", "name Wacom Intuos", "id 56a:374", "tablet_done")
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.DeviceAdded(fakePad{}) })
	until(t, right.c, "the pad announced to and entering the focused right", &rl.log, append(padAnnounced, "pad enter right", "mode 0")...)
	until(t, left.c, "the pad announced to left", &ll.log, padAnnounced...)

	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	pad(h,
		&tabletinput.PadButton{Time: ms(1), Button: 2, Pressed: true},
		&tabletinput.PadButton{Time: ms(2), Button: 2},
		&tabletinput.PadRing{Time: ms(3), Ring: 0, Degrees: 90, Finger: true},
		&tabletinput.PadRing{Time: ms(4), Ring: 0, Stopped: true, Finger: true},
		&tabletinput.PadStrip{Time: ms(5), Strip: 0, Position: 0.5},
		&tabletinput.PadDial{Time: ms(6), Dial: 0, Delta: -120},
		&tabletinput.PadButton{Time: ms(7), Button: 0, Pressed: true, Mode: 1},
	)
	until(t, right.c, "the pad's buttons, ring, strip, dial and mode on right", &rl.log,
		"pad button 2 1", "pad button 2 0",
		fmt.Sprintf("ring source %d", ctab.ZwpTabletPadRingV2SourceFinger), "ring angle 90", "ring frame",
		fmt.Sprintf("ring source %d", ctab.ZwpTabletPadRingV2SourceFinger), "ring stop", "ring frame",
		"strip position 32768", "strip frame",
		"dial delta -120", "dial frame",
		"mode 1", "pad button 0 1",
	)
	left.c.Sync()
	if len(ll.log) != 0 {
		t.Fatalf("the unfocused left got pad events: %q", ll.log)
	}

	h.dispatch("focus left")
	until(t, right.c, "the pad leaving right", &rl.log, "pad leave right")
	until(t, left.c, "the pad entering left in its current mode", &ll.log, "pad enter left", "mode 1")

	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.DeviceRemoved(fakeTablet{}) })
	until(t, left.c, "the pad removed with its tablet", &ll.log, "pad_removed", "tablet_removed")
}

func TestAPadAddedBeforeItsTabletEntersOnceTheTabletArrives(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	p := join(t, h, "canvas")
	l := tabletSeat(t, p)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.DeviceAdded(fakePad{}) })
	until(t, p.c, "the pad announced without entering", &l.log, padAnnounced...)
	addTablet(h)
	until(t, p.c, "the tablet, then the pad entering", &l.log, "tablet_added", "name Wacom Intuos", "id 56a:374", "tablet_done", "pad enter canvas", "mode 0")
	pad(h, &tabletinput.PadButton{Time: time.Millisecond, Button: 3, Pressed: true})
	until(t, p.c, "a button after the late enter", &l.log, "pad button 3 1")
}
