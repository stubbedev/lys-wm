package gpu

import (
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/fourcc"
)

func TestDefaultYUVFollowsTheVideoSize(t *testing.T) {
	for size, want := range map[image.Point]YUVEncoding{
		image.Pt(640, 480):   {Coefficients: BT601, Range: LimitedRange},
		image.Pt(1280, 719):  {Coefficients: BT601, Range: LimitedRange},
		image.Pt(1280, 720):  {Coefficients: BT709, Range: LimitedRange},
		image.Pt(3840, 2160): {Coefficients: BT709, Range: LimitedRange},
	} {
		if got := DefaultYUV(size); got != want {
			t.Errorf("DefaultYUV(%v) = %+v, want %+v", size, got, want)
		}
	}
}

func TestYUVHintsOnlyForYUVFormats(t *testing.T) {
	full709 := YUVEncoding{Coefficients: BT709, Range: FullRange}
	if got := appendYUVHints(nil, fourcc.NV12, full709); !slices.Equal(got, []int32{eglYUVColorSpaceHint, eglITURec709, eglSampleRangeHint, eglYUVFullRange}) {
		t.Fatalf("NV12 tagged BT.709 full range got attributes %#x", got)
	}
	if got := appendYUVHints(nil, fourcc.P010, YUVEncoding{Coefficients: BT2020, Range: LimitedRange}); !slices.Equal(got, []int32{eglYUVColorSpaceHint, eglITURec2020, eglSampleRangeHint, eglYUVNarrowRange}) {
		t.Fatalf("P010 tagged BT.2020 limited got attributes %#x", got)
	}
	if got := appendYUVHints(nil, fourcc.NV12, YUVEncoding{}); len(got) != 0 {
		t.Fatalf("an unspecified encoding added attributes %#x", got)
	}
	sited := YUVEncoding{Chroma: ChromaSiting{Horizontal: SitedAtHalf, Vertical: SitedAt1}}
	if got := appendYUVHints(nil, fourcc.NV12, sited); !slices.Equal(got, []int32{eglChromaHorizontalSiting, eglChromaSitingHalf}) {
		t.Fatalf("chroma sited at half and 1 got attributes %#x, want only the horizontal hint EGL can express", got)
	}
	if got := appendYUVHints(nil, fourcc.XRGB8888, full709); len(got) != 0 {
		t.Fatalf("an RGB buffer got YUV hints %#x", got)
	}
}
