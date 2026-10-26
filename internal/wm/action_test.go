package wm

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/actions.md")

var everyAction = []Action{
	FocusAction{Dir: Left},
	FocusAction{Dir: Down},
	MoveAction{Dir: Right},
	MoveAction{Dir: Up},
	WorkspaceAction{Target: WorkspaceID(3)},
	WorkspaceAction{Target: RelativeWorkspace(1)},
	WorkspaceAction{Target: RelativeWorkspace(-2)},
	MoveToWorkspaceAction{Target: WorkspaceID(10)},
	MoveToWorkspaceAction{Target: RelativeWorkspace(-1)},
	KillAction{},
	FocusWindowAction{ID: 7},
	CloseWindowAction{ID: 12},
	ReloadAction{},
	ExitAction{},
	DPMSAction{Mode: PowerOff},
	DPMSAction{Mode: PowerToggle, Output: "DP-1"},
	ExecAction{Command: "foot -e sh -c 'echo hi'"},
	GlobalAction{AppID: "discord", ID: "push-to-talk"},
	MakeGroupAction{Layout: SplitV},
	MakeGroupAction{Layout: Tabbed, Ephemeral: true},
	SubmapAction{Name: "resize"},
	SubmapAction{},
	FloatAction{},
	FullscreenAction{},
	DragAction{Mode: DragMove},
	DragAction{Mode: DragResize},
	ResizeAction{DX: 50, DY: 0},
	ResizeAction{DX: 0, DY: -10},
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
		"":                        `unknown action ""`,
		"teleport":                `unknown action "teleport"`,
		"focus":                   "focus <left|right|up|down>: takes 1 argument(s), got 0",
		"focus sideways":          `focus <left|right|up|down>: unknown direction "sideways"`,
		"workspace 0":             `workspace <n|e+n|e-n>: unknown workspace "0"`,
		"workspace e+0":           `workspace <n|e+n|e-n>: unknown workspace "e+0"`,
		"kill now":                "kill: takes 0 argument(s), got 1",
		"focuswindow":             "focuswindow <id>: takes 1 argument(s), got 0",
		"focuswindow 0":           `focuswindow <id>: window ID "0" is not a positive number`,
		"closewindow -3":          `closewindow <id>: window ID "-3" is not a positive number`,
		"closewindow foot":        `closewindow <id>: window ID "foot" is not a positive number`,
		"exec":                    "exec <command>: needs a command",
		"global discord":          `global <app_id>:<id>: "discord" is not app_id:id`,
		"global :ptt":             `global <app_id>:<id>: ":ptt" is not app_id:id`,
		"makegroup diagonal":      `makegroup <h|v|tab> [ephemeral]: unknown layout "diagonal"`,
		"makegroup v forevermore": `makegroup <h|v|tab> [ephemeral]: unknown option "forevermore"`,
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
	for _, in := range []string{"focus x", "focus ", "move le", "makegroup q", "makegroup", "dpms maybe", "dpms "} {
		if a, err := ParseAction(in); !errors.Is(err, ErrBadAction) {
			t.Errorf("%q parsed to %v, %v; want a bad action", in, a, err)
		}
	}
	if s := Direction(9).String(); s != "Direction(9)" {
		t.Errorf("an unknown direction prints as %q", s)
	}
}
