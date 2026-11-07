package wm

import (
	"errors"
	"fmt"
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
	OnActivate   ActivatePolicy
	NewOverCover CoverPolicy
	Scratchpads  map[ScratchpadName]ScratchpadSpec
	Peek         int
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
	return s.relative(int(r))
}

func (PreviousWorkspace) String() string { return "previous" }

func (PreviousWorkspace) resolve(s State) (WorkspaceID, error) {
	return s.previous()
}

type (
	FocusAction           struct{ Dir Direction }
	MoveAction            struct{ Dir Direction }
	WorkspaceAction       struct{ Target WorkspaceTarget }
	MoveToWorkspaceAction struct{ Target WorkspaceTarget }
	KillAction            struct{}
	FocusWindowAction     struct{ ID WindowID }
	CloseWindowAction     struct{ ID WindowID }
	ExecAction            struct{ Command string }
	MakeGroupAction       struct {
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

	OverviewAction struct{ Mode OverviewMode }
)

func optionalWindow(name string, id WindowID) string {
	if id == 0 {
		return name
	}
	return name + " " + strconv.FormatUint(uint64(id), 10)
}

func (a OverviewAction) String() string { return "overview " + a.Mode.String() }

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
	next, err := s.MoveDirection(a.Dir, settings.Collapse)
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
	next, err := s.MoveToWorkspace(id, settings.Autotile)
	return next, nil, err
}

func (KillAction) apply(s State, _ Settings) (State, []Effect, error) {
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
	next, err := s.FocusWindow(a.ID)
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
	w := s.activeWorkspace()
	if w == nil {
		return s, nil, nil
	}
	id, ok := w.Tree.Focused()
	if !ok {
		return s, nil, nil
	}
	tree, err := w.Tree.Wrap(id, a.Layout, a.Ephemeral)
	if err != nil {
		return State{}, nil, err
	}
	s = s.clone()
	s.activeWorkspace().Tree = tree
	return s, nil, nil
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
	if s == (PreviousWorkspace{}).String() {
		return PreviousWorkspace{}, true
	}
	if rest, ok := strings.CutPrefix(s, "e"); ok && len(rest) > 1 && (rest[0] == '+' || rest[0] == '-') {
		n, err := strconv.Atoi(rest)
		return RelativeWorkspace(n), err == nil && n != 0
	}
	n, err := strconv.Atoi(s)
	return WorkspaceID(n), err == nil && n >= 1
}

func ParseLayout(s string) (Layout, bool) {
	return parseEnum[Layout](layoutNames, s)
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

func workspaced(build func(WorkspaceTarget) Action) func(ActionSpec, []string, string) (Action, error) {
	return func(spec ActionSpec, args []string, _ string) (Action, error) {
		if err := exactly(spec, args, 1); err != nil {
			return nil, err
		}
		w, ok := parseWorkspace(args[0])
		if !ok {
			return nil, badArgs(spec, "unknown workspace %q", args[0])
		}
		return build(w), nil
	}
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
		Summary: "Move the focused window, hy3 style; at the workspace edge nothing happens.",
		parse:   directional(func(d Direction) Action { return MoveAction{Dir: d} }),
	},
	{
		Name: "workspace", Args: "<n|e+n|e-n|previous>",
		Summary: "Show workspace n, the n-th open workspace after or before the current one, or the one the output showed before it.",
		parse:   workspaced(func(w WorkspaceTarget) Action { return WorkspaceAction{Target: w} }),
	},
	{
		Name: "movetoworkspace", Args: "<n|e+n|e-n|previous>",
		Summary: "Move the focused window to a workspace without following it.",
		parse:   workspaced(func(w WorkspaceTarget) Action { return MoveToWorkspaceAction{Target: w} }),
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
		Name: "overview", Args: "[toggle|show|hide]",
		Summary: "Open or close the overview on every output: each output's windows side by side under a strip of its workspaces. Without an argument, toggle.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if len(args) > 1 {
				return nil, badArgs(spec, "takes toggle, show or hide, or nothing")
			}
			if len(args) == 0 {
				return OverviewAction{Mode: OverviewToggle}, nil
			}
			mode, ok := parseEnum[OverviewMode](overviewModes, args[0])
			if !ok {
				return nil, badArgs(spec, "%q is not toggle, show or hide", args[0])
			}
			return OverviewAction{Mode: mode}, nil
		},
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
		Name: "showdesktop", Args: "[toggle|on|off] [all]",
		Summary: "Slide the windows of the output under the pointer, or of every output with all, toward their nearest edges until only a sliver of each shows, to reach the desktop. Nothing is resized and the layout stays; keyboard focus leaves the windows. A click on a sliver, a window opening or being activated there, a workspace switch or focusing a window ends it, as does the action again. toggle unless told.",
		parse:   parseShowDesktop,
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
