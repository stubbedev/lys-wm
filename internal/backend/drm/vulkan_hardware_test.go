package drm

import (
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"slices"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/gpu"
)

var unloadable = gpu.Paths{EGL: "/nonexistent/libEGL.so.1", GLESv2: "/nonexistent/libGLESv2.so.2", GBM: "/nonexistent/libgbm.so.1"}

func TestVulkanExportsScanoutBuffersOnEveryRenderNodeOnHardware(t *testing.T) {
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Skip("no render node")
	}
	for _, node := range nodes {
		t.Run(filepath.Base(node), func(t *testing.T) {
			dev, err := kdrm.Open(node)
			if err != nil {
				t.Skipf("cannot open %s: %v", node, err)
			}
			defer dev.Close()
			a, err := newVulkanAllocator(dev)
			if err != nil {
				t.Skipf("no Vulkan driver drives %s here (unset VK_DRIVER_FILES on a GPU machine): %v", node, err)
			}
			mods, err := a.renderable(scanoutFormat)
			if err != nil {
				t.Fatal(errors.Join(err, a.close()))
			}
			for offered := mods; len(offered) > 0; {
				d, err := a.mem.drawable(image.Pt(64, 32), scanoutFormat, offered, scannedOut)
				if err != nil {
					t.Fatal(errors.Join(fmt.Errorf("%s: a scanout buffer in one of %v: %w", node, offered, err), a.close()))
				}
				if !slices.Contains(offered, d.buf.Modifier) {
					t.Fatal(errors.Join(fmt.Errorf("%s exported modifier %v, not one of the %v offered", node, d.buf.Modifier, offered), d.free(), a.close()))
				}
				testFrame(t, a.gpu, d.target, fmt.Sprintf("%s %v", node, d.buf.Modifier))
				if err := d.free(); err != nil {
					t.Fatal(errors.Join(err, a.close()))
				}
				t.Logf("%s picks %v among %v", node, d.buf.Modifier, offered)
				offered = withoutModifier(offered, d.buf.Modifier)
			}
			if err := a.close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
