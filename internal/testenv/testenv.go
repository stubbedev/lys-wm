// Package testenv decides whether tests that need tools or libraries from the
// dev shell skip or fail when they are missing, and gives tests directories
// short enough to hold Unix sockets.
package testenv

import (
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	gtext "github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/xcursor"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render/vulkan"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

const strictEnv = "LYS_WM_HEADLESS"

const HangLimit = 30 * time.Second

func missing(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv(strictEnv) == "1" {
		t.Fatalf(format+"; "+strictEnv+"=1 forbids skipping", args...)
	}
	t.Skipf(format, args...)
}

func Require(t testing.TB, binary string) string {
	t.Helper()
	path, err := exec.LookPath(binary)
	if err != nil {
		missing(t, "%s is not on PATH", binary)
	}
	return path
}

func GPU(t testing.TB) *gpu.Library {
	t.Helper()
	l, err := gpu.Load(gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		missing(t, "%v", err)
	}
	return l
}

func Library(t testing.TB, name, path string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if _, statErr := os.Stat(path); statErr != nil {
		Require(t, name)
	}
	t.Fatalf("open %s from %s: %v", name, path, err)
}

var driverClose sync.Mutex

func CloseDriver(closeDriver func() error) error {
	driverClose.Lock()
	defer driverClose.Unlock()
	return closeDriver()
}

func Vulkan(t testing.TB) *vulkan.Renderer {
	t.Helper()
	r, err := vulkan.OpenDefault()
	if err != nil {
		missing(t, "%v", err)
	}
	t.Cleanup(func() {
		if err := CloseDriver(r.Close); err != nil {
			t.Error(err)
		}
	})
	t.Logf("Vulkan device: %s", r.Device().Name())
	return r
}

func XKBCompiler(t testing.TB) *xkb.Compiler {
	t.Helper()
	return xkb.NewCompiler(os.Getenv)
}

func Keymap(t testing.TB) *xkb.Keymap {
	t.Helper()
	km, err := XKBCompiler(t).Compile(xkb.NamesFromEnv(os.Getenv))
	if err != nil {
		missing(t, "no keymap: %v", err)
	}
	t.Cleanup(func() { _ = km.Close() })
	return km
}

func CursorTheme(t testing.TB) *xcursor.Theme {
	t.Helper()
	path := os.Getenv("LYS_WM_TEST_XCURSOR_PATH")
	if path == "" {
		missing(t, "LYS_WM_TEST_XCURSOR_PATH is not set")
	}
	theme, err := xcursor.Open("Vanilla-DMZ", []string{path})
	if err != nil {
		missing(t, "no test cursor theme: %v", err)
	}
	return theme
}

func Font(t testing.TB) *gtext.Typeface {
	t.Helper()
	path := os.Getenv("LYS_WM_FONT")
	if path == "" {
		missing(t, "LYS_WM_FONT is not set")
	}
	data, err := os.ReadFile(path) //nolint:gosec // the test font path comes from the dev shell
	if err != nil {
		missing(t, "no test font: %v", err)
	}
	f, err := gtext.LoadFont(data)
	if err != nil {
		t.Fatalf("testenv: load %s: %v", path, err)
	}
	return f
}

func SocketDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lys-wm-")
	if err != nil {
		t.Fatalf("testenv: socket dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("testenv: remove socket dir: %v", err)
		}
	})
	return dir
}
