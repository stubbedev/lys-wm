package compositor

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/handover"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type windowPlan struct {
	Client   int
	Toplevel uint32
	ID       wm.WindowID
	Urgent   bool
	Floated  bool
}

type HandoverState struct {
	Display    *server.State
	WM         json.RawMessage
	Windows    []windowPlan
	NextWindow wm.WindowID
	Cursor     image.Point
	Off        []string
}

type planKey struct {
	client   int
	toplevel uint32
}

type adoption struct {
	plan       map[planKey]windowPlan
	wm         json.RawMessage
	windows    []windowPlan
	cursor     image.Point
	off        []string
	animations anim.Table
	shown      map[*output.Output]bool
}

func (c *Compositor) UpgradeBlockers(dropX11 bool) []string {
	var out []string
	if c.shell.Locked() {
		out = append(out, "the session is locked; unlock it first")
	}
	if c.interaction != nil || c.data.Dragging() {
		out = append(out, "a drag is in progress; let go of it first")
	}
	if dropX11 {
		return out
	}
	for _, w := range c.windows {
		if _, isXdg := w.top.(xdgToplevel); !isXdg {
			out = append(out, fmt.Sprintf("the X11 window %q cannot be kept", w.top.Title()))
		}
	}
	if len(c.unmanaged) > 0 {
		out = append(out, "an X11 override-redirect window is open and cannot be kept")
	}
	return out
}

func (c *Compositor) Settle() error {
	return errors.Join(c.dropOverview(), c.landInFlight())
}

func (c *Compositor) Park() {
	c.parked = true
}

func (c *Compositor) Resume() error {
	c.parked = false
	for _, sc := range c.scenes {
		sc.invalidate()
	}
	return c.scheduleAll()
}

func (c *Compositor) Busy() string {
	return c.surfaces.Busy()
}

func (c *Compositor) HandoverState(t *handover.FDs, skip func(*server.Client) bool) (*HandoverState, error) {
	st, kept, err := c.display.Snapshot(t, skip)
	if err != nil {
		return nil, err
	}
	layout := c.wm
	out := &HandoverState{Display: st, NextWindow: c.nextWindow, Cursor: c.cursor.pixel()}
	for _, o := range c.outputs {
		if c.blanked[o] {
			out.Off = append(out.Off, o.Backend().Name())
		}
	}
	for _, w := range c.windows {
		x, ok := w.top.(xdgToplevel)
		if !ok {
			if layout, err = layout.UnmapWindow(w.id); err != nil {
				return nil, fmt.Errorf("compositor: leave out the X11 window %d: %w", w.id, err)
			}
			continue
		}
		i, ok := kept[x.Surface().Client()]
		if !ok {
			return nil, fmt.Errorf("compositor: window %d belongs to a client that is not kept", w.id)
		}
		out.Windows = append(out.Windows, windowPlan{Client: i, Toplevel: x.ID(), ID: w.id, Urgent: w.urgent, Floated: w.floated})
	}
	if out.WM, err = layout.Save(); err != nil {
		return nil, fmt.Errorf("compositor: save the layout: %w", err)
	}
	return out, nil
}

func (c *Compositor) AdoptHandover(st *HandoverState, t *handover.FDs) (func() error, error) {
	c.nextWindow = st.NextWindow
	c.adopting = &adoption{plan: make(map[planKey]windowPlan, len(st.Windows)), windows: st.Windows, wm: st.WM, cursor: st.Cursor, off: st.Off, animations: c.animations, shown: map[*output.Output]bool{}}
	for _, p := range st.Windows {
		c.adopting.plan[planKey{p.Client, p.Toplevel}] = p
	}
	c.animations = anim.Table{}
	c.parked = false
	return c.display.Restore(st.Display, t)
}

func (c *Compositor) plannedWindow(t toplevel) (windowPlan, bool) {
	if c.adopting == nil {
		return windowPlan{}, false
	}
	x, ok := t.(xdgToplevel)
	if !ok {
		return windowPlan{}, false
	}
	i := slices.Index(c.display.RestoredClients(), x.Surface().Client())
	if i < 0 {
		return windowPlan{}, false
	}
	p, ok := c.adopting.plan[planKey{i, x.ID()}]
	return p, ok
}

func (c *Compositor) FinishAdoption() error {
	a := c.adopting
	if a == nil {
		return errors.New("compositor: nothing is being adopted")
	}
	mapped := make([]wm.WindowID, 0, len(c.windows))
	for _, w := range c.windows {
		mapped = append(mapped, w.id)
	}
	s, err := wm.LoadState(a.wm)
	if err != nil {
		return err
	}
	restored := c.display.RestoredClients()
	for _, p := range a.windows {
		if restored[p.Client].Gone() && slices.Contains(s.Windows(), p.ID) {
			if s, err = s.UnmapWindow(p.ID); err != nil {
				return fmt.Errorf("compositor: leave out the window of a client that left: %w", err)
			}
		}
	}
	saved := s.Windows()
	slices.Sort(mapped)
	slices.Sort(saved)
	if !slices.Equal(mapped, saved) {
		return fmt.Errorf("compositor: windows %v came back, the saved layout has %v", mapped, saved)
	}
	current := c.wm.Outputs()
	for _, o := range s.Outputs() {
		if !slices.ContainsFunc(current, func(h wm.Output) bool { return h.Name == o.Name }) {
			return fmt.Errorf("compositor: the saved layout has output %s, which is not here", o.Name)
		}
	}
	for _, o := range current {
		if !slices.ContainsFunc(s.Outputs(), func(h wm.Output) bool { return h.Name == o.Name }) {
			return fmt.Errorf("compositor: output %s is new since the layout was saved", o.Name)
		}
	}
	for _, w := range c.windows {
		if p, ok := c.plannedWindow(w.top); ok {
			w.urgent, w.floated = p.Urgent, p.Floated
		}
	}
	order := make(map[wm.WindowID]int, len(a.windows))
	for i, p := range a.windows {
		order[p.ID] = i
	}
	slices.SortFunc(c.windows, func(x, y *window) int { return order[x.id] - order[y.id] })
	c.cursor = point{X: float64(a.cursor.X), Y: float64(a.cursor.Y)}
	if err := c.update(s, nil); err != nil {
		return err
	}
	if err := c.syncAreas(); err != nil {
		return err
	}
	if err := c.refreshPointer(); err != nil {
		return err
	}
	for _, name := range a.off {
		if err := c.dpms(wm.DPMSAction{Mode: wm.SwitchOff, Output: name}); err != nil {
			return err
		}
	}
	a.shown = map[*output.Output]bool{}
	for _, sc := range c.scenes {
		sc.invalidate()
	}
	return c.scheduleAll()
}

func (c *Compositor) AdoptionShown() bool {
	if c.adopting == nil {
		return false
	}
	for _, o := range c.outputs {
		if !c.blanked[o] && !c.adopting.shown[o] {
			return false
		}
	}
	return true
}

func (c *Compositor) CompleteAdoption() error {
	a := c.adopting
	c.adopting = nil
	c.animations = a.animations
	if c.colorManager == nil {
		return nil
	}
	return c.colorManager.Adopted()
}

func (c *Compositor) Display() *server.Display {
	return c.display
}
