// Package shader compiles the one language lys-wm's screen shaders are written
// in, a subset of GLSL ES 1.00, and hands it to each renderer in the form it
// takes: GLSL for GLES, SPIR-V for Vulkan and a Go program for the CPU.
package shader

import (
	"fmt"
	"slices"
)

const (
	maxSource     = 64 << 10
	maxDepth      = 64
	maxIterations = 1024
)

type Error struct {
	Line, Column int
	Message      string
}

func (e *Error) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Column, e.Message)
}

type Type uint8

const (
	tVoid Type = iota
	tBool
	tInt
	tFloat
	tVec2
	tVec3
	tVec4
	tMat2
	tMat3
	tMat4
)

var typeNames = [...]string{tVoid: "void", tBool: "bool", tInt: "int", tFloat: "float", tVec2: "vec2", tVec3: "vec3", tVec4: "vec4", tMat2: "mat2", tMat3: "mat3", tMat4: "mat4"}

func (t Type) String() string { return typeNames[t] }

func (t Type) vector() bool { return t >= tVec2 && t <= tVec4 }

func (t Type) matrix() bool { return t >= tMat2 && t <= tMat4 }

func (t Type) floating() bool { return t >= tFloat }

func (t Type) size() int {
	switch t {
	case tVoid:
		return 0
	case tBool, tInt, tFloat:
		return 1
	case tVec2:
		return 2
	case tVec3:
		return 3
	case tVec4, tMat2:
		return 4
	case tMat3:
		return 9
	}
	return 16
}

func (t Type) columns() int {
	switch t {
	case tMat2:
		return 2
	case tMat3:
		return 3
	case tMat4:
		return 4
	}
	return 1
}

func vecOf(n int) Type {
	switch n {
	case 1:
		return tFloat
	case 2:
		return tVec2
	case 3:
		return tVec3
	}
	return tVec4
}

func (t Type) column() Type { return vecOf(t.columns()) }

type input uint8

const (
	inFragCoord input = iota
	inFragColor
	inTexcoord
	inTime
	inResolution
)

type variable struct {
	name  string
	t     Type
	loop  bool
	param bool
}

type function struct {
	name   string
	ret    Type
	params []*variable
	body   []stmt
	work   int64
}

type expr interface{ typ() Type }

type (
	literal struct {
		t Type
		f float32
		i int32
		b bool
	}
	varExpr     struct{ v *variable }
	inputExpr   struct{ in input }
	swizzleExpr struct {
		x   expr
		idx []int
	}
	indexExpr struct {
		x expr
		i int
	}
	unaryExpr struct {
		op string
		x  expr
	}
	binaryExpr struct {
		op   string
		a, b expr
		t    Type
	}
	logicalExpr struct {
		op   string
		a, b expr
	}
	ternaryExpr struct {
		c, a, b expr
	}
	assignExpr struct {
		target expr
		value  expr
	}
	stepExpr struct {
		target expr
		delta  float32
		post   bool
	}
	constructExpr struct {
		t    Type
		args []expr
	}
	callExpr struct {
		fn   *function
		args []expr
	}
	builtinExpr struct {
		b    *builtin
		args []expr
		t    Type
	}
	sampleExpr struct{ uv expr }
)

func (e *literal) typ() Type       { return e.t }
func (e *varExpr) typ() Type       { return e.v.t }
func (e *swizzleExpr) typ() Type   { return vecOf(len(e.idx)) }
func (e *unaryExpr) typ() Type     { return e.x.typ() }
func (e *binaryExpr) typ() Type    { return e.t }
func (e *logicalExpr) typ() Type   { return tBool }
func (e *ternaryExpr) typ() Type   { return e.a.typ() }
func (e *assignExpr) typ() Type    { return e.target.typ() }
func (e *stepExpr) typ() Type      { return e.target.typ() }
func (e *constructExpr) typ() Type { return e.t }
func (e *callExpr) typ() Type      { return e.fn.ret }
func (e *builtinExpr) typ() Type   { return e.t }
func (e *sampleExpr) typ() Type    { return tVec4 }

func (e *inputExpr) typ() Type {
	switch e.in {
	case inFragCoord, inFragColor:
		return tVec4
	case inTexcoord, inResolution:
		return tVec2
	}
	return tFloat
}

func (e *indexExpr) typ() Type {
	if t := e.x.typ(); t.matrix() {
		return t.column()
	}
	return tFloat
}

func effects(e expr) bool {
	switch e := e.(type) {
	case *assignExpr, *stepExpr, *callExpr:
		return true
	case *swizzleExpr:
		return effects(e.x)
	case *indexExpr:
		return effects(e.x)
	case *unaryExpr:
		return effects(e.x)
	case *binaryExpr:
		return effects(e.a) || effects(e.b)
	case *logicalExpr:
		return effects(e.a) || effects(e.b)
	case *ternaryExpr:
		return effects(e.c) || effects(e.a) || effects(e.b)
	case *constructExpr:
		return anyEffects(e.args)
	case *builtinExpr:
		return anyEffects(e.args)
	case *sampleExpr:
		return effects(e.uv)
	}
	return false
}

func anyEffects(es []expr) bool {
	return slices.ContainsFunc(es, effects)
}

func constant(e expr) bool {
	switch e := e.(type) {
	case *literal:
		return true
	case *swizzleExpr:
		return constant(e.x)
	case *indexExpr:
		return constant(e.x)
	case *unaryExpr:
		return constant(e.x)
	case *binaryExpr:
		return constant(e.a) && constant(e.b)
	case *logicalExpr:
		return constant(e.a) && constant(e.b)
	case *ternaryExpr:
		return constant(e.c) && constant(e.a) && constant(e.b)
	case *constructExpr:
		return allConstant(e.args)
	case *builtinExpr:
		return allConstant(e.args)
	}
	return false
}

func allConstant(es []expr) bool {
	for _, e := range es {
		if !constant(e) {
			return false
		}
	}
	return true
}

type stmt interface{ isStmt() }

type (
	declStmt struct {
		v    *variable
		init expr
	}
	exprStmt struct{ e expr }
	ifStmt   struct {
		cond      expr
		then, alt []stmt
	}
	forStmt struct {
		v    *variable
		init expr
		cond expr
		step expr
		body []stmt
	}
	breakStmt    struct{}
	continueStmt struct{}
	returnStmt   struct{ value expr }
	blockStmt    struct{ body []stmt }
)

func (*declStmt) isStmt()     {}
func (*exprStmt) isStmt()     {}
func (*ifStmt) isStmt()       {}
func (*forStmt) isStmt()      {}
func (*breakStmt) isStmt()    {}
func (*continueStmt) isStmt() {}
func (*returnStmt) isStmt()   {}
func (*blockStmt) isStmt()    {}

type span struct{ from, to int }

type Program struct {
	functions []*function
	main      *function
	source    []byte
	blanks    []span
	usesTime  bool
	spirv     []byte
	glsl      string
}

// UsesTime says the shader's image changes on its own, so its output keeps
// drawing frames.
func (p *Program) UsesTime() bool { return p.usesTime }

// GLES is the source for a GLSL ES 1.00 driver: the shader as written with
// precision blanked out in favour of highp, on the same lines and columns.
func (p *Program) GLES() string { return p.glsl }

// SPIRV is the module for lys-lib's gpu.FragmentShader, in the host's byte order.
func (p *Program) SPIRV() []byte { return p.spirv }

func (p *Program) buildGLES() {
	src := append([]byte(nil), p.source...)
	for _, s := range p.blanks {
		for i := s.from; i < s.to; i++ {
			if src[i] != '\n' {
				src[i] = ' '
			}
		}
	}
	p.glsl = "precision highp float;precision highp int;" + string(src)
}
