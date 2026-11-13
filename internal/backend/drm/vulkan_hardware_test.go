package drm

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

func withoutGL() gpu.Paths {
	paths := gpu.PathsFromEnv(os.Getenv)
	paths.EGL, paths.GLESv2 = "/nonexistent/libEGL.so.1", "/nonexistent/libGLESv2.so.2"
	return paths
}

func TestVulkanRendersIntoScanoutBuffersOnEveryCardOnHardware(t *testing.T) {
	for _, node := range cardNodes(t) {
		t.Run(filepath.Base(node), func(t *testing.T) {
			dev, err := kdrm.Open(node)
			if err != nil {
				t.Skipf("cannot open %s: %v", node, err)
			}
			defer dev.Close()
			a, err := openVulkan(dev)
			if err != nil {
				skipWithoutVulkan(t, node, err)
			}
			mods, err := a.renderable(scanoutFormat)
			if err != nil {
				t.Fatal(errors.Join(err, a.close()))
			}
			for offered := mods; len(offered) > 0; {
				before := testenv.OpenDMABUFs(t)
				d, err := a.mem.drawable(image.Pt(64, 32), scanoutFormat, offered, scannedOut)
				if err != nil {
					t.Fatal(errors.Join(fmt.Errorf("%s: a scanout buffer in one of %v: %w", node, offered, err), a.close()))
				}
				if !slices.Contains(offered, d.buf.Modifier) {
					t.Fatal(errors.Join(fmt.Errorf("%s allocated modifier %v, not one of the %v offered", node, d.buf.Modifier, offered), d.free(), a.close()))
				}
				testFrame(t, a.gpu, d.target, fmt.Sprintf("%s %v", node, d.buf.Modifier))
				if err := d.free(); err != nil {
					t.Fatal(errors.Join(err, a.close()))
				}
				if open := testenv.OpenDMABUFs(t); open != before {
					t.Fatal(errors.Join(fmt.Errorf("%s: %d dma-buf descriptors are open after a %v scanout buffer was freed, %d before it was allocated", node, open, d.buf.Modifier, before), a.close()))
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
