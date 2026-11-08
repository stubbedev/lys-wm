package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cxd "github.com/stubbedev/lys-wm/internal/protocol/client/xdgdecoration"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const (
	barHeight = 16
	barMargin = barHeight / 12
	barSlot   = barHeight * 5 / 6
)

const (
	closeButton = iota
	minimizeButton
	maximizeButton
)

var surfaceColor = [3]byte{0x1c, 0x1c, 0x2c}

var barScreen = backend.Mode{Size: image.Pt(320, 200), RefreshMHz: 60000, Preferred: true}

func titlebars(cfg *compositor.Config) {
	cfg.Decor.Titlebar.Floating, cfg.Decor.Titlebar.Height = true, barHeight
}

type decorationEvents struct{}

func (decorationEvents) Configure(*cxd.ZxdgToplevelDecorationV1, cxd.ZxdgToplevelDecorationV1Mode) error {
	return nil
}

func ssdWindow(c *testclient.Client, title string, paint testclient.Painter) *testclient.Window {
	return c.WindowWith(title, paint, func(w *testclient.Window) error {
		m, err := cxd.NewZxdgDecorationManagerV1(c.Display, 1)
		if err != nil {
			return err
		}
		c.Bind(m)
		_, err = m.GetToplevelDecoration(decorationEvents{}, w.Toplevel)
		return err
	})
}

type barScene struct {
	h *Harness
	c *testclient.Client
	w *testclient.Window
}

func floatingBar(t *testing.T, start gpuStarter) *barScene {
	t.Helper()
	h := start(t, titlebars, barScreen)
	c := testclient.Connect(t, h.Socket())
	w := ssdWindow(c, "Editor", testclient.Fill(0x20, 0x40, 0x60))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	win := query[ipc.Window](h, wmipc.KindActiveWindow)
	c.DispatchUntil("the floating size", func() bool { return w.Logical == image.Pt(win.Rect.Width, win.Rect.Height) })
	quiesce(t, h, c, w)
	return &barScene{h: h, c: c, w: w}
}

func (s *barScene) floatAgain() {
	s.h.t.Helper()
	s.h.dispatch("maximize off")
	win := s.window()
	s.c.DispatchUntil("the floating size again", func() bool { return s.w.Logical == image.Pt(win.Rect.Width, win.Rect.Height) })
	settle(s.h.t, s.h)
}

func (s *barScene) window() ipc.Window {
	return query[ipc.Window](s.h, wmipc.KindActiveWindow)
}

func (s *barScene) bar() image.Rectangle {
	r := s.window().Rect
	return image.Rect(r.X, r.Y-barHeight, r.X+r.Width, r.Y)
}

func (s *barScene) button(i int) image.Point {
	bar := s.bar()
	return image.Pt(bar.Min.X+barMargin+i*barSlot+barSlot/2, bar.Min.Y+barHeight/2)
}

func (s *barScene) title() image.Point {
	bar := s.bar()
	return image.Pt(bar.Max.X-barMargin-2, bar.Min.Y+barHeight/2)
}

func (s *barScene) click(at image.Point, button uint32) {
	s.h.click(0, float64(at.X), float64(at.Y), button)
}

func softStart(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	return StartWith(t, configure, modes...)
}

func TestAFloatingServerDecoratedWindowHasATitlebarAndAClientDecoratedOneNone(t *testing.T) {
	t.Parallel()
	s := floatingBar(t, softStart)
	win := s.window()
	if got := s.w.Logical; got.Y != win.Rect.Height {
		t.Fatalf("the client is %v inside a window of %+v", got, win.Rect)
	}
	frame := s.h.Rendered(0)
	bar := s.bar()
	if got := pixel(frame, bar.Min.X+1, bar.Min.Y+1); got != surfaceColor {
		t.Fatalf("the focused window's titlebar shows %v, want the surface colour", got)
	}

	csd := s.c.Window("Shell", testclient.Fill(0x60, 0x40, 0x20))
	s.c.DispatchUntil("the CSD window to map", csd.Mapped)
	s.h.dispatch("togglefloating")
	plain := query[ipc.Window](s.h, wmipc.KindActiveWindow)
	s.c.DispatchUntil("the CSD floating size", func() bool { return csd.Logical == image.Pt(plain.Rect.Width, plain.Rect.Height) })
	quiesce(t, s.h, s.c, csd)
	if got := pixel(s.h.Rendered(0), plain.Rect.X+2, plain.Rect.Y+2); got != [3]byte{0x60, 0x40, 0x20} {
		t.Fatalf("the top of a client-decorated floating window shows %v, want the client's own pixels", got)
	}
}

func TestTheTitlebarMovesClosesMinimizesAndMaximizes(t *testing.T) {
	t.Parallel()
	s := floatingBar(t, softStart)
	before := s.window().Rect
	title := s.title()
	s.h.movePointer(title)
	s.h.button(true)
	s.h.movePointer(title.Add(image.Pt(30, 20)))
	s.h.button(false)
	s.h.Eventually("the drag to land", func(_ *headless.Backend, c *compositor.Compositor) bool { return !c.Dragging() })
	if got := s.window().Rect; got.X != before.X+30 || got.Y-before.Y < 19 || got.Y-before.Y > 21 || got.Width != before.Width {
		t.Fatalf("dragging the titlebar by 30,20 moved the window from %+v to %+v", before, got)
	}

	s.click(s.button(maximizeButton), btnLeft)
	s.h.Eventually("maximized", func(_ *headless.Backend, c *compositor.Compositor) bool { return windowsOf(c)[0].Maximized })
	s.floatAgain()

	title = s.title()
	s.click(title, btnLeft)
	s.click(title, btnLeft)
	s.h.Eventually("a double click to maximize", func(_ *headless.Backend, c *compositor.Compositor) bool { return windowsOf(c)[0].Maximized })
	s.floatAgain()

	lines := s.h.watch()
	menuAt := image.Pt(s.title().X, s.bar().Min.Y+1)
	s.h.Eventually("the bar drawn where the menu click goes", func(b *headless.Backend, _ *compositor.Compositor) bool {
		snap, err := b.Outputs()[0].Snapshot()
		return err == nil && pixel(snap, menuAt.X, menuAt.Y) == surfaceColor
	})
	s.click(menuAt, btnRight)
	awaitStreamed(t, lines, "the window menu", func(e streamed) bool { return e.Event == "windowmenu" })

	s.click(s.button(minimizeButton), btnLeft)
	s.h.Eventually("minimized", func(_ *headless.Backend, c *compositor.Compositor) bool { return windowsOf(c)[0].Minimized })
	s.h.dispatch("restore")
	s.h.Eventually("restored", func(_ *headless.Backend, c *compositor.Compositor) bool { return !windowsOf(c)[0].Minimized })
	s.c.DispatchUntil("the restored window drawn", func() bool { return true })

	s.click(s.button(closeButton), btnLeft)
	s.c.DispatchUntil("the close request", func() bool { return s.w.Closed })
}

func TestTheButtonsFollowTheConfiguredSide(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		titlebars(cfg)
		cfg.Decor.Titlebar.Side = compositor.TitlebarRight
	}, barScreen)
	c := testclient.Connect(t, h.Socket())
	w := ssdWindow(c, "Editor", testclient.Fill(0x20, 0x40, 0x60))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	win := query[ipc.Window](h, wmipc.KindActiveWindow)
	c.DispatchUntil("the floating size", func() bool { return w.Logical == image.Pt(win.Rect.Width, win.Rect.Height) })
	quiesce(t, h, c, w)
	right := win.Rect.X + win.Rect.Width
	h.click(0, float64(right-barMargin-3*barSlot+barSlot/2), float64(win.Rect.Y-barHeight/2), btnLeft)
	c.DispatchUntil("the close request from the first button on the right", func() bool { return w.Closed })
}

func TestATitleChangeRedrawsTheTitlebar(t *testing.T) {
	t.Parallel()
	s := floatingBar(t, softStart)
	before := s.h.Rendered(0)
	bar := s.bar()
	if err := s.w.Toplevel.SetTitle("A much longer title"); err != nil {
		t.Fatal(err)
	}
	if err := s.c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.h.Eventually("the new title drawn", func(b *headless.Backend, c *compositor.Compositor) bool {
		snap, err := b.Outputs()[0].Snapshot()
		if err != nil || c.LabelsPending() {
			return false
		}
		for y := bar.Min.Y; y < bar.Max.Y; y++ {
			for x := bar.Min.X; x < bar.Max.X; x++ {
				if pixel(snap, x, y) != pixel(before, x, y) {
					return true
				}
			}
		}
		return false
	})
}

func TestASteadyTitlebarRastersNothingAgain(t *testing.T) {
	t.Parallel()
	h := StartWith(t, titlebars, barScreen)
	c := testclient.Connect(t, h.Socket())
	w := ssdWindow(c, "Busy", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	win := query[ipc.Window](h, wmipc.KindActiveWindow)
	c.DispatchUntil("the floating size", func() bool { return w.Logical == image.Pt(win.Rect.Width, win.Rect.Height) })
	settle(t, h)
	var landed uint64
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		landed = comp.Landed()
		return nil
	})
	from := w.Frames
	c.DispatchUntil("ten more frames", func() bool { return w.Frames >= from+10 })
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if n := comp.Landed() - landed; n != 0 {
			t.Errorf("a titlebar over a window that only redraws its content rastered %d labels", n)
		}
		return nil
	})
}

func titlebarGoldens(t *testing.T, start gpuStarter) {
	s := floatingBar(t, start)
	s.h.assertGolden("titlebar-active")
	s.h.movePointer(s.button(closeButton))
	s.h.assertGolden("titlebar-hover")
	other := s.c.Window("Other", testclient.Fill(0, 0, 0))
	s.c.DispatchUntil("another window to map", other.Mapped)
	quiesce(t, s.h, s.c, other)
	s.h.movePointer(image.Pt(barScreen.Size.X-2, barScreen.Size.Y-2))
	if err := errors.Join(s.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	s.h.assertGolden("titlebar-inactive")
}

func TestTitlebarGoldensOnTheSoftRenderer(t *testing.T) {
	t.Parallel()
	titlebarGoldens(t, softStart)
}

func TestTitlebarGoldensOnTheGPURenderers(t *testing.T) {
	onEachGPURenderer(t, titlebarGoldens)
}
