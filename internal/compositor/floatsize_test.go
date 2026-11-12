package compositor

import (
	"errors"
	"image"
	"regexp"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/loop"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type pongs struct{}

func (pongs) Ping(base *cxdg.XdgWmBase, serial uint32) error { return base.Pong(serial) }

type configures struct {
	sizes  []image.Point
	serial uint32
}

func (c *configures) Configure(_ *cxdg.XdgToplevel, width, height int32, _ []byte) error {
	c.sizes = append(c.sizes, image.Pt(int(width), int(height)))
	return nil
}

func (*configures) Close(*cxdg.XdgToplevel) error                         { return nil }
func (*configures) ConfigureBounds(*cxdg.XdgToplevel, int32, int32) error { return nil }
func (*configures) WmCapabilities(*cxdg.XdgToplevel, []byte) error        { return nil }

type acks struct{ c *configures }

func (a acks) Configure(_ *cxdg.XdgSurface, serial uint32) error {
	a.c.serial = serial
	return nil
}

type scales struct{ buffer int32 }

func (*scales) Enter(*cw.WlSurface, *cw.WlOutput) error                            { return nil }
func (*scales) Leave(*cw.WlSurface, *cw.WlOutput) error                            { return nil }
func (*scales) PreferredBufferTransform(*cw.WlSurface, cw.WlOutputTransform) error { return nil }

func (s *scales) PreferredBufferScale(_ *cw.WlSurface, factor int32) error {
	s.buffer = factor
	return nil
}

type floater struct {
	r       *rig
	surface *cw.WlSurface
	xdg     *cxdg.XdgSurface
	top     *cxdg.XdgToplevel
	seen    *configures
	scale   *scales
}

func floatingRig(t *testing.T) (*rig, *cxdg.XdgWmBase) {
	t.Helper()
	var b *headless.Backend
	r := setupWith(t, func(l *loop.Loop, _ *spyRenderer) backend.Backend {
		b = headless.New(l)
		if _, err := b.AddOutput(backend.Mode{Size: image.Pt(1600, 1200), RefreshMHz: 60000}); err != nil {
			t.Fatal(err)
		}
		return b
	}, func(cfg *Config) {
		cfg.Rules = []wm.Rule{{Match: wm.Match{AppID: regexp.MustCompile(`^floater$`)}, Mode: wm.RuleFloat}}
	})
	var err error
	r.on(func() { err = r.comp.SetOutputScale(b.Outputs()[0], 240) })
	must(t, err)
	base, err := cxdg.NewXdgWmBase(r.client, 6, pongs{})
	must(t, err, r.registry.Bind(r.globals["xdg_wm_base"], base))
	return r, base
}

func (r *rig) floater(base *cxdg.XdgWmBase, setup func(*floater) error) *floater {
	r.t.Helper()
	f := &floater{r: r, seen: &configures{}, scale: &scales{}}
	var err error
	f.surface, err = r.wlComp.CreateSurface(f.scale)
	must(r.t, err)
	f.xdg, err = base.GetXdgSurface(acks{f.seen}, f.surface)
	must(r.t, err)
	f.top, err = f.xdg.GetToplevel(f.seen)
	must(r.t, err)
	must(r.t, f.top.SetAppID("floater"), setup(f), f.surface.Commit())
	r.roundtrip()
	return f
}

func (f *floater) initial() image.Point {
	f.r.t.Helper()
	if len(f.seen.sizes) == 0 {
		f.r.t.Fatal("the initial commit got no configure")
	}
	return f.seen.sizes[0]
}

func (f *floater) draw(size image.Point) {
	f.r.t.Helper()
	_, pool := f.r.pool(size.X * size.Y * 4)
	b, err := pool.CreateBuffer(&releases{}, 0, int32(size.X), int32(size.Y), int32(size.X*4), cw.WlShmFormatXrgb8888)
	must(f.r.t, err)
	must(f.r.t, f.xdg.AckConfigure(f.seen.serial), f.surface.Attach(b, 0, 0), f.surface.DamageBuffer(0, 0, int32(size.X), int32(size.Y)), f.surface.Commit())
	f.r.roundtrip()
}

func TestAFloatingWindowWithAMinimumAndNoSizeChoosesItsOwnSize(t *testing.T) {
	r, base := floatingRig(t)
	f := r.floater(base, func(f *floater) error { return f.top.SetMinSize(480, 320) })
	if got := f.initial(); got != (image.Point{}) {
		t.Fatalf("the initial configure of a window with a 480x320 minimum and no size is %v, want 0x0 for the client to choose", got)
	}
	if f.scale.buffer != 2 {
		t.Fatalf("the initial configure on a scale 2 output prefers buffer scale %d", f.scale.buffer)
	}
	f.draw(image.Pt(760, 520))
	if last := f.seen.sizes[len(f.seen.sizes)-1]; last != image.Pt(760, 520) {
		t.Fatalf("a window that chose 760x520 was configured to %v", last)
	}
	r.on(func() {
		w := r.comp.findWindow(func(*window) bool { return true })
		if w == nil {
			t.Error("the window did not map")
			return
		}
		rect, floats := r.comp.wm.FloatingRect(w.id)
		border := 2 * r.comp.borderWidthOf(r.comp.decision(w))
		if want := image.Pt(760+border, 520+border); !floats || rect.Size() != want {
			t.Errorf("the window maps floating %v at %v, want the %v it chose framed", floats, rect.Size(), want)
		}
	})
}

func TestAFloatingWindowsKnownSizeKeepsWithinItsLimits(t *testing.T) {
	for name, c := range map[string]struct {
		geometry, minimum, maximum, want image.Point
	}{
		"below its minimum":                    {image.Pt(200, 100), image.Pt(480, 320), image.Point{}, image.Pt(480, 320)},
		"above its maximum":                    {image.Pt(900, 700), image.Point{}, image.Pt(800, 600), image.Pt(800, 600)},
		"within its limits":                    {image.Pt(500, 400), image.Pt(480, 320), image.Pt(800, 600), image.Pt(500, 400)},
		"a minimum width and a maximum height": {image.Pt(300, 600), image.Pt(480, 0), image.Pt(0, 500), image.Pt(480, 500)},
	} {
		t.Run(name, func(t *testing.T) {
			r, base := floatingRig(t)
			f := r.floater(base, func(f *floater) error {
				return errors.Join(
					f.xdg.SetWindowGeometry(0, 0, int32(c.geometry.X), int32(c.geometry.Y)),
					f.top.SetMinSize(int32(c.minimum.X), int32(c.minimum.Y)),
					f.top.SetMaxSize(int32(c.maximum.X), int32(c.maximum.Y)),
				)
			})
			if got := f.initial(); got != c.want {
				t.Fatalf("a window of %v with limits %v-%v is first configured to %v, want %v", c.geometry, c.minimum, c.maximum, got, c.want)
			}
		})
	}
}

func TestAFloatingWindowWithNoSizeChoosesEveryAxisItsLimitsLeaveOpen(t *testing.T) {
	for name, c := range map[string]struct {
		minimum, maximum, want image.Point
	}{
		"a minimum width":                {image.Pt(480, 0), image.Point{}, image.Point{}},
		"a maximum height":               {image.Point{}, image.Pt(0, 500), image.Point{}},
		"both limits":                    {image.Pt(480, 320), image.Pt(800, 600), image.Point{}},
		"a fixed size":                   {image.Pt(60, 40), image.Pt(60, 40), image.Pt(60, 40)},
		"a fixed width and a min height": {image.Pt(300, 320), image.Pt(300, 0), image.Pt(300, 0)},
	} {
		t.Run(name, func(t *testing.T) {
			r, base := floatingRig(t)
			f := r.floater(base, func(f *floater) error {
				return errors.Join(f.top.SetMinSize(int32(c.minimum.X), int32(c.minimum.Y)), f.top.SetMaxSize(int32(c.maximum.X), int32(c.maximum.Y)))
			})
			if got := f.initial(); got != c.want {
				t.Fatalf("a window with limits %v-%v and no size is first configured to %v, want %v", c.minimum, c.maximum, got, c.want)
			}
		})
	}
}
