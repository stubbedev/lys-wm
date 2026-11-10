package wm

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Action interface {
	fmt.Stringer
	apply(s State, settings Settings) (State, []Effect, error)
}

type Effect interface {
	isEffect()
}

type DragMode uint8

const (
	DragMove DragMode = iota + 1
	DragResize
)

type ZoomMode uint8

const (
	ZoomIn ZoomMode = iota + 1
	ZoomOut
	ZoomReset
	ZoomSet
)

var zoomModes = []string{ZoomIn: "in", ZoomOut: "out", ZoomReset: "reset", ZoomSet: "set"}

const (
	MaxZoom         = 16.0
	DefaultZoomStep = 1.5
)

type OverviewMode uint8

const (
	OverviewToggle OverviewMode = iota + 1
	OverviewShow
	OverviewHide
)

var overviewModes = []string{OverviewToggle: "toggle", OverviewShow: "show", OverviewHide: "hide"}

func (m OverviewMode) String() string {
	return enumName(overviewModes, m, "OverviewMode")
}

type OverviewScope uint8

const (
	OverviewWorkspace OverviewScope = iota
	OverviewOutput
	OverviewApp
)

var overviewScopes = []string{OverviewWorkspace: "workspace", OverviewOutput: "output", OverviewApp: "app"}

func (s OverviewScope) String() string {
	return enumName(overviewScopes, s, "OverviewScope")
}

func ParseOverviewScope(s string) (OverviewScope, bool) {
	return parseEnum[OverviewScope](overviewScopes, s)
}

func OverviewScopeNames() []string {
	return slices.Clone(overviewScopes)
}

func (CloseWindowAction) isEffect()         {}
func (ExecAction) isEffect()                {}
func (SubmapAction) isEffect()              {}
func (DragAction) isEffect()                {}
func (ReloadAction) isEffect()              {}
func (DismissConfigNoticeAction) isEffect() {}
func (ExitAction) isEffect()                {}
func (NextLayoutAction) isEffect()          {}
func (StickyKeysAction) isEffect()          {}
func (SlowKeysAction) isEffect()            {}
func (GlobalAction) isEffect()              {}
func (DPMSAction) isEffect()                {}
func (MinimizeAction) isEffect()            {}
func (RestoreAction) isEffect()             {}
func (ZoomAction) isEffect()                {}

func (OverviewAction) isEffect() {}

func emit(s State, e Effect) (State, []Effect, error) {
	return s, []Effect{e}, nil
}

type Settings struct {
	Collapse     CollapsePolicy
	Autotile     Autotile
	Gaps         Gaps
	Focus        FocusSettings
	NewOverCover CoverPolicy
	Scratchpads  map[ScratchpadName]ScratchpadSpec
	Peek         int
	FloatStep    int
	Scroll       Scrolling
	Layout       LayoutKind
}

type WorkspaceTarget interface {
	fmt.Stringer
	resolve(s State) (WorkspaceID, error)
}

type RelativeWorkspace int

type PreviousWorkspace struct{}

func (id WorkspaceID) String() string {
	return strconv.Itoa(int(id))
}

func (id WorkspaceID) resolve(State) (WorkspaceID, error) {
	return id, nil
}

func (r RelativeWorkspace) String() string {
	return fmt.Sprintf("e%+d", int(r))
}

func (r RelativeWorkspace) resolve(s State) (WorkspaceID, error) {
	return s.relative(int(r), false)
}

type OutputRelativeWorkspace int

func (r OutputRelativeWorkspace) String() string {
	return fmt.Sprintf("m%+d", int(r))
}

func (r OutputRelativeWorkspace) resolve(s State) (WorkspaceID, error) {
	return s.relative(int(r), true)
}

type EmptyWorkspace struct{}

func (EmptyWorkspace) String() string { return "empty" }

func (EmptyWorkspace) resolve(s State) (WorkspaceID, error) {
	return s.firstUnused(), nil
}

func (PreviousWorkspace) String() string { return "previous" }

func (PreviousWorkspace) resolve(s State) (WorkspaceID, error) {
	return s.previous()
}

type (
	FocusAction           struct{ Dir Direction }
	MoveAction            struct{ Dir Direction }
	WorkspaceAction       struct{ Target WorkspaceTarget }
	MoveToWorkspaceAction struct {
		Target WorkspaceTarget
		Follow bool
	}
	KillAction        struct{}
	FocusWindowAction struct{ ID WindowID }
	CloseWindowAction struct{ ID WindowID }
	ExecAction        struct{ Command string }
	MakeGroupAction   struct {
		Layout    Layout
		Ephemeral bool
	}
	SubmapAction     struct{ Name string }
	FloatAction      struct{}
	FullscreenAction struct {
		Switch Switch
		Fake   bool
	}
	MaximizeAction            struct{ Switch Switch }
	DragAction                struct{ Mode DragMode }
	ResizeAction              struct{ DX, DY int }
	ReloadAction              struct{}
	DismissConfigNoticeAction struct{}
	ExitAction                struct{}
	NextLayoutAction          struct{}
	StickyKeysAction          struct{ On bool }
	SlowKeysAction            struct{ Delay time.Duration }
	GlobalAction              struct{ AppID, ID string }
	DPMSAction                struct {
		Mode   Switch
		Output string
	}
	MinimizeAction struct{ ID WindowID }
	RestoreAction  struct{ ID WindowID }
	ZoomAction     struct {
		Mode   ZoomMode
		Factor float64
	}

	OverviewAction struct {
		Mode  OverviewMode
		Scope OverviewScope
	}
)

func optionalWindow(name string, id WindowID) string {
	if id == 0 {
		return name
	}
	return name + " " + strconv.FormatUint(uint64(id), 10)
}

func (a OverviewAction) String() string {
	if a.Scope == OverviewWorkspace {
		return "overview " + a.Mode.String()
	}
	return "overview " + a.Mode.String() + " " + a.Scope.String()
}

func (a OverviewAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a MinimizeAction) String() string { return optionalWindow("minimize", a.ID) }
func (a RestoreAction) String() string  { return optionalWindow("restore", a.ID) }

func (a MinimizeAction) apply(s State, _ Settings) (State, []Effect, error) {
	id := a.ID
	if id == 0 {
		focused, ok := s.Focused()
		if !ok {
			return s, nil, nil
		}
		id = focused
	}
	if _, ok := s.WorkspaceOf(id); !ok {
		return State{}, nil, fmt.Errorf("minimize window %d: %w", id, ErrUnknownWindow)
	}
	return emit(s, MinimizeAction{ID: id})
}

func (a RestoreAction) apply(s State, _ Settings) (State, []Effect, error) {
	id := a.ID
	if id == 0 {
		last, ok := s.LastMinimized()
		if !ok {
			return s, nil, nil
		}
		id = last
	}
	if _, ok := s.WorkspaceOf(id); !ok {
		return State{}, nil, fmt.Errorf("restore window %d: %w", id, ErrUnknownWindow)
	}
	return emit(s, RestoreAction{ID: id})
}

func (a DPMSAction) String() string {
	if a.Output == "" {
		return "dpms " + a.Mode.String()
	}
	return "dpms " + a.Mode.String() + " " + a.Output
}

func (a DPMSAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a ZoomAction) String() string {
	if a.Mode == ZoomReset || (a.Mode != ZoomSet && a.Factor == DefaultZoomStep) {
		return "zoom " + zoomModes[a.Mode]
	}
	return "zoom " + zoomModes[a.Mode] + " " + strconv.FormatFloat(a.Factor, 'g', -1, 64)
}

func (a ZoomAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a ZoomAction) Next(current float64) float64 {
	next := 1.0
	switch a.Mode {
	case ZoomIn:
		next = current * a.Factor
	case ZoomOut:
		next = current / a.Factor
	case ZoomSet:
		next = a.Factor
	}
	return min(max(next, 1), MaxZoom)
}

func parseZoom(spec ActionSpec, args []string, _ string) (Action, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, badArgs(spec, "takes a mode and at most one factor, not %d arguments", len(args))
	}
	var mode ZoomMode
	for m, name := range zoomModes {
		if name != "" && name == args[0] {
			mode = ZoomMode(m)
		}
	}
	switch {
	case mode == 0:
		return nil, badArgs(spec, "unknown mode %q", args[0])
	case mode == ZoomReset && len(args) == 2:
		return nil, badArgs(spec, "reset takes no factor, not %q", args[1])
	case mode == ZoomReset:
		return ZoomAction{Mode: mode}, nil
	case mode == ZoomSet && len(args) == 1:
		return nil, badArgs(spec, "set needs a factor")
	case len(args) == 1:
		return ZoomAction{Mode: mode, Factor: DefaultZoomStep}, nil
	}
	factor, err := strconv.ParseFloat(args[1], 64)
	if err != nil || !(factor >= 1 && factor <= MaxZoom) {
		return nil, badArgs(spec, "%q is not a factor from 1 to %g", args[1], MaxZoom)
	}
	return ZoomAction{Mode: mode, Factor: factor}, nil
}

func (a GlobalAction) String() string { return "global " + a.AppID + ":" + a.ID }

func (a GlobalAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (ReloadAction) String() string { return "reload" }

func (ExitAction) String() string { return "exit" }

func (a ExitAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (NextLayoutAction) String() string { return "nextlayout" }

func (a NextLayoutAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a StickyKeysAction) String() string {
	if a.On {
		return "stickykeys on"
	}
	return "stickykeys off"
}

func (a StickyKeysAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a SlowKeysAction) String() string {
	return "slowkeys " + strconv.FormatInt(a.Delay.Milliseconds(), 10)
}

func (a SlowKeysAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a ReloadAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (DismissConfigNoticeAction) String() string { return "dismissconfignotice" }

func (a DismissConfigNoticeAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a ResizeAction) String() string { return fmt.Sprintf("resizeactive %d %d", a.DX, a.DY) }

func (a ResizeAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.ResizeFocused(a.DX, a.DY)
	return next, nil, err
}

func (FloatAction) String() string { return "togglefloating" }
func switched(name string, sw Switch) string {
	if sw == SwitchToggle {
		return name
	}
	return name + " " + sw.String()
}

func (a FullscreenAction) String() string {
	text := switched("fullscreen", a.Switch)
	if a.Fake {
		text += " fake"
	}
	return text
}

func (a MaximizeAction) String() string { return switched("maximize", a.Switch) }
func (a DragAction) String() string {
	if a.Mode == DragResize {
		return "resizewindow"
	}
	return "movewindow"
}

func (FloatAction) apply(s State, settings Settings) (State, []Effect, error) {
	next, err := s.ToggleFloating(settings.Autotile)
	return next, nil, err
}

func (a FullscreenAction) apply(s State, _ Settings) (State, []Effect, error) {
	if a.Fake {
		next, err := s.SwitchFakeFullscreen(a.Switch)
		return next, nil, err
	}
	next, err := s.SwitchMode(ModeFullscreen, a.Switch)
	return next, nil, err
}

func (a MaximizeAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.SwitchMode(ModeMaximized, a.Switch)
	return next, nil, err
}

func (a DragAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a FocusAction) String() string     { return "focus " + a.Dir.String() }
func (a MoveAction) String() string      { return "move " + a.Dir.String() }
func (a WorkspaceAction) String() string { return "workspace " + a.Target.String() }
func (a MoveToWorkspaceAction) String() string {
	if a.Follow {
		return "movetoworkspace " + a.Target.String() + " follow"
	}
	return "movetoworkspace " + a.Target.String()
}
func (KillAction) String() string { return "kill" }
func (a FocusWindowAction) String() string {
	return "focuswindow " + strconv.FormatUint(uint64(a.ID), 10)
}

func (a CloseWindowAction) String() string {
	return "closewindow " + strconv.FormatUint(uint64(a.ID), 10)
}
func (a ExecAction) String() string { return "exec " + a.Command }
func (a MakeGroupAction) String() string {
	if a.Ephemeral {
		return "makegroup " + a.Layout.String() + " ephemeral"
	}
	return "makegroup " + a.Layout.String()
}

func (a SubmapAction) String() string {
	if a.Name == "" {
		return "submap reset"
	}
	return "submap " + a.Name
}

func (a SubmapAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a FocusAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.FocusDirection(a.Dir)
	return next, nil, err
}

func (a MoveAction) apply(s State, settings Settings) (State, []Effect, error) {
	next, err := s.MoveDirection(a.Dir, settings.Collapse, settings.FloatStep)
	return next, nil, err
}

func (a WorkspaceAction) apply(s State, _ Settings) (State, []Effect, error) {
	id, err := a.Target.resolve(s)
	if err != nil {
		return State{}, nil, err
	}
	next, err := s.SwitchWorkspace(id)
	return next, nil, err
}

func (a MoveToWorkspaceAction) apply(s State, settings Settings) (State, []Effect, error) {
	id, err := a.Target.resolve(s)
	if err != nil {
		return State{}, nil, err
	}
	if _, ok := s.Focused(); !ok {
		return s, nil, nil
	}
	var next State
	if _, g := s.lifted(); g != nil {
		next, err = s.moveFocusedNode(id)
	} else {
		next, err = s.MoveToWorkspace(id, settings.Autotile)
	}
	if err != nil || !a.Follow {
		return next, nil, err
	}
	next, err = next.SwitchWorkspace(id)
	return next, nil, err
}

func (KillAction) apply(s State, _ Settings) (State, []Effect, error) {
	if _, g := s.lifted(); g != nil {
		ids := appendWindows(nil, g)
		effects := make([]Effect, len(ids))
		for i, id := range ids {
			effects[i] = CloseWindowAction{ID: id}
		}
		return s, effects, nil
	}
	id, ok := s.Focused()
	if !ok {
		return s, nil, nil
	}
	return s, []Effect{CloseWindowAction{ID: id}}, nil
}

func (a FocusWindowAction) apply(s State, _ Settings) (State, []Effect, error) {
	if s.Minimized(a.ID) {
		return emit(s, RestoreAction(a))
	}
	next, err := s.FocusWindow(a.ID, Raise)
	return next, nil, err
}

func (a CloseWindowAction) apply(s State, _ Settings) (State, []Effect, error) {
	if _, ok := s.WorkspaceOf(a.ID); !ok {
		return State{}, nil, fmt.Errorf("close window %d: %w", a.ID, ErrUnknownWindow)
	}
	return emit(s, a)
}

func (a ExecAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a MakeGroupAction) apply(s State, _ Settings) (State, []Effect, error) {
	return s.withFocusedTree(func(t Tree) (Tree, bool) { return t.WrapFocused(a.Layout, a.Ephemeral) }), nil, nil
}

func Dispatch(s State, a Action, settings Settings) (State, []Effect, error) {
	next, effects, err := a.apply(s, settings)
	if err != nil {
		return State{}, nil, fmt.Errorf("wm: %s: %w", a, err)
	}
	return next, effects, nil
}

type ActionSpec struct {
	Name    string
	Args    string
	Summary string
	parse   func(spec ActionSpec, args []string, rest string) (Action, error)
}

var ErrBadAction = errors.New("bad action")

func (spec ActionSpec) Usage() string {
	if spec.Args == "" {
		return spec.Name
	}
	return spec.Name + " " + spec.Args
}

func badArgs(spec ActionSpec, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrBadAction, spec.Usage(), fmt.Sprintf(format, args...))
}

func parseDirection(s string) (Direction, bool) {
	if d, ok := parseEnum[Direction](directionNames, s); ok {
		return d, true
	}
	for d, name := range directionNames {
		if name != "" && s == name[:1] {
			return Direction(d), true
		}
	}
	return 0, false
}

func parseWorkspace(s string) (WorkspaceTarget, bool) {
	switch s {
	case PreviousWorkspace{}.String():
		return PreviousWorkspace{}, true
	case EmptyWorkspace{}.String():
		return EmptyWorkspace{}, true
	}
	if len(s) > 2 && (s[0] == 'e' || s[0] == 'm') && (s[1] == '+' || s[1] == '-') {
		n, err := strconv.Atoi(s[1:])
		if err != nil || n == 0 {
			return nil, false
		}
		if s[0] == 'm' {
			return OutputRelativeWorkspace(n), true
		}
		return RelativeWorkspace(n), true
	}
	if n, err := strconv.Atoi(s); err == nil {
		return WorkspaceID(n), n >= 1
	}
	return WorkspaceName(s), ValidWorkspaceName(s)
}

func ValidWorkspaceName(s string) bool {
	for i, r := range s {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || ((r < '0' || r > '9') && r != '-' && r != '_')) {
			return false
		}
	}
	return s != ""
}

func ParseLayout(s string) (Layout, bool) {
	l, ok := parseEnum[Layout](layoutNames, s)
	return l, ok && l != Scrolled
}

func exactly(spec ActionSpec, args []string, n int) error {
	if len(args) != n {
		return badArgs(spec, "takes %d argument(s), got %d", n, len(args))
	}
	return nil
}

func directional(build func(Direction) Action) func(ActionSpec, []string, string) (Action, error) {
	return func(spec ActionSpec, args []string, _ string) (Action, error) {
		if err := exactly(spec, args, 1); err != nil {
			return nil, err
		}
		d, ok := parseDirection(args[0])
		if !ok {
			return nil, badArgs(spec, "unknown direction %q", args[0])
		}
		return build(d), nil
	}
}

const workspaceTargets = "<n|name|e+n|e-n|m+n|m-n|empty|previous>"

func workspaceArg(spec ActionSpec, arg string) (WorkspaceTarget, error) {
	w, ok := parseWorkspace(arg)
	if !ok {
		return nil, badArgs(spec, "unknown workspace %q", arg)
	}
	return w, nil
}

func parseWorkspaceAction(spec ActionSpec, args []string, _ string) (Action, error) {
	if err := exactly(spec, args, 1); err != nil {
		return nil, err
	}
	w, err := workspaceArg(spec, args[0])
	if err != nil {
		return nil, err
	}
	return WorkspaceAction{Target: w}, nil
}

func parseMoveToWorkspace(spec ActionSpec, args []string, _ string) (Action, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, badArgs(spec, "takes a workspace and an optional follow, not %d arguments", len(args))
	}
	if len(args) == 2 && args[1] != "follow" {
		return nil, badArgs(spec, "unknown option %q", args[1])
	}
	w, err := workspaceArg(spec, args[0])
	if err != nil {
		return nil, err
	}
	return MoveToWorkspaceAction{Target: w, Follow: len(args) == 2}, nil
}

func windowed(build func(WindowID) Action) func(ActionSpec, []string, string) (Action, error) {
	return func(spec ActionSpec, args []string, _ string) (Action, error) {
		if err := exactly(spec, args, 1); err != nil {
			return nil, err
		}
		id, err := strconv.ParseUint(args[0], 10, 64)
		if err != nil || id == 0 {
			return nil, badArgs(spec, "window ID %q is not a positive number", args[0])
		}
		return build(WindowID(id)), nil
	}
}

func optionallyWindowed(build func(WindowID) Action) func(ActionSpec, []string, string) (Action, error) {
	one := windowed(build)
	return func(spec ActionSpec, args []string, rest string) (Action, error) {
		if len(args) == 0 {
			return build(0), nil
		}
		return one(spec, args, rest)
	}
}

var Actions = []ActionSpec{
	{
		Name: "focus", Args: "<left|right|up|down>",
		Summary: "Move focus to the neighbouring window, or to the next output at the edge.",
		parse:   directional(func(d Direction) Action { return FocusAction{Dir: d} }),
	},
	{
		Name: "move", Args: "<left|right|up|down>",
		Summary: "Move the focused window, hy3 style; at the workspace edge it crosses to the neighbouring output's workspace, entering its tree at the near edge, and focus follows. A floating window steps by `floating.step` pixels inside the usable area, and from its edge crosses to the neighbouring output.",
		parse:   directional(func(d Direction) Action { return MoveAction{Dir: d} }),
	},
	{
		Name: "workspace", Args: workspaceTargets,
		Summary: "Show workspace n, or the one a workspace rule names, on its bound output when a rule binds it; the n-th open workspace after or before the current one, of every output (e) or of the focused output (m); the lowest free number, opened on the focused output (empty); or the one the output showed before it.",
		parse:   parseWorkspaceAction,
	},
	{
		Name: "movetoworkspace", Args: workspaceTargets + " [follow]",
		Summary: "Move the focused window to a workspace, chosen as for workspace; with follow, show that workspace with the window focused, in one switch.",
		parse:   parseMoveToWorkspace,
	},
	{
		Name: "focusoutput", Args: "<left|right|up|down|name>",
		Summary: "Focus the neighbouring output in that direction, or the output with that name, and the window focused there.",
		parse:   outputTargeted(func(o OutputTarget) Action { return FocusOutputAction{Target: o} }),
	},
	{
		Name: "movetooutput", Args: "<left|right|up|down|name>",
		Summary: "Move the focused window onto the workspace that output shows and focus it there; a floating window keeps its place relative to the output.",
		parse:   outputTargeted(func(o OutputTarget) Action { return MoveToOutputAction{Target: o} }),
	},
	{
		Name: "moveworkspacetooutput", Args: "<left|right|up|down|name>",
		Summary: "Move the focused workspace with its windows, layout and focus to that output and show it there; floating windows keep their place relative to the output, inside its area.",
		parse:   outputTargeted(func(o OutputTarget) Action { return MoveWorkspaceToOutputAction{Target: o} }),
	},
	{
		Name: "swapworkspaces", Args: "<output> <output>",
		Summary: "Swap the workspaces two outputs show, each with its windows; an output is a direction from the focused one or a name.",
		parse:   parseSwapWorkspaces,
	},
	{
		Name: "scratchpad", Args: "<toggle <name>|hide>",
		Summary: "Show the named scratchpad over the focused output, above its workspace and dimming it, or hide it when it shows there; one shown elsewhere moves here. A scratchpad holds windows like a workspace that no number reaches. Toggling one that is empty runs its `exec` from `scratchpads`, and the window that opens lands in it. hide hides the one over the focused output.",
		parse:   parseScratchpad,
	},
	{
		Name: "movetoscratchpad", Args: "<name>",
		Summary: "Move the focused window into the named scratchpad; it hides with the scratchpad unless that shows on its output. movetoworkspace brings it back.",
		parse:   parseMoveToScratchpad,
	},
	{
		Name:    "kill",
		Summary: "Ask the focused window to close.",
		parse:   bare(KillAction{}),
	},
	{
		Name: "focuswindow", Args: "<id>",
		Summary: "Focus the window with this IPC ID, showing its workspace on its output and raising it, and its tab when grouped; a minimized window comes back.",
		parse:   windowed(func(id WindowID) Action { return FocusWindowAction{ID: id} }),
	},
	{
		Name: "minimize", Args: "[id]",
		Summary: "Hide the focused window, or the one with this IPC ID, with its dialogs; the rest of its workspace takes its space.",
		parse:   optionallyWindowed(func(id WindowID) Action { return MinimizeAction{ID: id} }),
	},
	{
		Name: "restore", Args: "[id]",
		Summary: "Bring back the window with this IPC ID, or the one minimized last on the focused workspace, and focus it.",
		parse:   optionallyWindowed(func(id WindowID) Action { return RestoreAction{ID: id} }),
	},
	{
		Name: "overview", Args: "[toggle|show|hide] [workspace|output|app]",
		Summary: "Open or close the overview on every output, under a strip of each output's workspaces: the windows of the workspace shown (workspace, the default), every window of the output grouped by workspace (output), or the focused window's app across every workspace and output, on the focused output (app). Without a mode, toggle.",
		parse:   parseOverview,
	},
	{
		Name: "closewindow", Args: "<id>",
		Summary: "Ask the window with this IPC ID to close.",
		parse:   windowed(func(id WindowID) Action { return CloseWindowAction{ID: id} }),
	},
	{
		Name: "dpms", Args: "<on|off|toggle> [output]",
		Summary: "Turn outputs on or off, or one output by name; off outputs keep their windows and wake only on dpms on.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if len(args) == 0 || len(args) > 2 {
				return nil, badArgs(spec, "needs on, off or toggle and an optional output")
			}
			mode, ok := ParseSwitch(args[0])
			if !ok {
				return nil, badArgs(spec, "%q is not on, off or toggle", args[0])
			}
			a := DPMSAction{Mode: mode}
			if len(args) == 2 {
				a.Output = args[1]
			}
			return a, nil
		},
	},
	{
		Name:    "reload",
		Summary: "Read the config file again; a bad file keeps the running config and reports the error.",
		parse:   bare(ReloadAction{}),
	},
	{
		Name:    "dismissconfignotice",
		Summary: "Hide the notice on screen about config problems; `lys-wmctl configproblems` still lists them, and the next reload shows what it finds.",
		parse:   bare(DismissConfigNoticeAction{}),
	},
	{
		Name:    "exit",
		Summary: "End the session: lys-wm closes its clients, Xwayland and IPC socket and exits, as on SIGTERM.",
		parse:   bare(ExitAction{}),
	},
	{
		Name:    "nextlayout",
		Summary: "Switch the keyboard to its next layout, wrapping after the last. With one layout it does nothing.",
		parse:   bare(NextLayoutAction{}),
	},
	{
		Name: "stickykeys", Args: "<on|off>",
		Summary: "Turn sticky keys on or off: a modifier pressed and released alone applies to the next key, pressed twice it stays on until pressed again.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if err := exactly(spec, args, 1); err != nil {
				return nil, err
			}
			switch args[0] {
			case "on":
				return StickyKeysAction{On: true}, nil
			case "off":
				return StickyKeysAction{}, nil
			}
			return nil, badArgs(spec, "%q is not on or off", args[0])
		},
	},
	{
		Name: "slowkeys", Args: "<ms>",
		Summary: "Accept a key only once it was held this many milliseconds; 0 turns slow keys off.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if err := exactly(spec, args, 1); err != nil {
				return nil, err
			}
			ms, err := strconv.ParseUint(args[0], 10, 16)
			if err != nil {
				return nil, badArgs(spec, "%q is not milliseconds from 0 to 65535", args[0])
			}
			return SlowKeysAction{Delay: time.Duration(ms) * time.Millisecond}, nil
		},
	},
	{
		Name: "global", Args: "<app_id>:<id>",
		Summary: "Press the global shortcut an application registered, and release it with the bind.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if err := exactly(spec, args, 1); err != nil {
				return nil, err
			}
			app, id, ok := strings.Cut(args[0], ":")
			if !ok || app == "" || id == "" {
				return nil, badArgs(spec, "%q is not app_id:id", args[0])
			}
			return GlobalAction{AppID: app, ID: id}, nil
		},
	},
	{
		Name: "exec", Args: "<command>",
		Summary: "Run a shell command with sh -c.",
		parse: func(spec ActionSpec, _ []string, rest string) (Action, error) {
			if rest == "" {
				return nil, badArgs(spec, "needs a command")
			}
			return ExecAction{Command: rest}, nil
		},
	},
	{
		Name: "makegroup", Args: "<h|v|tab> [ephemeral]",
		Summary: "Wrap the focused window in a new group; an ephemeral group dissolves when one window is left.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if len(args) == 0 || len(args) > 2 {
				return nil, badArgs(spec, "takes 1 or 2 arguments, got %d", len(args))
			}
			l, ok := ParseLayout(args[0])
			if !ok {
				return nil, badArgs(spec, "unknown layout %q", args[0])
			}
			if len(args) == 2 && args[1] != "ephemeral" {
				return nil, badArgs(spec, "unknown option %q", args[1])
			}
			return MakeGroupAction{Layout: l, Ephemeral: len(args) == 2}, nil
		},
	},
	{
		Name: "setlayout", Args: "<tree|scroll>",
		Summary: "Turn the focused workspace into a tree of splits and tab groups, or into a scrollable strip of columns: each top-level child becomes a column, keeping its share of the width, and nested groups flatten into the column's stack; back, the columns become a horizontal split. Every window and the focus stay.",
		parse:   parseSetLayout,
	},
	{
		Name: "columnwidth", Args: "<preset next|preset prev|+n%|-n%|n%|+npx|-npx|npx>",
		Summary: "On a scrolling workspace, set the focused column's width to the next or previous of `scroll.presets`, change it by n percent of the output's width or n pixels, or set it to n percent or a fixed n pixels.",
		parse:   parseColumnWidth,
	},
	{
		Name: "consume", Args: "<left|right>",
		Summary: "On a scrolling workspace, move the focused window into the bottom of the column on that side.",
		parse:   parseConsume,
	},
	{
		Name:    "expel",
		Summary: "On a scrolling workspace, take the focused window out of its column into a column of its own to the right.",
		parse:   bare(ExpelAction{}),
	},
	{
		Name:    "center",
		Summary: "On a scrolling workspace, scroll the view so the focused column is in the middle of the output.",
		parse:   bare(CenterAction{}),
	},
	{
		Name: "columndisplay", Args: "<stack|tab>",
		Summary: "On a scrolling workspace, show the windows of the focused column stacked over each other or as tabs.",
		parse:   parseColumnDisplay,
	},
	{
		Name: "changegroup", Args: "<h|v|tab|toggletab|opposite>",
		Summary: "Change the layout of the focused group, or of the group around the focused window, keeping its ratios. toggletab switches between tabs and the split it had before, h for a group made as tabs; opposite turns h into v and back.",
		parse:   parseChangeGroup,
	},
	{
		Name: "focustab", Args: "<prev|next|n> [wrap]",
		Summary: "Show the previous, next or n-th tab of the nearest tab group around the focused window; prev and next stop at the ends unless wrap.",
		parse:   parseFocusTab,
	},
	{
		Name:    "focusparent",
		Summary: "Focus the group around the focused window or group, drawn with the active border, so move, swap, moveout, kill, togglefloating and movetoworkspace act on all of it; keyboard input still goes to the window inside.",
		parse:   bare(FocusParentAction{}),
	},
	{
		Name:    "focuschild",
		Summary: "Undo one focusparent, down toward the focused window.",
		parse:   bare(FocusChildAction{}),
	},
	{
		Name: "swap", Args: "<left|right|up|down>",
		Summary: "Exchange the focused window or group with its neighbour in that direction; each slot keeps its size.",
		parse:   directional(func(d Direction) Action { return SwapAction{Dir: d} }),
	},
	{
		Name:    "moveout",
		Summary: "Lift the focused window or group out of its group, placing it after that group in the one around it.",
		parse:   bare(MoveOutAction{}),
	},
	{
		Name: "equalize", Args: "[all]",
		Summary: "Give the children of the focused group, or of the group around the focused window, equal shares again; all does so for the whole tree.",
		parse:   parseEqualize,
	},
	{
		Name: "locktab", Args: "[toggle|on|off]",
		Summary: "Lock the nearest tab group around the focused window: new windows open beside it instead of in it, and move and swap neither enter nor leave it, though its tabs still reorder. toggle unless told.",
		parse:   parseLockTab,
	},
	{
		Name: "submap", Args: "<name|reset>",
		Summary: "Switch binds to the named submap, or back to the default ones.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if err := exactly(spec, args, 1); err != nil {
				return nil, err
			}
			if args[0] == "reset" {
				return SubmapAction{}, nil
			}
			return SubmapAction{Name: args[0]}, nil
		},
	},
	{
		Name:    "togglefloating",
		Summary: "Float the focused window centred at half the output, or tile it again.",
		parse:   bare(FloatAction{}),
	},
	{
		Name: "place", Args: "<left|right|top|bottom|topleft|topright|bottomleft|bottomright|fill|center|restore>",
		Summary: "Float the focused window onto that half, quarter or the whole usable area, inside the gaps and its size limits, the same rect a drag to that edge snaps to, or centre it; restore returns it to where it was, floating or tiled, before the last place or drag snap.",
		parse:   parsePlace,
	},
	{
		Name: "focusfloating", Args: "[toggle|on|off]",
		Summary: "Move focus between the floating windows of the focused workspace (on) and its tiled ones (off); toggle unless told.",
		parse:   parseFocusFloating,
	},
	{
		Name: "fullscreen", Args: "[toggle|on|off] [fake]",
		Summary: "Make the focused window cover its output, above the top layer, or end that; toggle unless told. With fake the window stays where it is and is only told it is fullscreen, so a video player in a tile fills the tile.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			fake := len(args) > 0 && args[len(args)-1] == "fake"
			if fake {
				args = args[:len(args)-1]
			}
			sw, err := optionalSwitch(spec, args)
			return FullscreenAction{Switch: sw, Fake: fake}, err
		},
	},
	{
		Name: "setprop", Args: "<id> <key> <value>",
		Summary: "Give the window with this IPC ID a rule key until it closes, as a rule matching only it would, after every rule: `setprop 7 border {active: red}`, `setprop 7 opacity 0.8`. The value is YAML, as in the config file.",
		parse:   parseSetProp,
	},
	{
		Name: "showdesktop", Args: "[toggle|on|off] [all]",
		Summary: "Slide the windows of the output under the pointer, or of every output with all, toward their nearest edges until only a sliver of each shows, to reach the desktop. Nothing is resized and the layout stays; keyboard focus leaves the windows. A click on a sliver, a window opening or being activated there, a workspace switch or focusing a window ends it, as does the action again. toggle unless told.",
		parse:   parseShowDesktop,
	},
	{
		Name: "swallow", Args: "[toggle|on|off]",
		Summary: "For a window that swallowed the terminal it was started from (see `swallow` in config.md), or that terminal: off shows the terminal again beside the window, on hides it in the window's place once more; toggle unless told.",
		parse:   parseSwallow,
	},
	{
		Name: "pin", Args: "[toggle|on|off]",
		Summary: "Keep the focused window on screen through workspace switches of its output, floating above the workspace's other floating windows; a tiled window floats first. toggle unless told; moving it to a workspace or tiling it unpins it.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			sw, err := optionalSwitch(spec, args)
			return PinAction{Switch: sw}, err
		},
	},
	{
		Name: "maximize", Args: "[toggle|on|off]",
		Summary: "Make the focused window fill its output's usable area inside the outer gaps, keeping bars and its border, above its workspace's other windows, or end that; toggle unless told.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			sw, err := optionalSwitch(spec, args)
			return MaximizeAction{Switch: sw}, err
		},
	},
	{
		Name: "resizeactive", Args: "<dx> <dy>",
		Summary: "Grow or shrink the focused window by pixels; tiled windows move their right or bottom edge, else their left or top one.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if err := exactly(spec, args, 2); err != nil {
				return nil, err
			}
			dx, errX := strconv.Atoi(args[0])
			dy, errY := strconv.Atoi(args[1])
			if errX != nil || errY != nil {
				return nil, badArgs(spec, "%q and %q are not whole pixels", args[0], args[1])
			}
			return ResizeAction{DX: dx, DY: dy}, nil
		},
	},
	{
		Name:    "movewindow",
		Summary: "Mouse binds only: drag the window under the pointer; tiled windows drop into the tree where released. A window drawing its own title bar starts the same drag with xdg_toplevel.move or _NET_WM_MOVERESIZE, with the button or touch point it was pressed with.",
		parse:   bare(DragAction{Mode: DragMove}),
	},
	{
		Name:    "resizewindow",
		Summary: "Mouse binds only: resize the window under the pointer from its nearest corner; tiled windows change their split ratios. A window drawing its own edges starts the same drag from the edge it names with xdg_toplevel.resize or _NET_WM_MOVERESIZE.",
		parse:   bare(DragAction{Mode: DragResize}),
	},
	{
		Name: "zoom", Args: "<in|out|reset|set> [factor]",
		Summary: "Magnify the output under the pointer around the pointer: by factor, 1.5 unless given, from where it is going, back to 1, or to factor, up to 16.",
		parse:   parseZoom,
	},
	{
		Name: "debug", Args: "<damage|opaque|planes|protocol> [toggle|on|off], protocol dump <file>, <trace|profile> <start <file>|stop>, heap <file>",
		Summary: "Debugging a running session, bound to nothing by default. `damage` fills each frame's damage with a tint that fades over 8 frames; `opaque` tints what the culler takes as opaque; `planes` tints every composited frame, so whatever shows untinted went to direct scanout or an overlay plane. `trace` records the frame trace of `-frame-trace` to an absolute path, `profile` a Go CPU profile, and `heap` writes a heap profile once. `protocol` keeps the most recent Wayland messages of every client in memory, and `protocol dump` writes them to an absolute path, oldest first, in the `WAYLAND_DEBUG` format.",
		parse:   parseDebug,
	},
	{
		Name: "casttarget", Args: "<window [id]|output [name]|clear>",
		Summary: "Point the dynamic screencast source at a window, the focused one unless given, or an output, the focused one unless given, or at nothing. A stream on that source (lys_dynamic_capture_v1) follows without stopping. When the window closes or the output goes, the target clears and the stream shows nothing until the next one.",
		parse:   parseCastTarget,
	},
	{
		Name:    "focuslast",
		Summary: "Focus the window focused before the focused one, showing its workspace; pressed again, it toggles between the two.",
		parse:   bare(FocusLastAction{}),
	},
	{
		Name:    "focusurgent",
		Summary: "Focus the most recently used window that asks for attention, or the window focused before, as focuslast.",
		parse:   bare(FocusUrgentAction{}),
	},
	{
		Name: "cycle", Args: "<next|prev> [app|workspace|output]",
		Summary: "Walk the windows in most-recently-used order, optionally only the focused app's, workspace's or output's, focusing each; the order changes only when the walk ends, at the release of the bind's modifiers or the next other input.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			step, scope, err := parseStepAndScope(spec, args, ScopeApp, ScopeWorkspace, ScopeOutput)
			if err != nil {
				return nil, err
			}
			return CycleAction{Step: step, Scope: scope}, nil
		},
	},
	{
		Name: "switcher", Args: "<next|prev> [app]",
		Summary: "Alt-Tab: select the next or previous window in most-recently-used order, optionally of the focused app; the strip of previews shows after switcher.open_delay if the bind's modifiers are still held, releasing them focuses the selection and Escape cancels.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			step, scope, err := parseStepAndScope(spec, args, ScopeApp)
			if err != nil {
				return nil, err
			}
			return SwitcherAction{Step: step, Scope: scope}, nil
		},
	},
}

func optionalSwitch(spec ActionSpec, args []string) (Switch, error) {
	switch len(args) {
	case 0:
		return SwitchToggle, nil
	case 1:
		if sw, ok := ParseSwitch(args[0]); ok {
			return sw, nil
		}
		return 0, badArgs(spec, "%q is not toggle, on or off", args[0])
	}
	return 0, badArgs(spec, "takes toggle, on or off, or nothing, not %d arguments", len(args))
}

func bare(a Action) func(ActionSpec, []string, string) (Action, error) {
	return func(spec ActionSpec, args []string, _ string) (Action, error) {
		if err := exactly(spec, args, 0); err != nil {
			return nil, err
		}
		return a, nil
	}
}

func ParseAction(text string) (Action, error) {
	text = strings.TrimSpace(text)
	name, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)
	for _, spec := range Actions {
		if spec.Name == name {
			return spec.parse(spec, strings.Fields(rest), rest)
		}
	}
	return nil, fmt.Errorf("%w: unknown action %q", ErrBadAction, name)
}

func Reference() string {
	var b strings.Builder
	b.WriteString("# Actions\n\nEvery keybind, every `gestures` entry with an `action` and `lys-wmctl dispatch` takes one of these. A gesture can also drive something directly: see `gestures` in config.md. This file is generated from `wm.Actions`; run `go test ./internal/wm -update` after changing them.\n\n| Action | Arguments | Effect |\n| --- | --- | --- |\n")
	for _, spec := range Actions {
		args := spec.Args
		if args != "" {
			args = "`" + strings.ReplaceAll(args, "|", `\|`) + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", spec.Name, args, spec.Summary)
	}
	return b.String()
}

func parseOverview(spec ActionSpec, args []string, _ string) (Action, error) {
	a := OverviewAction{Mode: OverviewToggle}
	if len(args) > 2 {
		return nil, badArgs(spec, "takes a mode (toggle, show or hide) and a scope (workspace, output or app), each optional")
	}
	rest := args
	if len(rest) > 0 {
		if mode, ok := parseEnum[OverviewMode](overviewModes, rest[0]); ok {
			a.Mode, rest = mode, rest[1:]
		}
	}
	switch len(rest) {
	case 0:
		return a, nil
	case 1:
		scope, ok := ParseOverviewScope(rest[0])
		if !ok {
			return nil, badArgs(spec, "%q is not toggle, show, hide, workspace, output or app", rest[0])
		}
		a.Scope = scope
		return a, nil
	}
	return nil, badArgs(spec, "%q is not toggle, show or hide", args[0])
}
