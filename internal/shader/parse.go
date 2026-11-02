package shader

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
)

func errorAt(line, col int, format string, args ...any) error {
	return &Error{Line: line, Column: col, Message: fmt.Sprintf(format, args...)}
}

var reserved = []string{
	"attribute", "break", "centroid", "class", "continue", "discard", "do", "double", "else", "enum", "extern", "external", "fixed", "flat", "for", "goto", "half", "hvec2", "hvec3", "hvec4", "if", "in", "inline", "inout", "input", "interface", "invariant", "long", "namespace", "noinline", "out", "output", "packed", "public", "return", "sampler1D", "sampler1DShadow", "sampler2D", "sampler2DRect", "sampler2DRectShadow", "sampler2DShadow", "sampler3D", "sampler3DRect", "samplerCube", "short", "sizeof", "static", "struct", "superp", "switch", "template", "this", "typedef", "uniform", "union", "unsigned", "using", "varying", "void", "volatile", "while",
	"bvec2", "bvec3", "bvec4", "ivec2", "ivec3", "ivec4", "dvec2", "dvec3", "dvec4", "fvec2", "fvec3", "fvec4", "cast", "asm", "lowp", "mediump", "highp", "precision", "const", "true", "false", "bool", "int", "float", "vec2", "vec3", "vec4", "mat2", "mat3", "mat4", "texture2D", "sample", "filter", "common", "partition", "active",
}

var precisions = []string{"lowp", "mediump", "highp"}

var typeWords = map[string]Type{"void": tVoid, "bool": tBool, "int": tInt, "float": tFloat, "vec2": tVec2, "vec3": tVec3, "vec4": tVec4, "mat2": tMat2, "mat3": tMat3, "mat4": tMat4}

var provided = map[string]struct {
	qualifier string
	t         string
	in        input
}{
	"v_texcoord": {"varying", "vec2", inTexcoord},
	"time":       {"uniform", "float", inTime},
	"resolution": {"uniform", "vec2", inResolution},
}

type parser struct {
	tokens    []token
	at        int
	program   *Program
	scopes    []map[string]any
	functions map[string]*function
	current   *function
	loops     int
	depth     int
	scale     int64
	work      int64
	sampler   bool
	wrote     bool
}

type inScope struct{ e expr }

func Compile(src []byte) (*Program, error) {
	if len(src) > maxSource {
		return nil, errorAt(1, 1, "the source is %d bytes, more than the %d allowed", len(src), maxSource)
	}
	tokens, blanks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: tokens, program: &Program{source: src, blanks: blanks}, functions: map[string]*function{}, scale: 1}
	p.push()
	if err := p.parseProgram(); err != nil {
		return nil, err
	}
	p.program.buildGLES()
	p.program.spirv = emitSPIRV(p.program)
	return p.program, nil
}

func (p *parser) tok() token { return p.tokens[p.at] }

func (p *parser) failAt(t token, format string, args ...any) error {
	return errorAt(t.line, t.col, format, args...)
}

func (p *parser) fail(format string, args ...any) error {
	return p.failAt(p.tok(), format, args...)
}

func (p *parser) is(text string) bool {
	t := p.tok()
	return (t.kind == tokPunct || t.kind == tokIdent) && t.text == text
}

func (p *parser) accept(text string) bool {
	if p.is(text) {
		p.at++
		return true
	}
	return false
}

func describe(t token) string {
	if t.kind == tokEOF {
		return "the end of the source"
	}
	return strconv.Quote(t.text)
}

func (p *parser) expect(text string) error {
	if !p.accept(text) {
		return p.fail("expected %q, found %s", text, describe(p.tok()))
	}
	return nil
}

func (p *parser) push() { p.scopes = append(p.scopes, map[string]any{}) }
func (p *parser) pop()  { p.scopes = p.scopes[:len(p.scopes)-1] }

func (p *parser) lookup(name string) (any, bool) {
	for _, s := range slices.Backward(p.scopes) {
		if v, ok := s[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (p *parser) identifier() (token, error) {
	t := p.tok()
	if t.kind != tokIdent {
		return t, p.fail("expected a name, found %s", describe(t))
	}
	switch {
	case slices.Contains(reserved, t.text):
		return t, p.fail("%q is a reserved word", t.text)
	case len(t.text) >= 3 && t.text[:3] == "gl_":
		return t, p.fail("%q: names starting gl_ are reserved", t.text)
	case builtins[t.text] != nil:
		return t, p.fail("%q is a built-in function", t.text)
	}
	p.at++
	return t, nil
}

func (p *parser) declare(t token, v any) error {
	s := p.scopes[len(p.scopes)-1]
	if _, ok := s[t.text]; ok {
		return p.failAt(t, "%q is already declared in this scope", t.text)
	}
	if _, ok := p.functions[t.text]; ok {
		return p.failAt(t, "%q is already a function", t.text)
	}
	s[t.text] = v
	return nil
}

func (p *parser) skipPrecision() {
	t := p.tok()
	if t.kind == tokIdent && slices.Contains(precisions, t.text) {
		p.program.blanks = append(p.program.blanks, span{t.from, t.to})
		p.at++
	}
}

func (p *parser) typeWord() (Type, bool) {
	t, ok := typeWords[p.tok().text]
	if !ok || p.tok().kind != tokIdent {
		return 0, false
	}
	p.at++
	return t, true
}

func (p *parser) parseProgram() error {
	for p.tok().kind != tokEOF {
		if err := p.topLevel(); err != nil {
			return err
		}
	}
	if p.program.main == nil {
		return p.fail("no void main() function")
	}
	if !p.wrote {
		return p.fail("nothing writes gl_FragColor")
	}
	if p.work > maxIterations {
		return p.fail("the loops run %d iterations per pixel, more than the %d allowed", p.work, maxIterations)
	}
	return nil
}

func (p *parser) topLevel() error {
	t := p.tok()
	switch {
	case p.is("precision"):
		return p.precisionStatement()
	case p.is("uniform"), p.is("varying"):
		return p.provided()
	case p.is("attribute"):
		return p.fail("a fragment shader has no attributes")
	case p.is("const"):
		p.at++
		return p.constDecl()
	case p.is("struct"):
		return p.fail("structs are not supported")
	}
	p.skipPrecision()
	ret, ok := p.typeWord()
	if !ok {
		return p.fail("expected a declaration, found %s", describe(t))
	}
	return p.functionDef(ret)
}

func (p *parser) precisionStatement() error {
	start := p.tok()
	p.at++
	if !p.accept("lowp") && !p.accept("mediump") && !p.accept("highp") {
		return p.fail("precision takes lowp, mediump or highp")
	}
	if !p.accept("float") && !p.accept("int") && !p.accept("sampler2D") {
		return p.fail("precision applies to float, int or sampler2D")
	}
	end := p.tok()
	if err := p.expect(";"); err != nil {
		return err
	}
	p.program.blanks = append(p.program.blanks, span{start.from, end.to})
	return nil
}

func (p *parser) provided() error {
	qualifier := p.tok().text
	p.at++
	p.skipPrecision()
	typeTok := p.tok()
	p.at++
	nameTok := p.tok()
	if nameTok.kind != tokIdent {
		return p.fail("expected a name, found %s", describe(nameTok))
	}
	p.at++
	if err := p.expect(";"); err != nil {
		return err
	}
	if qualifier == "uniform" && typeTok.text == "sampler2D" && nameTok.text == "tex" {
		p.sampler = true
		return nil
	}
	want, ok := provided[nameTok.text]
	if !ok || want.qualifier != qualifier || want.t != typeTok.text {
		return p.failAt(nameTok, "%s %s %s: lys-wm provides only uniform sampler2D tex, varying vec2 v_texcoord, uniform float time and uniform vec2 resolution", qualifier, typeTok.text, nameTok.text)
	}
	return p.declare(nameTok, inScope{&inputExpr{in: want.in}})
}

func (p *parser) constDecl() error {
	p.skipPrecision()
	t, ok := p.typeWord()
	if !ok || t == tVoid {
		return p.fail("expected a type, found %s", describe(p.tok()))
	}
	for {
		name, err := p.identifier()
		if err != nil {
			return err
		}
		if err := p.expect("="); err != nil {
			return err
		}
		at := p.tok()
		e, err := p.assignment()
		if err != nil {
			return err
		}
		if e.typ() != t {
			return p.failAt(at, "const %s %s initialised with %s", t, name.text, e.typ())
		}
		if !constant(e) {
			return p.failAt(at, "the initialiser of const %s is not constant", name.text)
		}
		if err := p.declare(name, inScope{e}); err != nil {
			return err
		}
		if !p.accept(",") {
			break
		}
	}
	return p.expect(";")
}

func (p *parser) functionDef(ret Type) error {
	name := p.tok()
	if name.kind != tokIdent {
		return p.fail("mutable globals are not supported; expected a function name, found %s", describe(name))
	}
	if name.text != "main" {
		if _, err := p.identifier(); err != nil {
			return err
		}
	} else {
		p.at++
	}
	if p.is(";") || p.is("=") || p.is(",") {
		return p.failAt(name, "global %s: only const globals are supported", name.text)
	}
	if err := p.expect("("); err != nil {
		return err
	}
	if _, ok := p.functions[name.text]; ok {
		return p.failAt(name, "function %q is already defined; overloads are not supported", name.text)
	}
	if _, ok := p.lookup(name.text); ok {
		return p.failAt(name, "%q is already declared", name.text)
	}
	fn := &function{name: name.text, ret: ret}
	p.push()
	defer p.pop()
	if err := p.parameters(fn); err != nil {
		return err
	}
	if p.is(";") {
		return p.fail("prototypes are not supported; define %s before calling it", name.text)
	}
	if name.text == "main" && (ret != tVoid || len(fn.params) != 0) {
		return p.failAt(name, "main takes nothing and returns void")
	}
	outerScale, outerWork := p.scale, p.work
	p.scale, p.work, p.current = 1, 0, fn
	body, err := p.block()
	if err != nil {
		return err
	}
	fn.body, fn.work = body, p.work
	p.scale, p.work, p.current = outerScale, outerWork, nil
	if name.text == "main" {
		p.work = fn.work
	}
	if ret != tVoid && !alwaysReturns(body) {
		return p.failAt(name, "%s does not return on every path", name.text)
	}
	p.functions[name.text] = fn
	p.program.functions = append(p.program.functions, fn)
	if name.text == "main" {
		p.program.main = fn
	}
	return nil
}

func alwaysReturns(body []stmt) bool {
	for _, s := range body {
		switch s := s.(type) {
		case *returnStmt:
			return true
		case *blockStmt:
			if alwaysReturns(s.body) {
				return true
			}
		case *ifStmt:
			if s.alt != nil && alwaysReturns(s.then) && alwaysReturns(s.alt) {
				return true
			}
		}
	}
	return false
}

func (p *parser) parameters(fn *function) error {
	if p.accept(")") {
		return nil
	}
	if p.is("void") && p.tokens[p.at+1].text == ")" {
		p.at += 2
		return nil
	}
	for {
		if p.is("out") || p.is("inout") {
			return p.fail("out and inout parameters are not supported; return a value")
		}
		p.accept("in")
		if p.accept("const") {
			p.accept("in")
		}
		p.skipPrecision()
		t, ok := p.typeWord()
		if !ok || t == tVoid {
			return p.fail("expected a parameter type, found %s", describe(p.tok()))
		}
		name, err := p.identifier()
		if err != nil {
			return err
		}
		v := &variable{name: name.text, t: t, param: true}
		if err := p.declare(name, v); err != nil {
			return err
		}
		fn.params = append(fn.params, v)
		if !p.accept(",") {
			break
		}
	}
	return p.expect(")")
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > maxDepth {
		return p.fail("nested more than %d levels deep", maxDepth)
	}
	return nil
}

func (p *parser) block() ([]stmt, error) {
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	p.push()
	defer p.pop()
	return p.statementsUntilBrace()
}

func (p *parser) statementsUntilBrace() ([]stmt, error) {
	var out []stmt
	for !p.accept("}") {
		if p.tok().kind == tokEOF {
			return nil, p.fail("a block that never ends")
		}
		more, err := p.statement()
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}

func (p *parser) scopedStatement() ([]stmt, error) {
	if p.is("{") {
		return p.block()
	}
	p.push()
	defer p.pop()
	return p.statement()
}

func (p *parser) statement() ([]stmt, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	t := p.tok()
	switch {
	case p.is("{"):
		body, err := p.block()
		return []stmt{&blockStmt{body}}, err
	case p.is(";"):
		p.at++
		return nil, nil
	case p.is("if"):
		return p.ifStatement()
	case p.is("for"):
		return p.forStatement()
	case p.is("while"), p.is("do"):
		return nil, p.fail("only for loops of constant length exist, so that a shader always ends")
	case p.is("discard"):
		return nil, p.fail("discard is not supported: an output's image has no holes")
	case p.is("break"), p.is("continue"):
		if p.loops == 0 {
			return nil, p.fail("%s outside a loop", t.text)
		}
		p.at++
		if err := p.expect(";"); err != nil {
			return nil, err
		}
		if t.text == "break" {
			return []stmt{&breakStmt{}}, nil
		}
		return []stmt{&continueStmt{}}, nil
	case p.is("return"):
		return p.returnStatement()
	case p.is("const"):
		p.at++
		return nil, p.constDecl()
	}
	p.skipPrecision()
	if vt, ok := typeWords[p.tok().text]; ok && p.tok().kind == tokIdent && p.tokens[p.at+1].kind == tokIdent {
		p.at++
		return p.localDecl(vt)
	}
	e, err := p.expression()
	if err != nil {
		return nil, err
	}
	if err := p.expect(";"); err != nil {
		return nil, err
	}
	return []stmt{&exprStmt{e}}, nil
}

func (p *parser) localDecl(t Type) ([]stmt, error) {
	if t == tVoid {
		return nil, p.fail("a variable cannot be void")
	}
	var out []stmt
	for {
		name, err := p.identifier()
		if err != nil {
			return nil, err
		}
		v := &variable{name: name.text, t: t}
		var init expr
		if p.accept("=") {
			at := p.tok()
			if init, err = p.assignment(); err != nil {
				return nil, err
			}
			if init.typ() != t {
				return nil, p.failAt(at, "%s %s initialised with %s", t, name.text, init.typ())
			}
		}
		if err := p.declare(name, v); err != nil {
			return nil, err
		}
		out = append(out, &declStmt{v, init})
		if !p.accept(",") {
			break
		}
	}
	return out, p.expect(";")
}

func (p *parser) ifStatement() ([]stmt, error) {
	p.at++
	cond, err := p.condition()
	if err != nil {
		return nil, err
	}
	then, err := p.scopedStatement()
	if err != nil {
		return nil, err
	}
	s := &ifStmt{cond: cond, then: then}
	if p.accept("else") {
		if s.alt, err = p.scopedStatement(); err != nil {
			return nil, err
		}
		if s.alt == nil {
			s.alt = []stmt{}
		}
	}
	return []stmt{s}, nil
}

func (p *parser) condition() (expr, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	at := p.tok()
	cond, err := p.expression()
	if err != nil {
		return nil, err
	}
	if cond.typ() != tBool {
		return nil, p.failAt(at, "a condition must be bool, not %s", cond.typ())
	}
	return cond, p.expect(")")
}

func (p *parser) returnStatement() ([]stmt, error) {
	t := p.tok()
	p.at++
	if p.accept(";") {
		if p.current.ret != tVoid {
			return nil, p.failAt(t, "%s must return %s", p.current.name, p.current.ret)
		}
		return []stmt{&returnStmt{}}, nil
	}
	e, err := p.expression()
	if err != nil {
		return nil, err
	}
	if e.typ() != p.current.ret {
		return nil, p.failAt(t, "%s returns %s, not %s", p.current.name, p.current.ret, e.typ())
	}
	return []stmt{&returnStmt{e}}, p.expect(";")
}

func constNumber(e expr) (float64, bool) {
	if !constant(e) {
		return 0, false
	}
	switch e.typ() {
	case tInt:
		return float64(asInt(evalConstant(e)[0])), true
	case tFloat:
		return float64(evalConstant(e)[0]), true
	}
	return 0, false
}

func loopCompare(op string, a, b float64) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	case "==":
		return a == b
	}
	return a != b
}

func (p *parser) forStatement() ([]stmt, error) {
	head := p.tok()
	p.at++
	if err := p.expect("("); err != nil {
		return nil, err
	}
	p.push()
	defer p.pop()
	p.skipPrecision()
	t, ok := p.typeWord()
	if !ok || t != tInt && t != tFloat {
		return nil, p.fail("a loop starts by declaring an int or float index: for (int i = 0; i < 8; i++)")
	}
	name, err := p.identifier()
	if err != nil {
		return nil, err
	}
	if err := p.expect("="); err != nil {
		return nil, err
	}
	at := p.tok()
	init, err := p.assignment()
	if err != nil {
		return nil, err
	}
	start, isConst := constNumber(init)
	if !isConst || init.typ() != t {
		return nil, p.failAt(at, "a loop index starts at a constant %s", t)
	}
	v := &variable{name: name.text, t: t, loop: true}
	if err := p.declare(name, v); err != nil {
		return nil, err
	}
	if err := p.expect(";"); err != nil {
		return nil, err
	}
	cond, op, limit, err := p.loopCondition(v)
	if err != nil {
		return nil, err
	}
	if err := p.expect(";"); err != nil {
		return nil, err
	}
	at2 := p.tok()
	stepE, delta, err := p.loopStep(v)
	if err != nil {
		return nil, p.failAt(at2, "%v", err)
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	iterations := int64(0)
	for x := start; loopCompare(op, x, limit); x += delta {
		iterations++
		if iterations > maxIterations {
			return nil, p.failAt(head, "this loop runs more than %d iterations", maxIterations)
		}
	}
	p.work += iterations * p.scale
	outer := p.scale
	p.scale *= max(iterations, 1)
	p.loops++
	body, err := p.scopedStatement()
	p.loops--
	p.scale = outer
	if err != nil {
		return nil, err
	}
	if p.work > maxIterations {
		return nil, p.failAt(head, "the loops run %d iterations per pixel, more than the %d allowed", p.work, maxIterations)
	}
	return []stmt{&forStmt{v: v, init: init, cond: cond, step: stepE, body: body}}, nil
}

func (p *parser) loopCondition(v *variable) (expr, string, float64, error) {
	if p.tok().text != v.name {
		return nil, "", 0, p.fail("a loop condition compares the index %s with a constant", v.name)
	}
	p.at++
	op := p.tok().text
	if !slices.Contains([]string{"<", "<=", ">", ">=", "==", "!="}, op) {
		return nil, "", 0, p.fail("a loop condition compares the index %s with a constant", v.name)
	}
	p.at++
	at := p.tok()
	limit, err := p.additive()
	if err != nil {
		return nil, "", 0, err
	}
	n, ok := constNumber(limit)
	if !ok || limit.typ() != v.t {
		return nil, "", 0, p.failAt(at, "a loop condition compares the index %s with a constant %s", v.name, v.t)
	}
	return &binaryExpr{op: op, a: &varExpr{v}, b: limit, t: tBool}, op, n, nil
}

func (p *parser) loopStep(v *variable) (expr, float64, error) {
	if p.accept("++") || p.accept("--") {
		delta := float32(1)
		if p.tokens[p.at-1].text == "--" {
			delta = -1
		}
		if p.tok().text != v.name {
			return nil, 0, fmt.Errorf("a loop steps its index %s", v.name)
		}
		p.at++
		return &stepExpr{target: &varExpr{v}, delta: delta}, float64(delta), nil
	}
	if p.tok().text != v.name {
		return nil, 0, fmt.Errorf("a loop steps its index %s", v.name)
	}
	p.at++
	switch {
	case p.accept("++"):
		return &stepExpr{target: &varExpr{v}, delta: 1, post: true}, 1, nil
	case p.accept("--"):
		return &stepExpr{target: &varExpr{v}, delta: -1, post: true}, -1, nil
	}
	op := p.tok().text
	if op != "+=" && op != "-=" {
		return nil, 0, errors.New("a loop steps its index with ++, --, += or -=")
	}
	p.at++
	amount, err := p.additive()
	if err != nil {
		return nil, 0, err
	}
	n, ok := constNumber(amount)
	if !ok || amount.typ() != v.t {
		return nil, 0, fmt.Errorf("a loop steps its index by a constant %s", v.t)
	}
	if op == "-=" {
		n = -n
	}
	if n == 0 {
		return nil, 0, errors.New("a loop that does not move its index never ends")
	}
	return &assignExpr{target: &varExpr{v}, value: &binaryExpr{op: op[:1], a: &varExpr{v}, b: amount, t: v.t}}, n, nil
}

func (p *parser) expression() (expr, error) {
	return p.assignment()
}

func (p *parser) lvalue(at token, e expr) error {
	switch e := e.(type) {
	case *varExpr:
		if e.v.loop {
			return p.failAt(at, "the loop index %s cannot be assigned in its loop", e.v.name)
		}
		return nil
	case *inputExpr:
		if e.in == inFragColor {
			p.wrote = true
			return nil
		}
	case *swizzleExpr:
		seen := map[int]bool{}
		for _, i := range e.idx {
			if seen[i] {
				return p.failAt(at, "a swizzle with a repeated component cannot be assigned")
			}
			seen[i] = true
		}
		return p.lvalue(at, e.x)
	case *indexExpr:
		return p.lvalue(at, e.x)
	}
	return p.failAt(at, "this cannot be assigned to")
}

func (p *parser) assignment() (expr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	at := p.tok()
	left, err := p.ternary()
	if err != nil {
		return nil, err
	}
	op := p.tok().text
	if p.tok().kind != tokPunct || !slices.Contains([]string{"=", "+=", "-=", "*=", "/="}, op) {
		return left, nil
	}
	if err := p.lvalue(at, left); err != nil {
		return nil, err
	}
	p.at++
	opTok := p.tok()
	right, err := p.assignment()
	if err != nil {
		return nil, err
	}
	if op == "=" {
		if right.typ() != left.typ() {
			return nil, p.failAt(opTok, "assigning %s to %s", right.typ(), left.typ())
		}
		return &assignExpr{target: left, value: right}, nil
	}
	combined, err := p.arithmetic(opTok, op[:1], left, right)
	if err != nil {
		return nil, err
	}
	if combined.typ() != left.typ() {
		return nil, p.failAt(opTok, "%s %s %s is %s, not %s", left.typ(), op, right.typ(), combined.typ(), left.typ())
	}
	return &assignExpr{target: left, value: combined}, nil
}

func (p *parser) ternary() (expr, error) {
	at := p.tok()
	c, err := p.logical(0)
	if err != nil {
		return nil, err
	}
	if !p.accept("?") {
		return c, nil
	}
	if c.typ() != tBool {
		return nil, p.failAt(at, "the condition of ?: must be bool, not %s", c.typ())
	}
	a, err := p.assignment()
	if err != nil {
		return nil, err
	}
	if err := p.expect(":"); err != nil {
		return nil, err
	}
	b, err := p.assignment()
	if err != nil {
		return nil, err
	}
	if a.typ() != b.typ() {
		return nil, p.failAt(at, "the arms of ?: are %s and %s", a.typ(), b.typ())
	}
	return &ternaryExpr{c, a, b}, nil
}

var logicalLevels = []string{"||", "^^", "&&"}

func (p *parser) logical(level int) (expr, error) {
	if level == len(logicalLevels) {
		return p.equality()
	}
	left, err := p.logical(level + 1)
	if err != nil {
		return nil, err
	}
	for p.is(logicalLevels[level]) {
		at := p.tok()
		p.at++
		right, err := p.logical(level + 1)
		if err != nil {
			return nil, err
		}
		if left.typ() != tBool || right.typ() != tBool {
			return nil, p.failAt(at, "%s takes bools, not %s and %s", at.text, left.typ(), right.typ())
		}
		left = &logicalExpr{at.text, left, right}
	}
	return left, nil
}

func (p *parser) equality() (expr, error) {
	left, err := p.relational()
	if err != nil {
		return nil, err
	}
	for p.is("==") || p.is("!=") {
		at := p.tok()
		p.at++
		right, err := p.relational()
		if err != nil {
			return nil, err
		}
		if left.typ() != right.typ() {
			return nil, p.failAt(at, "comparing %s with %s", left.typ(), right.typ())
		}
		left = &binaryExpr{op: at.text, a: left, b: right, t: tBool}
	}
	return left, nil
}

func (p *parser) relational() (expr, error) {
	left, err := p.additive()
	if err != nil {
		return nil, err
	}
	for p.is("<") || p.is(">") || p.is("<=") || p.is(">=") {
		at := p.tok()
		p.at++
		right, err := p.additive()
		if err != nil {
			return nil, err
		}
		if left.typ() != right.typ() || left.typ() != tInt && left.typ() != tFloat {
			return nil, p.failAt(at, "%s compares two ints or two floats, not %s and %s", at.text, left.typ(), right.typ())
		}
		left = &binaryExpr{op: at.text, a: left, b: right, t: tBool}
	}
	return left, nil
}

func (p *parser) additive() (expr, error) {
	left, err := p.multiplicative()
	if err != nil {
		return nil, err
	}
	for p.is("+") || p.is("-") {
		at := p.tok()
		p.at++
		right, err := p.multiplicative()
		if err != nil {
			return nil, err
		}
		if left, err = p.arithmetic(at, at.text, left, right); err != nil {
			return nil, err
		}
	}
	return left, nil
}

func (p *parser) multiplicative() (expr, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.is("*") || p.is("/") {
		at := p.tok()
		p.at++
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		if left, err = p.arithmetic(at, at.text, left, right); err != nil {
			return nil, err
		}
	}
	return left, nil
}

func (p *parser) arithmetic(at token, op string, a, b expr) (expr, error) {
	ta, tb := a.typ(), b.typ()
	result := func(t Type) (expr, error) { return &binaryExpr{op: op, a: a, b: b, t: t}, nil }
	switch {
	case ta == tInt && tb == tInt:
		return result(tInt)
	case !ta.floating() || !tb.floating():
		return nil, p.failAt(at, "%s %s %s: there are no implicit conversions; write 2.0 or float(n)", ta, op, tb)
	case ta == tb && (op != "*" || !ta.matrix()):
		return result(ta)
	case ta == tFloat:
		return result(tb)
	case tb == tFloat:
		return result(ta)
	case op != "*":
	case ta.matrix() && tb == ta:
		return result(ta)
	case ta.matrix() && tb == ta.column():
		return result(tb)
	case ta.vector() && tb.matrix() && ta == tb.column():
		return result(ta)
	}
	return nil, p.failAt(at, "%s %s %s has no meaning", ta, op, tb)
}

func (p *parser) unary() (expr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	at := p.tok()
	switch {
	case p.accept("+"):
		return p.signed(at, p.unary)
	case p.accept("-"):
		x, err := p.signed(at, p.unary)
		if err != nil {
			return nil, err
		}
		return &unaryExpr{"-", x}, nil
	case p.accept("!"):
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		if x.typ() != tBool {
			return nil, p.failAt(at, "! takes a bool, not %s", x.typ())
		}
		return &unaryExpr{"!", x}, nil
	case p.accept("++"), p.accept("--"):
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return p.stepped(at, x, false)
	}
	return p.postfix()
}

func (p *parser) signed(at token, next func() (expr, error)) (expr, error) {
	x, err := next()
	if err != nil {
		return nil, err
	}
	if x.typ() == tBool {
		return nil, p.failAt(at, "a sign on a bool")
	}
	return x, nil
}

func (p *parser) stepped(at token, x expr, post bool) (expr, error) {
	if err := p.lvalue(at, x); err != nil {
		return nil, err
	}
	if x.typ() == tBool {
		return nil, p.failAt(at, "%s on a bool", at.text)
	}
	delta := float32(1)
	if at.text == "--" {
		delta = -1
	}
	return &stepExpr{target: x, delta: delta, post: post}, nil
}

var swizzleSets = []string{"xyzw", "rgba", "stpq"}

func (p *parser) swizzle(at token, x expr, text string) (expr, error) {
	t := x.typ()
	if !t.vector() {
		return nil, p.failAt(at, "%s has no field %s", t, text)
	}
	if len(text) > 4 {
		return nil, p.failAt(at, "swizzle %s is longer than a vec4", text)
	}
	for _, set := range swizzleSets {
		var idx []int
		for i := range text {
			at := slices.Index([]byte(set), text[i])
			if at < 0 {
				idx = nil
				break
			}
			idx = append(idx, at)
		}
		if idx == nil {
			continue
		}
		for _, i := range idx {
			if i >= t.size() {
				return nil, p.failAt(at, "%s has no component %c", t, set[i])
			}
		}
		return &swizzleExpr{x, idx}, nil
	}
	return nil, p.failAt(at, "%s is no swizzle", text)
}

func (p *parser) postfix() (expr, error) {
	x, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		at := p.tok()
		switch {
		case p.accept("."):
			field := p.tok()
			if field.kind != tokIdent {
				return nil, p.fail("expected a swizzle, found %s", describe(field))
			}
			p.at++
			if x, err = p.swizzle(field, x, field.text); err != nil {
				return nil, err
			}
		case p.accept("["):
			i := p.tok()
			index, err := p.expression()
			if err != nil {
				return nil, err
			}
			n, ok := constNumber(index)
			if !ok || index.typ() != tInt {
				return nil, p.failAt(i, "an index is a constant int, as GLSL ES 1.00 guarantees nothing else")
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			limit := x.typ().columns()
			if x.typ().vector() {
				limit = x.typ().size()
			}
			if !x.typ().vector() && !x.typ().matrix() {
				return nil, p.failAt(at, "%s cannot be indexed", x.typ())
			}
			if n < 0 || int(n) >= limit {
				return nil, p.failAt(i, "index %v outside %s", n, x.typ())
			}
			x = &indexExpr{x, int(n)}
		case p.is("++"), p.is("--"):
			p.at++
			if x, err = p.stepped(at, x, true); err != nil {
				return nil, err
			}
		default:
			return x, nil
		}
	}
}

func parseFloat(t token) (float32, error) {
	v, err := strconv.ParseFloat(t.text, 32)
	if err != nil {
		return 0, errorAt(t.line, t.col, "the number %s does not fit a float", t.text)
	}
	return float32(v), nil
}

func (p *parser) primary() (expr, error) {
	t := p.tok()
	switch t.kind {
	case tokInt:
		p.at++
		v, err := strconv.ParseInt(t.text, 10, 32)
		if err != nil {
			return nil, p.failAt(t, "the number %s does not fit an int", t.text)
		}
		return &literal{t: tInt, i: int32(v)}, nil
	case tokFloat:
		p.at++
		v, err := parseFloat(t)
		if err != nil {
			return nil, err
		}
		return &literal{t: tFloat, f: v}, nil
	case tokEOF:
		return nil, p.fail("expected an expression, found the end of the source")
	case tokPunct:
		if p.accept("(") {
			e, err := p.expression()
			if err != nil {
				return nil, err
			}
			return e, p.expect(")")
		}
		return nil, p.fail("expected an expression, found %s", describe(t))
	case tokIdent:
	}
	p.at++
	switch t.text {
	case "true", "false":
		return &literal{t: tBool, b: t.text == "true"}, nil
	case "gl_FragCoord":
		return &inputExpr{in: inFragCoord}, nil
	case "gl_FragColor":
		return &inputExpr{in: inFragColor}, nil
	case "gl_FragData", "gl_FrontFacing", "gl_PointCoord":
		return nil, p.failAt(t, "%s is not supported", t.text)
	}
	if vt, ok := typeWords[t.text]; ok {
		return p.construct(t, vt)
	}
	if t.text == "texture2D" {
		return p.sampleCall(t)
	}
	if p.is("(") {
		return p.call(t)
	}
	v, ok := p.lookup(t.text)
	if !ok {
		if t.text == "tex" {
			return nil, p.failAt(t, "tex passes only as texture2D's first argument")
		}
		if _, isProvided := provided[t.text]; isProvided {
			return nil, p.failAt(t, "%s is used without its declaration: %s %s %s;", t.text, provided[t.text].qualifier, provided[t.text].t, t.text)
		}
		return nil, p.failAt(t, "%s is not declared", t.text)
	}
	switch v := v.(type) {
	case *variable:
		return &varExpr{v}, nil
	case inScope:
		if in, ok := v.e.(*inputExpr); ok && in.in == inTime {
			p.program.usesTime = true
		}
		return v.e, nil
	}
	return nil, p.failAt(t, "%s cannot be used here", t.text)
}

func (p *parser) arguments() ([]expr, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	var args []expr
	if p.accept(")") {
		return nil, nil
	}
	for {
		a, err := p.assignment()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if !p.accept(",") {
			break
		}
	}
	return args, p.expect(")")
}

func (p *parser) sampleCall(t token) (expr, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	if !p.is("tex") {
		return nil, p.fail("texture2D samples tex, the output's image")
	}
	if !p.sampler {
		return nil, p.fail("tex is used without its declaration: uniform sampler2D tex;")
	}
	p.at++
	if err := p.expect(","); err != nil {
		return nil, err
	}
	uv, err := p.assignment()
	if err != nil {
		return nil, err
	}
	if uv.typ() != tVec2 {
		return nil, p.failAt(t, "texture2D takes a vec2 coordinate, not %s", uv.typ())
	}
	return &sampleExpr{uv}, p.expect(")")
}

func (p *parser) construct(at token, t Type) (expr, error) {
	if t == tVoid {
		return nil, p.failAt(at, "void is not a value")
	}
	if !p.is("(") {
		return nil, p.failAt(at, "expected %s(...)", t)
	}
	args, err := p.arguments()
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, p.failAt(at, "%s() takes arguments", t)
	}
	total := 0
	for _, a := range args {
		if a.typ() == tVoid {
			return nil, p.failAt(at, "a void argument to %s", t)
		}
		if a.typ().matrix() && len(args) > 1 {
			return nil, p.failAt(at, "a matrix argument to %s stands alone", t)
		}
		total += a.typ().size()
	}
	switch {
	case len(args) == 1 && (args[0].typ().size() == 1 || t.matrix() && args[0].typ().matrix()):
	case len(args) == 1 && t.size() <= total && !t.matrix() && !args[0].typ().matrix():
	case total != t.size():
		return nil, p.failAt(at, "%s takes %d components, not %d", t, t.size(), total)
	}
	return &constructExpr{t, args}, nil
}

func (p *parser) call(t token) (expr, error) {
	if b, ok := builtins[t.text]; ok {
		args, err := p.arguments()
		if err != nil {
			return nil, err
		}
		return p.builtinCall(t, b, args)
	}
	fn, ok := p.functions[t.text]
	if !ok {
		if p.current != nil && p.current.name == t.text {
			return nil, p.failAt(t, "%s calls itself; recursion is not supported", t.text)
		}
		return nil, p.failAt(t, "function %s is not defined above this call", t.text)
	}
	if fn.name == "main" {
		return nil, p.failAt(t, "main cannot be called")
	}
	args, err := p.arguments()
	if err != nil {
		return nil, err
	}
	if len(args) != len(fn.params) {
		return nil, p.failAt(t, "%s takes %d arguments, not %d", t.text, len(fn.params), len(args))
	}
	for i, a := range args {
		if a.typ() != fn.params[i].t {
			return nil, p.failAt(t, "argument %d of %s is %s, not %s", i+1, t.text, a.typ(), fn.params[i].t)
		}
	}
	p.work += fn.work * p.scale
	return &callExpr{fn, args}, nil
}

func gen(t Type) bool {
	return t == tFloat || t.vector()
}

func (p *parser) builtinCall(at token, b *builtin, args []expr) (expr, error) {
	bad := func(want string) (expr, error) {
		return nil, p.failAt(at, "%s takes %s", b.name, want)
	}
	result := func(t Type) (expr, error) { return &builtinExpr{b, args, t}, nil }
	genOrFloat := func(a, x Type) bool { return a == x || a == tFloat }
	types := make([]Type, len(args))
	for i, a := range args {
		types[i] = a.typ()
	}
	if b.name == "atan" {
		switch {
		case len(args) == 1 && gen(types[0]):
			return result(types[0])
		case len(args) == 2 && gen(types[0]) && types[0] == types[1]:
			return result(types[0])
		}
		return bad("one float or vector, or two of the same type")
	}
	switch b.sig {
	case sigGen:
		if len(args) != 1 || !gen(types[0]) {
			return bad("a float or vector")
		}
		return result(types[0])
	case sigGen2:
		if len(args) != 2 || !gen(types[0]) || types[0] != types[1] {
			return bad("two floats or vectors of the same type")
		}
		return result(types[0])
	case sigGenFloat:
		if len(args) != 2 || !gen(types[0]) || !genOrFloat(types[1], types[0]) {
			return bad("a float or vector and another of its type or a float")
		}
		return result(types[0])
	case sigClamp:
		if len(args) != 3 || !gen(types[0]) || !genOrFloat(types[1], types[0]) || types[1] != types[2] {
			return bad("a float or vector and two limits of its type or floats")
		}
		return result(types[0])
	case sigMix:
		if len(args) != 3 || !gen(types[0]) || types[0] != types[1] || !genOrFloat(types[2], types[0]) {
			return bad("two floats or vectors of the same type and a weight of that type or a float")
		}
		return result(types[0])
	case sigStep:
		if len(args) != 2 || !gen(types[1]) || !genOrFloat(types[0], types[1]) {
			return bad("an edge, of the value's type or a float, and a value")
		}
		return result(types[1])
	case sigSmooth:
		if len(args) != 3 || !gen(types[2]) || !genOrFloat(types[0], types[2]) || types[0] != types[1] {
			return bad("two edges, of the value's type or floats, and a value")
		}
		return result(types[2])
	case sigLength:
		if len(args) != 1 || !gen(types[0]) {
			return bad("a float or vector")
		}
		return result(tFloat)
	case sigDistance:
		if len(args) != 2 || !gen(types[0]) || types[0] != types[1] {
			return bad("two floats or vectors of the same type")
		}
		return result(tFloat)
	case sigCross:
		if len(args) != 2 || types[0] != tVec3 || types[1] != tVec3 {
			return bad("two vec3s")
		}
		return result(tVec3)
	case sigFace:
		if len(args) != 3 || !gen(types[0]) || types[0] != types[1] || types[0] != types[2] {
			return bad("three floats or vectors of the same type")
		}
		return result(types[0])
	case sigRefract:
		if len(args) != 3 || !gen(types[0]) || types[0] != types[1] || types[2] != tFloat {
			return bad("two floats or vectors of the same type and a float")
		}
		return result(types[0])
	case sigMatrix:
		if len(args) != 2 || !types[0].matrix() || types[0] != types[1] {
			return bad("two matrices of the same type")
		}
		return result(types[0])
	}
	return bad("something else")
}
