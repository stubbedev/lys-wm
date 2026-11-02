package shader

import (
	"math"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

type signature uint8

const (
	sigGen signature = iota
	sigGen2
	sigGenFloat
	sigClamp
	sigMix
	sigStep
	sigSmooth
	sigLength
	sigDistance
	sigCross
	sigFace
	sigRefract
	sigMatrix
)

const (
	extNone        = 0
	extRadians     = 11
	extDegrees     = 12
	extSin         = 13
	extCos         = 14
	extTan         = 15
	extAsin        = 16
	extAcos        = 17
	extAtan        = 18
	extAtan2       = 25
	extPow         = 26
	extExp         = 27
	extLog         = 28
	extExp2        = 29
	extLog2        = 30
	extSqrt        = 31
	extInverseSqrt = 32
	extFAbs        = 4
	extFSign       = 6
	extFloor       = 8
	extCeil        = 9
	extFract       = 10
	extFMin        = 37
	extFMax        = 40
	extFClamp      = 43
	extFMix        = 46
	extStep        = 48
	extSmoothStep  = 49
	extLength      = 66
	extDistance    = 67
	extCross       = 68
	extNormalize   = 69
	extFaceForward = 70
	extReflect     = 71
	extRefract     = 72
)

type builtin struct {
	name string
	sig  signature
	ext  uint32
	f1   func(float32) float32
	f2   func(a, b float32) float32
	f3   func(a, b, c float32) float32
}

func via(f func(float64) float64) func(float32) float32 {
	return func(x float32) float32 { return float32(f(float64(x))) }
}

func mod(a, b float32) float32 {
	return a - float32(b*float32(math.Floor(float64(a/b))))
}

func mix(a, b, t float32) float32 {
	return float32(a*(1-t)) + float32(b*t)
}

func step(edge, x float32) float32 {
	if x < edge {
		return 0
	}
	return 1
}

func smooth(e0, e1, x float32) float32 {
	t := min(max((x-e0)/(e1-e0), 0), 1)
	return float32(float32(t*t) * (3 - float32(2*t)))
}

func sign(x float32) float32 {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}

func fract(x float32) float32 {
	return x - float32(math.Floor(float64(x)))
}

func atan2(y, x float32) float32 {
	return float32(gomath.Atan2(float64(y), float64(x)))
}

var builtins = map[string]*builtin{}

func init() {
	for _, b := range []*builtin{
		{name: "radians", ext: extRadians, f1: func(x float32) float32 { return float32(x * (math.Pi / 180)) }},
		{name: "degrees", ext: extDegrees, f1: func(x float32) float32 { return float32(x * (180 / math.Pi)) }},
		{name: "sin", ext: extSin, f1: via(gomath.Sin)},
		{name: "cos", ext: extCos, f1: via(gomath.Cos)},
		{name: "tan", ext: extTan, f1: via(gomath.Tan)},
		{name: "asin", ext: extAsin, f1: via(gomath.Asin)},
		{name: "acos", ext: extAcos, f1: via(gomath.Acos)},
		{name: "exp", ext: extExp, f1: via(gomath.Exp)},
		{name: "log", ext: extLog, f1: via(gomath.Log)},
		{name: "exp2", ext: extExp2, f1: via(gomath.Exp2)},
		{name: "log2", ext: extLog2, f1: via(gomath.Log2)},
		{name: "sqrt", ext: extSqrt, f1: via(math.Sqrt)},
		{name: "inversesqrt", ext: extInverseSqrt, f1: func(x float32) float32 { return 1 / float32(math.Sqrt(float64(x))) }},
		{name: "abs", ext: extFAbs, f1: via(math.Abs)},
		{name: "sign", ext: extFSign, f1: sign},
		{name: "floor", ext: extFloor, f1: via(math.Floor)},
		{name: "ceil", ext: extCeil, f1: via(math.Ceil)},
		{name: "fract", ext: extFract, f1: fract},
		{name: "pow", sig: sigGen2, ext: extPow, f2: func(a, b float32) float32 { return float32(gomath.Pow(float64(a), float64(b))) }},
		{name: "mod", sig: sigGenFloat, f2: mod},
		{name: "min", sig: sigGenFloat, ext: extFMin, f2: func(a, b float32) float32 { return min(a, b) }},
		{name: "max", sig: sigGenFloat, ext: extFMax, f2: func(a, b float32) float32 { return max(a, b) }},
		{name: "clamp", sig: sigClamp, ext: extFClamp, f3: func(x, lo, hi float32) float32 { return min(max(x, lo), hi) }},
		{name: "mix", sig: sigMix, ext: extFMix, f3: mix},
		{name: "step", sig: sigStep, ext: extStep, f2: step},
		{name: "smoothstep", sig: sigSmooth, ext: extSmoothStep, f3: smooth},
		{name: "length", sig: sigLength, ext: extLength},
		{name: "distance", sig: sigDistance, ext: extDistance},
		{name: "dot", sig: sigDistance},
		{name: "cross", sig: sigCross, ext: extCross},
		{name: "normalize", ext: extNormalize},
		{name: "faceforward", sig: sigFace, ext: extFaceForward},
		{name: "reflect", sig: sigGen2, ext: extReflect},
		{name: "refract", sig: sigRefract, ext: extRefract},
		{name: "matrixCompMult", sig: sigMatrix},
	} {
		builtins[b.name] = b
	}
	builtins["atan"] = &builtin{name: "atan", ext: extAtan, f1: via(gomath.Atan), f2: atan2}
}
