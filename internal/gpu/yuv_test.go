package gpu

import (
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/fourcc"
)

func TestDefaultYUVFollowsTheVideoSize(t *testing.T) {
	for size, want := range map[image.Point]YUVEncoding{
		image.Pt(640, 480):   {BT601, LimitedRange},
		image.Pt(1280, 719):  {BT601, LimitedRange},
		image.Pt(1280, 720):  {BT709, LimitedRange},
		image.Pt(3840, 2160): {BT709, LimitedRange},
	} {
		if got := DefaultYUV(size); got != want {
			t.Errorf("DefaultYUV(%v) = %+v, want %+v", size, got, want)
		}
	}
}

func TestYUVHintsOnlyForYUVFormats(t *testing.T) {
	full709 := YUVEncoding{BT709, FullRange}
	if got := appendYUVHints(nil, fourcc.NV12, full709); !slices.Equal(got, []int32{eglYUVColorSpaceHint, eglITURec709, eglSampleRangeHint, eglYUVFullRange}) {
		t.Fatalf("NV12 tagged BT.709 full range got attributes %#x", got)
	}
	if got := appendYUVHints(nil, fourcc.P010, YUVEncoding{BT2020, LimitedRange}); !slices.Equal(got, []int32{eglYUVColorSpaceHint, eglITURec2020, eglSampleRangeHint, eglYUVNarrowRange}) {
		t.Fatalf("P010 tagged BT.2020 limited got attributes %#x", got)
	}
	if got := appendYUVHints(nil, fourcc.NV12, YUVEncoding{}); len(got) != 0 {
		t.Fatalf("an unspecified encoding added attributes %#x", got)
	}
	if got := appendYUVHints(nil, fourcc.XRGB8888, full709); len(got) != 0 {
		t.Fatalf("an RGB buffer got YUV hints %#x", got)
	}
}
