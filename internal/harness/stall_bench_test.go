package harness

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/xwayland"
)

func BenchmarkStallDragPublish(b *testing.B) {
	for _, windows := range []int{8, 64} {
		b.Run(fmt.Sprintf("%d-windows", windows), func(b *testing.B) {
			h := start(b, setup{open: softBackend, configure: flat}, benchMode)
			c := testclient.Connect(b, h.Socket())
			for i := range windows {
				w := c.Window(fmt.Sprintf("window %d", i), fillGreen)
				c.DispatchUntil("the window to map", w.Mapped)
				w.Stop()
			}
			c.Sync()
			h.Eventually("the layout landed", landed)
			samples := make([]time.Duration, 0, 1024)
			lines := 0
			h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
				last := comp.Snapshot()
				b.ReportAllocs()
				for b.Loop() {
					at := time.Now()
					next := comp.Snapshot()
					for _, e := range ipc.Diff(last, next) {
						if _, err := ipc.Encode(e); err != nil {
							return err
						}
						lines++
					}
					last = next
					samples = append(samples, time.Since(at))
				}
				return nil
			})
			slices.Sort(samples)
			b.ReportMetric(float64(samples[len(samples)/2])/float64(time.Millisecond), "p50-ms")
			b.ReportMetric(float64(samples[len(samples)*99/100])/float64(time.Millisecond), "p99-ms")
			b.ReportMetric(float64(lines)/float64(len(samples)), "events/op")
		})
	}
}

func BenchmarkStallXRequest(b *testing.B) {
	testenv.Require(b, xwayland.Path(os.Getenv))
	h := start(b, setup{open: softBackend, configure: flat, xwayland: func(error) {}}, benchMode)
	conn, err := net.DialTimeout("unix", filepath.Join(h.RuntimeDir, ".X11-unix", "X"+strings.TrimPrefix(h.xwayland.Display(), ":")), clientTimeout)
	if err != nil {
		b.Fatal(err)
	}
	x, err := xgb.NewConnNet(conn)
	if err != nil {
		b.Fatal(err)
	}
	defer x.Close()
	screen := xproto.Setup(x).DefaultScreen(x)
	win, err := xproto.NewWindowId(x)
	if err != nil {
		b.Fatal(err)
	}
	if err := xproto.CreateWindowChecked(x, 0, win, screen.Root, 0, 0, 64, 64, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		b.Fatal(err)
	}
	samples := make([]time.Duration, 0, 1<<16)
	sent := 0
	b.ReportAllocs()
	for b.Loop() {
		at := time.Now()
		xproto.ConfigureWindow(x, win, xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{uint32(sent % 64), 0, 64, 64})
		samples = append(samples, time.Since(at))
		if sent++; sent%256 == 0 {
			if _, err := xproto.GetInputFocus(x).Reply(); err != nil {
				b.Fatal(err)
			}
		}
	}
	slices.Sort(samples)
	b.ReportMetric(float64(samples[len(samples)/2])/float64(time.Microsecond), "p50-us")
	b.ReportMetric(float64(samples[len(samples)*99/100])/float64(time.Microsecond), "p99-us")
	b.ReportMetric(float64(samples[len(samples)-1])/float64(time.Microsecond), "max-us")
}
