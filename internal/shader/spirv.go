package shader

import (
	"encoding/binary"
	"math"

	"github.com/stubbedev/lys-lib/intconv"
)

const (
	opExtInstImport       = 11
	opExtInst             = 12
	opMemoryModel         = 14
	opEntryPoint          = 15
	opExecutionMode       = 16
	opCapability          = 17
	opTypeVoid            = 19
	opTypeBool            = 20
	opTypeInt             = 21
	opTypeFloat           = 22
	opTypeVector          = 23
	opTypeMatrix          = 24
	opTypeImage           = 25
	opTypeSampledImage    = 27
	opTypeStruct          = 30
	opTypePointer         = 32
	opTypeFunction        = 33
	opConstantTrue        = 41
	opConstantFalse       = 42
	opConstant            = 43
	opConstantNull        = 46
	opFunction            = 54
	opFunctionParameter   = 55
	opFunctionEnd         = 56
	opFunctionCall        = 57
	opVariable            = 59
	opLoad                = 61
	opStore               = 62
	opAccessChain         = 65
	opDecorate            = 71
	opMemberDecorate      = 72
	opVectorShuffle       = 79
	opCompositeConstruct  = 80
	opCompositeExtract    = 81
	opImageSampleExplicit = 88
	opConvertFToS         = 110
	opConvertSToF         = 111
	opSNegate             = 126
	opFNegate             = 127
	opIAdd                = 128
	opFAdd                = 129
	opISub                = 130
	opFSub                = 131
	opIMul                = 132
	opFMul                = 133
	opSDiv                = 135
	opFDiv                = 136
	opFMod                = 141
	opMatrixTimesScalar   = 143
	opVectorTimesMatrix   = 144
	opMatrixTimesVector   = 145
	opMatrixTimesMatrix   = 146
	opDot                 = 148
	opAll                 = 155
	opLogicalNotEqual     = 165
	opLogicalEqual        = 164
	opLogicalAnd          = 167
	opLogicalNot          = 168
	opSelect              = 169
	opIEqual              = 170
	opINotEqual           = 171
	opSGreaterThan        = 173
	opSGreaterThanEqual   = 175
	opSLessThan           = 177
	opSLessThanEqual      = 179
	opFOrdEqual           = 180
	opFUnordNotEqual      = 182
	opFOrdLessThan        = 184
	opFOrdGreaterThan     = 186
	opFOrdLessThanEqual   = 188
	opFOrdGreaterThanEq   = 190
	opLoopMerge           = 246
	opSelectionMerge      = 247
	opLabel               = 248
	opBranch              = 249
	opBranchConditional   = 250
	opReturn              = 253
	opReturnValue         = 254
	opUnreachable         = 255

	storageUniformConstant = 0
	storageInput           = 1
	storageOutput          = 3
	storagePushConstant    = 9
	storageFunction        = 7

	decorationBlock         = 2
	decorationBuiltIn       = 11
	decorationLocation      = 30
	decorationBinding       = 33
	decorationDescriptorSet = 34
	decorationOffset        = 35

	builtInFragCoord = 15

	drawKeepOffset = 64
)

type ptrKey struct {
	storage uint32
	t       Type
}

type spv struct {
	bound      uint32
	head       []uint32
	notes      []uint32
	globals    []uint32
	funcs      []uint32
	vars       []uint32
	body       []uint32
	types      map[Type]uint32
	pointers   map[ptrKey]uint32
	floats     map[uint32]uint32
	ints       map[int32]uint32
	nulls      map[Type]uint32
	bools      [2]uint32
	boolVec    map[int]uint32
	glsl       uint32
	functions  map[*function]uint32
	varIDs     map[*variable]uint32
	fragCoord  uint32
	fragColor  uint32
	texcoord   uint32
	keep       uint32
	sampler    uint32
	sampledTy  uint32
	ended      bool
	breakTo    uint32
	continueTo uint32
	ret        Type
}

func (s *spv) id() uint32 {
	s.bound++
	return s.bound
}

func u32(i int) uint32 { return intconv.Must[uint32](i) }

func word(op, n int) uint32 { return u32(n)<<16 | u32(op) }

func emit(dst *[]uint32, op int, args ...uint32) {
	*dst = append(*dst, word(op, len(args)+1))
	*dst = append(*dst, args...)
}

func litString(text string) []uint32 {
	raw := append([]byte(text), 0)
	for len(raw)%4 != 0 {
		raw = append(raw, 0)
	}
	out := make([]uint32, len(raw)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	return out
}

func (s *spv) typeID(t Type) uint32 {
	if id, ok := s.types[t]; ok {
		return id
	}
	var id uint32
	switch {
	case t == tVoid:
		id = s.id()
		emit(&s.globals, opTypeVoid, id)
	case t == tBool:
		id = s.id()
		emit(&s.globals, opTypeBool, id)
	case t == tInt:
		id = s.id()
		emit(&s.globals, opTypeInt, id, 32, 1)
	case t == tFloat:
		id = s.id()
		emit(&s.globals, opTypeFloat, id, 32)
	case t.vector():
		base := s.typeID(tFloat)
		id = s.id()
		emit(&s.globals, opTypeVector, id, base, u32(t.size()))
	default:
		col := s.typeID(t.column())
		id = s.id()
		emit(&s.globals, opTypeMatrix, id, col, u32(t.columns()))
	}
	s.types[t] = id
	return id
}

func (s *spv) boolVector(n int) uint32 {
	if id, ok := s.boolVec[n]; ok {
		return id
	}
	base := s.typeID(tBool)
	id := s.id()
	emit(&s.globals, opTypeVector, id, base, u32(n))
	s.boolVec[n] = id
	return id
}

func (s *spv) pointer(storage uint32, t Type) uint32 {
	k := ptrKey{storage, t}
	if id, ok := s.pointers[k]; ok {
		return id
	}
	base := s.typeID(t)
	id := s.id()
	emit(&s.globals, opTypePointer, id, storage, base)
	s.pointers[k] = id
	return id
}

func (s *spv) float(v float32) uint32 {
	bits := math.Float32bits(v)
	if id, ok := s.floats[bits]; ok {
		return id
	}
	t := s.typeID(tFloat)
	id := s.id()
	emit(&s.globals, opConstant, t, id, bits)
	s.floats[bits] = id
	return id
}

func (s *spv) int(v int32) uint32 {
	if id, ok := s.ints[v]; ok {
		return id
	}
	t := s.typeID(tInt)
	id := s.id()
	emit(&s.globals, opConstant, t, id, uint32(v)) //nolint:gosec // an int constant is its bits
	s.ints[v] = id
	return id
}

func (s *spv) boolean(v bool) uint32 {
	i, op := 0, opConstantFalse
	if v {
		i, op = 1, opConstantTrue
	}
	if s.bools[i] != 0 {
		return s.bools[i]
	}
	t := s.typeID(tBool)
	id := s.id()
	emit(&s.globals, op, t, id)
	s.bools[i] = id
	return id
}

func (s *spv) null(t Type) uint32 {
	if id, ok := s.nulls[t]; ok {
		return id
	}
	ty := s.typeID(t)
	id := s.id()
	emit(&s.globals, opConstantNull, ty, id)
	s.nulls[t] = id
	return id
}

func (s *spv) inst(op int, t Type, args ...uint32) uint32 {
	ty := s.typeID(t)
	id := s.id()
	emit(&s.body, op, append([]uint32{ty, id}, args...)...)
	return id
}

func (s *spv) ext(t Type, inst uint32, args ...uint32) uint32 {
	return s.inst(opExtInst, t, append([]uint32{s.glsl, inst}, args...)...)
}

func (s *spv) localVar(t Type) uint32 {
	ptr := s.pointer(storageFunction, t)
	id := s.id()
	emit(&s.vars, opVariable, ptr, id, storageFunction)
	return id
}

func (s *spv) label(id uint32) {
	emit(&s.body, opLabel, id)
	s.ended = false
}

func (s *spv) branch(to uint32) {
	if !s.ended {
		emit(&s.body, opBranch, to)
		s.ended = true
	}
}

func (s *spv) declareInterface() {
	vec4In := s.pointer(storageInput, tVec4)
	s.fragCoord = s.id()
	emit(&s.globals, opVariable, vec4In, s.fragCoord, storageInput)
	emit(&s.notes, opDecorate, s.fragCoord, decorationBuiltIn, builtInFragCoord)

	vec2In := s.pointer(storageInput, tVec2)
	s.texcoord = s.id()
	emit(&s.globals, opVariable, vec2In, s.texcoord, storageInput)
	emit(&s.notes, opDecorate, s.texcoord, decorationLocation, 0)

	vec4Out := s.pointer(storageOutput, tVec4)
	s.fragColor = s.id()
	emit(&s.globals, opVariable, vec4Out, s.fragColor, storageOutput)
	emit(&s.notes, opDecorate, s.fragColor, decorationLocation, 0)

	float := s.typeID(tFloat)
	image := s.id()
	emit(&s.globals, opTypeImage, image, float, 1, 0, 0, 0, 1, 0)
	s.sampledTy = s.id()
	emit(&s.globals, opTypeSampledImage, s.sampledTy, image)
	samplerPtr := s.id()
	emit(&s.globals, opTypePointer, samplerPtr, storageUniformConstant, s.sampledTy)
	s.sampler = s.id()
	emit(&s.globals, opVariable, samplerPtr, s.sampler, storageUniformConstant)
	emit(&s.notes, opDecorate, s.sampler, decorationDescriptorSet, 0)
	emit(&s.notes, opDecorate, s.sampler, decorationBinding, 0)

	vec4 := s.typeID(tVec4)
	block := s.id()
	emit(&s.globals, opTypeStruct, block, vec4)
	emit(&s.notes, opDecorate, block, decorationBlock)
	emit(&s.notes, opMemberDecorate, block, 0, decorationOffset, drawKeepOffset)
	blockPtr := s.id()
	emit(&s.globals, opTypePointer, blockPtr, storagePushConstant, block)
	s.keep = s.id()
	emit(&s.globals, opVariable, blockPtr, s.keep, storagePushConstant)
}

func emitSPIRV(p *Program) []byte {
	s := &spv{
		types: map[Type]uint32{}, pointers: map[ptrKey]uint32{}, floats: map[uint32]uint32{}, ints: map[int32]uint32{},
		nulls: map[Type]uint32{}, boolVec: map[int]uint32{}, functions: map[*function]uint32{}, varIDs: map[*variable]uint32{},
	}
	s.glsl = s.id()
	s.declareInterface()
	for _, fn := range p.functions {
		s.function(fn)
	}
	emit(&s.head, opCapability, 1)
	emit(&s.head, opExtInstImport, append([]uint32{s.glsl}, litString("GLSL.std.450")...)...)
	emit(&s.head, opMemoryModel, 0, 1)
	entry := append([]uint32{4, s.functions[p.main]}, litString("main")...)
	entry = append(entry, s.fragCoord, s.texcoord, s.fragColor)
	emit(&s.head, opEntryPoint, entry...)
	emit(&s.head, opExecutionMode, s.functions[p.main], 7)
	words := []uint32{0x07230203, 0x00010000, 0, s.bound + 1, 0}
	words = append(words, s.head...)
	words = append(words, s.notes...)
	words = append(words, s.globals...)
	words = append(words, s.funcs...)
	out := make([]byte, 0, len(words)*4)
	for _, w := range words {
		out = binary.NativeEndian.AppendUint32(out, w)
	}
	return out
}

func (s *spv) function(fn *function) {
	params := make([]uint32, 0, len(fn.params)+2)
	id := s.id()
	s.functions[fn] = id
	ret := s.typeID(fn.ret)
	params = append(params, 0, ret)
	for _, v := range fn.params {
		params = append(params, s.typeID(v.t))
	}
	fnType := s.id()
	params[0] = fnType
	emit(&s.globals, opTypeFunction, params...)
	emit(&s.funcs, opFunction, ret, id, 0, fnType)
	s.vars, s.body, s.ended, s.ret = nil, nil, false, fn.ret
	for _, v := range fn.params {
		arg := s.id()
		emit(&s.funcs, opFunctionParameter, s.typeID(v.t), arg)
		local := s.localVar(v.t)
		s.varIDs[v] = local
		emit(&s.body, opStore, local, arg)
	}
	s.stmts(fn.body)
	if !s.ended {
		if fn.ret == tVoid {
			emit(&s.body, opReturn)
		} else {
			emit(&s.body, opUnreachable)
		}
	}
	emit(&s.funcs, opLabel, s.id())
	s.funcs = append(s.funcs, s.vars...)
	s.funcs = append(s.funcs, s.body...)
	emit(&s.funcs, opFunctionEnd)
}

func (s *spv) stmts(body []stmt) {
	for _, st := range body {
		if s.ended {
			return
		}
		s.stmt(st)
	}
}

func (s *spv) stmt(st stmt) {
	switch st := st.(type) {
	case *declStmt:
		v := s.localVar(st.v.t)
		s.varIDs[st.v] = v
		value := s.null(st.v.t)
		if st.init != nil {
			value = s.expr(st.init)
		}
		emit(&s.body, opStore, v, value)
	case *exprStmt:
		s.expr(st.e)
	case *blockStmt:
		s.stmts(st.body)
	case *ifStmt:
		cond := s.expr(st.cond)
		then, alt, merge := s.id(), s.id(), s.id()
		if st.alt == nil {
			alt = merge
		}
		emit(&s.body, opSelectionMerge, merge, 0)
		emit(&s.body, opBranchConditional, cond, then, alt)
		s.label(then)
		s.ended = false
		s.stmts(st.then)
		s.branch(merge)
		if st.alt != nil {
			s.label(alt)
			s.stmts(st.alt)
			s.branch(merge)
		}
		s.label(merge)
	case *forStmt:
		s.loop(st)
	case *breakStmt:
		s.branch(s.breakTo)
	case *continueStmt:
		s.branch(s.continueTo)
	case *returnStmt:
		if st.value == nil {
			emit(&s.body, opReturn)
		} else {
			emit(&s.body, opReturnValue, s.expr(st.value))
		}
		s.ended = true
	}
}

func (s *spv) loop(st *forStmt) {
	v := s.localVar(st.v.t)
	s.varIDs[st.v] = v
	emit(&s.body, opStore, v, s.expr(st.init))
	header, check, body, cont, merge := s.id(), s.id(), s.id(), s.id(), s.id()
	s.branch(header)
	s.label(header)
	emit(&s.body, opLoopMerge, merge, cont, 0)
	s.branch(check)
	s.label(check)
	cond := s.expr(st.cond)
	emit(&s.body, opBranchConditional, cond, body, merge)
	s.ended = true
	s.label(body)
	outerBreak, outerContinue := s.breakTo, s.continueTo
	s.breakTo, s.continueTo = merge, cont
	s.stmts(st.body)
	s.breakTo, s.continueTo = outerBreak, outerContinue
	s.branch(cont)
	s.label(cont)
	s.expr(st.step)
	s.branch(header)
	s.label(merge)
}

func (s *spv) load(t Type, ptr uint32) uint32 {
	return s.inst(opLoad, t, ptr)
}

func (s *spv) keepField(i int32) uint32 {
	ptr := s.pointer(storagePushConstant, tFloat)
	id := s.id()
	emit(&s.body, opAccessChain, ptr, id, s.keep, s.int(0), s.int(i))
	return s.load(tFloat, id)
}

func (s *spv) expr(e expr) uint32 {
	switch e := e.(type) {
	case *literal:
		switch e.t {
		case tFloat:
			return s.float(e.f)
		case tInt:
			return s.int(e.i)
		}
		return s.boolean(e.b)
	case *varExpr:
		return s.load(e.v.t, s.varIDs[e.v])
	case *inputExpr:
		switch e.in {
		case inFragCoord:
			return s.load(tVec4, s.fragCoord)
		case inFragColor:
			return s.load(tVec4, s.fragColor)
		case inTexcoord:
			return s.load(tVec2, s.texcoord)
		case inTime:
			return s.keepField(0)
		}
		w, h := s.keepField(1), s.keepField(2)
		return s.inst(opCompositeConstruct, tVec2, w, h)
	case *swizzleExpr:
		x := s.expr(e.x)
		if len(e.idx) == 1 {
			return s.inst(opCompositeExtract, tFloat, x, u32(e.idx[0]))
		}
		args := []uint32{x, x}
		for _, i := range e.idx {
			args = append(args, u32(i))
		}
		return s.inst(opVectorShuffle, e.typ(), args...)
	case *indexExpr:
		return s.inst(opCompositeExtract, e.typ(), s.expr(e.x), u32(e.i))
	case *unaryExpr:
		return s.unary(e)
	case *binaryExpr:
		a := s.expr(e.a)
		b := s.expr(e.b)
		return s.binary(e.op, e.a.typ(), a, e.b.typ(), b, e.t)
	case *logicalExpr:
		return s.logical(e)
	case *ternaryExpr:
		return s.ternary(e)
	case *assignExpr:
		value := s.expr(e.value)
		s.store(e.target, value)
		return value
	case *stepExpr:
		return s.stepped(e)
	case *constructExpr:
		return s.construct(e)
	case *callExpr:
		return s.call(e)
	case *builtinExpr:
		return s.builtin(e)
	case *sampleExpr:
		uv := s.expr(e.uv)
		sampler := s.id()
		emit(&s.body, opLoad, s.sampledTy, sampler, s.sampler)
		const lodOperand = 2
		return s.inst(opImageSampleExplicit, tVec4, sampler, uv, lodOperand, s.float(0))
	}
	panic("shader: an expression the compiler does not know")
}

func (s *spv) unary(e *unaryExpr) uint32 {
	x := s.expr(e.x)
	t := e.x.typ()
	switch {
	case e.op == "!":
		return s.inst(opLogicalNot, tBool, x)
	case t == tInt:
		return s.inst(opSNegate, tInt, x)
	case t.matrix():
		return s.perColumn(t, func(i int) uint32 {
			return s.inst(opFNegate, t.column(), s.inst(opCompositeExtract, t.column(), x, u32(i)))
		})
	}
	return s.inst(opFNegate, t, x)
}

func (s *spv) perColumn(t Type, column func(int) uint32) uint32 {
	cols := make([]uint32, t.columns())
	for i := range cols {
		cols[i] = column(i)
	}
	return s.inst(opCompositeConstruct, t, cols...)
}

func (s *spv) widen(t Type, scalar uint32) uint32 {
	args := make([]uint32, t.size())
	for i := range args {
		args[i] = scalar
	}
	return s.inst(opCompositeConstruct, t, args...)
}

func arithOp(name string) int {
	switch name {
	case "+":
		return opFAdd
	case "-":
		return opFSub
	case "*":
		return opFMul
	}
	return opFDiv
}

func (s *spv) binary(name string, ta Type, a uint32, tb Type, b uint32, t Type) uint32 {
	if t == tBool {
		return s.compare(name, ta, a, b)
	}
	if t == tInt {
		return s.inst(map[string]int{"+": opIAdd, "-": opISub, "*": opIMul, "/": opSDiv}[name], tInt, a, b)
	}
	switch {
	case name == "*" && ta.matrix() && tb.matrix():
		return s.inst(opMatrixTimesMatrix, t, a, b)
	case name == "*" && ta.matrix() && tb.vector():
		return s.inst(opMatrixTimesVector, t, a, b)
	case name == "*" && ta.vector() && tb.matrix():
		return s.inst(opVectorTimesMatrix, t, a, b)
	case name == "*" && ta.matrix() && tb == tFloat:
		return s.inst(opMatrixTimesScalar, t, a, b)
	case name == "*" && ta == tFloat && tb.matrix():
		return s.inst(opMatrixTimesScalar, t, b, a)
	case t.matrix():
		col := t.column()
		part := func(ty Type, v uint32, i int) uint32 {
			if ty == tFloat {
				return s.widen(col, v)
			}
			return s.inst(opCompositeExtract, col, v, u32(i))
		}
		return s.perColumn(t, func(i int) uint32 {
			return s.inst(arithOp(name), col, part(ta, a, i), part(tb, b, i))
		})
	}
	if ta == tFloat && t != tFloat {
		a = s.widen(t, a)
	}
	if tb == tFloat && t != tFloat {
		b = s.widen(t, b)
	}
	return s.inst(arithOp(name), t, a, b)
}

func (s *spv) compare(name string, t Type, a, b uint32) uint32 {
	switch t {
	case tInt:
		return s.inst(map[string]int{"<": opSLessThan, "<=": opSLessThanEqual, ">": opSGreaterThan, ">=": opSGreaterThanEqual, "==": opIEqual, "!=": opINotEqual}[name], tBool, a, b)
	case tBool:
		if name == "==" {
			return s.inst(opLogicalEqual, tBool, a, b)
		}
		return s.inst(opLogicalNotEqual, tBool, a, b)
	case tFloat:
		return s.inst(map[string]int{"<": opFOrdLessThan, "<=": opFOrdLessThanEqual, ">": opFOrdGreaterThan, ">=": opFOrdGreaterThanEq, "==": opFOrdEqual, "!=": opFUnordNotEqual}[name], tBool, a, b)
	}
	equal := s.equalComponents(t, a, b)
	if name == "==" {
		return equal
	}
	return s.inst(opLogicalNot, tBool, equal)
}

func (s *spv) equalComponents(t Type, a, b uint32) uint32 {
	if t.vector() {
		bvec := s.boolVector(t.size())
		id := s.id()
		emit(&s.body, opFOrdEqual, bvec, id, a, b)
		return s.inst(opAll, tBool, id)
	}
	result := s.boolean(true)
	for i := range t.columns() {
		col := t.column()
		same := s.equalComponents(col, s.inst(opCompositeExtract, col, a, u32(i)), s.inst(opCompositeExtract, col, b, u32(i)))
		result = s.inst(opLogicalAnd, tBool, result, same)
	}
	return result
}

func (s *spv) choose(t Type, cond uint32, yes, no func() uint32) uint32 {
	v := s.localVar(t)
	then, alt, merge := s.id(), s.id(), s.id()
	emit(&s.body, opSelectionMerge, merge, 0)
	emit(&s.body, opBranchConditional, cond, then, alt)
	s.ended = true
	s.label(then)
	emit(&s.body, opStore, v, yes())
	s.branch(merge)
	s.label(alt)
	emit(&s.body, opStore, v, no())
	s.branch(merge)
	s.label(merge)
	return s.load(t, v)
}

func (s *spv) logical(e *logicalExpr) uint32 {
	a := s.expr(e.a)
	switch e.op {
	case "&&":
		return s.choose(tBool, a, func() uint32 { return s.expr(e.b) }, func() uint32 { return s.boolean(false) })
	case "||":
		return s.choose(tBool, a, func() uint32 { return s.boolean(true) }, func() uint32 { return s.expr(e.b) })
	}
	return s.inst(opLogicalNotEqual, tBool, a, s.expr(e.b))
}

func (s *spv) ternary(e *ternaryExpr) uint32 {
	return s.choose(e.typ(), s.expr(e.c), func() uint32 { return s.expr(e.a) }, func() uint32 { return s.expr(e.b) })
}

type place struct {
	ptr  uint32
	t    Type
	flat []int
	in   uint32
}

func (s *spv) place(e expr) place {
	switch e := e.(type) {
	case *varExpr:
		return place{ptr: s.varIDs[e.v], t: e.v.t, in: storageFunction}
	case *inputExpr:
		return place{ptr: s.fragColor, t: tVec4, in: storageOutput}
	case *swizzleExpr:
		p := s.place(e.x)
		flat := make([]int, len(e.idx))
		for i, k := range e.idx {
			flat[i] = k
			if p.flat != nil {
				flat[i] = p.flat[k]
			}
		}
		p.flat = flat
		return p
	case *indexExpr:
		p := s.place(e.x)
		t := e.x.typ()
		n, first := 1, e.i
		if t.matrix() {
			n, first = t.columns(), e.i*t.columns()
		}
		flat := make([]int, n)
		for i := range flat {
			flat[i] = first + i
			if p.flat != nil {
				flat[i] = p.flat[first+i]
			}
		}
		p.flat = flat
		return p
	}
	panic("shader: an assignment target the compiler does not know")
}

func (s *spv) store(target expr, value uint32) {
	p := s.place(target)
	if p.flat == nil {
		emit(&s.body, opStore, p.ptr, value)
		return
	}
	scalar := len(p.flat) == 1
	ptrType := s.pointer(p.in, tFloat)
	for i, k := range p.flat {
		id := s.id()
		if p.t.matrix() {
			dim := p.t.columns()
			emit(&s.body, opAccessChain, ptrType, id, p.ptr, s.int(intconv.Must[int32](k/dim)), s.int(intconv.Must[int32](k%dim)))
		} else {
			emit(&s.body, opAccessChain, ptrType, id, p.ptr, s.int(intconv.Must[int32](k)))
		}
		component := value
		if !scalar {
			component = s.inst(opCompositeExtract, tFloat, value, u32(i))
		}
		emit(&s.body, opStore, id, component)
	}
}

func (s *spv) stepped(e *stepExpr) uint32 {
	t := e.target.typ()
	before := s.expr(e.target)
	var after uint32
	if t == tInt {
		after = s.inst(opIAdd, tInt, before, s.int(int32(e.delta)))
	} else {
		after = s.binary("+", t, before, tFloat, s.float(e.delta), t)
	}
	s.store(e.target, after)
	if e.post {
		return before
	}
	return after
}

func (s *spv) convert(from, to Type, v uint32) uint32 {
	switch {
	case from == to:
		return v
	case from == tInt && to == tFloat:
		return s.inst(opConvertSToF, tFloat, v)
	case from == tFloat && to == tInt:
		return s.inst(opConvertFToS, tInt, v)
	case from == tBool && to == tFloat:
		return s.inst(opSelect, tFloat, v, s.float(1), s.float(0))
	case from == tBool && to == tInt:
		return s.inst(opSelect, tInt, v, s.int(1), s.int(0))
	case from == tFloat:
		return s.inst(opFUnordNotEqual, tBool, v, s.float(0))
	}
	return s.inst(opINotEqual, tBool, v, s.int(0))
}

func (s *spv) scalars(e expr, to Type) []uint32 {
	v := s.expr(e)
	t := e.typ()
	if t.size() == 1 {
		return []uint32{s.convert(t, to, v)}
	}
	out := make([]uint32, 0, t.size())
	if t.vector() {
		for i := range t.size() {
			out = append(out, s.convert(tFloat, to, s.inst(opCompositeExtract, tFloat, v, u32(i))))
		}
		return out
	}
	for c := range t.columns() {
		col := s.inst(opCompositeExtract, t.column(), v, u32(c))
		for r := range t.columns() {
			out = append(out, s.convert(tFloat, to, s.inst(opCompositeExtract, tFloat, col, u32(r))))
		}
	}
	return out
}

func (s *spv) fromScalars(t Type, c []uint32) uint32 {
	if t.size() == 1 {
		return c[0]
	}
	if t.vector() {
		return s.inst(opCompositeConstruct, t, c[:t.size()]...)
	}
	dim := t.columns()
	return s.perColumn(t, func(i int) uint32 {
		return s.inst(opCompositeConstruct, t.column(), c[i*dim:(i+1)*dim]...)
	})
}

func (s *spv) construct(e *constructExpr) uint32 {
	to := scalarOf(e.t)
	if len(e.args) == 1 {
		a := e.args[0]
		switch {
		case a.typ().size() == 1 && e.t.matrix():
			v := s.scalars(a, tFloat)[0]
			dim := e.t.columns()
			c := make([]uint32, dim*dim)
			for i := range c {
				c[i] = s.float(0)
				if i%(dim+1) == 0 {
					c[i] = v
				}
			}
			return s.fromScalars(e.t, c)
		case a.typ().size() == 1 && e.t.size() > 1:
			v := s.scalars(a, to)[0]
			c := make([]uint32, e.t.size())
			for i := range c {
				c[i] = v
			}
			return s.fromScalars(e.t, c)
		case e.t.matrix():
			src := s.scalars(a, tFloat)
			dim, from := e.t.columns(), a.typ().columns()
			c := make([]uint32, dim*dim)
			for col := range dim {
				for row := range dim {
					switch {
					case col < from && row < from:
						c[col*dim+row] = src[col*from+row]
					case col == row:
						c[col*dim+row] = s.float(1)
					default:
						c[col*dim+row] = s.float(0)
					}
				}
			}
			return s.fromScalars(e.t, c)
		}
	}
	var c []uint32
	for _, a := range e.args {
		c = append(c, s.scalars(a, to)...)
	}
	return s.fromScalars(e.t, c)
}

func (s *spv) call(e *callExpr) uint32 {
	args := []uint32{s.functions[e.fn]}
	for _, a := range e.args {
		args = append(args, s.expr(a))
	}
	return s.inst(opFunctionCall, e.fn.ret, args...)
}

func (s *spv) builtin(e *builtinExpr) uint32 {
	args := make([]uint32, len(e.args))
	for i, a := range e.args {
		args[i] = s.expr(a)
		if a.typ() == tFloat && e.t.vector() && e.b.sig != sigRefract {
			args[i] = s.widen(e.t, args[i])
		}
	}
	switch {
	case e.b.name == "atan" && len(args) == 2:
		return s.ext(e.t, extAtan2, args...)
	case e.b.name == "mod":
		return s.inst(opFMod, e.t, args...)
	case e.b.name == "dot":
		if e.args[0].typ() == tFloat {
			return s.inst(opFMul, tFloat, args...)
		}
		return s.inst(opDot, tFloat, args...)
	case e.b.name == "matrixCompMult":
		col := e.t.column()
		return s.perColumn(e.t, func(i int) uint32 {
			return s.inst(opFMul, col, s.inst(opCompositeExtract, col, args[0], u32(i)), s.inst(opCompositeExtract, col, args[1], u32(i)))
		})
	}
	return s.ext(e.t, e.b.ext, args...)
}
