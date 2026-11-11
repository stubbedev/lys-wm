package gpu

import (
	"errors"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
)

const eglBadParameter = 0x300C

func displayFailingItsDMABUFQueries(code int32) *Display {
	l := &Library{}
	l.egl.getError = func() int32 { return code }
	l.egl.queryString = func(uintptr, int32) string { return "ACME" }
	l.egl.queryDMABUFFormats = func(uintptr, int32, *int32, *int32) uint32 { return 0 }
	l.egl.queryDMABUFMods = func(uintptr, int32, int32, *uint64, *uint32, *int32) uint32 { return 0 }
	return &Display{lib: l, exts: []string{"EGL_EXT_image_dma_buf_import_modifiers"}}
}

func TestADMABUFQueryFailingWithoutAnEGLErrorIsADisplayThatImportsNone(t *testing.T) {
	d := displayFailingItsDMABUFQueries(eglSuccess)
	_, formats := d.DMABUFFormats()
	_, modifiers := d.DMABUFModifiers(fourcc.XRGB8888)
	for op, err := range map[string]error{"eglQueryDmaBufFormatsEXT": formats, "eglQueryDmaBufModifiersEXT(XR24)": modifiers} {
		var failed *EGLError
		if !errors.Is(err, ErrUnsupported) || !errors.As(err, &failed) || failed.Op != op || failed.Code != eglSuccess {
			t.Errorf("%s failing with EGL_SUCCESS gave %v, want ErrUnsupported naming the query", op, err)
			continue
		}
		if msg := err.Error(); !strings.Contains(msg, "ACME") || !strings.Contains(msg, op+" failed and set no EGL error") {
			t.Errorf("%s: %q names neither the vendor nor the missing error", op, msg)
		}
	}
}

func TestADMABUFQueryFailingWithAnEGLErrorReportsThatError(t *testing.T) {
	d := displayFailingItsDMABUFQueries(eglBadParameter)
	_, formats := d.DMABUFFormats()
	_, modifiers := d.DMABUFModifiers(fourcc.XRGB8888)
	for op, err := range map[string]error{"eglQueryDmaBufFormatsEXT": formats, "eglQueryDmaBufModifiersEXT(XR24)": modifiers} {
		var failed *EGLError
		if errors.Is(err, ErrUnsupported) || !errors.As(err, &failed) || failed.Op != op || failed.Code != eglBadParameter {
			t.Errorf("%s failing with EGL_BAD_PARAMETER gave %v, want that EGL error and not ErrUnsupported", op, err)
		}
	}
}
