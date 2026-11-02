package wm

import (
	"image"
	"reflect"
	"testing"
)

func dragState(t testing.TB) State {
	t.Helper()
	s, err := State{}.AddOutput("o", left, left)
	if err != nil {
		t.Fatal(err)
	}
	auto := Autotile{}
	for id := WindowID(1); id <= 8; id++ {
		if s, err = s.MapWindow(id, auto); err != nil {
			t.Fatal(err)
		}
	}
	if s, err = s.MapFloating(9, image.Rect(10, 10, 110, 110)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestADragLeavesTheStateItStartedFromAlone(t *testing.T) {
	s := dragState(t)
	before := s.Arrange(Spacing{})
	want := make([]Placement, len(before.Windows))
	copy(want, before.Windows)
	moved, err := s.SetFloatingRect(9, image.Rect(50, 50, 150, 150))
	if err != nil {
		t.Fatal(err)
	}
	resized, err := moved.ResizeTiled(3, Right, 40)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Arrange(Spacing{}); !reflect.DeepEqual(got.Windows, want) {
		t.Errorf("the old state now arranges as %v, want %v", got.Windows, want)
	}
	if reflect.DeepEqual(moved.Arrange(Spacing{}).Windows, want) {
		t.Error("the floating move changed nothing")
	}
	if reflect.DeepEqual(resized.Arrange(Spacing{}).Windows, moved.Arrange(Spacing{}).Windows) {
		t.Error("the resize changed nothing")
	}
	again, err := s.ResizeTiled(3, Right, 40)
	if err != nil {
		t.Fatal(err)
	}
	if got, gotAgain := moved.Arrange(Spacing{}).Windows[3].Rect, again.Arrange(Spacing{}).Windows[3].Rect; got == gotAgain {
		t.Errorf("two states resized apart share their tree: %v", got)
	}
}

func TestAReusedArrangerArrangesLikeAFreshArrangement(t *testing.T) {
	s := dragState(t)
	tabbed := s.clone()
	tree, err := tabbed.workspaces[0].Tree.Wrap(3, Tabbed, false)
	if err != nil {
		t.Fatal(err)
	}
	tree, err = tree.Wrap(4, Tabbed, false)
	if err != nil {
		t.Fatal(err)
	}
	tabbed.workspaces[0].Tree = tree
	var x Arranger
	sp := Spacing{TabBar: 20, Gaps: Gaps{Inner: 6, Outer: Insets{Top: 4, Right: 4, Bottom: 4, Left: 4}}}
	for _, st := range []State{s, tabbed, s, tabbed} {
		st.ArrangeInto(&x, sp)
		want := st.Arrange(sp)
		if !reflect.DeepEqual(x.Windows, want.Windows) || len(x.TabBars) != len(want.TabBars) || len(x.TabBars) > 0 && !reflect.DeepEqual(x.TabBars, want.TabBars) {
			t.Fatalf("reused: %v %v, want %v %v", x.Windows, x.TabBars, want.Windows, want.TabBars)
		}
	}
}

func TestDragStepsAllocateLittle(t *testing.T) {
	s := dragState(t)
	var x Arranger
	sp := Spacing{}
	s.ArrangeInto(&x, sp)
	if n := testing.AllocsPerRun(100, func() { s.ArrangeInto(&x, sp) }); n != 0 {
		t.Errorf("arranging into a reused arranger allocates %.0f times", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		if _, err := s.SetFloatingRect(9, image.Rect(50, 50, 150, 150)); err != nil {
			t.Error(err)
		}
	}); n > 2 {
		t.Errorf("a floating move allocates %.0f times, want the workspace list and its floating list", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		if _, err := s.ResizeTiled(3, Right, 4); err != nil {
			t.Error(err)
		}
	}); n > 3 {
		t.Errorf("a tiled resize allocates %.0f times, want the workspace list and a group per level", n)
	}
}

func BenchmarkFloatingDragStep(b *testing.B) {
	s := dragState(b)
	var x Arranger
	b.ReportAllocs()
	for b.Loop() {
		next, err := s.SetFloatingRect(9, image.Rect(50, 50, 150, 150))
		if err != nil {
			b.Fatal(err)
		}
		next.ArrangeInto(&x, Spacing{})
	}
}

func BenchmarkTiledDragStep(b *testing.B) {
	s := dragState(b)
	var x Arranger
	b.ReportAllocs()
	for b.Loop() {
		next, err := s.ResizeTiled(3, Right, 4)
		if err != nil {
			b.Fatal(err)
		}
		next.ArrangeInto(&x, Spacing{})
	}
}
