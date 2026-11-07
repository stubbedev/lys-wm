package ipc

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/stubbedev/lys-wm/internal/wm"
)

type Event interface {
	Name() string
}

type OutputChange string

const (
	OutputAdded   OutputChange = "added"
	OutputRemoved OutputChange = "removed"
	OutputChanged OutputChange = "changed"
)

type LayerChange string

const (
	LayerOpened LayerChange = "open"
	LayerClosed LayerChange = "close"
)

type (
	OutputEvent struct {
		Change OutputChange `json:"change"`
		Output Output       `json:"output"`
	}
	WorkspaceEvent struct {
		Output    string         `json:"output"`
		Workspace wm.WorkspaceID `json:"workspace"`
	}
	FocusedOutputEvent struct {
		Output string `json:"output"`
	}
	OpenEvent struct {
		Window Window `json:"window"`
	}
	CloseEvent struct {
		Window wm.WindowID `json:"window"`
	}
	MoveEvent struct {
		Window    wm.WindowID    `json:"window"`
		Workspace wm.WorkspaceID `json:"workspace"`
	}
	TitleEvent struct {
		Window wm.WindowID `json:"window"`
		Title  string      `json:"title"`
	}
	AppIDEvent struct {
		Window wm.WindowID `json:"window"`
		AppID  string      `json:"app_id"`
	}
	RectEvent struct {
		Window wm.WindowID `json:"window"`
		Output string      `json:"output"`
		Rect   Rect        `json:"rect"`
	}
	IconEvent struct {
		Window wm.WindowID `json:"window"`
		Icon   *Icon       `json:"icon"`
	}
	UrgentEvent struct {
		Window wm.WindowID `json:"window"`
		Urgent bool        `json:"urgent"`
	}
	ModeEvent struct {
		Window           wm.WindowID `json:"window"`
		Floating         bool        `json:"floating"`
		Fullscreen       bool        `json:"fullscreen"`
		Maximized        bool        `json:"maximized"`
		ClientFullscreen bool        `json:"client_fullscreen"`
	}
	MinimizedEvent struct {
		Window    wm.WindowID `json:"window"`
		Minimized bool        `json:"minimized"`
	}
	PinEvent struct {
		Window wm.WindowID `json:"window"`
		Pinned bool        `json:"pinned"`
	}
	SwallowEvent struct {
		Window      wm.WindowID `json:"window"`
		SwallowedBy wm.WindowID `json:"swallowed_by"`
	}
	UnresponsiveEvent struct {
		Window       wm.WindowID `json:"window"`
		Unresponsive bool        `json:"unresponsive"`
	}
	StackEvent struct {
		Window wm.WindowID `json:"window"`
		Stack  *int        `json:"stack"`
	}
	FocusEvent struct {
		Window *wm.WindowID `json:"window"`
	}
	SubmapEvent struct {
		Submap string `json:"submap"`
	}
	LayoutEvent struct {
		Layout Layout `json:"layout"`
	}
	LocksEvent struct {
		Locks Locks `json:"locks"`
	}
	ReducedMotionEvent struct {
		ReducedMotion bool `json:"reduced_motion"`
	}
	DispatchEvent struct {
		Action string `json:"action"`
	}
	OverviewEvent struct {
		Open bool `json:"open"`
	}
	ConfigProblemsEvent struct {
		ConfigProblems
	}
	ScratchpadEvent struct {
		Scratchpad
	}
	ShowDesktopEvent struct {
		Output string `json:"output"`
		On     bool   `json:"on"`
	}
	LayerEvent struct {
		Change    LayerChange `json:"change"`
		ID        uint64      `json:"id"`
		Namespace string      `json:"namespace"`
		Output    string      `json:"output"`
		Layer     LayerLevel  `json:"layer"`
	}
	GroupEvent struct {
		Window wm.WindowID `json:"window"`
		Group  *Group      `json:"group"`
	}
	BellEvent struct {
		Window *wm.WindowID `json:"window"`
	}
	WindowMenuEvent struct {
		Window wm.WindowID `json:"window"`
		X      int         `json:"x"`
		Y      int         `json:"y"`
	}
)

func (OutputEvent) Name() string         { return "output" }
func (WorkspaceEvent) Name() string      { return "workspace" }
func (FocusedOutputEvent) Name() string  { return "focusedoutput" }
func (OpenEvent) Name() string           { return "open" }
func (CloseEvent) Name() string          { return "close" }
func (MoveEvent) Name() string           { return "move" }
func (TitleEvent) Name() string          { return "title" }
func (AppIDEvent) Name() string          { return "appid" }
func (RectEvent) Name() string           { return "rect" }
func (IconEvent) Name() string           { return "icon" }
func (UrgentEvent) Name() string         { return "urgent" }
func (MinimizedEvent) Name() string      { return "minimized" }
func (ModeEvent) Name() string           { return "mode" }
func (UnresponsiveEvent) Name() string   { return "unresponsive" }
func (PinEvent) Name() string            { return "pin" }
func (SwallowEvent) Name() string        { return "swallow" }
func (StackEvent) Name() string          { return "stack" }
func (FocusEvent) Name() string          { return "focus" }
func (SubmapEvent) Name() string         { return "submap" }
func (LayoutEvent) Name() string         { return "layout" }
func (LocksEvent) Name() string          { return "locks" }
func (ReducedMotionEvent) Name() string  { return "reducedmotion" }
func (DispatchEvent) Name() string       { return "dispatch" }
func (OverviewEvent) Name() string       { return "overview" }
func (ConfigProblemsEvent) Name() string { return "configproblems" }
func (WindowMenuEvent) Name() string     { return "windowmenu" }
func (ShowDesktopEvent) Name() string    { return "showdesktop" }
func (ScratchpadEvent) Name() string     { return "scratchpad" }
func (LayerEvent) Name() string          { return "layer" }
func (GroupEvent) Name() string          { return "group" }
func (BellEvent) Name() string           { return "bell" }

func Encode(e Event) ([]byte, error) {
	return AppendEvent(nil, e)
}

func AppendEvent(dst []byte, e Event) ([]byte, error) {
	start := len(dst)
	dst = append(dst, `{"event":`...)
	dst = appendJSONString(dst, e.Name())
	if a, ok := e.(lineAppender); ok {
		return append(a.appendFields(dst), '}', '\n'), nil
	}
	body, err := json.Marshal(e)
	if err != nil {
		return dst[:start], fmt.Errorf("ipc: encode %s event: %w", e.Name(), err)
	}
	if len(body) > 2 {
		dst = append(dst, ',')
	}
	dst = append(dst, body[1:]...)
	return append(dst, '\n'), nil
}

func sameOutput(a, b Output) bool {
	a.Workspace, a.Focused = b.Workspace, b.Focused
	return a == b
}

func focusedOutput(s Snapshot) string {
	if i := slices.IndexFunc(s.Outputs, func(o Output) bool { return o.Focused }); i >= 0 {
		return s.Outputs[i].Name
	}
	return ""
}

func Diff(prev, next Snapshot) []Event {
	var out []Event
	prevOutputs := make(map[string]Output, len(prev.Outputs))
	for _, o := range prev.Outputs {
		prevOutputs[o.Name] = o
	}
	for _, o := range prev.Outputs {
		if !slices.ContainsFunc(next.Outputs, func(n Output) bool { return n.Name == o.Name }) {
			out = append(out, OutputEvent{Change: OutputRemoved, Output: o})
		}
	}
	for _, o := range next.Outputs {
		p, ok := prevOutputs[o.Name]
		switch {
		case !ok:
			out = append(out, OutputEvent{Change: OutputAdded, Output: o})
		case !sameOutput(p, o):
			out = append(out, OutputEvent{Change: OutputChanged, Output: o})
		}
		if !ok || p.Workspace != o.Workspace {
			out = append(out, WorkspaceEvent{Output: o.Name, Workspace: o.Workspace})
		}
	}
	if f := focusedOutput(next); f != "" && f != focusedOutput(prev) {
		out = append(out, FocusedOutputEvent{Output: f})
	}
	prevWindows := make(map[wm.WindowID]Window, len(prev.Windows))
	for _, w := range prev.Windows {
		prevWindows[w.ID] = w
	}
	nextWindows := make(map[wm.WindowID]bool, len(next.Windows))
	for _, w := range next.Windows {
		nextWindows[w.ID] = true
	}
	for _, w := range prev.Windows {
		if !nextWindows[w.ID] {
			out = append(out, CloseEvent{Window: w.ID})
		}
	}
	for _, w := range next.Windows {
		p, ok := prevWindows[w.ID]
		if !ok {
			out = append(out, OpenEvent{Window: w})
			continue
		}
		if p.Workspace != w.Workspace {
			out = append(out, MoveEvent{Window: w.ID, Workspace: w.Workspace})
		}
		if p.Title != w.Title {
			out = append(out, TitleEvent{Window: w.ID, Title: w.Title})
		}
		if p.AppID != w.AppID {
			out = append(out, AppIDEvent{Window: w.ID, AppID: w.AppID})
		}
		if p.Rect != w.Rect || p.Output != w.Output {
			out = append(out, RectEvent{Window: w.ID, Output: w.Output, Rect: w.Rect})
		}
		if p.Icon.serial() != w.Icon.serial() {
			out = append(out, IconEvent{Window: w.ID, Icon: w.Icon})
		}
		if p.Urgent != w.Urgent {
			out = append(out, UrgentEvent{Window: w.ID, Urgent: w.Urgent})
		}
		if p.Floating != w.Floating || p.Fullscreen != w.Fullscreen || p.Maximized != w.Maximized || p.ClientFull != w.ClientFull {
			out = append(out, ModeEvent{Window: w.ID, Floating: w.Floating, Fullscreen: w.Fullscreen, Maximized: w.Maximized, ClientFullscreen: w.ClientFull})
		}
		if p.Minimized != w.Minimized {
			out = append(out, MinimizedEvent{Window: w.ID, Minimized: w.Minimized})
		}
		if p.Pinned != w.Pinned {
			out = append(out, PinEvent{Window: w.ID, Pinned: w.Pinned})
		}
		if p.SwallowedBy != w.SwallowedBy {
			out = append(out, SwallowEvent{Window: w.ID, SwallowedBy: w.SwallowedBy})
		}
		if p.Unresponsive != w.Unresponsive {
			out = append(out, UnresponsiveEvent{Window: w.ID, Unresponsive: w.Unresponsive})
		}
		if !sameStack(p.Stack, w.Stack) {
			out = append(out, StackEvent{Window: w.ID, Stack: w.Stack})
		}
		if !sameGroup(p.Group, w.Group) {
			out = append(out, GroupEvent{Window: w.ID, Group: w.Group})
		}
	}
	was, hadFocus := prev.active()
	is, hasFocus := next.active()
	if hadFocus != hasFocus || was.ID != is.ID {
		e := FocusEvent{}
		if hasFocus {
			e.Window = &is.ID
		}
		out = append(out, e)
	}
	out = appendScratchpadEvents(out, prev.Scratchpads, next.Scratchpads)
	out = appendLayerEvents(out, prev.Layers, next.Layers)
	for _, o := range next.ShowDesktop {
		if !slices.Contains(prev.ShowDesktop, o) {
			out = append(out, ShowDesktopEvent{Output: o, On: true})
		}
	}
	for _, o := range prev.ShowDesktop {
		if !slices.Contains(next.ShowDesktop, o) {
			out = append(out, ShowDesktopEvent{Output: o})
		}
	}
	if prev.Submap != next.Submap {
		out = append(out, SubmapEvent{Submap: next.Submap})
	}
	if prev.Layout != next.Layout {
		out = append(out, LayoutEvent{Layout: next.Layout})
	}
	if prev.Locks != next.Locks {
		out = append(out, LocksEvent{Locks: next.Locks})
	}
	if prev.ReducedMotion != next.ReducedMotion {
		out = append(out, ReducedMotionEvent{ReducedMotion: next.ReducedMotion})
	}
	if prev.Overview != next.Overview {
		out = append(out, OverviewEvent{Open: next.Overview})
	}
	if prev.ConfigProblems.Rejected != next.ConfigProblems.Rejected || !slices.Equal(prev.ConfigProblems.Messages, next.ConfigProblems.Messages) {
		out = append(out, ConfigProblemsEvent{ConfigProblems{Rejected: next.ConfigProblems.Rejected, Messages: nonNil(next.ConfigProblems.Messages)}})
	}
	return out
}

func sameStack(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func appendScratchpadEvents(out []Event, prev, next []Scratchpad) []Event {
	for _, n := range next {
		if i := slices.IndexFunc(prev, func(p Scratchpad) bool { return p.Name == n.Name }); i < 0 || prev[i] != n {
			out = append(out, ScratchpadEvent{n})
		}
	}
	for _, p := range prev {
		if !slices.ContainsFunc(next, func(n Scratchpad) bool { return n.Name == p.Name }) {
			out = append(out, ScratchpadEvent{Scratchpad{Name: p.Name}})
		}
	}
	return out
}

func appendLayerEvents(out []Event, prev, next []Layer) []Event {
	for _, p := range prev {
		if !slices.ContainsFunc(next, func(n Layer) bool { return n.ID == p.ID }) {
			out = append(out, layerEvent(LayerClosed, p))
		}
	}
	for _, n := range next {
		if !slices.ContainsFunc(prev, func(p Layer) bool { return p.ID == n.ID }) {
			out = append(out, layerEvent(LayerOpened, n))
		}
	}
	return out
}

func layerEvent(change LayerChange, l Layer) LayerEvent {
	return LayerEvent{Change: change, ID: l.ID, Namespace: l.Namespace, Output: l.Output, Layer: l.Layer}
}
