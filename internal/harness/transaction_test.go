package harness

import (
	"errors"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	yellowColor = [3]byte{0xff, 0xff, 0}
	fillRed     = testclient.Fill(0, 0, 0xff)
	fillGreen   = testclient.Fill(0, 0xff, 0)
	fillBlue    = testclient.Fill(0xff, 0, 0)
)

func flat(cfg *compositor.Config) {
	cfg.WM.Autotile.Enabled = false
}

func heldForever(cfg *compositor.Config) {
	flat(cfg)
	cfg.HoldBound = time.Hour
}

func holding(_ *headless.Backend, c *compositor.Compositor) bool {
	return c.Holding()
}

func (h *Harness) holds() compositor.HoldStats {
	h.t.Helper()
	var s compositor.HoldStats
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		s = c.Holds()
		return nil
	})
	return s
}

func (h *Harness) stillShows(what string, x, y int, want [3]byte) {
	h.t.Helper()
	if got := pixel(h.Rendered(0), x, y); got != want {
		h.t.Errorf("%s: pixel %d,%d is %v, want %v", what, x, y, got, want)
	}
}

func open(t *testing.T, h *Harness, title string, paint testclient.Painter) (*testclient.Client, *testclient.Window) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	w := c.Window(title, paint)
	c.DispatchUntil(title+" to map", w.Mapped)
	return c, w
}

func TestATilingChangeLandsTogether(t *testing.T) {
	onEachConfiguredRenderer(t, testATilingChangeLandsTogether)
}

func testATilingChangeLandsTogether(t *testing.T, start gpuStarter) {
	h := start(t, heldForever, small)
	wide := func(pix []byte, stride int, size image.Point, frame int) {
		if size.X >= 80 {
			fillGreen(pix, stride, size, frame)
			return
		}
		testclient.Fill(0, 0xff, 0xff)(pix, stride, size, frame)
	}
	ca, a := open(t, h, "a", wide)
	cb, b := open(t, h, "b", fillBlue)
	h.Eventually("b asked to wait for a", holding)
	ca.DispatchUntil("a halved", func() bool { return a.Logical == image.Pt(80, 100) })
	ca.Sync()
	h.Eventually("the first change landed", landed)
	h.Eventually("a on the left", showing(70, 50, greenColor))
	h.Eventually("b on the right", showing(150, 50, blueColor))
	before := h.holds()

	_, _ = open(t, h, "c", fillRed)
	h.Eventually("the third window held back", holding)
	h.stillShows("c has not landed", 150, 50, blueColor)

	ca.DispatchUntil("a answers first, smaller and yellow", func() bool { return a.Logical.X < 80 })
	ca.Sync()
	if !h.compositorHolding() {
		t.Fatal("a's answer alone landed the change, b has not answered")
	}
	h.stillShows("a's answer waits for b's", 70, 50, greenColor)
	h.stillShows("a's old frame stands", 10, 50, greenColor)
	h.stillShows("b's old frame stands", 150, 50, blueColor)

	cb.DispatchUntil("b answers", func() bool { return b.Logical.X < 80 })
	cb.Sync()
	h.Eventually("the change landed", landed)
	frame := h.Rendered(0)
	if got := [3][3]byte{pixel(frame, 10, 50), pixel(frame, 70, 50), pixel(frame, 150, 50)}; got != [3][3]byte{yellowColor, blueColor, redColor} {
		t.Errorf("the landed frame shows %v, want yellow, blue and red together", got)
	}
	after := h.holds()
	if after.Landed != before.Landed+1 || after.Expired != 0 {
		t.Errorf("holds went from %+v to %+v, want one more landed and none expired", before, after)
	}
}

func (h *Harness) compositorHolding() bool {
	h.t.Helper()
	var held bool
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		held = c.Holding()
		return nil
	})
	return held
}

func TestAFrozenClientHoldsOthersOnlyUpToTheBoundAndOnce(t *testing.T) {
	onEachConfiguredRenderer(t, testAFrozenClientHoldsOthersOnlyUpToTheBoundAndOnce)
}

func testAFrozenClientHoldsOthersOnlyUpToTheBoundAndOnce(t *testing.T, start gpuStarter) {
	h := start(t, flat, small)
	cf, frozen := open(t, h, "frozen", fillGreen)
	cr, right := open(t, h, "right", fillBlue)
	h.Eventually("the frozen client's bound over", landed)
	first := h.holds()
	if first.Expired != 1 {
		t.Fatalf("holds %+v, want the frozen client's to have expired", first)
	}
	if first.Last < compositor.DefaultHoldBound-5*time.Millisecond {
		t.Errorf("the hold ended after %v, before its bound of %v, with nobody answering", first.Last, compositor.DefaultHoldBound)
	}
	if first.Armed != compositor.DefaultHoldBound {
		t.Errorf("the hold's timer was armed for %v, want its bound of %v", first.Armed, compositor.DefaultHoldBound)
	}
	t.Logf("the hold lasted %v against its bound of %v", first.Last, compositor.DefaultHoldBound)
	h.Eventually("the frozen window's old buffer fills its new frame", showing(75, 50, greenColor))
	h.Eventually("and stays out of its neighbour", showing(85, 50, blueColor))
	_, _ = open(t, h, "third", fillRed)
	cr.DispatchUntil("right answers", func() bool { return right.Logical.X < 80 })
	cr.Sync()
	h.Eventually("the frozen client, once late, holds nobody again", landed)
	if s := h.holds(); s.Expired != 1 {
		t.Errorf("holds %+v, want the frozen client skipped now", s)
	}

	cf.DispatchUntil("the frozen client thawed", func() bool { return frozen.Logical.X < 80 })
	cf.Sync()
	h.Eventually("its own buffer at last", showing(frozen.Logical.X-1, 50, greenColor))
	before := h.holds()
	_, _ = open(t, h, "fourth", fillRed)
	h.Eventually("caught up, it is waited for again", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Holds().Expired == before.Expired+1
	})
}

func TestClientsThatDoNotAnswerAConfigureHoldOnlyUpToTheBound(t *testing.T) {
	for name, answer := range map[string]testclient.Answer{"no ack": testclient.AnswerNever, "ack without a commit": testclient.AnswerAckOnly} {
		t.Run(name, func(t *testing.T) {
			h := Start(t, small)
			c, w := open(t, h, "mute", fillGreen)
			w.Answer = answer
			_, _ = open(t, h, "right", fillBlue)
			c.DispatchUntil("the configure seen", func() bool { return w.Logical.X == 160 && w.Configures > 1 })
			c.Sync()
			h.Eventually("the bound over", landed)
			if s := h.holds(); s.Expired != 1 {
				t.Fatalf("holds %+v, want one expired", s)
			}
			h.Eventually("the mute window's old buffer fills its new frame", showing(75, 50, greenColor))
		})
	}
}

func TestAClientKeepingItsBufferAnswersAndIsNotStretched(t *testing.T) {
	h := StartWith(t, heldForever, small)
	cl, left := open(t, h, "left", fillGreen)
	cr, right := open(t, h, "right", fillBlue)
	cl.DispatchUntil("left halved", func() bool { return left.Logical.X == 80 })
	cl.Sync()
	h.Eventually("the layout landed", landed)
	left.Answer = testclient.AnswerKeepingTheBuffer
	if err := errors.Join(right.Destroy(), cr.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("left asked to grow", holding)
	cl.DispatchUntil("the answer sent", func() bool { return left.Configures > 2 })
	cl.Sync()
	h.Eventually("the answer landed the change", landed)
	h.stillShows("its buffer as it is", 70, 50, greenColor)
	h.stillShows("nothing stretched over the rest of its frame", 150, 50, [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B})
	if s := h.holds(); s.Expired != 0 {
		t.Errorf("holds %+v, want none expired", s)
	}
}

func TestChangesWithoutAResizeAreNotHeld(t *testing.T) {
	h := StartWith(t, heldForever, small)
	c, w := open(t, h, "lone", fillGreen)
	dialogs := testclient.Connect(t, h.Socket())
	dialog := dialogs.WindowWith("dialog", fillBlue, func(d *testclient.Window) error {
		return errors.Join(d.Toplevel.SetMinSize(30, 30), d.Toplevel.SetMaxSize(30, 30))
	})
	dialogs.DispatchUntil("the fixed-size window to map", dialog.Mapped)
	c.Sync()
	h.dispatch("focus left")
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if comp.Holding() {
			t.Error("a focus change is held")
		}
		if s := comp.Holds(); s.Landed != 0 {
			t.Errorf("holds %+v, want none: nobody needed a new size", s)
		}
		return nil
	})
	if w.Logical != small.Size {
		t.Errorf("the tiled window is %v, want the whole output", w.Logical)
	}
	time.Sleep(100 * time.Millisecond)
	frames := framesOn(h, 0)
	woke := h.Wakeups()
	time.Sleep(100 * time.Millisecond)
	if n := framesOn(h, 0) - frames; n != 0 {
		t.Errorf("%d frames ran on an idle desktop", n)
	}
	if n := h.Wakeups() - woke; n > 2 {
		t.Errorf("the loop woke %d times on an idle desktop", n)
	}
}

func TestRealClientsCatchUpWithEveryChangeAtTheirOwnPace(t *testing.T) {
	for _, client := range []struct {
		name         string
		args         []string
		mayNotRedraw bool
	}{
		{name: "foot", args: []string{"--override=main.font=monospace:size=12", "sh", "-c", "sleep 60"}},
		{name: "weston-terminal"},
		{name: "weston-flower", mayNotRedraw: true},
		{name: "es2gears_wayland"},
		{name: "lys-ui-hello"},
	} {
		t.Run(client.name, func(t *testing.T) {
			var plain, gapped compositor.Config
			h := StartWith(t, func(cfg *compositor.Config) {
				plain, gapped = *cfg, *cfg
				gapped.WM.Gaps = wm.Gaps{Outer: wm.Insets{Top: 30, Right: 30, Bottom: 30, Left: 30}}
			}, desktop)
			caughtUp := func(_ *headless.Backend, comp *compositor.Compositor) bool {
				tops := comp.Toplevels()
				return !comp.Holding() && len(tops) > 0 && tops[0].Mapped() && (client.mayNotRedraw || tops[0].Current().Size == tops[0].Requested().Size)
			}
			h.Run(client.name, client.args...)
			h.Within(clientTimeout, client.name+" to map and draw its size", caughtUp)
			const changes = 20
			start := h.holds()
			for i := range changes {
				cfg := plain
				if i%2 == 0 {
					cfg = gapped
				}
				h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
				h.Within(clientTimeout, "the change landed, answered or past the bound", landed)
				if i%4 == 0 {
					h.Within(clientTimeout, client.name+" caught up", caughtUp)
				}
			}
			h.Within(clientTimeout, client.name+" caught up with the last change", caughtUp)
			end := h.holds()
			t.Logf("%s: %d changes, %d held, %d over the bound, %v at the longest", client.name, changes, end.Landed-start.Landed, end.Expired-start.Expired, end.Longest)
		})
	}
}

func TestClosingDuringAHoldLandsAtOnce(t *testing.T) {
	h := StartWith(t, heldForever, small)
	cl, left := open(t, h, "left", fillGreen)
	cr, _ := open(t, h, "right", fillBlue)
	cl.DispatchUntil("left halved", func() bool { return left.Logical.X == 80 })
	cl.Sync()
	h.Eventually("the layout landed", landed)
	c3, third := open(t, h, "third", fillRed)
	h.Eventually("the third window held back", holding)
	if err := errors.Join(third.Destroy(), c3.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("closing the window ended the hold", landed)
	h.stillShows("left is back", 70, 50, greenColor)
	h.stillShows("right is back", 150, 50, blueColor)
	c4, _ := open(t, h, "fourth", fillRed)
	h.Eventually("the fourth window held back", holding)
	before := h.holds()
	if err := cr.Display.Close(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the death of a client held for ended the hold", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Holds().Landed == before.Landed+1
	})
	if s := h.holds(); s.Expired != 0 {
		t.Errorf("holds %+v, want none expired", s)
	}
	c4.Sync()
}

func TestFullscreenLandsWithItsAnswer(t *testing.T) {
	h := StartWith(t, heldForever, small)
	cl, left := open(t, h, "left", fillGreen)
	cr, right := open(t, h, "right", fillBlue)
	cl.DispatchUntil("left halved", func() bool { return left.Logical.X == 80 })
	cl.Sync()
	h.Eventually("the layout landed", landed)
	h.dispatch("fullscreen")
	h.Eventually("fullscreen waits for right", holding)
	h.stillShows("left still drawn", 10, 50, greenColor)
	cr.DispatchUntil("right fullscreen", func() bool { return right.Logical == small.Size })
	cr.Sync()
	h.Eventually("the fullscreen landed", landed)
	h.stillShows("right over left", 10, 50, blueColor)
	h.dispatch("fullscreen")
	h.Eventually("leaving waits for right too", holding)
	h.stillShows("right still over left", 10, 50, blueColor)
	cr.DispatchUntil("right back", func() bool { return right.Logical.X == 80 })
	cr.Sync()
	h.Eventually("the way back landed", landed)
	h.stillShows("left back", 10, 50, greenColor)
}

func TestAWindowsLookLandsWithItsChange(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			heldForever(cfg)
			cfg.Decor.Opacity.Fullscreen = 0.5
			cfg.WM.Gaps = wm.Gaps{Outer: wm.Insets{Top: 10, Right: 10, Bottom: 10, Left: 10}}
		}, small)
		c, w := open(t, h, "fading", testclient.Fill(0xff, 0xff, 0xff))
		white := [3]byte{0xff, 0xff, 0xff}
		h.Eventually("the window opaque while tiled", showing(15, 15, white))
		h.dispatch("fullscreen")
		h.Eventually("the fullscreen held", holding)
		h.stillShows("still opaque while held", 15, 15, white)
		c.DispatchUntil("the answer", func() bool { return w.Logical == small.Size })
		c.Sync()
		h.Eventually("the fullscreen landed", landed)
		want := blend(white, [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}, 0.5)
		if got := pixel(h.Rendered(0), 15, 15); !near(got, want) {
			t.Errorf("a landed fullscreen window shows %v, want its half opacity, %v", got, want)
		}
	})
}

func TestAWorkspaceSwitchIsNotHeldBackByAFrozenClient(t *testing.T) {
	h := StartWith(t, heldForever, small)
	cl, left := open(t, h, "left", fillGreen)
	_, _ = open(t, h, "right", fillBlue)
	cl.DispatchUntil("left halved", func() bool { return left.Logical.X == 80 })
	cl.Sync()
	h.Eventually("the layout landed", landed)
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	h.dispatch("workspace 2")
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if c.Holding() {
			t.Error("a switch to windows at their sizes is held")
		}
		return nil
	})
	h.stillShows("the empty workspace", 10, 50, bg)
	h.dispatch("workspace 1")
	h.stillShows("back", 10, 50, greenColor)
	_, _ = open(t, h, "third", fillRed)
	h.Eventually("the third window held back", holding)
	h.dispatch("workspace 2")
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if c.Holding() {
			t.Error("a switch waits for the hold before it")
		}
		return nil
	})
	h.stillShows("the empty workspace at once", 10, 50, bg)
}

func TestWindowsOnAnOutputThatWentAwayAreNotHeldBack(t *testing.T) {
	h := StartWith(t, heldForever, small, small)
	var doomed string
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		doomed = comp.WM().FocusedOutput()
		return nil
	})
	_, _ = open(t, h, "left", fillGreen)
	_, _ = open(t, h, "right", fillBlue)
	h.Eventually("the second window held back", holding)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		for _, o := range b.Outputs() {
			if o.Name() == doomed {
				return b.RemoveOutput(o)
			}
		}
		t.Errorf("no output %s", doomed)
		return nil
	})
	h.Eventually("the hold over with the output gone", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return !comp.Holding() && len(b.Outputs()) == 1
	})
	h.dispatch("workspace 1")
	h.Eventually("the windows on the output that is left", showing(150, 50, blueColor))
}

func TestAnimationsStartFromTheLandingAndRetargetFromWhatIsShown(t *testing.T) {
	onEachConfiguredRenderer(t, testAnimationsStartFromTheLandingAndRetargetFromWhatIsShown)
}

func testAnimationsStartFromTheLandingAndRetargetFromWhatIsShown(t *testing.T, start gpuStarter) {
	now := time.Hour
	var gaps20, gaps40 compositor.Config
	h := start(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.PlainDecor()
		cfg.Animations = linearMoves(t, time.Second)
		cfg.Clock = func() time.Duration { return now }
		cfg.HoldBound = time.Hour
		gaps20, gaps40 = *cfg, *cfg
		gaps20.WM.Gaps = wm.Gaps{Outer: wm.Insets{Top: 20, Right: 20, Bottom: 20, Left: 20}}
		gaps40.WM.Gaps = wm.Gaps{Outer: wm.Insets{Top: 40, Right: 40, Bottom: 40, Left: 40}}
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = time.Hour + d
			return nil
		})
	}
	reconfigure := func(cfg compositor.Config) {
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(cfg) })
	}
	gray := [3]byte{0x60, 0x60, 0x60}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	c, w := open(t, h, "moved", testclient.Fill(gray[0], gray[1], gray[2]))
	h.Eventually("the window over the output", showing(5, 5, gray))

	reconfigure(gaps20)
	h.Eventually("the change held for the client", holding)
	at(400 * time.Millisecond)
	h.stillShows("nothing moves while held", 5, 5, gray)
	c.DispatchUntil("the client answers", func() bool { return w.Logical == image.Pt(118, 78) })
	c.Sync()
	h.Eventually("the change landed", landed)
	h.stillShows("the animation starts where the hold ended", 5, 5, gray)
	at(900 * time.Millisecond)
	h.Eventually("halfway a second after the landing, not the change", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(5, 5, bg)(b, comp) && showing(12, 12, gray)(b, comp)
	})

	reconfigure(gaps40)
	h.Eventually("the retarget held for the client", holding)
	at(1150 * time.Millisecond)
	h.Eventually("the first motion runs on during the hold", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(12, 12, bg)(b, comp) && showing(17, 17, gray)(b, comp)
	})
	c.DispatchUntil("the client answers again", func() bool { return w.Logical == image.Pt(78, 38) })
	c.Sync()
	h.Eventually("the retarget landed", landed)
	h.stillShows("the retarget starts from what is shown", 12, 12, bg)
	h.stillShows("the retarget starts from what is shown", 17, 17, gray)
	at(2150 * time.Millisecond)
	h.Eventually("the window at its last goal", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(38, 38, bg)(b, comp) && showing(42, 42, gray)(b, comp)
	})
}

func dragBinds(t *testing.T, more func(*compositor.Config)) func(*compositor.Config) {
	t.Helper()
	return func(cfg *compositor.Config) {
		withBinds(t, bindSpec{chord: "SUPER + mouse:273", action: "resizewindow"})(cfg)
		more(cfg)
	}
}

func (h *Harness) pointerAt(x, y int) func(b *headless.Backend) error {
	return func(b *headless.Backend) error {
		o, p := b.Outputs()[0], b.Pointer()
		return errors.Join(p.MoveTo(o, float64(x)/float64(small.Size.X), float64(y)/float64(small.Size.Y)), p.Frame())
	}
}

func TestADragResizeCoalescesItsConfiguresToTheFrames(t *testing.T) {
	h := StartWith(t, dragBinds(t, func(*compositor.Config) {}), small)
	c := testclient.Connect(t, h.Socket())
	left := c.Window("left", fillGreen)
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", fillBlue)
	c.DispatchUntil("right to map", right.Mapped)
	c.DispatchUntil("left halved", func() bool { return left.Logical.X == 80 })
	c.Sync()
	h.Eventually("the layout landed", landed)
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(h.pointerAt(40, 50)(b), b.Pointer().Button(btnRight, true), b.Pointer().Frame())
	})
	before := left.Configures
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		var errs []error
		for x := 41; x <= 70; x++ {
			errs = append(errs, h.pointerAt(x, 50)(b))
		}
		return errors.Join(errs...)
	})
	c.DispatchUntil("left grown by the whole burst", func() bool { return left.Logical.X == 110 })
	if n := left.Configures - before; n > 2 {
		t.Errorf("30 pointer motions in one frame sent %d configures", n)
	}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(h.pointerAt(75, 50)(b), b.Pointer().Button(btnRight, false), b.Pointer().Frame())
	})
	h.key(keySuper, false)
	c.DispatchUntil("the release sends the last of it", func() bool { return left.Logical.X == 115 && right.Logical.X == 45 })
	c.Sync()
	h.Eventually("the layout landed", landed)
	h.Eventually("left where the pointer left it", showing(113, 50, greenColor))
	h.Eventually("right beside it", showing(117, 50, blueColor))
}

func TestADragKeepsMovingAgainstAFrozenClient(t *testing.T) {
	h := StartWith(t, dragBinds(t, heldForever), small)
	cf, frozen := open(t, h, "frozen", fillBlue)
	c, w := open(t, h, "dragged", fillGreen)
	cf.DispatchUntil("frozen halved", func() bool { return frozen.Logical.X == 80 })
	cf.Sync()
	h.Eventually("the layout landed", landed)
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(h.pointerAt(100, 50)(b), b.Pointer().Button(btnRight, true), b.Pointer().Frame())
	})
	began := time.Now()
	for x := 95; x >= 60; x -= 5 {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return h.pointerAt(x, 50)(b) })
		want := 180 - x
		c.DispatchUntil("the dragged window at the pointer", func() bool { return w.Logical.X == want })
		c.Sync()
		h.Eventually("the step landed", landed)
		h.Eventually("the frame there, the frozen window stretched to it", showing(x-20-2, 50, blueColor))
		h.Eventually("the dragged window beside it", showing(x-20+2, 50, greenColor))
	}
	if s := h.holds(); s.Expired > 1 {
		t.Errorf("holds %+v: only the first step may wait for the frozen client", s)
	}
	t.Logf("8 steps against a frozen client took %v", time.Since(began))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnRight, false), b.Pointer().Frame())
	})
	h.key(keySuper, false)
	h.Eventually("the drag ended", landed)
	cf.DispatchUntil("the thawed client gets the last size", func() bool { return frozen.Logical.X == 80 })
	if frozen.Configures > 4 {
		t.Errorf("the frozen client was sent %d configures", frozen.Configures)
	}
}

func TestInputFlowsDuringAHold(t *testing.T) {
	h := StartWith(t, heldForever, small)
	a := join(t, h, "a")
	a.w.Answer = testclient.AnswerNever
	b := join(t, h, "b")
	h.Eventually("b held back", holding)
	b.seen(t, "the keyboard already with b", func() bool { return slices.Contains(b.in.Log, "kb_enter b") })
	h.tap(keyH)
	b.seen(t, "a key during the hold", func() bool {
		return slices.ContainsFunc(b.in.Log, func(l string) bool { return strings.HasPrefix(l, "key ") })
	})
	h.Do(func(bk *headless.Backend, _ *compositor.Compositor) error { return h.pointerAt(10, 10)(bk) })
	a.seen(t, "the pointer on the window it is over, as shown", func() bool {
		return slices.ContainsFunc(a.in.Log, func(l string) bool { return strings.HasPrefix(l, "motion ") || strings.HasPrefix(l, "enter ") })
	})
	if !h.compositorHolding() {
		t.Fatal("input ended the hold")
	}
}

func TestASynchronizedSubsurfaceLandsWithItsParent(t *testing.T) {
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, heldForever, small)
		ca, a := open(t, h, "a", fillBlue)
		a.Stop()
		child := ca.Surface()
		sub, err := ca.Subcompositor.GetSubsurface(child, a.Surface)
		if err != nil {
			t.Fatal(err)
		}
		size := image.Pt(10, 10)
		if err := errors.Join(sub.SetPosition(5, 5), child.Attach(ca.Buffer(size, 0, fillGreen), 0, 0), child.DamageBuffer(0, 0, 10, 10), child.Commit(), a.Surface.Commit(), ca.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		h.Eventually("the child drawn", showing(10, 10, greenColor))
		cb, b := open(t, h, "b", fillRed)
		ca.DispatchUntil("a halved", func() bool { return a.Logical.X == 80 })
		ca.Sync()
		h.Eventually("the layout landed", landed)
		_, _ = open(t, h, "c", fillRed)
		h.Eventually("c held back", holding)
		if err := errors.Join(child.Attach(ca.Buffer(size, 0, fillRed), 0, 0), child.DamageBuffer(0, 0, 10, 10), child.Commit(), ca.Display.Flush()); err != nil {
			t.Fatal(err)
		}
		ca.Sync()
		h.stillShows("the child waits for its parent", 10, 10, greenColor)
		h.stillShows("the parent's old frame", 70, 50, blueColor)
		cb.DispatchUntil("b answers", func() bool { return b.Logical.X < 80 })
		cb.Sync()
		h.Eventually("the change landed", landed)
		h.stillShows("the child landed with its parent", 10, 10, redColor)
		h.stillShows("the parent's new frame", 40, 50, blueColor)
		h.stillShows("b's new frame", 70, 50, redColor)
	})
}

func TestAPopupStaysWithItsWindowDuringAHold(t *testing.T) {
	h := StartWith(t, heldForever, small)
	ca, a := open(t, h, "a", fillGreen)
	cb, b := open(t, h, "b", fillBlue)
	ca.DispatchUntil("a halved", func() bool { return a.Logical.X == 80 })
	ca.Sync()
	h.Eventually("the layout landed", landed)
	seat, in := cb.Seat()
	in.Name(b.Surface, "b")
	h.click(0, 120, 50, btnLeft)
	cb.DispatchUntil("the click on b", func() bool { return slices.Contains(in.Log, "button 272 1") })
	popup := cb.Popup(b, menuAt(image.Pt(10, 10), image.Pt(20, 20)), &testclient.Grab{Seat: seat, Serial: in.Serial}, fillRed)
	cb.DispatchUntil("the popup to map", popup.Mapped)
	h.Eventually("the popup over b", showing(100, 20, redColor))
	_, _ = open(t, h, "c", fillGreen)
	h.Eventually("c held back", holding)
	h.stillShows("b's frame stands with its popup on it", 100, 20, redColor)
	ca.DispatchUntil("a answers", func() bool { return a.Logical.X < 80 })
	ca.Sync()
	cb.DispatchUntil("b answers", func() bool { return b.Logical.X < 80 })
	cb.Sync()
	h.Eventually("the change landed", landed)
	moved := b.Logical.X
	h.stillShows("the popup moved with b, not ahead of it", 100-(80-moved), 20, redColor)
}

func TestAnUnsignalledFenceKeepsAnAnswerFromLanding(t *testing.T) {
	h := StartWith(t, heldForever, small)
	c, w := open(t, h, "fenced", fillGreen)
	var fences []*surface.Hold
	var gate *surface.Hooks
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		s := comp.Toplevels()[0].Surface()
		gate = &surface.Hooks{Commit: func(u *surface.Update) error {
			fences = append(fences, u.Hold())
			return nil
		}}
		s.AddHooks(gate)
		return nil
	})
	_, _ = open(t, h, "right", fillBlue)
	c.DispatchUntil("the answer sent", func() bool { return w.Logical.X == 80 })
	c.Sync()
	h.Eventually("the fenced answer in the queue", func(*headless.Backend, *compositor.Compositor) bool { return len(fences) > 0 })
	h.stillShows("the old frame until its fence signals", 150, 50, greenColor)
	if !h.compositorHolding() {
		t.Fatal("an answer behind an unsignalled fence landed the change")
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		comp.Toplevels()[0].Surface().RemoveHooks(gate)
		var errs []error
		for _, f := range fences {
			errs = append(errs, f.Release())
		}
		return errors.Join(errs...)
	})
	h.Eventually("the signalled answer landed the change", landed)
	h.stillShows("the new frame", 150, 50, blueColor)
	h.stillShows("its own buffer", 70, 50, greenColor)
}

func TestAFenceThatNeverSignalsHoldsOthersOnlyUpToTheBound(t *testing.T) {
	h := StartWith(t, flat, small)
	c, w := open(t, h, "fenced", fillGreen)
	var fence *surface.Hold
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		var gate *surface.Hooks
		s := comp.Toplevels()[0].Surface()
		gate = &surface.Hooks{Commit: func(u *surface.Update) error {
			fence = u.Hold()
			s.RemoveHooks(gate)
			return nil
		}}
		s.AddHooks(gate)
		return nil
	})
	_, _ = open(t, h, "right", fillBlue)
	c.DispatchUntil("the answer sent", func() bool { return w.Logical.X == 80 })
	c.Sync()
	h.Eventually("the bound over", landed)
	if s := h.holds(); s.Expired != 1 {
		t.Errorf("holds %+v, want the fenced answer to have expired", s)
	}
	h.Eventually("the old buffer fills the new frame meanwhile", showing(75, 50, greenColor))
	h.Eventually("the other window landed", showing(85, 50, blueColor))
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return fence.Release() })
	h.Eventually("the signalled buffer shows, stale no more", showing(75, 50, greenColor))
}

func benchDesktop(b *testing.B, n int) *Harness {
	b.Helper()
	h := start(b, setup{open: softBackend, configure: flat}, benchMode)
	for range n {
		c := testclient.Connect(b, h.Socket())
		w := c.Window("bench", fillGreen)
		c.DispatchUntil("the window to map", w.Mapped)
		w.Stop()
	}
	return h
}

func BenchmarkFrameWithEightWindows(b *testing.B) {
	h := benchDesktop(b, 8)
	time.Sleep(300 * time.Millisecond)
	for b.Loop() {
		h.Rendered(0)
	}
}

func BenchmarkFocusChangeWithEightWindows(b *testing.B) {
	h := benchDesktop(b, 8)
	left, err := wm.ParseAction("focus left")
	if err != nil {
		b.Fatal(err)
	}
	right, err := wm.ParseAction("focus right")
	if err != nil {
		b.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	i := 0
	for b.Loop() {
		a := left
		if i%2 == 1 {
			a = right
		}
		i++
		h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.Dispatch(a) })
	}
}

func TestLayoutPathAllocations(t *testing.T) {
	h := StartWith(t, dragBinds(t, flat), desktop)
	var clients []*testclient.Client
	var windows []*testclient.Window
	for range 8 {
		c := testclient.Connect(t, h.Socket())
		w := c.Window("bench", fillGreen)
		c.DispatchUntil("the window to map", w.Mapped)
		c.Sync()
		w.Stop()
		clients, windows = append(clients, c), append(windows, w)
	}
	for i, c := range clients {
		c.DispatchUntil("the window at its size", func() bool { return windows[i].Logical.X < 200 })
		c.Sync()
	}
	h.Eventually("the layout landed", landed)
	time.Sleep(100 * time.Millisecond)
	left, err := wm.ParseAction("focus left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := wm.ParseAction("focus right")
	if err != nil {
		t.Fatal(err)
	}
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		frame := testing.AllocsPerRun(200, func() {
			if err := comp.Frame(o); err != nil {
				t.Error(err)
			}
		})
		i := 0
		focus := testing.AllocsPerRun(200, func() {
			a := left
			if i%2 == 1 {
				a = right
			}
			i++
			if err := comp.Dispatch(a); err != nil {
				t.Error(err)
			}
		})
		if frame != 0 {
			t.Errorf("an unchanged frame allocates %.1f times", frame)
		}
		t.Logf("allocations: an unchanged frame %.1f, a focus change %.1f", frame, focus)
		return nil
	})
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		return errors.Join(p.MoveTo(o, 0.1, 0.5), p.Frame(), p.Button(btnRight, true), p.Frame())
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		x := 0.1
		motion := testing.AllocsPerRun(200, func() {
			x += 0.0005
			if err := errors.Join(p.MoveTo(o, x, 0.5), p.Frame()); err != nil {
				t.Error(err)
			}
		})
		if motion > 2 {
			t.Errorf("a pointer motion during a drag allocates %.1f times; the fake pointer's own event accounts for 2", motion)
		}
		t.Logf("allocations: a pointer motion during a drag %.1f", motion)
		return errors.Join(p.Button(btnRight, false), p.Frame())
	})
	h.key(keySuper, false)
}

func BenchmarkResizeOfEightWindows(b *testing.B) {
	h := start(b, setup{open: softBackend, configure: flat}, benchMode)
	var clients []*testclient.Client
	for range 8 {
		c := testclient.Connect(b, h.Socket())
		w := c.Window("bench", fillGreen)
		c.DispatchUntil("the window to map", w.Mapped)
		c.Sync()
		w.Stop()
		clients = append(clients, c)
	}
	for _, c := range clients {
		c.Sync()
	}
	h.Eventually("the layout landed", landed)
	wider, err := wm.ParseAction("resizeactive 10 0")
	if err != nil {
		b.Fatal(err)
	}
	narrower, err := wm.ParseAction("resizeactive -10 0")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		a := wider
		if i%2 == 1 {
			a = narrower
		}
		i++
		before := h.holds().Landed
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Dispatch(a) })
		for _, c := range clients {
			c.Sync()
		}
		h.Eventually("the resize landed", func(_ *headless.Backend, comp *compositor.Compositor) bool {
			return !comp.Holding() && comp.Holds().Landed > before
		})
	}
}

func BenchmarkDragResizeStep(b *testing.B) {
	h := start(b, setup{open: softBackend, configure: func(cfg *compositor.Config) {
		flat(cfg)
	}}, benchMode)
	c := testclient.Connect(b, h.Socket())
	left := c.Window("left", fillGreen)
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", fillBlue)
	c.DispatchUntil("right to map", right.Mapped)
	c.DispatchUntil("left halved", func() bool { return left.Logical.X < 1000 })
	c.Sync()
	left.Stop()
	right.Stop()
	h.Eventually("the layout landed", landed)
	wider, err := wm.ParseAction("resizeactive 2 0")
	if err != nil {
		b.Fatal(err)
	}
	narrower, err := wm.ParseAction("resizeactive -2 0")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		a := wider
		if i%2 == 1 {
			a = narrower
		}
		i++
		landedBefore := h.holds().Landed
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Dispatch(a) })
		c.DispatchUntil("the answers", func() bool {
			var done bool
			h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
				done = !comp.Holding() && comp.Holds().Landed > landedBefore
				return nil
			})
			return done
		})
	}
}

func TestALongDragEndsWhereThePointerDoes(t *testing.T) {
	h := StartWith(t, dragBinds(t, heldForever), small)
	c := testclient.Connect(t, h.Socket())
	left := c.Window("left", fillGreen)
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", fillBlue)
	c.DispatchUntil("right to map", right.Mapped)
	c.DispatchUntil("left halved", func() bool { return left.Logical.X == 80 })
	c.Sync()
	left.Stop()
	right.Stop()
	h.Eventually("the layout landed", landed)
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(h.pointerAt(60, 50)(b), b.Pointer().Button(btnRight, true), b.Pointer().Frame())
	})
	const steps = 300
	for i := range steps {
		x := 60 + i%40
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return h.pointerAt(x, 50)(b) })
		want := 80 + i%40
		c.DispatchUntil("the step answered", func() bool {
			var done bool
			h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
				done = left.Logical.X == want && !comp.Holding()
				return nil
			})
			return done
		})
	}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnRight, false), b.Pointer().Frame())
	})
	h.key(keySuper, false)
	h.Eventually("the drag ended", landed)
	last := 80 + (steps-1)%40
	c.DispatchUntil("the client at the last size", func() bool { return left.Logical.X == last && right.Logical.X == 160-last })
	c.Sync()
	h.Eventually("the last change landed", landed)
	h.Eventually("left where the pointer left it", showing(last-2, 50, greenColor))
	h.Eventually("right beside it", showing(last+2, 50, blueColor))
}

func TestDragFrameAllocations(t *testing.T) {
	h := start(t, setup{open: softBackend, configure: func(cfg *compositor.Config) {
		flat(cfg)
		withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"})(cfg)
	}}, desktop)
	var last *testclient.Window
	var clients []*testclient.Client
	for range 8 {
		c := testclient.Connect(t, h.Socket())
		last = c.Window("bench", fillGreen)
		c.DispatchUntil("the window to map", last.Mapped)
		c.Sync()
		last.Stop()
		clients = append(clients, c)
	}
	for _, c := range clients {
		c.Sync()
	}
	h.Eventually("the layout landed", landed)
	wider, err := wm.ParseAction("resizeactive 2 0")
	if err != nil {
		t.Fatal(err)
	}
	narrower, err := wm.ParseAction("resizeactive -2 0")
	if err != nil {
		t.Fatal(err)
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		i := 0
		tiled := testing.AllocsPerRun(200, func() {
			a := wider
			if i%2 == 1 {
				a = narrower
			}
			i++
			if err := comp.Dispatch(a); err != nil {
				t.Error(err)
			}
		})
		if tiled > 3 {
			t.Errorf("a tiled resize allocates %.1f times, want the workspace list and the group resized", tiled)
		}
		t.Logf("allocations: a tiled resize, 8 windows, %.1f", tiled)
		return nil
	})
	for _, c := range clients {
		c.Sync()
	}
	h.Eventually("the layout landed", landed)
	h.dispatch("togglefloating")
	clients[len(clients)-1].DispatchUntil("a floating window", func() bool { return last.Logical.X != 160 && last.Logical.Y != desktop.Size.Y })
	for _, c := range clients {
		c.Sync()
	}
	h.Eventually("the layout landed", landed)
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.Button(btnLeft, true), p.Frame())
	})
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		x := 0.5
		floating := testing.AllocsPerRun(200, func() {
			x += 0.0005
			if err := errors.Join(p.MoveTo(o, x, 0.5), p.Frame(), comp.Frame(o)); err != nil {
				t.Error(err)
			}
		})
		if floating > 4 {
			t.Errorf("a frame of a floating drag allocates %.1f times; 2 are the fake pointer's event, 2 the workspace and floating lists", floating)
		}
		t.Logf("allocations: a frame of a floating drag, 8 windows, %.1f", floating)
		return errors.Join(p.Button(btnLeft, false), p.Frame())
	})
	h.key(keySuper, false)
}
