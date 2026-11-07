package wm

import (
	"image"
	"testing"
)

func scratchState(t *testing.T) State {
	t.Helper()
	must := ok(t)
	s := must(State{}.AddOutput("A", left, left))
	s = must(s.AddOutput("B", right, right))
	return must(s.MapWindow(1, Autotile{}))
}

func toggle(t *testing.T, s State, spec ScratchpadSpec) (State, []Effect) {
	t.Helper()
	next, effects, err := Dispatch(s, ScratchpadAction{Name: "term"}, Settings{Scratchpads: map[ScratchpadName]ScratchpadSpec{"term": spec}})
	if err != nil {
		t.Fatal(err)
	}
	return next, effects
}

func TestAScratchpadShowsOverTheFocusedOutputAndHidesAgain(t *testing.T) {
	must := ok(t)
	spec := ScratchpadSpec{Exec: "foot", Size: ScratchSize{W: 50, H: 50}}
	s, effects := toggle(t, scratchState(t), spec)
	if len(effects) != 1 {
		t.Fatalf("toggling an empty scratchpad gave %v, want its exec", effects)
	}
	e := effects[0].(ScratchpadExecAction)
	if e.Command != "foot" || e.Name != "term" {
		t.Fatalf("exec effect %+v", e)
	}
	s = must(s.MapWindow(2, Autotile{}))
	s = must(s.MoveWindowToScratchpad(2, "term", spec, Autotile{}))
	p, _ := placementOf(s, Spacing{}, 2)
	if want := image.Rect(480, 270, 1440, 810); !p.Scratch || !p.Visible || p.Rect != want || p.Workspace >= 0 {
		t.Fatalf("the scratchpad window: %+v, want visible at %v", p, want)
	}
	if f, _ := s.Focused(); f != 2 {
		t.Fatalf("focus %d, want the scratchpad's window", f)
	}
	if under, _ := placementOf(s, Spacing{}, 1); !under.Visible {
		t.Fatal("the workspace under a scratchpad is hidden")
	}
	s, effects = toggle(t, s, spec)
	if p, _ := placementOf(s, Spacing{}, 2); p.Visible || len(effects) != 0 {
		t.Fatalf("hidden scratchpad window %+v, effects %v", p, effects)
	}
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("focus %d after hiding, want 1 back", f)
	}
	s, effects = toggle(t, s, spec)
	if len(effects) != 0 {
		t.Fatal("a scratchpad with a window ran its exec again")
	}
	s = must(s.FocusOutput("B"))
	s, _ = toggle(t, s, spec)
	if p, _ := placementOf(s, Spacing{}, 2); !p.Visible || !p.Rect.In(right) {
		t.Fatalf("toggled on B: %+v", p)
	}
	if o := s.Outputs()[0]; o.Scratch != 0 {
		t.Fatal("the scratchpad still shows on A too")
	}
}

func TestMovingWindowsIntoAndOutOfAScratchpad(t *testing.T) {
	must := ok(t)
	s := scratchState(t)
	s = must(s.MapWindow(2, Autotile{}))
	s = must(s.MoveToScratchpad("notes", ScratchpadSpec{}, Autotile{}))
	if p, _ := placementOf(s, Spacing{}, 2); !p.Scratch || p.Visible {
		t.Fatalf("a window moved to a hidden scratchpad: %+v", p)
	}
	if len(s.Scratchpads()) != 1 {
		t.Fatal("the scratchpad holding a window was dropped")
	}
	s = must(s.SwitchWorkspace(5))
	if len(s.Scratchpads()) != 1 {
		t.Fatal("a workspace switch dropped a scratchpad with a window")
	}
	s, _, err := s.ToggleScratchpad("notes", ScratchpadSpec{})
	if err != nil {
		t.Fatal(err)
	}
	s = must(s.MoveToWorkspace(5, Autotile{}))
	if p, _ := placementOf(s, Spacing{}, 2); p.Scratch || p.Workspace != 5 {
		t.Fatalf("moved back out: %+v", p)
	}
	s = must(s.HideScratchpad())
	if len(s.Scratchpads()) != 0 {
		t.Fatal("an empty hidden scratchpad lingers")
	}
}

func TestAWorkspaceSwitchHidesAScratchpadUnlessItStays(t *testing.T) {
	for _, stays := range []bool{false, true} {
		s, _ := toggle(t, scratchState(t), ScratchpadSpec{Stays: stays})
		s = ok(t)(s.MapWindow(2, Autotile{}))
		s = ok(t)(s.MoveWindowToScratchpad(2, "term", ScratchpadSpec{Stays: stays}, Autotile{}))
		s = ok(t)(s.SwitchWorkspace(3))
		if p, _ := placementOf(s, Spacing{}, 2); p.Visible != stays {
			t.Errorf("stays %t: shown %t after a switch", stays, p.Visible)
		}
	}
}

func TestScratchpadsSurviveSaving(t *testing.T) {
	s, _ := toggle(t, scratchState(t), ScratchpadSpec{Size: ScratchSize{W: 60, H: 40}})
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = ok(t)(s.MoveWindowToScratchpad(2, "term", ScratchpadSpec{Size: ScratchSize{W: 60, H: 40}}, Autotile{}))
	data, err := s.Save()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(data)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := placementOf(s, Spacing{}, 2)
	b, _ := placementOf(loaded, Spacing{}, 2)
	if a != b || !b.Scratch {
		t.Fatalf("after a save %+v, before %+v", b, a)
	}
}
