package backend_test

import (
	"errors"
	"fmt"
	"testing"

	lgpu "github.com/stubbedev/lys-lib/gpu"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
)

func TestOnlyAGoneDeviceOrABrokenInvariantIsFatalAndAGPUResetIsRecoverable(t *testing.T) {
	for _, tc := range []struct {
		err          error
		fatal, reset bool
	}{
		{err: fmt.Errorf("%w: drm: /dev/dri/card0 no longer answers: %w", backend.ErrDeviceLost, unix.ENODEV), fatal: true},
		{err: fmt.Errorf("dmabuf: import a client's buffer: %w", unix.ENODEV)},
		{err: fmt.Errorf("%w: nested: host gone", backend.ErrDeviceLost), fatal: true},
		{err: backend.Broken("present without BeginFrame"), fatal: true},
		{err: fmt.Errorf("gles: render pass: %w", &gpu.EGLError{Op: "eglMakeCurrent", Code: 0x300E}), reset: true},
		{err: fmt.Errorf("gles: render pass: %w", &gpu.GLError{Op: "draw", Code: 0x0507}), reset: true},
		{err: fmt.Errorf("gles: %w", &gpu.GLError{Op: "glGetGraphicsResetStatus", Code: 0x8254}), reset: true},
		{err: fmt.Errorf("vulkan: render pass: %w", lgpu.ErrDeviceLost), reset: true},
		{err: fmt.Errorf("drm: commit the frame: %w", unix.EBUSY)},
		{err: fmt.Errorf("drm: test the frame: %w", unix.EINVAL)},
		{err: fmt.Errorf("drm: commit the frame: %w", unix.EACCES)},
		{err: fmt.Errorf("gles: render pass: %w", &gpu.GLError{Op: "draw", Code: 0x0505})},
		{err: fmt.Errorf("gles: render pass: %w", &gpu.EGLError{Op: "eglMakeCurrent", Code: 0x3002})},
		{err: errors.New("compositor: tab label: no glyphs")},
	} {
		if got := backend.Fatal(tc.err); got != tc.fatal {
			t.Errorf("Fatal(%v) = %v, want %v", tc.err, got, tc.fatal)
		}
		if got := backend.GPULost(tc.err); got != tc.reset {
			t.Errorf("GPULost(%v) = %v, want %v", tc.err, got, tc.reset)
		}
	}
}
