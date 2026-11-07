package harness

import (
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func BenchmarkLookFullFrame(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		for _, look := range []struct {
			name string
			set  func(*compositor.Config)
		}{
			{"plain", func(*compositor.Config) {}},
			{"default", func(cfg *compositor.Config) {
				cfg.Decor, cfg.WM = compositor.DefaultDecor(compositor.DefaultAccent), compositor.DefaultSettings()
			}},
		} {
			b.Run(look.name, func(b *testing.B) {
				s := open(b)
				s.configure = func(cfg *compositor.Config) {
					look.set(cfg)
					cfg.Animations = anim.Table{}
				}
				h := start(b, s, benchMode)
				c := testclient.Connect(b, h.Socket())
				for _, title := range []string{"a", "b", "c", "d"} {
					w := c.Window(title, quadrants)
					c.DispatchUntil("the window to map", w.Mapped)
					w.Stop()
				}
				h.Rendered(0)
				h.Do(func(bk *headless.Backend, comp *compositor.Compositor) error {
					o := bk.Outputs()[0]
					var actions [2]wm.Action
					for i, name := range [2]string{"workspace 2", "workspace 1"} {
						var err error
						if actions[i], err = wm.ParseAction(name); err != nil {
							return err
						}
					}
					i := 0
					b.ReportAllocs()
					for b.Loop() {
						i++
						if err := comp.Dispatch(actions[i%2]); err != nil {
							return err
						}
						if err := comp.Frame(o); err != nil {
							return err
						}
					}
					return nil
				})
			})
		}
	})
}

func BenchmarkLookAnimatedFrame(b *testing.B) {
	benchRenderers(b, func(b *testing.B, open func(testing.TB) setup) {
		s := open(b)
		slow := 20 * time.Second
		s.configure = func(cfg *compositor.Config) {
			cfg.Decor, cfg.WM = compositor.DefaultDecor(compositor.DefaultAccent), compositor.DefaultSettings()
			off := false
			table, errs := anim.Resolve(true, map[anim.Event]anim.Setting{anim.Global: {Enabled: &off}, anim.Workspaces: {Enabled: new(true), Shape: anim.Linear, Duration: &slow}})
			if len(errs) != 0 {
				b.Fatal(errs)
			}
			cfg.Animations = table
		}
		h := start(b, s, benchMode)
		c := testclient.Connect(b, h.Socket())
		for _, title := range []string{"a", "b", "c", "d"} {
			w := c.Window(title, quadrants)
			c.DispatchUntil("the window to map", w.Mapped)
			w.Stop()
		}
		h.Rendered(0)
		h.Do(func(bk *headless.Backend, comp *compositor.Compositor) error {
			o := bk.Outputs()[0]
			action, err := wm.ParseAction("workspace 2")
			if err != nil {
				return err
			}
			if err := comp.Dispatch(action); err != nil {
				return err
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := comp.Frame(o); err != nil {
					return err
				}
			}
			return nil
		})
	})
}
