package harness

import (
	"bytes"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwlr "github.com/stubbedev/lys-wm/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func linearMoves(t *testing.T, d time.Duration) anim.Table {
	t.Helper()
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.WindowsMove: {Enabled: &on, Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return table
}

func TestAMovedWindowAnimatesOnTheFrameClock(t *testing.T) {
	now := time.Hour
	var next compositor.Config
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor = compositor.DefaultDecor()
		cfg.Animations = linearMoves(t, time.Second)
		cfg.Clock = func() time.Duration { return now }
		next = *cfg
		next.WM.Gaps = wm.Gaps{Outer: wm.Insets{Top: 20, Right: 20, Bottom: 20, Left: 20}}
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = time.Hour + d
			return nil
		})
	}
	c := testclient.Connect(t, h.Socket())
	gray := [3]byte{0x60, 0x60, 0x60}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	w := c.Window("moved", testclient.Fill(gray[0], gray[1], gray[2]))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window over the whole output", showing(5, 5, gray))

	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(next) })
	c.DispatchUntil("the goal size, configured at once", func() bool { return w.Logical == image.Pt(118, 78) })
	h.Eventually("the window still covering its start", showing(5, 5, gray))

	at(500 * time.Millisecond)
	h.Eventually("the window halfway in", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(5, 5, bg)(b, comp) && showing(12, 12, gray)(b, comp)
	})
	var damage image.Rectangle
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		for _, r := range b.Outputs()[0].LastDamage() {
			damage = damage.Union(r)
		}
		return nil
	})
	if damage.Empty() || !damage.In(image.Rect(0, 0, 160, 120)) || !image.Pt(5, 5).In(damage) {
		t.Fatalf("the halfway frame repainted %v, want the strip the window left", damage)
	}

	at(time.Second)
	h.Eventually("the window at its goal", func(b *headless.Backend, comp *compositor.Compositor) bool {
		return showing(12, 12, bg)(b, comp) && showing(22, 22, gray)(b, comp)
	})
	var before uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		before = b.Outputs()[0].Frames()
		return nil
	})
	time.Sleep(200 * time.Millisecond)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if after := b.Outputs()[0].Frames(); after != before {
			t.Errorf("%d frames ran after the animation ended", after-before)
		}
		return nil
	})
}

func TestADraggedWindowFollowsThePointerWithoutAnimating(t *testing.T) {
	now := time.Hour
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = linearMoves(t, time.Hour)
		cfg.Clock = func() time.Duration { return now }
		withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"})(cfg)
	}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("dragged", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("a floating window", func() bool { return w.Logical == image.Pt(80, 50) })
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		now = 3 * time.Hour
		return nil
	})
	h.Eventually("the floating window settled in the middle", showing(45, 30, blueColor))
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.Button(btnLeft, true), p.Frame(), p.MoveTo(o, 100.0/160, 0.6), p.Frame())
	})
	h.Eventually("the window under the pointer while the clock stands still", showing(135, 80, blueColor))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Button(btnLeft, false), p.Frame())
	})
	h.key(keySuper, false)
}

func TestWindowsPopInAndOutWhileTheyFade(t *testing.T) {
	now := time.Hour
	d := time.Second
	half := anim.Style{Kind: anim.Popin, Percent: 50}
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{
		anim.Global:     {Shape: anim.Linear, Duration: &d},
		anim.WindowsIn:  {Style: &half},
		anim.WindowsOut: {Style: &half},
	})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = time.Hour + d
			return nil
		})
	}
	white := [3]byte{0xff, 0xff, 0xff}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	showingNear := func(x, y int, want [3]byte) func(*headless.Backend, *compositor.Compositor) bool {
		return func(b *headless.Backend, _ *compositor.Compositor) bool {
			return near(pixel(snapshot(b.Outputs()[0]), x, y), want)
		}
	}
	both := func(a, b func(*headless.Backend, *compositor.Compositor) bool) func(*headless.Backend, *compositor.Compositor) bool {
		return func(x *headless.Backend, c *compositor.Compositor) bool { return a(x, c) && b(x, c) }
	}
	c := testclient.Connect(t, h.Socket())
	w := c.Window("popping", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("nothing drawn at the start of the open", both(showingNear(25, 20, bg), showingNear(100, 80, bg)))
	at(500 * time.Millisecond)
	h.Eventually("the window three quarters grown and half faded in", both(showingNear(25, 20, blend(white, bg, 0.5)), showingNear(5, 5, bg)))
	at(time.Second)
	h.Eventually("the window open and opaque", showingNear(5, 5, white))

	at(2 * time.Second)
	open := h.Rendered(0)
	if err := errors.Join(w.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the closed window still shown at its start", showingNear(5, 5, white))
	if closed := h.Rendered(0); !bytes.Equal(closed.Pix, open.Pix) {
		t.Fatal("the closing window's snapshot does not match the window it replaced pixel for pixel")
	}
	at(2500 * time.Millisecond)
	h.Eventually("the closing window three quarters shrunk and half faded out", both(showingNear(25, 20, blend(white, bg, 0.5)), showingNear(5, 5, bg)))
	at(3 * time.Second)
	h.Eventually("the closed window gone", showingNear(100, 80, bg))
	var before uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		before = b.Outputs()[0].Frames()
		return nil
	})
	time.Sleep(200 * time.Millisecond)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if after := b.Outputs()[0].Frames(); after != before {
			t.Errorf("%d frames ran after the close animation ended", after-before)
		}
		return nil
	})
}

func TestAWorkspaceSwitchSlidesBothWorkspaces(t *testing.T) {
	now := time.Hour
	d := time.Second
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Workspaces: {Enabled: &on, Shape: anim.Linear, Duration: &d}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	red, blue := [3]byte{0xff, 0, 0}, [3]byte{0, 0, 0xff}
	c := testclient.Connect(t, h.Socket())
	r := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", r.Mapped)
	h.dispatch("workspace 2")
	at(2 * time.Hour)
	b := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", b.Mapped)
	h.Eventually("the second workspace shown", showing(20, 60, blue))

	at(3 * time.Hour)
	h.dispatch("workspace 1")
	h.Eventually("the switch starting from the workspace it leaves", showing(20, 60, blue))
	at(3*time.Hour + 500*time.Millisecond)
	h.Eventually("both workspaces halfway", func(x *headless.Backend, comp *compositor.Compositor) bool {
		return showing(20, 60, red)(x, comp) && showing(140, 60, blue)(x, comp)
	})
	at(3*time.Hour + time.Second)
	h.Eventually("the first workspace in place", showing(140, 60, red))
	var before uint64
	h.Do(func(x *headless.Backend, _ *compositor.Compositor) error {
		before = x.Outputs()[0].Frames()
		return nil
	})
	time.Sleep(200 * time.Millisecond)
	h.Do(func(x *headless.Backend, _ *compositor.Compositor) error {
		if n := x.Outputs()[0].Frames(); n != before {
			t.Errorf("%d frames ran after the switch ended", n-before)
		}
		return nil
	})
}

func TestLayerSurfacesSlideInAndFadeOut(t *testing.T) {
	now := time.Hour
	d := time.Second
	off, on := false, true
	fade := anim.Style{Kind: anim.FadeStyle}
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{
		anim.Global:    {Enabled: &off},
		anim.Layers:    {Enabled: &on, Shape: anim.Linear, Duration: &d},
		anim.LayersOut: {Style: &fade},
	})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = time.Hour + d
			return nil
		})
	}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	shell := testclient.Connect(t, h.Socket())
	bar := shell.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	shell.DispatchUntil("the bar to map", bar.Mapped)
	h.Eventually("the bar above the output at the start", showing(80, 5, bg))
	at(500 * time.Millisecond)
	h.Eventually("the bar halfway in from the top", func(b *headless.Backend, c *compositor.Compositor) bool {
		return showing(80, 5, barColor)(b, c) && showing(80, 15, bg)(b, c)
	})
	at(time.Second)
	h.Eventually("the bar in place", showing(80, 15, barColor))

	at(2 * time.Second)
	if err := errors.Join(bar.Destroy(), shell.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	at(2500 * time.Millisecond)
	h.Eventually("the closed bar half faded where it was", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return near(pixel(snapshot(b.Outputs()[0]), 80, 15), blend(barColor, bg, 0.5))
	})
	at(3 * time.Second)
	h.Eventually("the closed bar gone", showing(80, 15, bg))
}

func TestAFocusChangeFadesTheOpacityOfBothWindows(t *testing.T) {
	now := time.Hour
	d := time.Second
	off, on := false, true
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Fade: {Enabled: &on, Shape: anim.Linear, Duration: &d}, anim.FadeIn: {Enabled: &off}, anim.FadeOut: {Enabled: &off}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
		cfg.Decor.Opacity = compositor.Opacity{Active: 1, Inactive: 0.5, Fullscreen: 1}
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	white := [3]byte{0xff, 0xff, 0xff}
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	look := func(lx, rx float64) func(*headless.Backend, *compositor.Compositor) bool {
		return func(b *headless.Backend, _ *compositor.Compositor) bool {
			buf := snapshot(b.Outputs()[0])
			return near(pixel(buf, 20, 100), blend(white, bg, lx)) && near(pixel(buf, 140, 100), blend(white, bg, rx))
		}
	}
	c := testclient.Connect(t, h.Socket())
	l := c.Window("left", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("left to map", l.Mapped)
	r := c.Window("right", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("right to map", r.Mapped)
	c.DispatchUntil("both windows drawn at their tiles", func() bool { return l.Logical == image.Pt(80, 120) && r.Logical == image.Pt(80, 120) })
	at(2 * time.Hour)
	h.Eventually("left faded to inactive, right focused", look(0.5, 1))
	at(3 * time.Hour)
	h.dispatch("focus left")
	h.Eventually("the focus change starting from what was shown", look(0.5, 1))
	at(3*time.Hour + 500*time.Millisecond)
	h.Eventually("both halfway", look(0.75, 0.75))
	at(3*time.Hour + time.Second)
	h.Eventually("left focused, right inactive", look(1, 0.5))
}

func TestBorderColoursFadeAndGradientsTurnOnlyTheRing(t *testing.T) {
	now := time.Hour
	d := time.Second
	off, on := false, true
	loop := anim.Style{Kind: anim.Loop}
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{
		anim.Global:      {Enabled: &off},
		anim.Border:      {Enabled: &on, Shape: anim.Linear, Duration: &d},
		anim.BorderAngle: {Enabled: &on, Shape: anim.Linear, Duration: &d, Style: &loop},
	})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	red, blue := render.Color{R: 0xff, A: 0xff}, render.Color{B: 0xff, A: 0xff}
	gradient, err := compositor.GradientBorder([]render.Color{red, blue}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var next compositor.Config
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
		cfg.Decor = compositor.DefaultDecor()
		cfg.Decor.BorderWidth = 4
		cfg.Decor.ActiveBorder, cfg.Decor.InactiveBorder = compositor.SolidBorder(red), compositor.SolidBorder(blue)
		next = *cfg
		next.Decor.ActiveBorder = gradient
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	c := testclient.Connect(t, h.Socket())
	l := c.Window("left", testclient.Fill(0x30, 0x30, 0x30))
	c.DispatchUntil("left to map", l.Mapped)
	r := c.Window("right", testclient.Fill(0x30, 0x30, 0x30))
	c.DispatchUntil("right to map", r.Mapped)
	at(2 * time.Hour)
	h.Eventually("the left border inactive", showing(1, 100, [3]byte{0, 0, 0xff}))
	at(3 * time.Hour)
	h.dispatch("focus left")
	at(3*time.Hour + 500*time.Millisecond)
	h.Eventually("the left border halfway from blue to red", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return near(pixel(snapshot(b.Outputs()[0]), 1, 100), [3]byte{0x80, 0, 0x7f})
	})
	at(4 * time.Hour)
	h.Eventually("the left border red", showing(1, 100, [3]byte{0xff, 0, 0}))

	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error { return comp.Reconfigure(next) })
	at(5 * time.Hour)
	h.Eventually("the gradient border drawn", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return pixel(snapshot(b.Outputs()[0]), 1, 100) != [3]byte{0xff, 0, 0}
	})
	at(5*time.Hour + 250*time.Millisecond)
	inner := image.Rect(4, 4, 76, 116)
	h.Eventually("the turned gradient repainting the ring alone", func(b *headless.Backend, _ *compositor.Compositor) bool {
		damage := b.Outputs()[0].LastDamage()
		if len(damage) == 0 {
			return false
		}
		for _, d := range damage {
			if d.Intersect(inner.Inset(1)).Dx() > 2 && d.Intersect(inner.Inset(1)).Dy() > 2 {
				return false
			}
		}
		return true
	})
}

func TestASpringDraggedWindowTrailsThePointer(t *testing.T) {
	now := time.Hour
	off, on := false, true
	s, err := anim.NewSpring(300, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.WindowsMove: {Enabled: &on, Shape: s}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
		withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"})(cfg)
	}, small)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	c := testclient.Connect(t, h.Socket())
	w := c.Window("dragged", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("a floating window", func() bool { return w.Logical == image.Pt(80, 50) })
	at(2 * time.Hour)
	h.Eventually("the floating window settled in the middle", showing(45, 30, blueColor))
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Pointer(), b.Outputs()[0]
		return errors.Join(o.MoveTo(p, 0.5, 0.5), o.Frame(), o.Button(btnLeft, true), o.Frame(), o.MoveTo(p, 100.0/160, 0.6), o.Frame())
	})
	h.Eventually("the window still where the drag began while the clock stands still", func(b *headless.Backend, comp *compositor.Compositor) bool {
		for _, win := range comp.Snapshot().Windows {
			if win.Title == "dragged" && win.Rect.X == 60 {
				return showing(45, 30, blueColor)(b, comp) && !showing(135, 80, blueColor)(b, comp)
			}
		}
		return false
	})
	at(3 * time.Hour)
	h.Eventually("the window caught up with the pointer", showing(135, 80, blueColor))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Button(btnLeft, false), p.Frame())
	})
	h.key(keySuper, false)
}

func TestAMorphingMoveStretchesTheWindowAheadOfItself(t *testing.T) {
	now := time.Hour
	d := time.Second
	off, on := false, true
	morph := anim.Style{Kind: anim.Morph}
	table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.WindowsMove: {Enabled: &on, Shape: anim.Linear, Duration: &d, Style: &morph}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Animations = table
		cfg.Clock = func() time.Duration { return now }
		cfg.Decor = compositor.DefaultDecor()
		cfg.Decor.BorderWidth = 0
	}, tall)
	at := func(d time.Duration) {
		h.Do(func(*headless.Backend, *compositor.Compositor) error {
			now = d
			return nil
		})
	}
	c := testclient.Connect(t, h.Socket())
	l := c.Window("left", testclient.Fill(0x60, 0x60, 0x60))
	c.DispatchUntil("left to map", l.Mapped)
	r := c.Window("right", testclient.Fill(0xff, 0xff, 0xff))
	c.DispatchUntil("right to map", r.Mapped)
	at(2 * time.Hour)
	h.Eventually("the windows side by side", showing(140, 100, [3]byte{0xff, 0xff, 0xff}))
	at(3 * time.Hour)
	h.dispatch("move left")
	at(3*time.Hour + 500*time.Millisecond)
	mauve := compositor.DefaultDecor().ActiveBorder.Colors()[0]
	h.Eventually("the blob reaching ahead of the moving window", showing(37, 100, [3]byte{mauve.R, mauve.G, mauve.B}))
	at(4 * time.Hour)
	h.Eventually("the blob gone once the move ends", showing(37, 100, [3]byte{0xff, 0xff, 0xff}))
}
