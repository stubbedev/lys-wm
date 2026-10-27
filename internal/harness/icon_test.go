package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-ui/icontheme"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cxti "github.com/stubbedev/lys-wm/internal/protocol/client/xdgtoplevelicon"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type iconSizes struct {
	sizes []int32
	done  bool
}

func (s *iconSizes) IconSize(_ *cxti.XdgToplevelIconManagerV1, size int32) error {
	s.sizes = append(s.sizes, size)
	return nil
}

func (s *iconSizes) Done(*cxti.XdgToplevelIconManagerV1) error {
	s.done = true
	return nil
}

func iconManager(t *testing.T, c *testclient.Client) *cxti.XdgToplevelIconManagerV1 {
	t.Helper()
	sizes := &iconSizes{}
	m, err := cxti.NewXdgToplevelIconManagerV1(c.Display, 1, sizes)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	c.DispatchUntil("the icon sizes", func() bool { return sizes.done })
	if !slices.Equal(sizes.sizes, shell.IconSizes) {
		t.Fatalf("icon sizes %v, want %v", sizes.sizes, shell.IconSizes)
	}
	return m
}

func setIcon(t *testing.T, c *testclient.Client, m *cxti.XdgToplevelIconManagerV1, w *testclient.Window, name string, buffers ...int) {
	t.Helper()
	icon, err := m.CreateIcon()
	if err != nil {
		t.Fatal(err)
	}
	errs := []error{}
	if name != "" {
		errs = append(errs, icon.SetName(name))
	}
	for _, size := range buffers {
		errs = append(errs, icon.AddBuffer(c.Buffer(image.Pt(size, size), 0, testclient.Fill(0, 0xff, 0)), 1))
	}
	errs = append(errs, m.SetIcon(w.Toplevel, icon), icon.Destroy(), w.Surface.Commit(), c.Display.Flush())
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

func eventuallyIcon(t *testing.T, h *Harness, title string, ok func(*ipc.Icon) bool) *ipc.Icon {
	t.Helper()
	deadline := time.Now().Add(clientTimeout)
	for {
		for _, w := range query[[]ipc.Window](h, ipc.KindWindows) {
			if w.Title == title && ok(w.Icon) {
				return w.Icon
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never reported the expected icon: %+v", title, query[[]ipc.Window](h, ipc.KindWindows))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func iconImage(t *testing.T, h *Harness, size int) (ipc.IconImage, image.Image) {
	t.Helper()
	raw, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindIcon, Window: query[[]ipc.Window](h, ipc.KindWindows)[0].ID, Size: size})
	if err != nil {
		t.Fatal(err)
	}
	var img ipc.IconImage
	if err := json.Unmarshal(raw, &img); err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(img.PNG))
	if err != nil {
		t.Fatal(err)
	}
	return img, decoded
}

func TestIconBuffersInRGBOrderKeepTheirColours(t *testing.T) {
	for _, format := range []cw.WlShmFormat{cw.WlShmFormatAbgr8888, cw.WlShmFormatXbgr8888} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			h := Start(t, small)
			c := testclient.Connect(t, h.Socket())
			m := iconManager(t, c)
			w := c.Window("iconic", quadrants)
			c.DispatchUntil("the window to map", w.Mapped)
			icon, err := m.CreateIcon()
			if err != nil {
				t.Fatal(err)
			}
			red := c.FormatBuffer(image.Pt(16, 16), format, 0, func(pix []byte, _ int, _ image.Point, _ int) {
				for i := 0; i < len(pix); i += 4 {
					pix[i], pix[i+1], pix[i+2], pix[i+3] = 0xff, 0, 0, 0xff
				}
			})
			if err := errors.Join(icon.AddBuffer(red, 1), m.SetIcon(w.Toplevel, icon), icon.Destroy(), w.Surface.Commit(), c.Display.Flush()); err != nil {
				t.Fatal(err)
			}
			eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i != nil })
			if _, decoded := iconImage(t, h, 16); color.RGBAModel.Convert(decoded.At(5, 5)) != (color.RGBA{R: 0xff, A: 0xff}) {
				t.Fatalf("the icon pixel is %v, want opaque red", decoded.At(5, 5))
			}
		})
	}
}

func TestToplevelIconsReachIPCAndTheTab(t *testing.T) {
	decor := compositor.DefaultDecor()
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Decor = decor }, tall)
	c := testclient.Connect(t, h.Socket())
	m := iconManager(t, c)
	w := c.Window("iconic", testclient.Fill(0x60, 0x60, 0x60))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("makegroup tab")
	tabActive := [3]byte{decor.Tab.Active.Background.R, decor.Tab.Active.Background.G, decor.Tab.Active.Background.B}
	h.Eventually("the tab strip", showing(1, 1, tabActive))

	setIcon(t, c, m, w, "lys-wm-test", 16, 32)
	first := eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i != nil })
	if first.Name != "lys-wm-test" || !slices.Equal(first.Sizes, []int{16, 32}) || first.Serial == 0 {
		t.Fatalf("icon %+v, want lys-wm-test with 16 and 32 pixel buffers", first)
	}
	img, decoded := iconImage(t, h, 20)
	if r, g, b, a := decoded.At(5, 5).RGBA(); img.Size != 32 || img.Scale != 1 || r != 0 || g != 0xffff || b != 0 || a != 0xffff {
		t.Fatalf("the icon for 20 pixels is %d at scale %d with %v, want the green 32 pixel buffer", img.Size, img.Scale, decoded.At(5, 5))
	}
	green := [3]byte{0, 0xff, 0}
	h.Eventually("the icon in the tab", showing(7, 7, green))

	setIcon(t, c, m, w, "other")
	second := eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i != nil && i.Serial != first.Serial })
	if second.Name != "other" || len(second.Sizes) != 0 {
		t.Fatalf("the replaced icon is %+v, want only the name other", second)
	}
	h.Eventually("the tab without the buffer icon", func(b *headless.Backend, c *compositor.Compositor) bool {
		return !showing(7, 7, green)(b, c)
	})

	if err := errors.Join(m.SetIcon(w.Toplevel, nil), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i == nil })
}

func iconTheme(t *testing.T, icons map[string]string) *icontheme.Theme {
	t.Helper()
	base := filepath.Join(t.TempDir(), "icons")
	dir := filepath.Join(base, "lys-test", "16x16", "apps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	index := "[Icon Theme]\nName=lys-test\nDirectories=16x16/apps\n\n[16x16/apps]\nSize=16\nType=Scalable\nMinSize=1\nMaxSize=512\n"
	if err := os.WriteFile(filepath.Join(base, "lys-test", "index.theme"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, color := range icons {
		svg := fmt.Appendf(nil, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" fill="#%s"/></svg>`, color)
		if err := os.WriteFile(filepath.Join(dir, name+".svg"), svg, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return icontheme.New("lys-test", []string{base})
}

func TestTabsDrawThemeIconsByNameAndBuffersWhenTheNameIsUnknown(t *testing.T) {
	decor := compositor.DefaultDecor()
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Decor, cfg.Icons = decor, iconTheme(t, map[string]string{"lys-wm-red": "ff0000"})
	}, tall)
	c := testclient.Connect(t, h.Socket())
	m := iconManager(t, c)
	w := c.Window("iconic", testclient.Fill(0x60, 0x60, 0x60))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("makegroup tab")

	setIcon(t, c, m, w, "lys-wm-red", 16)
	h.Eventually("the theme's red icon in the tab, not the green buffer", showing(7, 7, [3]byte{0xff, 0, 0}))
	setIcon(t, c, m, w, "lys-wm-no-such-icon", 16)
	h.Eventually("the green buffer for a name the theme lacks", showing(7, 7, [3]byte{0, 0xff, 0}))
}

func TestIconsKeepNoBufferLargerThanAnythingDrawsThem(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := iconManager(t, c)
	w := c.Window("iconic", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	setIcon(t, c, m, w, "", 16, 256, 512)
	first := eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i != nil })
	if !slices.Equal(first.Sizes, []int{16, 256}) {
		t.Fatalf("kept sizes %v, want 16 and 256 without the 512 px buffer", first.Sizes)
	}
	setIcon(t, c, m, w, "", 1024, 512)
	only := eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i != nil && i.Serial != first.Serial })
	if !slices.Equal(only.Sizes, []int{512}) {
		t.Fatalf("kept sizes %v, want the smallest buffer when all are large", only.Sizes)
	}
}

func TestAnEmptyIconResetsTheIcon(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := iconManager(t, c)
	w := c.Window("iconic", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	setIcon(t, c, m, w, "named")
	eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i != nil })
	setIcon(t, c, m, w, "")
	eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i == nil })
}

func TestToplevelIconErrors(t *testing.T) {
	cases := []struct {
		name string
		code cxti.XdgToplevelIconV1Error
		run  func(c *testclient.Client, m *cxti.XdgToplevelIconManagerV1, w *testclient.Window, icon *cxti.XdgToplevelIconV1) error
	}{
		{
			name: "a name after set_icon",
			code: cxti.XdgToplevelIconV1ErrorImmutable,
			run: func(_ *testclient.Client, m *cxti.XdgToplevelIconManagerV1, w *testclient.Window, icon *cxti.XdgToplevelIconV1) error {
				return errors.Join(icon.SetName("a"), m.SetIcon(w.Toplevel, icon), icon.SetName("b"))
			},
		},
		{
			name: "a buffer after set_icon",
			code: cxti.XdgToplevelIconV1ErrorImmutable,
			run: func(c *testclient.Client, m *cxti.XdgToplevelIconManagerV1, w *testclient.Window, icon *cxti.XdgToplevelIconV1) error {
				return errors.Join(m.SetIcon(w.Toplevel, icon), icon.AddBuffer(c.Buffer(image.Pt(16, 16), 0, testclient.Fill(0, 0, 0)), 1))
			},
		},
		{
			name: "a buffer that is not square",
			code: cxti.XdgToplevelIconV1ErrorInvalidBuffer,
			run: func(c *testclient.Client, _ *cxti.XdgToplevelIconManagerV1, _ *testclient.Window, icon *cxti.XdgToplevelIconV1) error {
				return icon.AddBuffer(c.Buffer(image.Pt(16, 8), 0, testclient.Fill(0, 0, 0)), 1)
			},
		},
		{
			name: "a scale below 1",
			code: cxti.XdgToplevelIconV1ErrorInvalidBuffer,
			run: func(c *testclient.Client, _ *cxti.XdgToplevelIconManagerV1, _ *testclient.Window, icon *cxti.XdgToplevelIconV1) error {
				return icon.AddBuffer(c.Buffer(image.Pt(16, 16), 0, testclient.Fill(0, 0, 0)), 0)
			},
		},
		{
			name: "a buffer destroyed before its icon",
			code: cxti.XdgToplevelIconV1ErrorNoBuffer,
			run: func(c *testclient.Client, _ *cxti.XdgToplevelIconManagerV1, _ *testclient.Window, icon *cxti.XdgToplevelIconV1) error {
				buf := c.Buffer(image.Pt(16, 16), 0, testclient.Fill(0, 0, 0))
				return errors.Join(icon.AddBuffer(buf, 1), buf.Destroy())
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Start(t, small)
			c := testclient.Connect(t, h.Socket())
			m := iconManager(t, c)
			w := c.Window("iconic", quadrants)
			c.DispatchUntil("the window to map", w.Mapped)
			icon, err := m.CreateIcon()
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.run(c, m, w, icon); err != nil {
				t.Fatal(err)
			}
			c.ExpectProtocolError(uint32(tc.code))
		})
	}
}

func TestDestroyingABufferAfterItsIconIsFine(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := iconManager(t, c)
	w := c.Window("iconic", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	icon, err := m.CreateIcon()
	if err != nil {
		t.Fatal(err)
	}
	buf := c.Buffer(image.Pt(16, 16), 0, testclient.Fill(0, 0xff, 0))
	if err := errors.Join(icon.AddBuffer(buf, 1), m.SetIcon(w.Toplevel, icon), icon.Destroy(), buf.Destroy(), w.Surface.Commit(), c.Display.Roundtrip()); err != nil {
		t.Fatalf("destroying the buffer after its icon: %v", err)
	}
	eventuallyIcon(t, h, "iconic", func(i *ipc.Icon) bool { return i != nil && slices.Equal(i.Sizes, []int{16}) })
}
