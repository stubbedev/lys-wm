package seat

import (
	"fmt"
	"slices"
	"testing"
	"time"

	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
)

type buttonEvents struct{ *pointerEvents }

func (p buttonEvents) Button(_ *cw.WlPointer, _, _, button uint32, state cw.WlPointerButtonState) error {
	p.log = append(p.log, fmt.Sprintf("button %#x %d", button, state))
	return nil
}

func (c *client) buttons() *pointerEvents {
	c.t.Helper()
	p := &pointerEvents{}
	if _, err := c.seat.GetPointer(buttonEvents{p}); err != nil {
		c.t.Fatal(err)
	}
	c.roundtrip()
	return p
}

func TestAPointerMadeWhileAButtonIsHeldSeesItReleased(t *testing.T) {
	e := setup(t)
	e.plug(sw.WlSeatCapabilityPointer)
	c := e.connect()
	first := c.buttons()
	c.surface()
	e.do(
		func() error { return e.seat.SetPointerFocus(e.surf.list[0], 1, 2) },
		e.seat.PointerFrame,
		func() error { return e.seat.PointerButton(time.Second, leftButton, true) },
		e.seat.PointerFrame,
	)
	late := c.buttons()
	e.do(func() error { return e.seat.PointerButton(2*time.Second, leftButton, false) }, e.seat.PointerFrame)
	c.roundtrip()

	if want := []string{"enter 1 2", "frame", "button 0x110 1", "frame", "button 0x110 0", "frame"}; !slices.Equal(first.log, want) {
		t.Errorf("the wl_pointer that saw the press saw %v, want %v", first.log, want)
	}
	if want := []string{"enter 1 2", "frame", "button 0x110 0", "frame"}; !slices.Equal(late.log, want) {
		t.Errorf("a wl_pointer made while a button is held saw %v, want %v: the button belongs to the seat, and every wl_pointer of the focused client is told when it is let go", late.log, want)
	}
}
