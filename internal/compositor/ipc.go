package compositor

import (
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/frametrace"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) TraceFrames(r *frametrace.Recorder) {
	c.frames = r
}

type IPCEvents interface {
	Dispatched(a wm.Action)
	Announce(e ipc.Event)
}

func (c *Compositor) OnIPCEvents(e IPCEvents) {
	c.ipcEvents = e
}

func (c *Compositor) Snapshot() ipc.Snapshot {
	s := c.wm
	focusedOutput := s.FocusedOutput()
	locks := c.seat.Locks()
	snap := ipc.Snapshot{
		Submap:         c.submap,
		Overview:       c.overviewHolds(),
		Layout:         c.keyboardLayout(),
		Locks:          ipc.Locks{Caps: locks.Caps, Num: locks.Num, Scroll: locks.Scroll},
		ReducedMotion:  c.animations.Reduced(),
		ConfigProblems: ipc.ConfigProblems(c.configProblems),
	}
	for _, o := range c.outputs {
		b := o.Backend()
		mode := b.State().Mode
		active, _ := s.ActiveOn(b.Name())
		shader, shaderError := c.shaderReport(o)
		snap.Outputs = append(snap.Outputs, ipc.Output{
			Name:        b.Name(),
			Description: b.Description(),
			Rect:        ipc.RectOf(o.Logical()),
			Scale:       float64(o.Scale()) / float64(output.Unit),
			Mode:        ipc.Mode{Width: mode.Size.X, Height: mode.Size.Y, RefreshMHz: mode.RefreshMHz},
			Workspace:   active,
			Focused:     b.Name() == focusedOutput,
			Powered:     b.State().Lit(),
			Shader:      shader,
			ShaderError: shaderError,
			HDR:         b.State().HDR.Active,
			Zoom:        c.ipcZoom(o),
		})
	}
	for _, w := range s.Workspaces() {
		visible := s.Shown(w)
		snap.Workspaces = append(snap.Workspaces, ipc.Workspace{
			ID:      w.ID,
			Output:  w.Output,
			Windows: len(w.Tree.Windows()) + len(w.Floating) + len(w.Minimized),
			Visible: visible,
			Focused: visible && w.Output == focusedOutput,
		})
	}
	focused, hasFocus := s.Focused()
	stack := c.stacking()
	for _, w := range c.windows {
		id, ok := s.WorkspaceOf(w.id)
		if !ok {
			continue
		}
		ws, _ := s.Workspace(id)
		contentType, contentSource := c.contentOf(w.top.Surface())
		snap.Windows = append(snap.Windows, ipc.Window{
			ID:            w.id,
			AppID:         w.top.AppID(),
			Title:         w.top.Title(),
			PID:           w.top.pid(),
			Workspace:     id,
			Output:        ws.Output,
			Rect:          ipc.RectOf(w.goal),
			Floating:      s.Floats(w.id),
			Fullscreen:    s.Fullscreen(w.id),
			Minimized:     s.Minimized(w.id),
			Urgent:        w.urgent,
			Unresponsive:  w.hung,
			Focused:       hasFocus && w.id == focused,
			ContentType:   contentType,
			ContentSource: contentSource,
			Icon:          ipcIcon(w.top.Icon()),
			Stack:         stackOf(stack, w.id),
		})
	}
	return snap
}

func (c *Compositor) ipcZoom(o *output.Output) float64 {
	if sc, ok := c.scenes[o]; ok && sc.zoom != nil && sc.zoom.told != 1 {
		return sc.zoom.told
	}
	return 0
}

func (c *Compositor) shaderReport(o *output.Output) (path, problem string) {
	s, ok := c.shaders[c.configuredRule(o.Backend()).Shader]
	if !ok {
		return "", ""
	}
	if s.err != nil {
		return s.path, s.err.Error()
	}
	return s.path, ""
}

func ipcIcon(i *shell.Icon) *ipc.Icon {
	if i == nil {
		return nil
	}
	out := &ipc.Icon{Serial: i.Serial, Name: i.Name}
	for _, img := range i.Images {
		out.Sizes = append(out.Sizes, img.Pixels())
	}
	return out
}

func (c *Compositor) Icon(id wm.WindowID, size int) (*image.RGBA, int, error) {
	w := c.windowByID(id)
	if w == nil {
		return nil, 0, fmt.Errorf("compositor: no window %d", id)
	}
	img, ok := w.top.Icon().Best(size)
	if !ok {
		return nil, 0, fmt.Errorf("compositor: window %d has no icon buffer", id)
	}
	return img.Image, img.Scale, nil
}

func (c *Compositor) keyboardLayout() ipc.Layout {
	group := c.seat.Layout()
	l := ipc.Layout{Index: group}
	if names := c.seatConfig.Keymap.Layouts(); int(group) < len(names) {
		l.Name = names[group]
	}
	return l
}

func (c *Compositor) OnStateChange(f func()) {
	c.changed = f
}

func (c *Compositor) stateChanged() {
	if c.changed != nil {
		c.changed()
	}
}

func (c *Compositor) KeyboardStateChanged() {
	c.stateChanged()
}

var stackLayers = [...]wm.PlacementLayer{wm.LayerFullscreen, wm.LayerFloating, wm.LayerTiled}

func (c *Compositor) stacking() map[wm.WindowID]int {
	order := map[wm.WindowID]int{}
	for _, o := range c.outputs {
		n := 0
		for _, layer := range stackLayers {
			for _, w := range slices.Backward(c.stack) {
				if w.layer == layer && w.visible && !w.minimized && w.output == o {
					order[w.id] = n
					n++
				}
			}
		}
	}
	return order
}

func stackOf(order map[wm.WindowID]int, id wm.WindowID) *int {
	if n, ok := order[id]; ok {
		return &n
	}
	return nil
}

func (c *Compositor) showWindowMenu(w *window, t *shell.Toplevel, r shell.ShowWindowMenu) error {
	if c.ipcEvents == nil || !c.seat.PressSerial(t.Surface().Client(), r.Serial) {
		return nil
	}
	at := c.windowOrigin(w).global(point{float64(r.At.X), float64(r.At.Y)}).pixel()
	c.ipcEvents.Announce(ipc.WindowMenuEvent{Window: w.id, X: at.X, Y: at.Y})
	return nil
}
