package harness

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	keyAlt   = 56
	keyGrave = 41
)

type mruRig struct {
	t   *testing.T
	h   *Harness
	c   *testclient.Client
	ids map[string]wm.WindowID
	win map[string]*testclient.Window
}

func startMRU(t *testing.T, delay time.Duration, specs ...bindSpec) *mruRig {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Switcher.OpenDelay = delay
		withBinds(t, specs...)(cfg)
	}, small)
	return &mruRig{t: t, h: h, c: testclient.Connect(t, h.Socket()), ids: map[string]wm.WindowID{}, win: map[string]*testclient.Window{}}
}

func (r *mruRig) open(titles ...string) {
	r.t.Helper()
	for _, title := range titles {
		w := r.c.Window(title, quadrants)
		r.c.DispatchUntil(title+" to map", w.Mapped)
		r.win[title] = w
		r.h.Eventually(title+" focused", func(_ *headless.Backend, comp *compositor.Compositor) bool {
			for _, s := range comp.Snapshot().Windows {
				if s.Title == title && s.Focused {
					r.ids[title] = s.ID
					return true
				}
			}
			return false
		})
	}
}

func (r *mruRig) snapshot() ipc.Snapshot {
	var s ipc.Snapshot
	r.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		s = comp.Snapshot()
		return nil
	})
	return s
}

func (r *mruRig) order() []string {
	byID := map[wm.WindowID]string{}
	for title, id := range r.ids {
		byID[id] = title
	}
	var out []string
	for _, id := range r.snapshot().MRU {
		out = append(out, byID[id])
	}
	return out
}

func (r *mruRig) wantOrder(why string, want ...string) {
	r.t.Helper()
	if got := r.order(); !slices.Equal(got, want) {
		r.t.Fatalf("%s: most recently used %q, want %q", why, got, want)
	}
}

func (r *mruRig) focused() string {
	for _, w := range r.snapshot().Windows {
		if w.Focused {
			return w.Title
		}
	}
	return ""
}

func (r *mruRig) wantFocus(title, why string) {
	r.t.Helper()
	r.h.Eventually(title+" focused "+why, func(_ *headless.Backend, comp *compositor.Compositor) bool {
		for _, w := range comp.Snapshot().Windows {
			if w.Focused {
				return w.Title == title
			}
		}
		return false
	})
}

func (r *mruRig) counters() (frames, redraws uint64, open bool) {
	r.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		frames, redraws, open = comp.SwitcherFrames(), comp.SwitcherThumbRedraws(), comp.SwitcherOpen()
		return nil
	})
	return frames, redraws, open
}

func TestFocusLastTogglesBetweenWindowsOnTwoWorkspaces(t *testing.T) {
	t.Parallel()
	r := startMRU(t, 0)
	r.open("a")
	r.h.dispatch("workspace 2")
	r.open("b")
	r.wantOrder("after opening", "b", "a")
	active := func() wm.WorkspaceID {
		return r.snapshot().Outputs[0].Workspace
	}
	r.h.dispatch("focuslast")
	r.wantFocus("a", "after focuslast")
	if ws := active(); ws != 1 {
		t.Fatalf("focuslast to a showed workspace %d", ws)
	}
	r.h.dispatch("focuslast")
	r.wantFocus("b", "after focuslast again")
	if ws := active(); ws != 2 {
		t.Fatalf("focuslast back to b showed workspace %d", ws)
	}
	s := r.snapshot()
	stamps := map[string]uint64{}
	for _, w := range s.Windows {
		stamps[w.Title] = w.FocusTimestamp
	}
	if stamps["b"] <= stamps["a"] || stamps["a"] == 0 {
		t.Fatalf("focus timestamps %v do not follow the focus order", stamps)
	}
}

func TestFocusUrgentGoesToTheWindowAskingForAttention(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	target, minter := twoWorkspaces(t, h)
	target.activate(t, requestToken(t, minter.c, minter.act, minter.seat, 0))
	h.expectActivation("the target urgent", 2, "minter", true, "target")
	h.dispatch("focusurgent")
	h.expectActivation("the urgent target focused on its workspace", 1, "target", false, "target")
}

func TestAQuickAltTabFlipsToThePreviousWindowWithoutDrawing(t *testing.T) {
	t.Parallel()
	r := startMRU(t, time.Hour, bindSpec{chord: "ALT + Tab", action: "switcher next"})
	r.open("a", "b")
	r.h.key(keyAlt, true)
	r.h.tap(keyTab)
	if _, _, open := r.counters(); !open {
		t.Fatal("alt+tab did not open the switcher")
	}
	r.h.key(keyAlt, false)
	r.wantFocus("a", "after a quick alt+tab")
	if frames, redraws, open := r.counters(); frames != 0 || redraws != 0 || open {
		t.Fatalf("a quick alt+tab drew %d switcher frames and %d previews, open %t; want none", frames, redraws, open)
	}
	r.wantOrder("after the flip", "a", "b")
}

func TestTabbingThreeTimesUnderAltPicksTheFourthWindowAndKeepsThePassedOrder(t *testing.T) {
	t.Parallel()
	r := startMRU(t, time.Hour, bindSpec{chord: "ALT + Tab", action: "switcher next"})
	r.open("w1", "w2", "w3", "w4", "w5")
	r.wantOrder("after opening", "w5", "w4", "w3", "w2", "w1")
	lines := r.h.watch()
	r.h.key(keyAlt, true)
	r.h.tap(keyTab, keyTab, keyTab)
	expectEvent(t, lines, fmt.Sprintf(`{"event":"switcher","open":true,"selected":%d,"title":"w2"}`, r.ids["w2"]))
	r.wantOrder("while the switcher is open", "w5", "w4", "w3", "w2", "w1")
	r.h.key(keyAlt, false)
	r.wantFocus("w2", "after releasing alt")
	expectEvent(t, lines, `{"event":"switcher","open":false,"selected":null,"title":""}`)
	r.wantOrder("after the switch", "w2", "w5", "w4", "w3", "w1")
}

func TestEscapeCancelsTheSwitcher(t *testing.T) {
	t.Parallel()
	r := startMRU(t, time.Hour, bindSpec{chord: "ALT + Tab", action: "switcher next"})
	r.open("a", "b")
	r.h.key(keyAlt, true)
	r.h.tap(keyTab, keyEsc)
	r.h.key(keyAlt, false)
	if _, _, open := r.counters(); open {
		t.Fatal("escape left the switcher open")
	}
	if f := r.focused(); f != "b" {
		t.Fatalf("a cancelled switch focused %s", f)
	}
}

func TestTheAppSwitcherListsOnlyTheFocusedAppsWindows(t *testing.T) {
	t.Parallel()
	r := startMRU(t, time.Hour, bindSpec{chord: "ALT + grave", action: "switcher next app"})
	r.open("same 1", "other", "same 2")
	if err := errors.Join(r.win["other"].Toplevel.SetAppID("other.app"), r.win["other"].Surface.Commit()); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	r.h.key(keyAlt, true)
	r.h.tap(keyGrave)
	var listed int
	r.h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		listed = len(comp.SwitcherWindows())
		return nil
	})
	r.h.key(keyAlt, false)
	if listed != 2 {
		t.Fatalf("the app switcher listed %d windows, want the 2 sharing the focused app's ID", listed)
	}
	r.wantFocus("same 1", "after switching within the app")
}

func TestTheSwitcherListsAndRestoresMinimizedWindows(t *testing.T) {
	t.Parallel()
	r := startMRU(t, time.Hour)
	r.open("a", "b")
	r.h.dispatch("minimize")
	r.wantFocus("a", "after minimizing b")
	r.h.dispatch("switcher next")
	r.wantFocus("b", "after switching to the minimized window")
	for _, w := range r.snapshot().Windows {
		if w.Title == "b" && w.Minimized {
			t.Fatal("the switcher focused b without restoring it")
		}
	}
}

func TestSwitcherPreviewsRedrawOnlyForCommittedWindows(t *testing.T) {
	t.Parallel()
	r := startMRU(t, 0, bindSpec{chord: "ALT + Tab", action: "switcher next"})
	r.open("a", "b", "c")
	for _, w := range r.win {
		w.Stop()
	}
	r.c.Sync()
	r.h.key(keyAlt, true)
	r.h.tap(keyTab)
	r.h.Eventually("the switcher drawn with its three previews", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return comp.SwitcherFrames() > 0 && comp.SwitcherThumbRedraws() == 3
	})
	if err := r.win["b"].Redraw(); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	r.h.Eventually("b's preview redrawn", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return comp.SwitcherThumbRedraws() == 4
	})
	if err := r.win["c"].Redraw(); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	r.h.Eventually("c's preview redrawn", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return comp.SwitcherThumbRedraws() >= 5
	})
	if _, redraws, _ := r.counters(); redraws != 5 {
		t.Fatalf("%d preview redraws after two commits, want 5", redraws)
	}
	r.h.key(keyAlt, false)
	if err := r.win["a"].Redraw(); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	if _, redraws, open := r.counters(); redraws != 5 || open {
		t.Fatalf("after closing: %d preview redraws, open %t; want 5 and closed", redraws, open)
	}
}

func TestCycleWalksWithoutReorderingUntilTheModifierComesUp(t *testing.T) {
	t.Parallel()
	r := startMRU(t, 0, bindSpec{chord: "ALT + grave", action: "cycle next"})
	r.open("a", "b", "c")
	r.h.key(keyAlt, true)
	r.h.tap(keyGrave)
	r.wantFocus("b", "after one step")
	r.h.tap(keyGrave)
	r.wantFocus("a", "after two steps")
	r.wantOrder("during the walk", "c", "b", "a")
	r.h.key(keyAlt, false)
	r.wantOrder("after the walk", "a", "c", "b")
}

func TestHoverFocusIsRecordedAtTheNextKey(t *testing.T) {
	t.Parallel()
	r := startMRU(t, 0)
	r.open("a", "b")
	var at map[string]int
	for _, w := range r.snapshot().Windows {
		if at == nil {
			at = map[string]int{}
		}
		at[w.Title] = w.Rect.X + w.Rect.Width/2
	}
	r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		return errors.Join(b.Pointer().MoveTo(o, float64(at["a"])/float64(small.Size.X), 0.5), b.Pointer().Frame())
	})
	r.wantFocus("a", "under the pointer")
	r.wantOrder("before any key", "b", "a")
	r.h.tap(30)
	r.wantOrder("after a key", "a", "b")
}
