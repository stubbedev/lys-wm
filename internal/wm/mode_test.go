package wm

import (
	"image"
	"testing"
)

func placementOf(s State, sp Spacing, id WindowID) (Placement, bool) {
	for _, p := range s.Arrange(sp).Windows {
		if p.ID == id {
			return p, true
		}
	}
	return Placement{}, false
}

func TestAMaximizedWindowFillsTheUsableAreaInsideTheOuterGapsAndComesBackToItsTile(t *testing.T) {
	must := ok(t)
	full, area := image.Rect(0, 0, 1000, 600), image.Rect(0, 30, 1000, 600)
	sp := Spacing{Gaps: Gaps{Inner: 4, Outer: Insets{Top: 8, Right: 8, Bottom: 8, Left: 8}}}
	s := must(State{}.AddOutput("DP-1", full, area))
	s = must(s.MapWindow(1, Autotile{}))
	s = must(s.MapWindow(2, Autotile{}))
	tile, _ := placementOf(s, sp, 2)
	s = must(s.SetMaximized(2, true))
	p, _ := placementOf(s, sp, 2)
	if want := image.Rect(8, 38, 992, 592); p.Rect != want || p.Layer != LayerMaximized || !p.Visible || p.Told() != LayerMaximized {
		t.Fatalf("maximized window 2: %+v, want %v on the maximized layer", p, want)
	}
	if under, _ := placementOf(s, sp, 1); under.Visible {
		t.Fatal("the window under a maximized one is visible")
	}
	if !s.Maximized(2) || s.Fullscreen(2) || s.Mode(1) != ModeNormal {
		t.Fatalf("modes: 2 %s, 1 %s", s.Mode(2), s.Mode(1))
	}
	s = must(s.SetFullscreen(2, false))
	if !s.Maximized(2) {
		t.Fatal("ending fullscreen on a maximized window ended its maximize")
	}
	s = must(s.SetMaximized(2, false))
	if p, _ := placementOf(s, sp, 2); p != tile {
		t.Fatalf("unmaximized to %+v, want its tile %+v", p, tile)
	}
	s = must(s.SwitchMode(ModeMaximized, SwitchToggle))
	s = must(s.SwitchMode(ModeFullscreen, SwitchOn))
	if p, _ := placementOf(s, sp, 2); p.Rect != full || p.Layer != LayerFullscreen {
		t.Fatalf("fullscreen over maximized: %+v", p)
	}
}

func TestFakeFullscreenKeepsTheTileAndTellsTheClientFullscreen(t *testing.T) {
	must := ok(t)
	s := must(minimizable(t).SwitchFakeFullscreen(SwitchToggle))
	p, _ := placementOf(s, Spacing{}, 2)
	if p.Layer != LayerTiled || p.Told() != LayerFullscreen || p.Rect != image.Rect(960, 0, 1920, 1080) {
		t.Fatalf("fake fullscreen window: %+v", p)
	}
	if other, _ := placementOf(s, Spacing{}, 1); !other.Visible || other.Fake {
		t.Fatalf("the other window: %+v", other)
	}
	s = must(s.ClientRequestsFullscreen(2, true))
	if s.Fullscreen(2) || !s.FakeFullscreen(2) {
		t.Fatal("a client's set_fullscreen ended fake fullscreen")
	}
	s = must(s.ClientRequestsFullscreen(2, false))
	if s.FakeFullscreen(2) || s.Fullscreen(2) {
		t.Fatal("a client's unset_fullscreen left fake fullscreen on")
	}
	s = must(s.ClientRequestsFullscreen(2, true))
	if !s.Fullscreen(2) {
		t.Fatal("set_fullscreen without fake did not fullscreen")
	}
	s = must(s.SetFakeFullscreen(1, true))
	s = must(s.UnmapWindow(1))
	if s.FakeFullscreen(1) {
		t.Fatal("an unmapped window is still fake fullscreen")
	}
}

func TestANewWindowOverACoveringOneFollowsThePolicy(t *testing.T) {
	for _, tc := range []struct {
		policy  CoverPolicy
		focused WindowID
		cover   Cover
	}{
		{CoverBehind, 1, Cover{ID: 1, Mode: ModeFullscreen}},
		{CoverTakeOver, 2, Cover{ID: 2, Mode: ModeFullscreen}},
		{CoverExit, 2, Cover{}},
	} {
		must := ok(t)
		s := must(State{}.AddOutput("A", left, left))
		s = must(s.MapWindow(1, Autotile{}))
		s = must(s.SetFullscreen(1, true))
		s = must(s.MapWindow(2, Autotile{}))
		s = must(s.Opened(2, tc.policy))
		focused, _ := s.Focused()
		ws, _ := s.Workspace(1)
		if focused != tc.focused || ws.Cover != tc.cover {
			t.Errorf("policy %d: focused %d, cover %+v; want %d, %+v", tc.policy, focused, ws.Cover, tc.focused, tc.cover)
		}
	}
}

func TestModesSurviveSaving(t *testing.T) {
	must := ok(t)
	s := must(minimizable(t).SetMaximized(2, true))
	s = must(s.SetFakeFullscreen(1, true))
	data, err := s.Save()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(data)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Maximized(2) || !loaded.FakeFullscreen(1) || loaded.FakeFullscreen(2) {
		t.Fatalf("after a save: maximized %t, fake %t %t", loaded.Maximized(2), loaded.FakeFullscreen(1), loaded.FakeFullscreen(2))
	}
}
