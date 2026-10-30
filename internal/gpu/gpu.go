// Package gpu binds libEGL, libGLESv2 and libgbm through purego: displays on
// the GBM, device and surfaceless platforms, GLES2 contexts, dmabuf images,
// native fences and buffer allocation.
package gpu

import (
	"fmt"
	"image"

	"github.com/ebitengine/purego"
	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
)

const (
	EGLEnv    = "LYS_WM_LIBEGL"
	GLESv2Env = "LYS_WM_LIBGLESV2"
	GBMEnv    = "LYS_WM_LIBGBM"
)

var (
	defaultEGL    = "libEGL.so.1"
	defaultGLESv2 = "libGLESv2.so.2"
	defaultGBM    = "libgbm.so.1"
)

type Paths struct {
	EGL, GLESv2, GBM string
}

func PathsFromEnv(getenv func(string) string) Paths {
	pick := func(env, fallback string) string {
		if p := getenv(env); p != "" {
			return p
		}
		return fallback
	}
	return Paths{EGL: pick(EGLEnv, defaultEGL), GLESv2: pick(GLESv2Env, defaultGLESv2), GBM: pick(GBMEnv, defaultGBM)}
}

func open(path, env string) (uintptr, error) {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return 0, fmt.Errorf("gpu: load %s (set %s to its path): %w", path, env, err)
	}
	return h, nil
}

type symbol struct {
	fn   any
	name string
}

func bind(lib uintptr, path string, syms []symbol) error {
	for _, s := range syms {
		if _, err := purego.Dlsym(lib, s.name); err != nil {
			return fmt.Errorf("gpu: %s lacks %s: %w", path, s.name, err)
		}
		if raw, ok := s.fn.(*uintptr); ok {
			*raw, _ = purego.Dlsym(lib, s.name)
			continue
		}
		purego.RegisterLibFunc(s.fn, lib, s.name)
	}
	return nil
}

type Plane struct {
	FD     int
	Offset uint32
	Stride uint32
}

type DMABUF struct {
	Size     image.Point
	Format   fourcc.Format
	Modifier fourcc.Modifier
	Planes   []Plane
	YUV      YUVEncoding
}

func (d DMABUF) Dmabuf() dmabuf.Buffer {
	planes := make([]dmabuf.Plane, len(d.Planes))
	for i, p := range d.Planes {
		planes[i] = dmabuf.Plane{FD: p.FD, Offset: p.Offset, Stride: p.Stride}
	}
	return dmabuf.Buffer{Width: d.Size.X, Height: d.Size.Y, Format: d.Format, Modifier: d.Modifier, Planes: planes}
}

type Coefficients uint8

const (
	CoefficientsUnspecified Coefficients = iota
	BT601
	BT709
	BT2020
)

type Range uint8

const (
	RangeUnspecified Range = iota
	LimitedRange
	FullRange
)

type Siting uint8

const (
	SitingUnspecified Siting = iota
	SitedAt0
	SitedAtHalf
	SitedAt1
)

type ChromaSiting struct {
	Horizontal, Vertical Siting
}

type YUVEncoding struct {
	Coefficients Coefficients
	Range        Range
	Chroma       ChromaSiting
}

func DefaultYUV(size image.Point) YUVEncoding {
	if size.Y >= 720 {
		return YUVEncoding{Coefficients: BT709, Range: LimitedRange}
	}
	return YUVEncoding{Coefficients: BT601, Range: LimitedRange}
}

type FormatModifier struct {
	Format   fourcc.Format
	Modifier fourcc.Modifier
}

type ScanoutTarget struct {
	Device  uint64
	Formats []FormatModifier
}
