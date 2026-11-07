package wm

import "fmt"

type WindowMode uint8

const (
	ModeNormal WindowMode = iota
	ModeMaximized
	ModeFullscreen
)

var windowModes = []string{ModeNormal: "normal", ModeMaximized: "maximized", ModeFullscreen: "fullscreen"}

func (m WindowMode) String() string {
	return enumName(windowModes, m, "WindowMode")
}

func (m WindowMode) layer() PlacementLayer {
	switch m {
	case ModeMaximized:
		return LayerMaximized
	case ModeFullscreen:
		return LayerFullscreen
	}
	return LayerTiled
}

func modeOf(l PlacementLayer) WindowMode {
	switch l {
	case LayerMaximized:
		return ModeMaximized
	case LayerFullscreen:
		return ModeFullscreen
	}
	return ModeNormal
}

type Cover struct {
	ID   WindowID
	Mode WindowMode
}

func (l PlacementLayer) Told(fake bool) PlacementLayer {
	if fake {
		return LayerFullscreen
	}
	return l
}

type CoverPolicy uint8

const (
	CoverBehind CoverPolicy = iota
	CoverTakeOver
	CoverExit
)

type Switch uint8

const (
	SwitchToggle Switch = iota
	SwitchOn
	SwitchOff
)

var switches = []string{SwitchToggle: "toggle", SwitchOn: "on", SwitchOff: "off"}

func (s Switch) String() string {
	return enumName(switches, s, "Switch")
}

func (s Switch) From(current bool) bool {
	switch s {
	case SwitchOn:
		return true
	case SwitchOff:
		return false
	}
	return !current
}

func ParseSwitch(text string) (Switch, bool) {
	return parseEnum[Switch](switches, text)
}

func (m *Minimized) setMode(mode WindowMode, on bool) {
	switch {
	case on:
		m.Layer = mode.layer()
	case modeOf(m.Layer) == mode:
		m.Layer = LayerTiled
	}
}

func (s State) Mode(id WindowID) WindowMode {
	w := s.windowWorkspace(id)
	if w == nil {
		return ModeNormal
	}
	if i := w.minimizedIndex(id); i >= 0 {
		return modeOf(w.Minimized[i].Layer)
	}
	if w.Cover.ID == id {
		return w.Cover.Mode
	}
	return ModeNormal
}

func (s State) Fullscreen(id WindowID) bool {
	return s.Mode(id) == ModeFullscreen
}

func (s State) Maximized(id WindowID) bool {
	return s.Mode(id) == ModeMaximized
}

func (s State) SetFullscreen(id WindowID, on bool) (State, error) {
	return s.SetMode(id, ModeFullscreen, on)
}

func (s State) SetMaximized(id WindowID, on bool) (State, error) {
	return s.SetMode(id, ModeMaximized, on)
}

func (s State) SetMode(id WindowID, mode WindowMode, on bool) (State, error) {
	if mode == ModeNormal {
		return State{}, fmt.Errorf("wm: set window %d to mode %s: only maximized and fullscreen switch", id, mode)
	}
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("make window %d %s: %w", id, mode, ErrUnknownWindow)
	}
	if i := w.minimizedIndex(id); i >= 0 {
		w.Minimized[i].setMode(mode, on)
		return s, nil
	}
	switch {
	case on:
		w.Cover = Cover{ID: id, Mode: mode}
		if err := w.focus(id); err != nil {
			return State{}, err
		}
	case w.Cover == Cover{ID: id, Mode: mode}:
		w.Cover = Cover{}
	}
	return s, nil
}

func (s State) SwitchMode(mode WindowMode, sw Switch) (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	return s.SetMode(id, mode, sw.From(s.Mode(id) == mode))
}

func (s State) FakeFullscreen(id WindowID) bool {
	_, ok := s.fake[id]
	return ok
}

func (s State) SetFakeFullscreen(id WindowID, on bool) (State, error) {
	if s.windowWorkspace(id) == nil {
		return State{}, fmt.Errorf("fake fullscreen for window %d: %w", id, ErrUnknownWindow)
	}
	if s.FakeFullscreen(id) == on {
		return s, nil
	}
	s = s.clone()
	if !on {
		delete(s.fake, id)
		return s, nil
	}
	if s.fake == nil {
		s.fake = map[WindowID]struct{}{}
	}
	s.fake[id] = struct{}{}
	return s, nil
}

func (s State) SwitchFakeFullscreen(sw Switch) (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	return s.SetFakeFullscreen(id, sw.From(s.FakeFullscreen(id)))
}

func (s State) ClientRequestsFullscreen(id WindowID, on bool) (State, error) {
	if s.FakeFullscreen(id) {
		return s.SetFakeFullscreen(id, on)
	}
	return s.SetFullscreen(id, on)
}

func (s State) Opened(id WindowID, policy CoverPolicy) (State, error) {
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("open window %d: %w", id, ErrUnknownWindow)
	}
	cover := w.Cover
	if cover.ID == 0 || cover.ID == id {
		return s, nil
	}
	s = s.clone()
	w = s.windowWorkspace(id)
	switch policy {
	case CoverBehind:
		return s, w.focus(cover.ID)
	case CoverTakeOver:
		w.Cover = Cover{ID: id, Mode: cover.Mode}
	case CoverExit:
		w.Cover = Cover{}
	}
	return s, w.focus(id)
}
