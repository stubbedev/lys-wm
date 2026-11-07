package wm

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite docs/actions.md")

var everyAction = []Action{
	MinimizeAction{},
	MinimizeAction{ID: 7},
	RestoreAction{},
	RestoreAction{ID: 7},
	OverviewAction{Mode: OverviewToggle},
	OverviewAction{Mode: OverviewShow},
	OverviewAction{Mode: OverviewHide},
	OverviewAction{Mode: OverviewShow, Scope: OverviewApp},
	OverviewAction{Mode: OverviewToggle, Scope: OverviewOutput},
	FocusAction{Dir: Left},
	FocusAction{Dir: Down},
	MoveAction{Dir: Right},
	MoveAction{Dir: Up},
	WorkspaceAction{Target: WorkspaceID(3)},
	WorkspaceAction{Target: RelativeWorkspace(1)},
	WorkspaceAction{Target: RelativeWorkspace(-2)},
	WorkspaceAction{Target: PreviousWorkspace{}},
	MoveToWorkspaceAction{Target: PreviousWorkspace{}},
	MoveToWorkspaceAction{Target: WorkspaceID(10)},
	WorkspaceAction{Target: WorkspaceName("chat")},
	MoveToWorkspaceAction{Target: RelativeWorkspace(-1)},
	MoveToWorkspaceAction{Target: OutputRelativeWorkspace(2), Follow: true},
	MoveToWorkspaceAction{Target: EmptyWorkspace{}},
	WorkspaceAction{Target: OutputRelativeWorkspace(-1)},
	WorkspaceAction{Target: EmptyWorkspace{}},
	FocusOutputAction{Target: Left},
	FocusOutputAction{Target: OutputName("DP-2")},
	MoveToOutputAction{Target: Down},
	MoveToOutputAction{Target: OutputName("HDMI-A-1")},
	MoveWorkspaceToOutputAction{Target: Right},
	SwapWorkspacesAction{A: OutputName("DP-1"), B: OutputName("DP-2")},
	KillAction{},
	FocusWindowAction{ID: 7},
	CloseWindowAction{ID: 12},
	ReloadAction{},
	DismissConfigNoticeAction{},
	ExitAction{},
	NextLayoutAction{},
	StickyKeysAction{On: true},
	StickyKeysAction{},
	SlowKeysAction{Delay: 300 * time.Millisecond},
	SlowKeysAction{},
	DPMSAction{Mode: SwitchOff},
	ZoomAction{Mode: ZoomIn, Factor: 1.25},
	ZoomAction{Mode: ZoomOut, Factor: 2},
	ZoomAction{Mode: ZoomReset},
	DebugOverlayAction{Overlay: DebugDamage, Mode: SwitchOn},
	DebugRecordAction{Recording: DebugTrace, Path: "/tmp/frames.jsonl"},
	DebugRecordAction{Recording: DebugHeap, Path: "/tmp/heap.pprof"},
	CastTargetAction{Kind: CastWindow},
	CastTargetAction{Kind: CastWindow, Window: 4},
	CastTargetAction{Kind: CastOutput},
	CastTargetAction{Kind: CastOutput, Output: "DP-1"},
	CastTargetAction{Kind: CastClear},
	ZoomAction{Mode: ZoomIn, Factor: DefaultZoomStep},
	ZoomAction{Mode: ZoomSet, Factor: 3.5},
	DPMSAction{Mode: SwitchToggle, Output: "DP-1"},
	ExecAction{Command: "foot -e sh -c 'echo hi'"},
	GlobalAction{AppID: "discord", ID: "push-to-talk"},
	MakeGroupAction{Layout: SplitV},
	MakeGroupAction{Layout: Tabbed, Ephemeral: true},
	ChangeGroupAction{Change: ChangeToggleTab},
	ChangeGroupAction{Change: ChangeOpposite},
	FocusTabAction{Target: TabStep(-1)},
	FocusTabAction{Target: TabStep(1), Wrap: true},
	FocusTabAction{Target: TabIndex(3)},
	FocusParentAction{},
	FocusChildAction{},
	SwapAction{Dir: Left},
	MoveOutAction{},
	EqualizeAction{},
	EqualizeAction{All: true},
	LockTabAction{},
	LockTabAction{Switch: SwitchOff},
	SubmapAction{Name: "resize"},
	SubmapAction{},
	FloatAction{},
	PlaceAction{Target: PlaceTopLeft},
	PlaceAction{Target: PlaceRestore},
	FocusFloatingAction{},
	SwallowAction{},
	SetLayoutAction{Kind: ScrollLayout},
	SetLayoutAction{Kind: TreeLayout},
	ColumnWidthAction{Change: PresetStep(1)},
	ColumnWidthAction{Change: PresetStep(-1)},
	ColumnWidthAction{Change: WidthBy(Share(0.1))},
	ColumnWidthAction{Change: WidthBy(Pixels(120))},
	ColumnWidthAction{Change: WidthBy(Pixels(-40))},
	ColumnWidthAction{Change: WidthBy(Share(-0.05))},
	ColumnWidthAction{Change: WidthTo(Share(0.5))},
	ColumnWidthAction{Change: WidthTo(Pixels(1280))},
	ConsumeAction{Dir: Left},
	ExpelAction{},
	CenterAction{},
	ColumnDisplayAction{Layout: Tabbed},
	ColumnDisplayAction{Layout: SplitV},
	DebugOverlayAction{Overlay: DebugDamage, Mode: SwitchOn},
	DebugRecordAction{Recording: DebugTrace, Stop: true},
	SwallowAction{Switch: SwitchOff},
	FocusFloatingAction{Switch: SwitchOn},
	FullscreenAction{},
	ScratchpadAction{Name: "term"},
	ScratchpadAction{Hide: true},
	MoveToScratchpadAction{Name: "notes"},
	FullscreenAction{Switch: SwitchOn},
	FullscreenAction{Switch: SwitchOff, Fake: true},
	FullscreenAction{Fake: true},
	MaximizeAction{},
	PinAction{},
	ShowDesktopAction{},
	SetPropAction{ID: 7, Key: "border", Value: "{active: red, width: 3}"},
	ShowDesktopAction{Switch: SwitchOff, All: true},
	PinAction{Switch: SwitchOn},
	MaximizeAction{Switch: SwitchOff},
	DragAction{Mode: DragMove},
	DragAction{Mode: DragResize},
	ResizeAction{DX: 50, DY: 0},
	ResizeAction{DX: 0, DY: -10},
	FocusLastAction{},
	FocusUrgentAction{},
	CycleAction{Step: StepNext},
	CycleAction{Step: StepPrev, Scope: ScopeApp},
	CycleAction{Step: StepNext, Scope: ScopeWorkspace},
	CycleAction{Step: StepPrev, Scope: ScopeOutput},
	SwitcherAction{Step: StepNext},
	SwitcherAction{Step: StepPrev, Scope: ScopeApp},
}

func TestEveryActionRoundTripsThroughTheParser(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range everyAction {
		got, err := ParseAction(a.String())
		if err != nil {
			t.Fatalf("parse %q: %v", a, err)
		}
		if got != a {
			t.Fatalf("%q parsed to %#v, want %#v", a, got, a)
		}
		name, _, _ := strings.Cut(a.String(), " ")
		seen[name] = true
	}
	for _, spec := range Actions {
		if !seen[spec.Name] {
			t.Errorf("action %s has no round-trip case", spec.Name)
		}
	}
}

func TestBadActionsFailPrecisely(t *testing.T) {
	cases := map[string]string{
		"":                          `unknown action ""`,
		"teleport":                  `unknown action "teleport"`,
		"focus":                     "focus <left|right|up|down>: takes 1 argument(s), got 0",
		"focus sideways":            `focus <left|right|up|down>: unknown direction "sideways"`,
		"workspace 0":               `workspace <n|name|e+n|e-n|m+n|m-n|empty|previous>: unknown workspace "0"`,
		"workspace e+0":             `workspace <n|name|e+n|e-n|m+n|m-n|empty|previous>: unknown workspace "e+0"`,
		"workspace 9back":           `workspace <n|name|e+n|e-n|m+n|m-n|empty|previous>: unknown workspace "9back"`,
		"kill now":                  "kill: takes 0 argument(s), got 1",
		"focuswindow":               "focuswindow <id>: takes 1 argument(s), got 0",
		"focuswindow 0":             `focuswindow <id>: window ID "0" is not a positive number`,
		"closewindow -3":            `closewindow <id>: window ID "-3" is not a positive number`,
		"closewindow foot":          `closewindow <id>: window ID "foot" is not a positive number`,
		"exec":                      "exec <command>: needs a command",
		"global discord":            `global <app_id>:<id>: "discord" is not app_id:id`,
		"global :ptt":               `global <app_id>:<id>: ":ptt" is not app_id:id`,
		"makegroup diagonal":        `makegroup <h|v|tab> [ephemeral]: unknown layout "diagonal"`,
		"makegroup v forevermore":   `makegroup <h|v|tab> [ephemeral]: unknown option "forevermore"`,
		"movetoworkspace 3 quickly": `unknown option "quickly"`,
		"workspace m+0":             `unknown workspace "m+0"`,
		"swapworkspaces DP-1":       "swapworkspaces <output> <output>: takes 2 argument(s), got 1",
		"focusoutput":               "focusoutput <left|right|up|down|name>: takes 1 argument(s), got 0",
	}
	for text, want := range cases {
		_, err := ParseAction(text)
		if !errors.Is(err, ErrBadAction) || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseAction(%q) = %v, want an error containing %q", text, err, want)
		}
	}
}

func TestDispatch(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	settings := Settings{Collapse: CollapseEmptySplits}
	run := func(text string) []Effect {
		t.Helper()
		a, err := ParseAction(text)
		if err != nil {
			t.Fatal(err)
		}
		var effects []Effect
		if s, effects, err = Dispatch(s, a, settings); err != nil {
			t.Fatal(err)
		}
		return effects
	}
	run("focus left")
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("focus left landed on %d", f)
	}
	run("move right")
	if got := s.workspace(1).Tree.String(); got != "h[2 *1]" {
		t.Fatalf("after move right: %s", got)
	}
	run("movetoworkspace e+1")
	if w, _ := s.WorkspaceOf(1); w != 1 {
		t.Fatalf("e+1 with one open workspace wraps to itself, window went to %d", w)
	}
	run("movetoworkspace 2")
	run("workspace 2")
	if f, _ := s.Focused(); f != 1 || active(s)[0] != 2 {
		t.Fatalf("workspace 2 shows %v, focus %d", active(s), f)
	}
	if effects := run("kill"); !slices.Equal(effects, []Effect{CloseWindowAction{ID: 1}}) {
		t.Fatalf("kill gave %v", effects)
	}
	if effects := run("reload"); !slices.Equal(effects, []Effect{ReloadAction{}}) {
		t.Fatalf("reload gave %v", effects)
	}
	if effects := run("nextlayout"); !slices.Equal(effects, []Effect{NextLayoutAction{}}) {
		t.Fatalf("nextlayout gave %v", effects)
	}
	if effects := run("exit"); !slices.Equal(effects, []Effect{ExitAction{}}) {
		t.Fatalf("exit gave %v", effects)
	}
	if effects := run("exec foot --server"); !slices.Equal(effects, []Effect{ExecAction{Command: "foot --server"}}) {
		t.Fatalf("exec gave %v", effects)
	}
	if effects := run("global obs:record"); !slices.Equal(effects, []Effect{GlobalAction{AppID: "obs", ID: "record"}}) {
		t.Fatalf("global gave %v", effects)
	}
	run("makegroup tab ephemeral")
	if got := s.workspace(2).Tree.String(); got != "h[*tab~[*1]]" {
		t.Fatalf("after makegroup: %s", got)
	}
	if _, _, err := Dispatch(State{}, WorkspaceAction{Target: RelativeWorkspace(1)}, settings); !errors.Is(err, ErrUnknownOutput) {
		t.Fatalf("a relative workspace with no output: %v", err)
	}
}

func TestFocusAndCloseAWindowByID(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("DP-2", right, right))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.SwitchWorkspace(3))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	run := func(a Action) []Effect {
		t.Helper()
		next, effects, err := Dispatch(s, a, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		s = next
		return effects
	}
	run(MakeGroupAction{Layout: Tabbed})
	s = ok(t)(s.MapWindow(3, Autotile{}))
	s = ok(t)(s.FocusOutput("DP-2"))
	run(FocusWindowAction{ID: 1})
	if f, _ := s.Focused(); f != 1 || s.FocusedOutput() != "DP-1" || !slices.Equal(active(s), []WorkspaceID{1, 2}) {
		t.Fatalf("focuswindow 1 focused %d on %s showing %v", f, s.FocusedOutput(), active(s))
	}
	run(FocusWindowAction{ID: 2})
	if f, _ := s.Focused(); f != 2 || !slices.Equal(active(s), []WorkspaceID{3, 2}) {
		t.Fatalf("focuswindow 2 focused %d showing %v", f, active(s))
	}
	if got := s.workspace(3).Tree.String(); got != "h[*tab[*2 3]]" {
		t.Fatalf("the tab of window 2 is not raised: %s", got)
	}
	before := s
	if effects := run(CloseWindowAction{ID: 3}); !slices.Equal(effects, []Effect{CloseWindowAction{ID: 3}}) {
		t.Fatalf("closewindow 3 gave %v", effects)
	}
	if f, _ := s.Focused(); f != 2 || s.workspace(3).Tree.String() != before.workspace(3).Tree.String() {
		t.Fatalf("closewindow moved focus to %d", f)
	}
	for _, a := range []Action{FocusWindowAction{ID: 9}, CloseWindowAction{ID: 9}} {
		if _, _, err := Dispatch(s, a, Settings{}); !errors.Is(err, ErrUnknownWindow) || !strings.Contains(err.Error(), "window 9") {
			t.Errorf("%s: %v", a, err)
		}
	}
}

func TestWorkspacePreviousGoesBackOnTheFocusedOutput(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("DP-2", right, right))
	run := func(a Action) {
		t.Helper()
		next, _, err := Dispatch(s, a, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		s = next
	}
	back := WorkspaceAction{Target: PreviousWorkspace{}}
	run(back)
	if got := active(s); !slices.Equal(got, []WorkspaceID{1, 2}) {
		t.Fatalf("with no previous workspace, previous showed %v", got)
	}
	s = ok(t)(s.MapWindow(1, Autotile{}))
	run(WorkspaceAction{Target: WorkspaceID(3)})
	run(back)
	if got := active(s); !slices.Equal(got, []WorkspaceID{1, 2}) {
		t.Fatalf("previous after workspace 3 showed %v", got)
	}
	run(back)
	if got := active(s); !slices.Equal(got, []WorkspaceID{3, 2}) {
		t.Fatalf("previous twice did not toggle back to 3: %v", got)
	}
	run(back)
	run(MoveToWorkspaceAction{Target: PreviousWorkspace{}})
	if w, _ := s.WorkspaceOf(1); w != 3 || !slices.Equal(active(s), []WorkspaceID{1, 2}) {
		t.Fatalf("movetoworkspace previous put window 1 on %d, showing %v", w, active(s))
	}
	s = ok(t)(s.FocusOutput("DP-2"))
	run(back)
	if got := active(s); !slices.Equal(got, []WorkspaceID{1, 2}) || s.FocusedOutput() != "DP-2" {
		t.Fatalf("previous on an output that showed nothing else showed %v on %s", got, s.FocusedOutput())
	}
}

func TestActionReferenceIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "actions.md")
	if *update {
		if err := os.WriteFile(path, []byte(Reference()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	have, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != Reference() {
		t.Fatalf("%s is stale; run go test ./internal/wm -update", path)
	}
}

func TestEnumArgumentsParseByName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Action
	}{
		{"focus l", FocusAction{Dir: Left}},
		{"focus r", FocusAction{Dir: Right}},
		{"move u", MoveAction{Dir: Up}},
		{"move d", MoveAction{Dir: Down}},
	} {
		if got, err := ParseAction(tc.in); err != nil || got != tc.want {
			t.Errorf("%q parsed to %v (%v), want %v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"focus x", "focus ", "move le", "makegroup q", "makegroup", "dpms maybe", "dpms ", "zoom", "zoom sideways", "zoom set", "zoom set 0.5", "zoom set 17", "zoom in NaN", "zoom reset 2", "zoom in 2 3", "stickykeys", "stickykeys maybe", "slowkeys", "slowkeys -1", "slowkeys 70000", "cycle", "cycle sideways", "cycle next everywhere", "switcher next workspace", "switcher next app now", "focuslast 3"} {
		if a, err := ParseAction(in); !errors.Is(err, ErrBadAction) {
			t.Errorf("%q parsed to %v, %v; want a bad action", in, a, err)
		}
	}
	if s := Direction(9).String(); s != "Direction(9)" {
		t.Errorf("an unknown direction prints as %q", s)
	}
}
