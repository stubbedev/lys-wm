package harness

import (
	"fmt"
	"image"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func BenchmarkGCCountPerThousandFrames(b *testing.B) {
	for _, percent := range []int{100, 400, 800} {
		b.Run(fmt.Sprintf("GOGC=%d", percent), func(b *testing.B) {
			old := debug.SetGCPercent(percent)
			b.Cleanup(func() { debug.SetGCPercent(old) })
			mode := backend.Mode{Size: image.Pt(640, 360), RefreshMHz: 60000, Preferred: true}
			now := time.Second
			h := start(b, setup{open: softBackend, configure: func(cfg *compositor.Config) {
				cfg.WM = compositor.DefaultSettings()
				cfg.Decor = compositor.DefaultDecor()
				cfg.Animations = anim.MustResolve(anim.DefaultMotion())
				cfg.Clock = func() time.Duration { return now }
			}}, mode)
			for i := range 6 {
				c := testclient.Connect(b, h.Socket())
				w := c.Window(fmt.Sprintf("w%d", i), quadrants)
				c.DispatchUntil("map", w.Mapped)
				w.Stop()
			}
			actions := []string{"focus left", "move left", "workspace 2", "workspace 1", "overview toggle", "overview toggle"}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			frames := 0
			for b.Loop() {
				h.Do(func(bk *headless.Backend, comp *compositor.Compositor) error {
					for i := range 20 {
						a, err := wm.ParseAction(actions[i%len(actions)])
						if err != nil {
							return err
						}
						if err := comp.Dispatch(a); err != nil {
							return err
						}
						for range 50 {
							now += 16 * time.Millisecond
							if err := comp.Frame(bk.Outputs()[0]); err != nil {
								return err
							}
							frames++
						}
					}
					return nil
				})
			}
			runtime.ReadMemStats(&after)
			b.ReportMetric(float64(after.NumGC-before.NumGC)*1000/float64(frames), "gc/1000frames")
			b.ReportMetric(float64(after.PauseTotalNs-before.PauseTotalNs)/float64(frames), "gcpause-ns/frame")
			b.ReportMetric(float64(after.Mallocs-before.Mallocs)/float64(frames), "mallocs/frame")
			b.ReportMetric(float64(after.Sys)/(1<<20), "sys-MiB")
		})
	}
}
