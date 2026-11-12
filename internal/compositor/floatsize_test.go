package compositor

import (
	"errors"
	"image"
	"regexp"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/loop"
	ckd "github.com/stubbedev/lys-wm/internal/protocol/client/kdeserverdecoration"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cxd "github.com/stubbedev/lys-wm/internal/protocol/client/xdgdecoration"
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

const (
	floatBorder = 3
	floatBar    = 20
)

type decorationModes struct{}

func (decorationModes) Configure(*cxd.ZxdgToplevelDecorationV1, cxd.ZxdgToplevelDecorationV1Mode) error {
	return nil
}

type kdeModes struct{}

func (kdeModes) DefaultMode(*ckd.OrgKdeKwinServerDecorationManager, uint32) error { return nil }
func (kdeModes) Mode(*ckd.OrgKdeKwinServerDecoration, uint32) error               { return nil }

type floater struct {
	r          *rig
	surface    *cw.WlSurface
	xdg        *cxdg.XdgSurface
	top        *cxdg.XdgToplevel
	seen       *configures
	scale      *scales
	decoration *cxd.ZxdgToplevelDecorationV1
	kde        *ckd.OrgKdeKwinServerDecoration
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
		cfg.Rules = []wm.Rule{
			{Match: wm.Match{AppID: regexp.MustCompile(`^floater$`)}, Mode: wm.RuleFloat},
			{Match: wm.Match{AppID: regexp.MustCompile(`^sized$`)}, Mode: wm.RuleFloat, Size: wm.Px(640, 480)},
		}
		cfg.Decor.BorderWidth = floatBorder
		cfg.Decor.Titlebar.Floating, cfg.Decor.Titlebar.Height = true, floatBar
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

func (f *floater) serverDecorated() error {
	m, err := cxd.NewZxdgDecorationManagerV1(f.r.client, 1)
	if err != nil {
		return err
	}
	if err := f.r.registry.Bind(f.r.globals["zxdg_decoration_manager_v1"], m); err != nil {
		return err
	}
	f.decoration, err = m.GetToplevelDecoration(decorationModes{}, f.top)
	return err
}

func (f *floater) kdeDecorated() error {
	m, err := ckd.NewOrgKdeKwinServerDecorationManager(f.r.client, 1, kdeModes{})
	if err != nil {
		return err
	}
	if err := f.r.registry.Bind(f.r.globals["org_kde_kwin_server_decoration_manager"], m); err != nil {
		return err
	}
	f.kde, err = m.Create(kdeModes{}, f.surface)
	return err
}

func (f *floater) settles(t *testing.T, when string, content image.Point, bar int) {
	t.Helper()
	f.r.roundtrip()
	if got := f.last(); got != content {
		t.Errorf("%s, the window is configured to %v, want its %v kept", when, got, content)
	}
	if got, want := f.floatingFrame(), content.Add(image.Pt(2*floatBorder, 2*floatBorder+bar)); got != want {
		t.Errorf("%s, the window floats in a frame of %v, want %v around its %v", when, got, want, content)
	}
}

func (f *floater) floatingFrame() image.Point {
	f.r.t.Helper()
	var size image.Point
	f.r.on(func() {
		w := f.r.comp.findWindow(func(*window) bool { return true })
		if w == nil {
			f.r.t.Error("the window did not map")
			return
		}
		rect, floats := f.r.comp.wm.FloatingRect(w.id)
		if !floats {
			f.r.t.Error("the window does not float")
		}
		size = rect.Size()
	})
	return size
}

func (f *floater) last() image.Point {
	return f.seen.sizes[len(f.seen.sizes)-1]
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
	if last := f.last(); last != image.Pt(760, 520) {
		t.Fatalf("a window that chose 760x520 was configured to %v", last)
	}
	if got, want := f.floatingFrame(), image.Pt(760+2*floatBorder, 520+2*floatBorder); got != want {
		t.Errorf("the window maps floating at %v, want the %v it chose framed", got, want)
	}
}

func TestAFloatingWindowIsConfiguredAtTheSizeItAskedForWithOrWithoutATitlebar(t *testing.T) {
	for titling, bar := range map[string]int{"with a titlebar": floatBar, "without a titlebar": 0} {
		for source, c := range map[string]struct {
			appID                 string
			geometry, fixed, want image.Point
		}{
			"its own geometry": {"floater", image.Pt(500, 400), image.Point{}, image.Pt(500, 400)},
			"a rule's size":    {"sized", image.Point{}, image.Point{}, image.Pt(640, 480)},
			"its fixed size":   {"floater", image.Point{}, image.Pt(300, 200), image.Pt(300, 200)},
		} {
			t.Run(titling+" sized by "+source, func(t *testing.T) {
				r, base := floatingRig(t)
				f := r.floater(base, func(f *floater) error {
					err := f.top.SetAppID(c.appID)
					if c.geometry != (image.Point{}) {
						err = errors.Join(err, f.xdg.SetWindowGeometry(0, 0, int32(c.geometry.X), int32(c.geometry.Y)))
					}
					if c.fixed != (image.Point{}) {
						err = errors.Join(err, f.top.SetMinSize(int32(c.fixed.X), int32(c.fixed.Y)), f.top.SetMaxSize(int32(c.fixed.X), int32(c.fixed.Y)))
					}
					if bar > 0 {
						err = errors.Join(err, f.serverDecorated())
					}
					return err
				})
				if got := f.initial(); got != c.want {
					t.Fatalf("the initial configure is %v, want the %v asked for", got, c.want)
				}
				f.draw(c.want)
				if got := f.last(); got != c.want {
					t.Fatalf("the mapped window is configured to %v, want the %v asked for", got, c.want)
				}
				if got, want := f.floatingFrame(), c.want.Add(image.Pt(2*floatBorder, 2*floatBorder+bar)); got != want {
					t.Errorf("the window floats in a frame of %v, want %v around the %v asked for", got, want, c.want)
				}
			})
		}
	}
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

func TestAMappedFloatingWindowsFrameFollowsItsDecoration(t *testing.T) {
	for name, fixed := range map[string]bool{"a resizable window": false, "a fixed-size window": true} {
		t.Run(name, func(t *testing.T) {
			size := image.Pt(500, 400)
			limit := func(f *floater) error {
				if !fixed {
					return f.xdg.SetWindowGeometry(0, 0, int32(size.X), int32(size.Y))
				}
				return errors.Join(f.top.SetMinSize(int32(size.X), int32(size.Y)), f.top.SetMaxSize(int32(size.X), int32(size.Y)))
			}
			t.Run("through xdg-decoration", func(t *testing.T) {
				r, base := floatingRig(t)
				f := r.floater(base, func(f *floater) error { return errors.Join(limit(f), f.serverDecorated()) })
				f.draw(size)
				f.settles(t, "mapped with a decoration", size, floatBar)
				must(t, f.decoration.SetMode(cxd.ZxdgToplevelDecorationV1ModeClientSide))
				f.settles(t, "after asking for client-side decorations", size, floatBar)
				must(t, f.decoration.UnsetMode())
				f.settles(t, "after unsetting the mode", size, floatBar)
				must(t, f.decoration.Destroy())
				f.settles(t, "after destroying the decoration", size, 0)
			})
			t.Run("through the KDE server decoration", func(t *testing.T) {
				r, base := floatingRig(t)
				f := r.floater(base, limit)
				f.draw(size)
				f.settles(t, "mapped without a decoration", size, 0)
				must(t, f.kdeDecorated())
				f.settles(t, "after creating the decoration", size, floatBar)
				must(t, f.kde.RequestMode(uint32(ckd.OrgKdeKwinServerDecorationModeClient)))
				f.settles(t, "after asking for client-side decorations", size, floatBar)
				must(t, f.kde.Release())
				f.settles(t, "after releasing the decoration", size, 0)
			})
		})
	}
}
