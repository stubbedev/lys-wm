package harness

import (
	"errors"
	"image"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const keyUp = 103

var (
	overviewMode = backend.Mode{Size: image.Pt(800, 500), RefreshMHz: 60000, Preferred: true}
	redPix       = [3]byte{0xff, 0, 0}
	bluePix      = [3]byte{0, 0, 0xff}
	greenPix     = [3]byte{0, 0xff, 0}
)

func plainOverview(cfg *compositor.Config) {
	cfg.Overview = compositor.OverviewStyle{Dim: 0.35}
}

type overviewPlan struct {
	wm.OverviewPlan
	windows   map[wm.WindowID]image.Rectangle
	minimized map[wm.WindowID]image.Rectangle
	spaces    []wm.WorkspaceID
}

func planOf(h *Harness, outputIndex int) overviewPlan {
	var plan overviewPlan
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		snap := c.Snapshot()
		out := snap.Outputs[outputIndex]
		for _, w := range snap.Workspaces {
			if w.Output == out.Name {
				plan.spaces = append(plan.spaces, w.ID)
			}
		}
		state := c.WM()
		rect := func(r wmipc.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height) }
		var shown, hidden []wm.WindowID
		var shownRects, hiddenRects []image.Rectangle
		for _, p := range state.WorkspacePlacements(out.Workspace, wm.Spacing{TabBar: 20}) {
			if p.Minimized {
				hidden, hiddenRects = append(hidden, p.ID), append(hiddenRects, p.Rect)
			} else {
				shown, shownRects = append(shown, p.ID), append(shownRects, p.Rect)
			}
		}
		plan.OverviewPlan = wm.PlanOverview(rect(out.Rect), len(plan.spaces), shownRects, hiddenRects)
		plan.windows, plan.minimized = map[wm.WindowID]image.Rectangle{}, map[wm.WindowID]image.Rectangle{}
		for i, id := range shown {
			plan.windows[id] = plan.Windows[i]
		}
		for i, id := range hidden {
			plan.minimized[id] = plan.Minimized[i]
		}
		return nil
	})
	return plan
}

func centre(r image.Rectangle) image.Point {
	return r.Min.Add(r.Size().Div(2))
}

func (h *Harness) moveAt(p image.Point, size image.Point) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		return errors.Join(b.Pointer().MoveTo(o, float64(p.X)/float64(size.X), float64(p.Y)/float64(size.Y)), b.Pointer().Frame())
	})
}

func (h *Harness) clickAt(p image.Point) {
	h.t.Helper()
	h.click(0, float64(p.X), float64(p.Y), btnLeft)
}

func (h *Harness) overviewOpen() bool {
	var open bool
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		open = c.Snapshot().Overview
		return nil
	})
	return open
}

func (h *Harness) expectOverview(want bool) {
	h.t.Helper()
	if got := h.overviewOpen(); got != want {
		h.t.Fatalf("the overview is open: %v, want %v", got, want)
	}
}

func colouredPair(t *testing.T, h *Harness) (*testclient.Client, *testclient.Window, *testclient.Window) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	red := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", red.Mapped)
	blue := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", blue.Mapped)
	c.DispatchUntil("red at half the output", func() bool { return red.Logical.X == 400 })
	h.Eventually("both windows", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(20, 50, redPix)(b, comp) && showing(700, 50, bluePix)(b, comp)
	})
	return c, red, blue
}

func TestTheOverviewOnEveryRenderer(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, plainOverview, overviewMode)
		colouredPair(t, h)
		h.dispatch("overview show")
		h.expectOverview(true)
		plan := planOf(h, 0)
		if len(plan.windows) != 2 {
			t.Fatalf("plan %+v", plan)
		}
		h.Eventually("the windows in their slots", func(b *headless.Backend, comp *compositor.Compositor) bool {
			r, bl := centre(plan.windows[1]), centre(plan.windows[2])
			return showingAbout(r.X, r.Y, redPix)(b, comp) && showingAbout(bl.X, bl.Y, bluePix)(b, comp)
		})
		h.assertGolden("overview")
		h.dispatch("overview hide")
		h.expectOverview(false)
		h.Eventually("the windows back", func(b *headless.Backend, comp *compositor.Compositor) bool {
			return showingAbout(20, 50, redPix)(b, comp) && showingAbout(700, 50, bluePix)(b, comp)
		})
	})
}

func TestTheOverviewBlursTheWallpaperOnEveryRenderer(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(*compositor.Config) {}, overviewMode)
		c := testclient.Connect(t, h.Socket())
		half := func(pix []byte, stride int, size image.Point, _ int) {
			for y := range size.Y {
				for x := range size.X {
					at := y*stride + x*4
					v := byte(0)
					if x > size.X/2 {
						v = 0xff
					}
					pix[at], pix[at+1], pix[at+2], pix[at+3] = v, v, v, 0xff
				}
			}
		}
		wallpaper := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerBackground, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight, ExclusiveZone: -1}, half)
		c.DispatchUntil("the wallpaper to map", wallpaper.Mapped)
		h.Eventually("the wallpaper's hard edge", func(b *headless.Backend, comp *compositor.Compositor) bool {
			return showing(399, 300, [3]byte{})(b, comp) && showing(402, 300, [3]byte{0xff, 0xff, 0xff})(b, comp)
		})
		var before uint64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			before = comp.BlurRedraws()
			return nil
		})
		h.dispatch("overview show")
		h.Rendered(0)
		var redraws uint64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			redraws = comp.BlurRedraws() - before
			return nil
		})
		if redraws == 0 {
			t.Fatal("no blur chain ran")
		}
		h.Rendered(0)
		h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
			px := snapshot(b.Outputs()[0])
			l, r := pixel(px, 396, 300)[0], pixel(px, 404, 300)[0]
			if r-l > 0x30 {
				t.Errorf("the wallpaper's edge is still sharp: %#x then %#x", l, r)
			}
			if comp.BlurRedraws()-before != redraws {
				t.Errorf("an idle overview redrew the wallpaper's blur")
			}
			return nil
		})
	})
}

func TestTheOverviewTakesInputAndGivesItBack(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 400,250", "frame")
	r.h.dispatch("overview show")
	r.expect("leave win", "frame", "kb_leave win")
	r.inject(func(b *headless.Backend, _ *headless.Output) error { return b.Keyboard().Key(keyA, true) })
	r.inject(func(b *headless.Backend, _ *headless.Output) error { return b.Keyboard().Key(keyA, false) })
	r.inject(func(b *headless.Backend, o *headless.Output) error {
		return errors.Join(b.Pointer().MoveTo(o, 0.5, 0.5), b.Pointer().Frame())
	})
	h.Rendered(0)
	r.c.Sync()
	if len(r.in.Log) != r.seen {
		t.Fatalf("a client heard the overview's input: %q", r.in.Log[r.seen:])
	}
	r.h.tap(keyEsc)
	h.expectOverview(true)
	r.h.tap(keyEsc)
	h.expectOverview(false)
	r.expect("kb_enter win", "enter win 400,250", "frame")
}

func TestAKeyHeldAcrossTheOverviewIsReleasedForTheClient(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 400,250", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error { return b.Keyboard().Key(keyA, true) })
	r.expect("key 30 1")
	r.h.dispatch("overview show")
	r.expect("leave win", "frame", "kb_leave win")
	r.inject(func(b *headless.Backend, _ *headless.Output) error { return b.Keyboard().Key(keyA, false) })
	r.h.dispatch("overview hide")
	r.expect("kb_enter win", "enter win 400,250", "frame")
	r.c.Sync()
	if slices.Contains(r.in.Log[r.seen:], "key 30 0") {
		t.Fatalf("the client heard a release it was never focused for: %q", r.in.Log[r.seen:])
	}
}

func TestClickingAWindowFocusesItAndCloses(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	plan := planOf(h, 0)
	h.clickAt(centre(plan.windows[1]))
	h.expectOverview(false)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if id, ok := c.WM().Focused(); !ok || id != 1 {
			t.Errorf("focused %d %v, want window 1", id, ok)
		}
		return nil
	})
}

func TestClickingEmptySpaceCloses(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	h.clickAt(image.Pt(3, 497))
	h.expectOverview(false)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if id, ok := c.WM().Focused(); !ok || id != 2 {
			t.Errorf("focus moved to %d %v", id, ok)
		}
		return nil
	})
}

func TestArrowsAndEnterPickAWindow(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	h.tap(keyLeft)
	h.tap(keyEnter)
	h.expectOverview(false)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if id, ok := c.WM().Focused(); !ok || id != 1 {
			t.Errorf("focused %d %v, want the window to the left", id, ok)
		}
		return nil
	})
}

func focusedAfter(t *testing.T, h *Harness, want wm.WindowID) {
	t.Helper()
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if id, ok := c.WM().Focused(); !ok || id != want {
			t.Errorf("focused %d %v, want %d", id, ok, want)
		}
		return nil
	})
}

func TestTabWalksTheWindowsAndWraps(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	h.tap(keyTab)
	h.tap(keyEnter)
	h.expectOverview(false)
	focusedAfter(t, h, 1)

	h.dispatch("overview show")
	h.key(keyShift, true)
	h.tap(keyTab)
	h.key(keyShift, false)
	h.tap(keyEnter)
	h.expectOverview(false)
	focusedAfter(t, h, 2)
}

func TestAltNumberKeysShowAWorkspaceAndTheShownOneCloses(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	h.withAlt(key3)
	h.expectOverview(true)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if got := c.Snapshot().Outputs[0].Workspace; got != 3 {
			t.Errorf("Alt + 3 shows workspace %d", got)
		}
		return nil
	})
	h.withAlt(key3)
	h.expectOverview(false)
}

func TestDeleteAsksTheRingedWindowToClose(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c, red, blue := colouredPair(t, h)
	h.dispatch("overview show")
	h.tap(keyDelete)
	c.DispatchUntil("the focused window asked to close", func() bool { return blue.Closed })
	if red.Closed {
		t.Error("the other window was asked to close too")
	}
	h.expectOverview(true)
}

func TestShiftArrowsMoveTheRingedWindowAWorkspace(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	workspaceOf := func(id wm.WindowID) wm.WorkspaceID {
		var got wm.WorkspaceID
		h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
			got, _ = c.WM().WorkspaceOf(id)
			return nil
		})
		return got
	}
	h.key(keyShift, true)
	h.tap(keyLeft)
	h.key(keyShift, false)
	if got := workspaceOf(2); got != 1 {
		t.Fatalf("left of workspace 1 moved the window to %d", got)
	}
	h.key(keyShift, true)
	h.tap(keyRight)
	h.key(keyShift, false)
	h.expectOverview(true)
	if got := workspaceOf(2); got != 2 {
		t.Fatalf("right moved the ringed window to %d, want 2", got)
	}
	if got := workspaceOf(1); got != 1 {
		t.Fatalf("the other window moved to %d", got)
	}
}

func TestModifiedAndOtherKeysDoNothingInTheOverview(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c, red, blue := colouredPair(t, h)
	h.dispatch("overview show")
	h.key(keyCtrl, true)
	h.tap(keyQ, keyDelete, key3)
	h.key(keyCtrl, false)
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if red.Closed || blue.Closed {
		t.Error("a key the overview does not take closed a window")
	}
	h.expectOverview(true)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if got := c.Snapshot().Outputs[0].Workspace; got != 1 {
			t.Errorf("a modified number switched to workspace %d", got)
		}
		return nil
	})
}

func TestArrowsReachTheStrip(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	h.tap(keyUp)
	h.tap(keyEnter)
	h.expectOverview(true)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if got := c.Snapshot().Outputs[0].Workspace; got != 2 {
			t.Errorf("up from the right window and enter shows workspace %d, want the plus above it", got)
		}
		return nil
	})
}

func TestTheThumbnailsSwitchAndThePlusCreates(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	_, _, _ = colouredPair(t, h)
	h.dispatch("overview show")
	plan := planOf(h, 0)
	h.clickAt(centre(plan.Plus))
	h.expectOverview(true)
	active := func() wm.WorkspaceID {
		var id wm.WorkspaceID
		h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
			id = c.Snapshot().Outputs[0].Workspace
			return nil
		})
		return id
	}
	if got := active(); got != 2 {
		t.Fatalf("the plus went to workspace %d", got)
	}
	plan = planOf(h, 0)
	if !slices.Equal(plan.spaces, []wm.WorkspaceID{1, 2}) {
		t.Fatalf("spaces %v", plan.spaces)
	}
	h.clickAt(centre(plan.Strip[0]))
	if got := active(); got != 1 {
		t.Fatalf("the thumbnail went to workspace %d", got)
	}
	h.expectOverview(true)
	plan = planOf(h, 0)
	h.clickAt(centre(plan.Strip[0]))
	h.expectOverview(false)
}

func dragBetween(h *Harness, from, to image.Point) {
	h.t.Helper()
	size := overviewMode.Size
	h.moveAt(from, size)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame())
	})
	h.moveAt(from.Add(image.Pt(20, 20)), size)
	h.moveAt(to, size)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, false), b.Pointer().Frame())
	})
}

func TestDraggingAWindowOntoAThumbnailMovesIt(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	plan := planOf(h, 0)
	dragBetween(h, centre(plan.windows[2]), centre(plan.Plus))
	h.expectOverview(true)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if ws, _ := c.WM().WorkspaceOf(2); ws != 2 {
			t.Errorf("the window is on workspace %d", ws)
		}
		if ws, _ := c.WM().WorkspaceOf(1); ws != 1 {
			t.Errorf("the other window moved to %d", ws)
		}
		return nil
	})
	plan = planOf(h, 0)
	if len(plan.windows) != 1 || len(plan.spaces) != 2 {
		t.Fatalf("plan %+v", plan)
	}
	dragBetween(h, centre(plan.windows[1]), image.Pt(3, 497))
	h.expectOverview(true)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if ws, _ := c.WM().WorkspaceOf(1); ws != 1 {
			t.Errorf("a window dropped on nothing went to workspace %d", ws)
		}
		return nil
	})
}

func TestMinimizedWindowsHaveARowAndComeBackOnClick(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("minimize 1")
	h.dispatch("overview show")
	plan := planOf(h, 0)
	if len(plan.minimized) != 1 {
		t.Fatalf("plan %+v", plan)
	}
	at := centre(plan.minimized[1])
	h.Eventually("the minimized window in its row", showingAbout(at.X, at.Y, redPix))
	h.clickAt(at)
	h.expectOverview(false)
	if minimized, found := snapshotWindow(h, 1); !found || minimized {
		t.Fatalf("minimized %v found %v", minimized, found)
	}
}

func TestTabbedAndFullscreenWindowsGetSlots(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, plainOverview, overviewMode)
		c := testclient.Connect(t, h.Socket())
		red := c.Window("red", testclient.Fill(0, 0, 0xff))
		c.DispatchUntil("red to map", red.Mapped)
		h.dispatch("makegroup tab")
		blue := c.Window("blue", testclient.Fill(0xff, 0, 0))
		c.DispatchUntil("blue to map", blue.Mapped)
		green := c.Window("green", testclient.Fill(0, 0xff, 0))
		c.DispatchUntil("green to map", green.Mapped)
		h.Eventually("green focused in the layout", focusedIs("green"))
		h.dispatch("fullscreen")
		c.DispatchUntil("green fullscreen", func() bool { return green.Logical == overviewMode.Size })
		h.Eventually("green over everything", showing(10, 10, greenPix))
		h.dispatch("overview show")
		plan := planOf(h, 0)
		if len(plan.windows) != 3 {
			t.Fatalf("plan %+v", plan)
		}
		for id, want := range map[wm.WindowID][3]byte{1: redPix, 2: bluePix, 3: greenPix} {
			at := centre(plan.windows[id])
			h.Eventually("window in its slot", showingAbout(at.X, at.Y, want))
		}
	})
}

func TestDialogsTravelWithTheirParents(t *testing.T) {
	t.Parallel()
	s := modal(t, false)
	s.h.dispatch("overview show")
	var slots []image.Rectangle
	s.h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		slots = c.OverviewSlots(c.Outputs()[0])
		return nil
	})
	if len(slots) != 1 {
		t.Fatalf("a window and its dialog took slots %v", slots)
	}
	s.h.Rendered(0)
	shown := snapshotPixels(s.h, slots[0])
	if !shown[[3]byte{0, 0, 0xff}] || !shown[[3]byte{0, 0xff, 0}] {
		t.Fatalf("the slot %v shows %v", slots[0], shown)
	}
}

func snapshotPixels(h *Harness, r image.Rectangle) map[[3]byte]bool {
	out := map[[3]byte]bool{}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		px := snapshot(b.Outputs()[0])
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				out[pixel(px, x, y)] = true
			}
		}
		return nil
	})
	return out
}

func TestPopupsTravelWithTheirWindows(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("win", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	p := c.Popup(w, menuAt(image.Pt(100, 100), image.Pt(200, 100)), nil, testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the popup to map", p.Mapped)
	h.Eventually("the popup in place", showing(200, 150, greenPix))
	h.dispatch("overview show")
	plan := planOf(h, 0)
	slot := plan.windows[1]
	at := image.Pt(slot.Min.X+200*slot.Dx()/800, slot.Min.Y+150*slot.Dy()/500)
	h.Eventually("the popup over its window, scaled with it", showing(at.X, at.Y, greenPix))
}

func TestLockWinsOverTheOverview(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 400,250", "frame")
	h.dispatch("overview show")
	r.expect("leave win", "frame", "kb_leave win")
	l := lockSession(t, h)
	l.seen("locked")
	h.expectOverview(false)
	h.Eventually("the desktop blanked", showing(5, 5, [3]byte{compositor.LockedColour.R, compositor.LockedColour.G, compositor.LockedColour.B}))
	h.dispatch("overview show")
	h.expectOverview(false)
	l.cover(overviewMode.Size, 0xff, 0, 0)
	l.c.DispatchUntil("the keyboard on the lock surface", func() bool { return slices.Contains(l.in.Log, "kb_enter lock") })
	r.h.tap(keyEsc)
	l.c.DispatchUntil("Esc at the lock", func() bool { return slices.Contains(l.in.Log, "key 1 1") })
	if err := errors.Join(l.lock.UnlockAndDestroy(), l.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("kb_enter win")
}

func TestEveryOutputShowsItsOwnAtOnce(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode, overviewMode)
	c := testclient.Connect(t, h.Socket())
	red := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", red.Mapped)
	h.dispatch("focus right")
	blue := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", blue.Mapped)
	h.dispatch("overview show")
	a, b := planOf(h, 0), planOf(h, 1)
	if len(a.windows) != 1 || len(b.windows) != 1 {
		t.Fatalf("plans %+v and %+v", a, b)
	}
	first, second := centre(a.Windows[0]), centre(b.Windows[0])
	h.Eventually("both outputs showing their window", func(bk *headless.Backend, _ *compositor.Compositor) bool {
		o := bk.Outputs()
		return pixel(snapshot(o[0]), first.X, first.Y) == redPix && pixel(snapshot(o[1]), second.X-800, second.Y) == bluePix
	})
	h.dispatch("overview hide")
	h.expectOverview(false)
}

func TestTheOverviewIsAnEventAndAQuery(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	lines := h.watch()
	query := func() string {
		got, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: wmipc.KindOverview})
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}
	if got := query(); got != "false" {
		t.Fatalf("closed answers %s", got)
	}
	h.dispatch("overview")
	expectEvent(t, lines, `{"event":"overview","open":true}`)
	if got := query(); got != "true" {
		t.Fatalf("open answers %s", got)
	}
	h.dispatch("overview toggle")
	expectEvent(t, lines, `{"event":"overview","open":false}`)
}

func TestClosedAgainTheDesktopIsExactlyWhatItWas(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	before := h.Rendered(0)
	h.dispatch("overview show")
	h.Rendered(0)
	h.dispatch("overview hide")
	h.Eventually("the desktop back", func(b *headless.Backend, _ *compositor.Compositor) bool {
		now := snapshot(b.Outputs()[0])
		return slices.Equal(now.Pix, before.Pix)
	})
}

func TestThumbnailsRedrawOnlyWhenTheirWindowsDamage(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c := testclient.Connect(t, h.Socket())
	still := c.Window("still", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("still to map", still.Mapped)
	still.Stop()
	h.dispatch("workspace 2")
	other := c.Window("other", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("other to map", other.Mapped)
	other.Stop()
	h.Rendered(0)
	h.dispatch("overview show")
	h.Rendered(0)
	count := func() uint64 {
		var n uint64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			n = comp.OverviewThumbRedraws()
			return nil
		})
		return n
	}
	h.Eventually("both thumbnails drawn and filtered, one a frame", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return comp.OverviewThumbRedraws() >= 2 && !comp.OverviewThumbsStale()
	})
	first := count()
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		for range 5 {
			if err := comp.Frame(b.Outputs()[0]); err != nil {
				return err
			}
		}
		return nil
	})
	if got := count(); got != first {
		t.Fatalf("idle frames redrew %d thumbnails", got-first)
	}
	plan := planOf(h, 0)
	for _, at := range []image.Point{{400, 300}, centre(plan.Strip[0]), centre(plan.Plus)} {
		h.moveAt(at, overviewMode.Size)
		h.Rendered(0)
		if got := count(); got != first {
			t.Fatalf("a hover redrew %d thumbnails", got-first)
		}
	}
}

func TestAWindowOnAnotherWorkspaceIsLiveInItsThumbnail(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c := testclient.Connect(t, h.Socket())
	other := c.Window("other", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("other to map", other.Mapped)
	h.dispatch("workspace 2")
	mine := c.Window("mine", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("mine to map", mine.Mapped)
	h.Rendered(0)
	c.Sync()
	frames := other.Frames
	h.dispatch("overview show")
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return b.Outputs()[0].ScheduleFrame() })
	c.DispatchUntil("a frame callback for the hidden window", func() bool { return other.Frames > frames })
	h.dispatch("overview hide")
}

func springTable(t testing.TB) anim.Table {
	t.Helper()
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Overview: {Enabled: &on}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return table
}

func TestTheOverviewAnimatesOnASpringAndReversesInFlight(t *testing.T) {
	t.Parallel()
	var now time.Duration
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = springTable(t)
		cfg.Clock = func() time.Duration { return now }
		plainOverview(cfg)
	}, overviewMode)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("win", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	h.Rendered(0)
	progress := func() float64 {
		var p float64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			p = comp.OverviewProgress(now)
			return nil
		})
		return p
	}
	h.dispatch("overview show")
	if p := progress(); p != 0 {
		t.Fatalf("opening starts at %v", p)
	}
	h.advanceClock(&now, 100*time.Millisecond)
	mid := progress()
	if mid <= 0 || mid >= 1 {
		t.Fatalf("100ms in the spring is at %v", mid)
	}
	h.dispatch("overview hide")
	if p := progress(); p != mid {
		t.Fatalf("reversing jumped from %v to %v", mid, p)
	}
	h.advanceClock(&now, 8*time.Millisecond)
	if p := progress(); p < mid {
		t.Fatalf("the spring lost its velocity: %v after %v", p, mid)
	}
	h.advanceClock(&now, 5*time.Second)
	h.Eventually("the overview gone", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return comp.OverviewProgress(now) == 0 && showing(10, 10, redPix)(b, comp)
	})
}

func TestAWindowThatOpensOrClosesInTheOverviewMovesTheRest(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c, red, blue := colouredPair(t, h)
	h.dispatch("overview show")
	green := c.Window("green", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("green to map", green.Mapped)
	plan := planOf(h, 0)
	if len(plan.windows) != 3 {
		t.Fatalf("plan after a new window: %+v", plan)
	}
	at := centre(plan.windows[3])
	h.Eventually("the new window in its slot", showing(at.X, at.Y, greenPix))
	h.expectOverview(true)
	if err := errors.Join(blue.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("two slots again", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return len(comp.OverviewSlots(comp.Outputs()[0])) == 2
	})
	plan = planOf(h, 0)
	at = centre(plan.windows[1])
	h.Eventually("the rest moved", showing(at.X, at.Y, redPix))
	red.Stop()
}

func TestTouchPicksAWindow(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	plan := planOf(h, 0)
	at := centre(plan.windows[1])
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		tp := b.Touch()
		return errors.Join(tp.Down(0, o, float64(at.X)/800, float64(at.Y)/500), tp.Frame(), tp.Up(0), tp.Frame())
	})
	h.expectOverview(false)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if id, ok := c.WM().Focused(); !ok || id != 1 {
			t.Errorf("focused %d %v", id, ok)
		}
		return nil
	})
}

func TestAnOverlayStaysOnTopAndTakesItsOwnInput(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 400,250", "frame")
	h.dispatch("overview show")
	r.expect("leave win", "frame", "kb_leave win")
	oc := testclient.Connect(t, h.Socket())
	_, in := oc.Seat()
	note := oc.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerOverlay, Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft, Size: image.Pt(100, 100), Interactivity: cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityExclusive}, testclient.Fill(0, 0xff, 0))
	in.Name(note.Surface, "note")
	oc.DispatchUntil("the overlay to map", note.Mapped)
	h.Eventually("the overlay above the overview", showing(50, 50, greenPix))
	oc.DispatchUntil("the keyboard on the overlay", func() bool { return slices.Contains(in.Log, "kb_enter note") })
	h.tap(keyEsc)
	oc.DispatchUntil("Esc at the overlay", func() bool { return slices.Contains(in.Log, "key 1 1") })
	h.expectOverview(true)
}

func TestATopBarFadesOutAndComesBackWithoutLeavingItsOutput(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	h.Eventually("the bar", showing(790, 5, barColor))
	h.dispatch("overview show")
	h.Eventually("the bar gone", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return !showing(790, 5, barColor)(b, comp)
	})
	if strings.Contains(h.Trace(), "wl_surface@"+strconv.Itoa(int(bar.Surface.Base().ID()))+".leave") {
		t.Error("the bar left its output")
	}
	h.dispatch("overview hide")
	h.Eventually("the bar back", showing(790, 5, barColor))
}

func TestAFloatingWindowAndAMoveToAnotherOutput(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode, overviewMode)
	colouredPair(t, h)
	h.dispatch("togglefloating")
	h.dispatch("overview show")
	a := planOf(h, 0)
	if len(a.windows) != 2 {
		t.Fatalf("plan %+v", a)
	}
	from := centre(a.windows[2])
	h.moveAt(from, overviewMode.Size)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame())
	})
	h.moveAt(from.Add(image.Pt(20, 20)), overviewMode.Size)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[1]
		return errors.Join(b.Pointer().MoveTo(o, 0.5, 0.6), b.Pointer().Frame(), b.Pointer().Button(btnLeft, false), b.Pointer().Frame())
	})
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		ws, _ := c.WM().WorkspaceOf(2)
		w, _ := c.WM().Workspace(ws)
		if w.Output != c.Snapshot().Outputs[1].Name {
			t.Errorf("the window is on %s", w.Output)
		}
		return nil
	})
}

func TestAnXWindowTakesPartInTheOverview(t *testing.T) {
	t.Parallel()
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland: %v", err) }, overviewMode)
	xc := xDial(t, h)
	xc.window(image.Rect(10, 10, 90, 70), 0xff0000, false, map[string][]byte{"WM_CLASS": []byte("xtest\x00XTest\x00")})
	h.Eventually("the X window managed", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 1 })
	h.Eventually("the X window drawn", showing(100, 100, redPix))
	h.dispatch("overview show")
	var slots []image.Rectangle
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		slots = c.OverviewSlots(c.Outputs()[0])
		return nil
	})
	if len(slots) != 1 {
		t.Fatalf("slots %v", slots)
	}
	at := centre(slots[0])
	h.Eventually("the X window in its slot", showing(at.X, at.Y, redPix))
	h.clickAt(at)
	h.expectOverview(false)
	h.Eventually("the X window focused", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].Focused
	})
}

func (h *Harness) swipeOverview(dy float64) {
	h.t.Helper()
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.Swipe(0, dy) })
}

func (h *Harness) overviewProgress() float64 {
	var p float64
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		p = c.OverviewProgress(c.Now())
		return nil
	})
	return p
}

func TestThreeFingersScrubTheOverview(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 400,250", "frame")
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureBegin(backend.Swipe, 3) })
	h.swipeOverview(-100)
	h.expectOverview(true)
	if got := h.overviewProgress(); math.Abs(got-0.25) > 1e-3 {
		t.Fatalf("100 units up of 400 scrub to %v", got)
	}
	r.expect("leave win", "frame", "kb_leave win")
	h.swipeOverview(-100)
	if got := h.overviewProgress(); math.Abs(got-0.5) > 1e-3 {
		t.Fatalf("200 units up of 400 scrub to %v", got)
	}
	h.swipeOverview(60)
	if got := h.overviewProgress(); math.Abs(got-0.35) > 1e-3 {
		t.Fatalf("back down 60 scrubs to %v", got)
	}
	h.Advance(60 * time.Millisecond)
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureEnd(backend.Swipe, false) })
	h.expectOverview(false)
	r.expect("kb_enter win", "enter win 400,250", "frame")

	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureBegin(backend.Swipe, 3) })
	h.swipeOverview(-1000)
	got := h.overviewProgress()
	if got <= 1 || got > 1.13 {
		t.Fatalf("a swipe past the end resists at %v", got)
	}
	h.Advance(60 * time.Millisecond)
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureEnd(backend.Swipe, false) })
	h.expectOverview(true)
	if got := h.overviewProgress(); got != 1 {
		t.Fatalf("with animations off the release lands at %v", got)
	}

	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureBegin(backend.Swipe, 3) })
	h.swipeOverview(300)
	h.expectOverview(true)
	h.Advance(60 * time.Millisecond)
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureEnd(backend.Swipe, false) })
	h.expectOverview(false)

	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureBegin(backend.Swipe, 3) })
	h.swipeOverview(-300)
	h.expectOverview(true)
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureEnd(backend.Swipe, true) })
	h.expectOverview(false)
}

func TestThreeFingersGrabASettlingOverview(t *testing.T) {
	t.Parallel()
	var now time.Duration
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = springTable(t)
		cfg.Clock = func() time.Duration { return now }
	}, overviewMode)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("win", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	h.Rendered(0)
	h.dispatch("overview show")
	h.advanceClock(&now, 80*time.Millisecond)
	at := func() float64 {
		var p float64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			p = comp.OverviewProgress(now)
			return nil
		})
		return p
	}
	held := at()
	if held <= 0 || held >= 1 {
		t.Fatalf("80ms into opening the overview is at %v", held)
	}
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureBegin(backend.Swipe, 3) })
	h.advanceClock(&now, time.Second)
	if got := at(); got != held {
		t.Fatalf("held fingers let it go from %v to %v", held, got)
	}
	h.swipeOverview(-40)
	want := held + 0.1
	if got := at(); math.Abs(got-want) > 0.05 {
		t.Fatalf("40 units from %v scrub to %v", held, got)
	}
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error { return p.GestureEnd(backend.Swipe, false) })
	h.advanceClock(&now, 5*time.Second)
	if got := at(); got != 0 && got != 1 {
		t.Fatalf("settled at %v", got)
	}
}

func TestAStyleOnTheVerticalAxisGivesWayToWorkspaces(t *testing.T) {
	t.Parallel()
	for _, b := range compositor.DefaultGestureBinds(anim.Style{Kind: anim.SlideVert}) {
		if b.Drive == binds.DriveOverview {
			t.Errorf("the overview takes %+v from a vertical workspace style", b)
		}
	}
	got := compositor.DefaultGestureBinds(anim.Style{Kind: anim.Slide})
	if !slices.ContainsFunc(got, func(b binds.GestureBind) bool {
		return b.Drive == binds.DriveOverview && b.Direction == binds.SwipeVertical
	}) {
		t.Errorf("default binds %+v", got)
	}
}

func TestAnOutputGoingAwayAndAReloadLeaveTheOverviewWhole(t *testing.T) {
	t.Parallel()
	var next compositor.Config
	h := StartWith(t, func(cfg *compositor.Config) { next = *cfg }, overviewMode, overviewMode)
	c := testclient.Connect(t, h.Socket())
	red := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", red.Mapped)
	h.dispatch("overview show")
	h.Rendered(0)
	h.Rendered(1)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(next) })
	h.expectOverview(true)
	at := centre(planOf(h, 0).windows[1])
	h.Eventually("the window after a reload", showing(at.X, at.Y, redPix))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return b.RemoveOutput(b.Outputs()[1]) })
	h.expectOverview(true)
	h.Rendered(0)
	h.dispatch("overview hide")
	h.Eventually("the desktop back", showing(10, 10, redPix))
}

func TestAHeldArrowRepeats(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Seat.RepeatRate, cfg.Seat.RepeatDelay = 200, 10
	}, overviewMode)
	c := testclient.Connect(t, h.Socket())
	for _, name := range []string{"a", "b", "c", "d"} {
		w := c.Window(name, testclient.Fill(0, 0, 0xff))
		c.DispatchUntil(name+" to map", w.Mapped)
	}
	h.dispatch("overview show")
	plan := planOf(h, 0)
	leftmost, focused := wm.WindowID(0), wm.WindowID(0)
	for id, r := range plan.windows {
		if leftmost == 0 || r.Min.X < plan.windows[leftmost].Min.X || r.Min.X == plan.windows[leftmost].Min.X && r.Min.Y < plan.windows[leftmost].Min.Y {
			leftmost = id
		}
	}
	h.Freeze()
	h.key(keyLeft, true)
	h.key(keyUp, true)
	h.Advance(150 * time.Millisecond)
	h.key(keyUp, false)
	h.key(keyLeft, false)
	h.key(keyLeft, true)
	h.Advance(150 * time.Millisecond)
	h.key(keyLeft, false)
	h.tap(keyEnter)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		focused, _ = comp.WM().Focused()
		return nil
	})
	if comp := plan.windows[focused]; comp.Min.X != plan.windows[leftmost].Min.X {
		t.Errorf("holding left ended on window %d at %v, not in the leftmost column at %v", focused, comp, plan.windows[leftmost])
	}
}

func TestDraggingAWindowShowsAGrabbingCursor(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Cursor.Theme = testenv.CursorTheme(t) }, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	plan := planOf(h, 0)
	shape := func() string {
		var s string
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			s = comp.CursorShape()
			return nil
		})
		return s
	}
	from := centre(plan.windows[1])
	h.moveAt(from, overviewMode.Size)
	if got := shape(); got != "default" {
		t.Fatalf("hovering shows %q", got)
	}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame())
	})
	h.moveAt(from.Add(image.Pt(30, 30)), overviewMode.Size)
	if got := shape(); got != "grabbing" {
		t.Fatalf("dragging shows %q", got)
	}
	h.Rendered(0)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, false), b.Pointer().Frame())
	})
	if got := shape(); got != "default" {
		t.Fatalf("after the drop the cursor is %q", got)
	}
}

func TestAThumbnailOfFineLinesIsGrey(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, plainOverview, overviewMode)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("lines", func(pix []byte, stride int, size image.Point, _ int) {
			for y := range size.Y {
				v := byte(0xf0)
				if y%2 == 0 {
					v = 0x10
				}
				for x := range size.X {
					at := y*stride + x*4
					pix[at], pix[at+1], pix[at+2], pix[at+3] = v, v, v, 0xff
				}
			}
		})
		c.DispatchUntil("the lines to map", w.Mapped)
		w.Stop()
		h.Rendered(0)
		h.dispatch("overview show")
		inner := planOf(h, 0).Strip[0].Inset(8)
		h.Eventually("a filtered thumbnail once it rests", func(b *headless.Backend, _ *compositor.Compositor) bool {
			px := snapshot(b.Outputs()[0])
			lo, hi := byte(0xff), byte(0)
			for y := inner.Min.Y; y < inner.Max.Y; y++ {
				v := pixel(px, centre(inner).X, y)[0]
				lo, hi = min(lo, v), max(hi, v)
			}
			return hi-lo <= 8
		})
	})
}

func TestABorderStaysWholeAsAWindowShrinks(t *testing.T) {
	t.Parallel()
	magenta := render.Color{R: 0xff, B: 0xff, A: 0xff}
	want := [3]byte{0xff, 0, 0xff}
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		var now time.Duration
		h := start(t, func(cfg *compositor.Config) {
			cfg.Animations = springTable(t)
			cfg.Clock = func() time.Duration { return now }
			cfg.Decor.BorderWidth = 1
			cfg.Decor.ActiveBorder, cfg.Decor.InactiveBorder = render.SolidBorder(magenta), render.SolidBorder(magenta)
			plainOverview(cfg)
		}, overviewMode)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("win", testclient.Fill(0x40, 0x40, 0x40))
		c.DispatchUntil("the window to map", w.Mapped)
		w.Stop()
		h.Rendered(0)
		h.dispatch("overview show")
		h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
			o := b.Outputs()[0]
			for range 40 {
				now += 8 * time.Millisecond
				if err := comp.Frame(o); err != nil {
					return err
				}
				r, ok := comp.OverviewWindowRect(1, now)
				if !ok {
					t.Fatal("no rectangle for the window")
				}
				px := snapshot(o)
				mid := centre(r)
				for _, at := range []image.Point{{mid.X, r.Min.Y}, {mid.X, r.Max.Y - 1}, {r.Min.X, mid.Y}, {r.Max.X - 1, mid.Y}} {
					if at.In(image.Rectangle{Max: overviewMode.Size}) && pixel(px, at.X, at.Y) != want {
						t.Errorf("at %v with the window at %v the edge is %v", now, r, pixel(px, at.X, at.Y))
						return nil
					}
				}
			}
			return nil
		})
	})
}

func TestAFullscreenWindowGetsTheCornersOfTheRest(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			cfg.Decor.Rounding = 24
			plainOverview(cfg)
		}, overviewMode)
		c := testclient.Connect(t, h.Socket())
		green := c.Window("green", testclient.Fill(0, 0xff, 0))
		c.DispatchUntil("green to map", green.Mapped)
		h.dispatch("fullscreen")
		h.dispatch("overview show")
		slot := planOf(h, 0).windows[1]
		mid := centre(slot)
		h.Eventually("the slot", showingAbout(mid.X, mid.Y, greenPix))
		h.Eventually("the corners of the slot "+slot.String()+" rounded", func(b *headless.Backend, _ *compositor.Compositor) bool {
			return pixel(snapshot(b.Outputs()[0]), slot.Min.X, slot.Min.Y) != greenPix
		})
	})
}

func TestAVirtualKeyboardDrivesTheOverview(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	typist := testclient.Connect(t, h.Socket())
	kb := virtualKeyboardWith(t, typist, testenv.Keymap(t).Text())
	h.dispatch("overview show")
	virtualTap(t, typist, kb, keyLeft)
	virtualTap(t, typist, kb, keyEnter)
	typist.Sync()
	h.expectOverview(false)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if id, ok := c.WM().Focused(); !ok || id != 1 {
			t.Errorf("focused %d %v", id, ok)
		}
		return nil
	})
	h.dispatch("overview show")
	virtualTap(t, typist, kb, keyEsc)
	typist.Sync()
	h.expectOverview(false)
	h.dispatch("overview show")
	virtualTap(t, typist, kb, keyA)
	typist.Sync()
	h.expectOverview(true)
}

func TestAnIconifiedXWindowKeepsItsContentInTheRow(t *testing.T) {
	t.Parallel()
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland: %v", err) }, overviewMode)
	xc := xDial(t, h)
	main := xc.window(image.Rect(10, 10, 90, 70), 0xff0000, false, map[string][]byte{"WM_CLASS": []byte("xtest\x00XTest\x00")})
	h.Eventually("the X window drawn", showing(100, 100, redPix))
	iconifyX(t, h, xc, main)
	h.dispatch("overview show")
	slot := onlySlot(t, h)
	at := centre(slot)
	h.Rendered(0)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if got := pixel(snapshot(b.Outputs()[0]), at.X, at.Y); got != redPix {
			t.Errorf("the slot %v of an iconified X window shows %v, not its content", slot, got)
		}
		return nil
	})
	h.clickAt(at)
	h.expectOverview(false)
	h.Eventually("the X window back", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && !ws[0].Minimized
	})
}

func TestAFingerDragsAWindowOntoAThumbnail(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	colouredPair(t, h)
	h.dispatch("overview show")
	plan := planOf(h, 0)
	from, to := centre(plan.windows[2]), centre(plan.Plus)
	norm := func(p image.Point) (float64, float64) { return float64(p.X) / 800, float64(p.Y) / 500 }
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, tp := b.Outputs()[0], b.Touch()
		fx, fy := norm(from)
		tx, ty := norm(to)
		return errors.Join(tp.Down(0, o, fx, fy), tp.Frame(), tp.Motion(0, fx+0.05, fy+0.05), tp.Frame(), tp.Motion(0, tx, ty), tp.Frame(), tp.Up(0), tp.Frame())
	})
	h.expectOverview(true)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if ws, _ := c.WM().WorkspaceOf(2); ws != 2 {
			t.Errorf("the window is on workspace %d", ws)
		}
		return nil
	})
}

func TestADroppedWindowFliesIntoItsThumbnail(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		var now time.Duration
		h := start(t, func(cfg *compositor.Config) {
			cfg.Animations = springTable(t)
			cfg.Clock = func() time.Duration { return now }
			plainOverview(cfg)
		}, overviewMode)
		_, red, blue := colouredPair(t, h)
		red.Stop()
		blue.Stop()
		h.dispatch("overview show")
		h.advanceClock(&now, time.Minute)
		h.Rendered(0)
		plan := planOf(h, 0)
		drop := centre(plan.Plus)
		dragBetween(h, centre(plan.windows[2]), drop)
		h.advanceClock(&now, 30*time.Millisecond)
		var mid image.Point
		var closing int
		h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
			closing = comp.ClosingCount()
			if err := comp.Frame(b.Outputs()[0]); err != nil {
				return err
			}
			mid = comp.ClosingAt(now).Add(image.Pt(30, 15))
			return nil
		})
		if closing != 1 {
			t.Fatalf("%d windows in flight", closing)
		}
		if ws := planOf(h, 0); len(ws.spaces) != 2 {
			t.Fatalf("spaces %v", ws.spaces)
		}
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			if got := pixel(snapshot(b.Outputs()[0]), mid.X, mid.Y); got[2] < 0x40 {
				t.Errorf("no blue at %v along the way: %v", mid, got)
			}
			return nil
		})
		h.advanceClock(&now, time.Minute)
		h.Eventually("the flight over", func(b *headless.Backend, comp *compositor.Compositor) bool {
			if err := comp.Frame(b.Outputs()[0]); err != nil {
				return false
			}
			return comp.ClosingCount() == 0
		})
	})
}

func TestAMovingOverviewFrameAllocatesNothing(t *testing.T) {
	var now time.Duration
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = springTable(t)
		cfg.Clock = func() time.Duration { return now }
		cfg.Decor.Rounding = 12
		cfg.Decor.BorderWidth = 2
	}, overviewMode)
	c := testclient.Connect(t, h.Socket())
	for _, name := range []string{"a", "b"} {
		w := c.Window(name, testclient.Fill(0, 0, 0xff))
		c.DispatchUntil(name+" to map", w.Mapped)
		w.Stop()
	}
	hover := func(b *headless.Backend) {
		o := b.Outputs()[0]
		if err := errors.Join(b.Pointer().MoveTo(o, 0.3, 0.5), b.Pointer().Frame(), b.Pointer().MoveTo(o, 0.7, 0.5), b.Pointer().Frame()); err != nil {
			t.Error(err)
		}
	}
	var closed float64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		hover(b)
		closed = testing.AllocsPerRun(100, func() { hover(b) })
		return nil
	})
	h.dispatch("overview show")
	h.Rendered(0)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		step := func() {
			now += time.Millisecond
			if err := comp.Frame(o); err != nil {
				t.Error(err)
			}
		}
		for range 5 {
			step()
		}
		if got := testing.AllocsPerRun(10, step); got != 0 {
			t.Errorf("a moving frame allocates %v times", got)
		}
		if got := testing.AllocsPerRun(100, func() { hover(b) }); got > closed {
			t.Errorf("hovering allocates %v times, against %v with the overview closed", got, closed)
		}
		return nil
	})
}

func (h *Harness) advanceClock(now *time.Duration, by time.Duration) {
	h.t.Helper()
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		*now += by
		return nil
	})
}

func TestAClosedOverviewKeepsItsBlurUntilTheWallpaperChanges(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(*compositor.Config) {}, overviewMode)
		c := testclient.Connect(t, h.Socket())
		all := cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight
		wallpaper := c.Layer(testclient.LayerSpec{Layer: cwlr.ZwlrLayerShellV1LayerBackground, Anchor: all, ExclusiveZone: -1}, testclient.Fill(0, 0, 0xff))
		c.DispatchUntil("the wallpaper to map", wallpaper.Mapped)
		wallpaper.Stop()
		h.Eventually("the wallpaper", showing(10, 10, redPix))
		redraws := func() uint64 {
			var n uint64
			h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
				n = comp.BlurRedraws()
				return nil
			})
			return n
		}
		h.dispatch("overview show")
		h.Rendered(0)
		first := redraws()
		if first == 0 {
			t.Fatal("no blur chain ran")
		}
		h.dispatch("overview hide")
		h.Rendered(0)
		h.Rendered(0)
		h.dispatch("overview show")
		h.Rendered(0)
		if got := redraws(); got != first {
			t.Fatalf("opening again redrew the chain %d times", got-first)
		}
		h.dispatch("overview hide")
		h.Rendered(0)
		h.Rendered(0)
		if err := errors.Join(wallpaper.Surface.Attach(c.Buffer(wallpaper.Size, 0, testclient.Fill(0xff, 0, 0)), 0, 0), wallpaper.Surface.Damage(0, 0, 800, 500), wallpaper.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the new wallpaper", showing(10, 10, bluePix))
		h.dispatch("overview show")
		h.Rendered(0)
		if got := redraws(); got == first {
			t.Fatal("a wallpaper changed while closed kept its old blur")
		}
	})
}

func TestTheFirstThumbnailIsDrawnBeforeTheFirstOverviewFrame(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("window", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	h.Rendered(0)
	action, err := wm.ParseAction("overview show")
	if err != nil {
		t.Fatal(err)
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		before := comp.OverviewThumbRedraws()
		if err := comp.Dispatch(action); err != nil {
			return err
		}
		if got := comp.OverviewThumbRedraws() - before; got != 1 {
			t.Errorf("entering the overview drew %d thumbnails, want the active workspace's", got)
		}
		return nil
	})
}

func iconifyX(t *testing.T, h *Harness, xc *xclient, win xproto.Window) {
	t.Helper()
	iconify := xproto.ClientMessageEvent{Format: 32, Window: win, Type: xc.atoms["WM_CHANGE_STATE"], Data: xproto.ClientMessageDataUnionData32New([]uint32{3, 0, 0, 0, 0})}
	if err := xproto.SendEventChecked(xc.x, false, xc.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(iconify.Bytes())).Check(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the X window minimized", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].Minimized
	})
}

func onlySlot(t *testing.T, h *Harness) image.Rectangle {
	t.Helper()
	var slots []image.Rectangle
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		slots = c.OverviewSlots(c.Outputs()[0])
		return nil
	})
	if len(slots) != 1 {
		t.Fatalf("slots %v", slots)
	}
	return slots[0]
}

func TestAnIconifiedXWindowThatNeverDrewHoldsATileInTheRow(t *testing.T) {
	t.Parallel()
	tile := render.Color{R: 0xc0, G: 0x20, B: 0xa0, A: 0xff}
	h := StartXwaylandWith(t, func(cfg *compositor.Config) {
		plainOverview(cfg)
		cfg.Decor.Tab.Inactive.Background = tile
	}, func(err error) { t.Errorf("xwayland: %v", err) }, overviewMode)
	xc := xDial(t, h)
	main := xc.create(image.Rect(10, 10, 90, 70), 0, nil, map[string][]byte{"WM_CLASS": []byte("xtest\x00XTest\x00")})
	h.Eventually("the X window managed", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 1 })
	iconifyX(t, h, xc, main)
	h.dispatch("overview show")
	slot := onlySlot(t, h)
	if slot.Dx() < 20 || slot.Dy() < 20 {
		t.Fatalf("an X window iconified before its first layout landed has the slot %v", slot)
	}
	at := centre(slot)
	want := [3]byte{tile.R, tile.G, tile.B}
	h.Eventually("the tile in the slot "+slot.String(), showing(at.X, at.Y, want))
	h.clickAt(at)
	h.expectOverview(false)
	h.Eventually("the X window back and tiled", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && !ws[0].Minimized && ws[0].Rect.Width == 800
	})
}

func TestAWindowWhoseBufferIsGoneKeepsItsTileEvenDragged(t *testing.T) {
	onEachDMABUFRenderer(t, func(t *testing.T, start gpuStarter) {
		var now time.Duration
		tile := render.Color{R: 0xc0, G: 0x20, B: 0xa0, A: 0xff}
		h := start(t, func(cfg *compositor.Config) {
			cfg.Animations = springTable(t)
			cfg.Clock = func() time.Duration { return now }
			plainOverview(cfg)
			cfg.Decor.Tab.Inactive.Background = tile
		}, overviewMode)
		c := testclient.Connect(t, h.Socket())
		w := c.Window("gone", testclient.Fill(0, 0, 0xff))
		c.DispatchUntil("the window to map", w.Mapped)
		w.Stop()
		d := c.DMABUF(image.Pt(800, 500), testclient.Fill(0, 0xff, 0))
		if err := errors.Join(w.Surface.Attach(d.WL, 0, 0), w.Surface.DamageBuffer(0, 0, 800, 500), w.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the dmabuf drawn", showing(10, 10, greenPix))
		if err := errors.Join(d.WL.Destroy(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.Sync()
		h.dispatch("overview show")
		h.advanceClock(&now, time.Minute)
		h.Rendered(0)
		slot := onlySlot(t, h)
		from := centre(slot)
		want := [3]byte{tile.R, tile.G, tile.B}
		h.Eventually("the tile in the slot", showing(from.X, from.Y, want))
		to := from.Add(image.Pt(slot.Dx()/2+60, 0))
		h.moveAt(from, overviewMode.Size)
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame())
		})
		h.moveAt(to, overviewMode.Size)
		h.Eventually("the tile beside the pointer", showing(to.X-40, to.Y, want))
	})
}

func TestADraggedWindowKeepsItsPopup(t *testing.T) {
	t.Parallel()
	var now time.Duration
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = springTable(t)
		cfg.Clock = func() time.Duration { return now }
		plainOverview(cfg)
	}, overviewMode)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("win", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	p := c.Popup(w, menuAt(image.Pt(100, 100), image.Pt(200, 100)), nil, testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the popup to map", p.Mapped)
	h.Eventually("the popup in place", showing(200, 150, greenPix))
	h.dispatch("overview show")
	h.advanceClock(&now, time.Minute)
	h.Rendered(0)
	slot := planOf(h, 0).windows[1]
	on := image.Pt(slot.Min.X+200*slot.Dx()/800, slot.Min.Y+150*slot.Dy()/500)
	grab := centre(slot)
	by := image.Pt(-90, 40)
	h.moveAt(grab, overviewMode.Size)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnLeft, true), b.Pointer().Frame())
	})
	h.moveAt(grab.Add(by), overviewMode.Size)
	moved := on.Add(by)
	h.Eventually("the popup carried with its window", showing(moved.X, moved.Y, greenPix))
	left := image.Pt(slot.Min.X+100*slot.Dx()/800+by.X+2, slot.Min.Y+100*slot.Dy()/500+by.Y+2)
	h.Eventually("its corner carried too", showing(left.X, left.Y, greenPix))
}

func fingerAt(p image.Point) (float64, float64) {
	return float64(p.X) / float64(overviewMode.Size.X), float64(p.Y) / float64(overviewMode.Size.Y)
}

func (h *Harness) fingerDown(p image.Point) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		x, y := fingerAt(p)
		return errors.Join(b.Touch().Down(0, b.Outputs()[0], x, y), b.Touch().Frame())
	})
}

func (h *Harness) fingerTo(p image.Point) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		x, y := fingerAt(p)
		return errors.Join(b.Touch().Motion(0, x, y), b.Touch().Frame())
	})
}

func (h *Harness) fingerUp() {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Touch().Up(0), b.Touch().Frame())
	})
}

func springOverview(t *testing.T, now *time.Duration, start gpuStarter) (*Harness, *testclient.Client) {
	t.Helper()
	h := start(t, func(cfg *compositor.Config) {
		cfg.Animations = springTable(t)
		cfg.Clock = func() time.Duration { return *now }
		plainOverview(cfg)
	}, overviewMode)
	c, red, blue := colouredPair(t, h)
	red.Stop()
	blue.Stop()
	h.dispatch("overview show")
	h.advanceClock(now, time.Minute)
	h.Rendered(0)
	return h, c
}

func (h *Harness) frameAt(now *time.Duration, by time.Duration) {
	h.t.Helper()
	h.advanceClock(now, by)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		return comp.Frame(b.Outputs()[0])
	})
}

func (h *Harness) windowRect(id wm.WindowID, now *time.Duration) image.Rectangle {
	h.t.Helper()
	var r image.Rectangle
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		var ok bool
		if r, ok = comp.OverviewWindowRect(id, *now); !ok {
			h.t.Errorf("window %d has no rectangle in the overview", id)
		}
		return nil
	})
	return r
}

func TestACancelledDragPutsTheWindowBack(t *testing.T) {
	t.Parallel()
	for name, cancel := range map[string]func(*Harness){
		"touch cancel": func(h *Harness) {
			h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
				return errors.Join(b.Touch().Cancel(0), b.Touch().Frame())
			})
		},
		"overview closed": func(h *Harness) {
			h.dispatch("overview hide")
			h.dispatch("overview show")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var now time.Duration
			h, _ := springOverview(t, &now, StartWith)
			plan := planOf(h, 0)
			slot := plan.windows[2]
			h.fingerDown(centre(slot))
			h.fingerTo(centre(slot).Add(image.Pt(60, 40)))
			if got := h.windowRect(2, &now); got == slot {
				t.Fatalf("the finger did not carry the window out of %v", slot)
			}
			cancel(h)
			h.frameAt(&now, time.Minute)
			if got := h.windowRect(2, &now); got != slot {
				t.Errorf("the window is at %v, its slot is %v", got, slot)
			}
			h.Eventually("the window drawn in its slot", showing(centre(slot).X, centre(slot).Y, bluePix))
		})
	}
}

func TestASecondFingerLeavesADragAlone(t *testing.T) {
	t.Parallel()
	var now time.Duration
	h, _ := springOverview(t, &now, StartWith)
	plan := planOf(h, 0)
	slot := plan.windows[2]
	h.fingerDown(centre(slot))
	to := centre(slot).Add(image.Pt(60, 40))
	h.fingerTo(to)
	carried := h.windowRect(2, &now)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		x, y := fingerAt(centre(plan.windows[1]))
		return errors.Join(b.Touch().Down(1, b.Outputs()[0], x, y), b.Touch().Frame(), b.Touch().Motion(1, x+0.1, y), b.Touch().Frame(), b.Touch().Up(1), b.Touch().Frame())
	})
	if got := h.windowRect(2, &now); got != carried {
		t.Errorf("a second finger moved the carried window from %v to %v", carried, got)
	}
	h.expectOverview(true)
	h.fingerUp()
	h.frameAt(&now, time.Minute)
	if got := h.windowRect(2, &now); got != slot {
		t.Errorf("the window dropped on nothing is at %v, its slot is %v", got, slot)
	}
}

func TestAFingerCarriesAWindowAndItFliesIntoItsThumbnail(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		var now time.Duration
		h, _ := springOverview(t, &now, start)
		plan := planOf(h, 0)
		slot := plan.windows[2]
		h.fingerDown(centre(slot))
		held := image.Pt(centre(slot).X+60, plan.Strip[0].Max.Y+70)
		h.fingerTo(centre(slot).Add(image.Pt(20, 20)))
		h.fingerTo(held)
		h.frameAt(&now, time.Millisecond)
		h.Rendered(0)
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			if got := pixel(snapshot(b.Outputs()[0]), held.X, held.Y); got != bluePix {
				t.Errorf("under the finger at %v: %v, not the window it carries", held, got)
			}
			return nil
		})
		drop := centre(plan.Plus)
		h.fingerTo(drop)
		h.fingerUp()
		h.expectOverview(true)
		var flights int
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			flights = comp.ClosingCount()
			if ws, _ := comp.WM().WorkspaceOf(2); ws != 2 {
				t.Errorf("the window is on workspace %d, want the new one", ws)
			}
			return nil
		})
		if flights != 1 {
			t.Fatalf("%d windows in flight, want 1", flights)
		}
		h.frameAt(&now, 30*time.Millisecond)
		h.frameAt(&now, time.Minute)
		h.Eventually("the flight over", func(b *headless.Backend, comp *compositor.Compositor) bool {
			return comp.Frame(b.Outputs()[0]) == nil && comp.ClosingCount() == 0
		})
	})
}

func TestAFlightLandsOnTheThumbnailWhereItNowIs(t *testing.T) {
	t.Parallel()
	var now time.Duration
	h, _ := springOverview(t, &now, StartWith)
	drop := centre(planOf(h, 0).Plus)
	dragBetween(h, centre(planOf(h, 0).windows[2]), drop)
	plan := planOf(h, 0)
	if len(plan.spaces) != 2 {
		t.Fatalf("spaces %v", plan.spaces)
	}
	want := centre(plan.Strip[1])
	var at image.Point
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if comp.ClosingCount() != 1 {
			t.Fatalf("%d windows in flight", comp.ClosingCount())
		}
		at = comp.ClosingAt(now + time.Minute)
		return nil
	})
	if at != want {
		t.Errorf("the flight lands at %v, the new thumbnail is centred at %v", at, want)
	}
}

func TestDroppingAWindowOnItsOwnThumbnailSendsItBackWithoutAFlight(t *testing.T) {
	t.Parallel()
	var now time.Duration
	h, _ := springOverview(t, &now, StartWith)
	plan := planOf(h, 0)
	dragBetween(h, centre(plan.windows[2]), centre(plan.Strip[0]))
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if comp.ClosingCount() != 0 {
			t.Errorf("%d flights for a window that stays", comp.ClosingCount())
		}
		return nil
	})
	h.frameAt(&now, time.Minute)
	if got := h.windowRect(2, &now); got != plan.windows[2] {
		t.Errorf("the window is at %v, its slot is %v", got, plan.windows[2])
	}
}

func TestAThumbnailDrawnOnEnteringAnOverviewAtRestIsAlreadyFiltered(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("window", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	h.Rendered(0)
	action, err := wm.ParseAction("overview show")
	if err != nil {
		t.Fatal(err)
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if err := comp.Dispatch(action); err != nil {
			return err
		}
		if comp.OverviewThumbsStale() {
			t.Error("entering an overview that does not animate left an unfiltered thumbnail on screen")
		}
		return nil
	})
}

func thumbnailBand(strip image.Rectangle) image.Point {
	return image.Pt(centre(strip).X, strip.Min.Y+strip.Dy()/10)
}

func tallBar() testclient.LayerSpec {
	spec := topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone)
	spec.Size.Y, spec.ExclusiveZone = 100, 100
	return spec
}

func thumbnailsShow(strip []image.Rectangle, want ...[3]byte) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, comp *compositor.Compositor) bool {
		if comp.OverviewProgress(comp.Now()) < 1 || comp.OverviewThumbsStale() {
			return false
		}
		px := snapshot(b.Outputs()[0])
		for i, c := range want {
			at := thumbnailBand(strip[i])
			if !within(pixel(px, at.X, at.Y), c, 4) {
				return false
			}
		}
		return true
	}
}

func stillWindowOnEachOfTwoWorkspaces(h *Harness, c *testclient.Client) {
	h.t.Helper()
	for i, title := range []string{"first", "second"} {
		if i > 0 {
			h.dispatch("workspace 2")
		}
		w := c.Window(title, testclient.Fill(0xff, 0, 0))
		c.DispatchUntil(title+" to map", w.Mapped)
		w.Stop()
	}
}

func TestThumbnailsShowATopBarAndItsCommits(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, plainOverview, overviewMode)
		c := testclient.Connect(t, h.Socket())
		colour := barColor
		bar := c.Layer(tallBar(), func(pix []byte, stride int, size image.Point, frame int) {
			testclient.Fill(colour[2], colour[1], colour[0])(pix, stride, size, frame)
		})
		c.DispatchUntil("the bar to map", bar.Mapped)
		bar.Stop()
		stillWindowOnEachOfTwoWorkspaces(h, c)
		h.Rendered(0)
		h.dispatch("overview show")
		strip := planOf(h, 0).Strip
		h.Eventually("the bar in the thumbnails of the active and the other workspace", thumbnailsShow(strip, barColor, barColor))

		colour = [3]byte{0xe0, 0xe0, 0x10}
		if err := bar.Redraw(); err != nil {
			t.Fatal(err)
		}
		c.Sync()
		h.Eventually("the bar's commit in both thumbnails", thumbnailsShow(strip, colour, colour))

		if err := bar.Destroy(); err != nil {
			t.Fatal(err)
		}
		c.Sync()
		h.Eventually("the bar gone from both thumbnails", func(b *headless.Backend, comp *compositor.Compositor) bool {
			px := snapshot(b.Outputs()[0])
			for _, r := range strip[:2] {
				at := thumbnailBand(r)
				if within(pixel(px, at.X, at.Y), colour, 40) {
					return false
				}
			}
			return !comp.OverviewThumbsStale()
		})
	})
}

func TestAFullscreenWindowCoversTheBarInItsThumbnail(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, plainOverview, overviewMode)
		c := testclient.Connect(t, h.Socket())
		bar := c.Layer(tallBar(), testclient.Fill(barColor[2], barColor[1], barColor[0]))
		c.DispatchUntil("the bar to map", bar.Mapped)
		bar.Stop()
		full := c.Window("full", testclient.Fill(0xff, 0, 0))
		c.DispatchUntil("full to map", full.Mapped)
		h.dispatch("fullscreen")
		c.DispatchUntil("full at the whole output", func() bool { return full.Logical == overviewMode.Size })
		full.Stop()
		h.dispatch("workspace 2")
		other := c.Window("other", testclient.Fill(0xff, 0, 0))
		c.DispatchUntil("other to map", other.Mapped)
		other.Stop()
		h.Rendered(0)
		h.dispatch("overview show")
		h.Eventually("the fullscreen window over the bar in its thumbnail, the bar in the other", thumbnailsShow(planOf(h, 0).Strip, [3]byte{0, 0, 0xff}, barColor))
	})
}

func TestABarCommittingNoDamageRedrawsNoThumbnail(t *testing.T) {
	t.Parallel()
	h := Start(t, overviewMode)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(tallBar(), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	bar.Stop()
	stillWindowOnEachOfTwoWorkspaces(h, c)
	h.Rendered(0)
	h.dispatch("overview show")
	h.Rendered(0)
	h.Eventually("both thumbnails drawn and filtered", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		return comp.OverviewThumbRedraws() >= 2 && !comp.OverviewThumbsStale()
	})
	redraws := func() uint64 {
		var n uint64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			n = comp.OverviewThumbRedraws()
			return nil
		})
		return n
	}
	first := redraws()
	idle := func() {
		h.t.Helper()
		h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
			for range 5 {
				if err := comp.Frame(b.Outputs()[0]); err != nil {
					return err
				}
			}
			return nil
		})
	}
	idle()
	if got := redraws(); got != first {
		t.Fatalf("idle frames under an idle bar redrew %d thumbnails", got-first)
	}
	for range 3 {
		if err := errors.Join(bar.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		c.Sync()
		idle()
	}
	if got := redraws(); got != first {
		t.Fatalf("commits without damage redrew %d thumbnails", got-first)
	}
}

func TestTheStripIsGlassOverTheBlurredLayers(t *testing.T) {
	t.Parallel()
	magenta := render.Color{R: 0xff, B: 0xff, A: 0xff}
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			plainOverview(cfg)
			cfg.Decor.ActiveBorder = render.SolidBorder(magenta)
		}, overviewMode)
		c := testclient.Connect(t, h.Socket())
		wallpaper := c.Layer(testclient.LayerSpec{
			Layer:  cwlr.ZwlrLayerShellV1LayerBackground,
			Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight,
		}, testclient.Fill(0, 0xff, 0))
		c.DispatchUntil("the wallpaper to map", wallpaper.Mapped)
		wallpaper.Stop()
		w := c.Window("window", testclient.Fill(0xff, 0, 0))
		c.DispatchUntil("the window to map", w.Mapped)
		w.Stop()
		h.Rendered(0)
		h.dispatch("overview show")
		plan := planOf(h, 0)
		thumb := plan.Strip[0]
		mid := centre(thumb).Y
		h.Eventually("the overview at rest", func(_ *headless.Backend, comp *compositor.Compositor) bool {
			return comp.OverviewProgress(comp.Now()) >= 1 && !comp.OverviewThumbsStale()
		})
		px := h.Rendered(0)
		backdrop := pixel(px, 2, overviewMode.Size.Y-2)
		if got := pixel(px, thumb.Min.X-4, mid); got != [3]byte{0xff, 0, 0xff} {
			t.Errorf("the ring of the workspace shown: %v", got)
		}
		if got := pixel(px, thumb.Min.X-2, mid); got == [3]byte{0xff, 0, 0xff} {
			t.Error("no gap between the ring and its thumbnail")
		}
		pane := pixel(px, thumb.Max.X+(plan.Plus.Min.X-thumb.Max.X)/2, mid)
		if pane == backdrop {
			t.Errorf("no pane behind the strip: %v", pane)
		}
		plus := pixel(px, plan.Plus.Min.X+6, mid)
		if plus[1] <= plus[0] || plus[1] <= plus[2] {
			t.Errorf("the + tile hides the layers behind it: %v", plus)
		}
		if plus == pane {
			t.Errorf("the + tile is not its own pane: %v", plus)
		}
	})
}

func focusedIs(title string) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		for _, w := range c.Snapshot().Windows {
			if w.Focused {
				return w.Title == title
			}
		}
		return false
	}
}

const keyLeftAlt = 56

func (h *Harness) withAlt(keys ...uint32) {
	h.t.Helper()
	h.key(keyLeftAlt, true)
	h.tap(keys...)
	h.key(keyLeftAlt, false)
}
