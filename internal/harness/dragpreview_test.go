package harness

import (
	"errors"
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	previewBackground = [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	mauveBorder       = [3]byte{0xcb, 0xa6, 0xf7}
)

func holdDrag(t *testing.T, h *Harness, from, to image.Point) {
	t.Helper()
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		size := small.Size
		return errors.Join(
			p.MoveTo(o, float64(from.X)/float64(size.X), float64(from.Y)/float64(size.Y)), p.Frame(),
			p.Button(btnLeft, true), p.Frame(),
			p.MoveTo(o, float64(to.X)/float64(size.X), float64(to.Y)/float64(size.Y)), p.Frame(),
		)
	})
}

func dragTo(h *Harness, to image.Point) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		size := small.Size
		return errors.Join(p.MoveTo(o, float64(to.X)/float64(size.X), float64(to.Y)/float64(size.Y)), p.Frame())
	})
}

func releaseDrag(h *Harness) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Button(btnLeft, false), p.Frame())
	})
	h.key(keySuper, false)
}

func dragConfig(t *testing.T, cfg *compositor.Config) {
	t.Helper()
	withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"})(cfg)
	cfg.WM.Gaps = wm.Gaps{}
	cfg.Decor.BorderWidth = 0
}

func floatingForDrag(t *testing.T, h *Harness) (*testclient.Client, *testclient.Window) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	w := c.Window("dragged", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("a floating window", func() bool { return w.Logical == image.Pt(80, 50) })
	h.Eventually("the window in the middle", showing(45, 30, redColor))
	return c, w
}

func TestAFloatingWindowDraggedToAnEdgePreviewsAndSnapsOnEveryRenderer(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) { dragConfig(t, cfg) }, small)
		c, w := floatingForDrag(t, h)
		holdDrag(t, h, image.Pt(80, 50), image.Pt(100, 60))
		h.Eventually("the window under the pointer", showing(135, 80, redColor))
		if got := pixel(h.Rendered(0), 10, 10); got != previewBackground {
			t.Errorf("a preview with no snap in reach: %v", got)
		}
		dragTo(h, image.Pt(4, 50))
		h.Eventually("a translucent preview on the left half, its border around it", func(b *headless.Backend, comp *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			fill, edge := pixel(snap, 60, 10), pixel(snap, 78, 10)
			return fill != previewBackground && fill != edge && pixel(snap, 120, 10) == previewBackground && edge == mauveBorder
		})
		releaseDrag(h)
		c.DispatchUntil("the window on the left half", func() bool { return w.Logical == image.Pt(80, 100) })
		h.Eventually("the window drawn there and the preview gone", func(b *headless.Backend, _ *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			return pixel(snap, 60, 10) == redColor && pixel(snap, 120, 10) == previewBackground
		})
		settle(t, h)
	})
}

func TestATiledWindowDraggedPreviewsWhereItDrops(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) { dragConfig(t, cfg) }, small)
		c := testclient.Connect(t, h.Socket())
		left := c.Window("left", testclient.Fill(0, 0, 0xff))
		c.DispatchUntil("left to map", left.Mapped)
		right := c.Window("right", testclient.Fill(0xff, 0, 0))
		c.DispatchUntil("right to map", right.Mapped)
		h.Eventually("both tiled", func(b *headless.Backend, comp *compositor.Compositor) bool {
			return showing(10, 50, redColor)(b, comp) && showing(150, 50, blueColor)(b, comp)
		})
		var id wm.WindowID
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			id = comp.Snapshot().Windows[0].ID
			return nil
		})
		holdDrag(t, h, image.Pt(40, 50), image.Pt(42, 50))
		if got := pixel(h.Rendered(0), 10, 50); got != redColor {
			t.Errorf("a preview over the place the window already has: %v", got)
		}
		var want image.Rectangle
		dragTo(h, image.Pt(150, 90))
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			var err error
			want, err = comp.WM().DropTarget(id, image.Pt(150, 90), compositor.PlainSettings().Autotile, wm.Spacing{TabBar: compositor.PlainDecor().Tab.Height + compositor.PlainDecor().Tab.Padding})
			return err
		})
		inside := want.Min.Add(want.Size().Div(2))
		h.Eventually("a tint over the drop target, the window still in its tile", func(b *headless.Backend, comp *compositor.Compositor) bool {
			snap := snapshot(b.Outputs()[0])
			got := pixel(snap, inside.X, inside.Y)
			return got != blueColor && got[2] > 0x80 && got[0] > 0x10 && pixel(snap, 10, 50) == redColor
		})
		releaseDrag(h)
		h.Eventually("the window where the preview said", showing(inside.X, inside.Y, redColor))
		settle(t, h)
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			for _, w := range comp.Snapshot().Windows {
				if w.ID == id && image.Rect(w.Rect.X, w.Rect.Y, w.Rect.X+w.Rect.Width, w.Rect.Y+w.Rect.Height) != want {
					t.Errorf("landed on %+v, the preview showed %v", w.Rect, want)
				}
			}
			return nil
		})
	})
}

func TestThePreviewCanBeTurnedOffAndThenNothingSnaps(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) {
		dragConfig(t, cfg)
		cfg.Decor.DragPreview.Enabled = false
	}, small)
	c, w := floatingForDrag(t, h)
	holdDrag(t, h, image.Pt(80, 50), image.Pt(4, 50))
	settle(t, h)
	if got := pixel(h.Rendered(0), 60, 10); got != previewBackground {
		t.Errorf("a preview while off: %v", got)
	}
	releaseDrag(h)
	settle(t, h)
	c.Sync()
	if w.Logical != image.Pt(80, 50) {
		t.Errorf("the window snapped to %v", w.Logical)
	}
	if got := pixel(h.Rendered(0), 60, 10); got != previewBackground {
		t.Errorf("something on the left half: %v", got)
	}
}

func TestThePreviewBlursWhatIsBehindItOnEveryRenderer(t *testing.T) {
	t.Parallel()
	onEachConfiguredRenderer(t, func(t *testing.T, start gpuStarter) {
		h := start(t, func(cfg *compositor.Config) {
			dragConfig(t, cfg)
			cfg.Decor.DragPreview.Blur = true
			cfg.Decor.Blur.Size, cfg.Decor.Blur.Passes = 2, 1
		}, small)
		c, _ := floatingForDrag(t, h)
		behind := c.Window("behind", fineChecker)
		c.DispatchUntil("the window behind to map", behind.Mapped)
		var before uint64
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			before = comp.BlurRedraws()
			return nil
		})
		holdDrag(t, h, image.Pt(80, 50), image.Pt(4, 50))
		h.Eventually("a blur behind the preview", func(_ *headless.Backend, comp *compositor.Compositor) bool {
			return comp.BlurRedraws() > before
		})
		releaseDrag(h)
		settle(t, h)
	})
}

func TestThePreviewSpringsAndFadesAndUnderReducedMotionOnlyFades(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func(map[anim.Event]anim.Setting) (anim.Table, map[anim.Event]error){
		"full":    func(s map[anim.Event]anim.Setting) (anim.Table, map[anim.Event]error) { return anim.Resolve(true, s) },
		"reduced": anim.ResolveReduced,
	} {
		t.Run(name, func(t *testing.T) {
			d := time.Hour
			off, on := false, true
			table, errs := build(map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Snap: {Enabled: &on, Shape: anim.Linear, Duration: &d}, anim.FadeSnap: {Enabled: &on, Shape: anim.Linear, Duration: &d}})
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			now := time.Hour
			h := StartWith(t, func(cfg *compositor.Config) {
				dragConfig(t, cfg)
				cfg.Animations = table
				cfg.Clock = func() time.Duration { return now }
			}, small)
			_, _ = floatingForDrag(t, h)
			holdDrag(t, h, image.Pt(80, 50), image.Pt(4, 50))
			h.Do(func(*headless.Backend, *compositor.Compositor) error {
				now += d / 2
				return nil
			})
			far := pixel(h.Rendered(0), 2, 2)
			if name == "reduced" && far == previewBackground {
				t.Error("under reduced motion the preview did not start at its target")
			}
			if name == "full" && far != previewBackground {
				t.Errorf("halfway along, the far corner already shows %v", far)
			}
			mid := pixel(h.Rendered(0), 60, 30)
			if mid == previewBackground {
				t.Error("the preview is not drawn while it fades in")
			}
			releaseDrag(h)
		})
	}
}
