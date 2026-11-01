package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const (
	vendorFilesEnv = "__EGL_VENDOR_LIBRARY_FILENAMES"
	vendorDirsEnv  = "__EGL_VENDOR_LIBRARY_DIRS"
	nvidiaDriver   = "nvidia"
	nvidiaVendor   = "10_nvidia.json"
)

var vendorDirs = []string{
	"/run/opengl-driver/share/glvnd/egl_vendor.d",
	"/etc/glvnd/egl_vendor.d",
	"/usr/share/glvnd/egl_vendor.d",
}

// nvidiaVendorFile names the one EGL vendor a machine driving every card
// with NVIDIA's driver needs: libglvnd otherwise opens every vendor
// installed, and Mesa's brings libgallium and libLLVM with it. An
// environment that already chooses its vendors keeps its choice, and one
// without the file loads what it always did.
func nvidiaVendorFile(drivers []string, getenv func(string) string, exists func(string) bool) (string, bool) {
	if len(drivers) == 0 || getenv(vendorFilesEnv) != "" || getenv(vendorDirsEnv) != "" {
		return "", false
	}
	if slices.ContainsFunc(drivers, func(d string) bool { return d != nvidiaDriver }) {
		return "", false
	}
	for _, dir := range vendorDirs {
		if path := filepath.Join(dir, nvidiaVendor); exists(path) {
			return path, true
		}
	}
	return "", false
}

// WithOwnVendorOnly runs open, which loads EGL for the first time in this
// process, with libglvnd limited to the vendor of drivers, the kernel
// drivers of every card on the seat.
func WithOwnVendorOnly(drivers []string, open func() error) error {
	path, ok := nvidiaVendorFile(drivers, os.Getenv, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if !ok {
		return open()
	}
	return withVendorFile(path, open)
}

// withVendorFile sets the vendor list only around open: libglvnd reads it
// once, and a child must not inherit it.
func withVendorFile(path string, open func() error) error {
	if err := os.Setenv(vendorFilesEnv, path); err != nil {
		return fmt.Errorf("gpu: limit EGL to %s: %w", path, err)
	}
	defer func() { _ = os.Unsetenv(vendorFilesEnv) }()
	return open()
}
