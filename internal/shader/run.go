package shader

import (
	"math"
)

const (
	slotFragCoord  = 0
	slotFragColor  = 4
	slotTexcoord   = 8
	slotTime       = 10
	slotResolution = 11
	slotFirst      = 13
)

func asInt(f float32) int32   { return int32(math.Float32bits(f)) }      //nolint:gosec // an int register holds its bits
func fromInt(i int32) float32 { return math.Float32frombits(uint32(i)) } //nolint:gosec // an int register holds its bits

func fromBool(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

func toInt(f float32) int32 {
	switch {
	case f != f:
		return 0
	case f >= math.MaxInt32:
		return math.MaxInt32
	case f <= math.MinInt32:
		return math.MinInt32
	}
	return int32(f)
}

type machine struct {
	r      []float32
	sample func(u, v float32) [4]float32
}

type op func(m *machine)

type signal uint8

const (
	flowNext signal = iota
	flowBreak
	flowContinue
	flowReturn
)

type stmtOp func(m *machine) signal

func run(code []op, m *machine) {
	for _, f := range code {
		f(m)
	}
}

func runBlock(body []stmtOp, m *machine) signal {
	for _, s := range body {
		if flow := s(m); flow != flowNext {
			return flow
		}
	}
	return flowNext
}

type compiled struct {
	params []int
	ret    int
	body   []stmtOp
}

type builder struct {
	init  []float32
	vars  map[*variable]int
	funcs map[*function]*compiled
	cur   *compiled
}

func newBuilder() *builder {
	return &builder{init: make([]float32, slotFirst), vars: map[*variable]int{}, funcs: map[*function]*compiled{}}
}

func (b *builder) alloc(n int) int {
	at := len(b.init)
	b.init = append(b.init, make([]float32, n)...)
	return at
}

func (b *builder) slotOf(v *variable) int {
	if s, ok := b.vars[v]; ok {
		return s
	}
	s := b.alloc(v.t.size())
	b.vars[v] = s
	return s
}

func copySlots(dst, src, n int) op {
	if n == 1 {
		return func(m *machine) { m.r[dst] = m.r[src] }
	}
	return func(m *machine) { copy(m.r[dst:dst+n], m.r[src:src+n]) }
}

func (b *builder) owned(e expr, code *[]op) int {
	s := b.expr(e, code)
	n := e.typ().size()
	dst := b.alloc(n)
	*code = append(*code, copySlots(dst, s, n))
	return dst
}

func (b *builder) expr(e expr, code *[]op) int {
	switch e := e.(type) {
	case *literal:
		at := b.alloc(1)
		switch e.t {
		case tFloat:
			b.init[at] = e.f
		case tInt:
			b.init[at] = fromInt(e.i)
		default:
			b.init[at] = fromBool(e.b)
		}
		return at
	case *varExpr:
		return b.slotOf(e.v)
	case *inputExpr:
		return [...]int{inFragCoord: slotFragCoord, inFragColor: slotFragColor, inTexcoord: slotTexcoord, inTime: slotTime, inResolution: slotResolution}[e.in]
	case *swizzleExpr:
		return b.swizzle(e, code)
	case *indexExpr:
		at := b.expr(e.x, code)
		if e.x.typ().matrix() {
			return at + e.i*e.x.typ().columns()
		}
		return at + e.i
	case *unaryExpr:
		return b.unary(e, code)
	case *binaryExpr:
		return b.binary(e, code)
	case *logicalExpr:
		return b.logical(e, code)
	case *ternaryExpr:
		return b.ternary(e, code)
	case *assignExpr:
		return b.assign(e, code)
	case *stepExpr:
		return b.stepped(e, code)
	case *constructExpr:
		return b.construct(e, code)
	case *callExpr:
		return b.call(e, code)
	case *builtinExpr:
		return b.builtin(e, code)
	case *sampleExpr:
		uv := b.expr(e.uv, code)
		dst := b.alloc(4)
		*code = append(*code, func(m *machine) {
			c := m.sample(m.r[uv], m.r[uv+1])
			m.r[dst], m.r[dst+1], m.r[dst+2], m.r[dst+3] = c[0], c[1], c[2], c[3]
		})
		return dst
	}
	panic("shader: an expression the compiler does not know")
}

func (b *builder) swizzle(e *swizzleExpr, code *[]op) int {
	at := b.expr(e.x, code)
	contiguous := true
	for i := 1; i < len(e.idx); i++ {
		contiguous = contiguous && e.idx[i] == e.idx[i-1]+1
	}
	if contiguous {
		return at + e.idx[0]
	}
	dst := b.alloc(len(e.idx))
	idx := e.idx
	*code = append(*code, func(m *machine) {
		var tmp [4]float32
		for i, k := range idx {
			tmp[i] = m.r[at+k]
		}
		copy(m.r[dst:], tmp[:len(idx)])
	})
	return dst
}

func (b *builder) unary(e *unaryExpr, code *[]op) int {
	x := b.expr(e.x, code)
	n := e.x.typ().size()
	dst := b.alloc(n)
	switch {
	case e.op == "!":
		*code = append(*code, func(m *machine) { m.r[dst] = 1 - m.r[x] })
	case e.x.typ() == tInt:
		*code = append(*code, func(m *machine) { m.r[dst] = fromInt(-asInt(m.r[x])) })
	default:
		*code = append(*code, func(m *machine) {
			for i := range n {
				m.r[dst+i] = -m.r[x+i]
			}
		})
	}
	return dst
}

func stepOf(t Type) int {
	if t == tFloat {
		return 0
	}
	return 1
}

func (b *builder) binary(e *binaryExpr, code *[]op) int {
	x := b.expr(e.a, code)
	if effects(e.b) {
		copied := b.alloc(e.a.typ().size())
		*code = append(*code, copySlots(copied, x, e.a.typ().size()))
		x = copied
	}
	y := b.expr(e.b, code)
	ta, tb := e.a.typ(), e.b.typ()
	if e.t == tBool {
		return b.compare(e.op, ta, x, y, code)
	}
	dst := b.alloc(e.t.size())
	if e.t == tInt {
		*code = append(*code, intOp(e.op, dst, x, y))
		return dst
	}
	if e.op == "*" && (ta.matrix() && tb != tFloat || tb.matrix() && ta != tFloat) {
		*code = append(*code, linear(ta, tb, dst, x, y))
		return dst
	}
	*code = append(*code, floatOp(e.op, e.t.size(), dst, x, stepOf(ta), y, stepOf(tb)))
	return dst
}

func intOp(name string, dst, x, y int) op {
	switch name {
	case "+":
		return func(m *machine) { m.r[dst] = fromInt(asInt(m.r[x]) + asInt(m.r[y])) }
	case "-":
		return func(m *machine) { m.r[dst] = fromInt(asInt(m.r[x]) - asInt(m.r[y])) }
	case "*":
		return func(m *machine) { m.r[dst] = fromInt(asInt(m.r[x]) * asInt(m.r[y])) }
	}
	return func(m *machine) {
		if d := asInt(m.r[y]); d != 0 {
			m.r[dst] = fromInt(asInt(m.r[x]) / d)
		} else {
			m.r[dst] = 0
		}
	}
}

func floatOp(name string, n, dst, x, sx, y, sy int) op {
	switch name {
	case "+":
		return func(m *machine) {
			for i := range n {
				m.r[dst+i] = m.r[x+i*sx] + m.r[y+i*sy]
			}
		}
	case "-":
		return func(m *machine) {
			for i := range n {
				m.r[dst+i] = m.r[x+i*sx] - m.r[y+i*sy]
			}
		}
	case "*":
		return func(m *machine) {
			for i := range n {
				m.r[dst+i] = float32(m.r[x+i*sx] * m.r[y+i*sy])
			}
		}
	}
	return func(m *machine) {
		for i := range n {
			m.r[dst+i] = m.r[x+i*sx] / m.r[y+i*sy]
		}
	}
}

func linear(ta, tb Type, dst, x, y int) op {
	dim := max(ta.columns(), tb.columns())
	return func(m *machine) {
		var tmp [16]float32
		switch {
		case ta.matrix() && tb.matrix():
			for c := range dim {
				for r := range dim {
					var sum float32
					for k := range dim {
						sum += float32(m.r[x+k*dim+r] * m.r[y+c*dim+k])
					}
					tmp[c*dim+r] = sum
				}
			}
			copy(m.r[dst:dst+dim*dim], tmp[:dim*dim])
		case ta.matrix():
			for r := range dim {
				var sum float32
				for k := range dim {
					sum += float32(m.r[x+k*dim+r] * m.r[y+k])
				}
				tmp[r] = sum
			}
			copy(m.r[dst:dst+dim], tmp[:dim])
		default:
			for c := range dim {
				var sum float32
				for k := range dim {
					sum += float32(m.r[x+k] * m.r[y+c*dim+k])
				}
				tmp[c] = sum
			}
			copy(m.r[dst:dst+dim], tmp[:dim])
		}
	}
}

func (b *builder) compare(name string, t Type, x, y int, code *[]op) int {
	dst := b.alloc(1)
	if t == tInt {
		switch name {
		case "<":
			*code = append(*code, func(m *machine) { m.r[dst] = fromBool(asInt(m.r[x]) < asInt(m.r[y])) })
			return dst
		case "<=":
			*code = append(*code, func(m *machine) { m.r[dst] = fromBool(asInt(m.r[x]) <= asInt(m.r[y])) })
			return dst
		case ">":
			*code = append(*code, func(m *machine) { m.r[dst] = fromBool(asInt(m.r[x]) > asInt(m.r[y])) })
			return dst
		case ">=":
			*code = append(*code, func(m *machine) { m.r[dst] = fromBool(asInt(m.r[x]) >= asInt(m.r[y])) })
			return dst
		case "==":
			*code = append(*code, func(m *machine) { m.r[dst] = fromBool(asInt(m.r[x]) == asInt(m.r[y])) })
			return dst
		}
		*code = append(*code, func(m *machine) { m.r[dst] = fromBool(asInt(m.r[x]) != asInt(m.r[y])) })
		return dst
	}
	switch name {
	case "<":
		*code = append(*code, func(m *machine) { m.r[dst] = fromBool(m.r[x] < m.r[y]) })
	case "<=":
		*code = append(*code, func(m *machine) { m.r[dst] = fromBool(m.r[x] <= m.r[y]) })
	case ">":
		*code = append(*code, func(m *machine) { m.r[dst] = fromBool(m.r[x] > m.r[y]) })
	case ">=":
		*code = append(*code, func(m *machine) { m.r[dst] = fromBool(m.r[x] >= m.r[y]) })
	default:
		n, want := t.size(), name == "=="
		*code = append(*code, func(m *machine) {
			equal := true
			for i := range n {
				equal = equal && m.r[x+i] == m.r[y+i]
			}
			m.r[dst] = fromBool(equal == want)
		})
	}
	return dst
}

func (b *builder) logical(e *logicalExpr, code *[]op) int {
	x := b.expr(e.a, code)
	dst := b.alloc(1)
	var right []op
	y := b.expr(e.b, &right)
	switch e.op {
	case "&&":
		*code = append(*code, func(m *machine) {
			m.r[dst] = 0
			if m.r[x] != 0 {
				run(right, m)
				m.r[dst] = m.r[y]
			}
		})
	case "||":
		*code = append(*code, func(m *machine) {
			m.r[dst] = 1
			if m.r[x] == 0 {
				run(right, m)
				m.r[dst] = m.r[y]
			}
		})
	default:
		*code = append(*code, func(m *machine) {
			left := m.r[x]
			run(right, m)
			m.r[dst] = fromBool(left != m.r[y])
		})
	}
	return dst
}

func (b *builder) ternary(e *ternaryExpr, code *[]op) int {
	c := b.expr(e.c, code)
	n := e.typ().size()
	dst := b.alloc(n)
	var yes, no []op
	y := b.expr(e.a, &yes)
	z := b.expr(e.b, &no)
	*code = append(*code, func(m *machine) {
		if m.r[c] != 0 {
			run(yes, m)
			copy(m.r[dst:dst+n], m.r[y:y+n])
		} else {
			run(no, m)
			copy(m.r[dst:dst+n], m.r[z:z+n])
		}
	})
	return dst
}

func (b *builder) place(e expr) (base int, flat []int) {
	switch e := e.(type) {
	case *varExpr:
		return b.slotOf(e.v), nil
	case *inputExpr:
		return slotFragColor, nil
	case *swizzleExpr:
		at, inner := b.place(e.x)
		out := make([]int, len(e.idx))
		for i, k := range e.idx {
			out[i] = k
			if inner != nil {
				out[i] = inner[k]
			}
		}
		return at, out
	case *indexExpr:
		at, inner := b.place(e.x)
		t := e.x.typ()
		n, first := 1, e.i
		if t.matrix() {
			n, first = t.columns(), e.i*t.columns()
		}
		out := make([]int, n)
		for i := range n {
			out[i] = first + i
			if inner != nil {
				out[i] = inner[first+i]
			}
		}
		return at, out
	}
	panic("shader: an assignment target the compiler does not know")
}

func (b *builder) store(target expr, src int, code *[]op) {
	at, flat := b.place(target)
	if flat == nil {
		*code = append(*code, copySlots(at, src, target.typ().size()))
		return
	}
	*code = append(*code, func(m *machine) {
		for i, k := range flat {
			m.r[at+k] = m.r[src+i]
		}
	})
}

func (b *builder) assign(e *assignExpr, code *[]op) int {
	src := b.owned(e.value, code)
	b.store(e.target, src, code)
	return src
}

func (b *builder) stepped(e *stepExpr, code *[]op) int {
	t := e.target.typ()
	n := t.size()
	before := b.owned(e.target, code)
	after := b.alloc(n)
	delta := e.delta
	if t == tInt {
		d := int32(delta)
		*code = append(*code, func(m *machine) { m.r[after] = fromInt(asInt(m.r[before]) + d) })
	} else {
		*code = append(*code, func(m *machine) {
			for i := range n {
				m.r[after+i] = m.r[before+i] + delta
			}
		})
	}
	b.store(e.target, after, code)
	if e.post {
		return before
	}
	return after
}

func convert(from, to Type, v float32) float32 {
	switch {
	case from == to:
		return v
	case from == tBool && to == tInt:
		return fromInt(int32(v))
	case from == tInt && to == tBool:
		return fromBool(asInt(v) != 0)
	case from == tInt:
		return float32(asInt(v))
	case from == tBool, to == tFloat:
		return v
	case to == tBool:
		return fromBool(v != 0)
	case to == tInt:
		return fromInt(toInt(v))
	}
	return v
}

func scalarOf(t Type) Type {
	if t.size() == 1 {
		return t
	}
	return tFloat
}

func (b *builder) construct(e *constructExpr, code *[]op) int {
	n := e.t.size()
	dst := b.alloc(n)
	to := scalarOf(e.t)
	if len(e.args) == 1 {
		a := e.args[0]
		src := b.expr(a, code)
		from := scalarOf(a.typ())
		switch {
		case a.typ().size() == 1 && e.t.matrix():
			cols := e.t.columns()
			*code = append(*code, func(m *machine) {
				clear(m.r[dst : dst+n])
				v := convert(from, tFloat, m.r[src])
				for c := range cols {
					m.r[dst+c*cols+c] = v
				}
			})
		case a.typ().size() == 1:
			*code = append(*code, func(m *machine) {
				v := convert(from, to, m.r[src])
				for i := range n {
					m.r[dst+i] = v
				}
			})
		case e.t.matrix():
			cols, srcCols := e.t.columns(), a.typ().columns()
			*code = append(*code, func(m *machine) {
				for c := range cols {
					for r := range cols {
						switch {
						case c < srcCols && r < srcCols:
							m.r[dst+c*cols+r] = m.r[src+c*srcCols+r]
						case c == r:
							m.r[dst+c*cols+r] = 1
						default:
							m.r[dst+c*cols+r] = 0
						}
					}
				}
			})
		default:
			*code = append(*code, func(m *machine) {
				for i := range n {
					m.r[dst+i] = convert(from, to, m.r[src+i])
				}
			})
		}
		return dst
	}
	at := 0
	for _, a := range e.args {
		src := b.expr(a, code)
		from, k, first := scalarOf(a.typ()), a.typ().size(), at
		*code = append(*code, func(m *machine) {
			for i := range k {
				m.r[dst+first+i] = convert(from, to, m.r[src+i])
			}
		})
		at += k
	}
	return dst
}

func (b *builder) function(fn *function) *compiled {
	if c, ok := b.funcs[fn]; ok {
		return c
	}
	c := &compiled{ret: b.alloc(fn.ret.size())}
	for _, p := range fn.params {
		c.params = append(c.params, b.slotOf(p))
	}
	b.funcs[fn] = c
	outer := b.cur
	b.cur = c
	c.body = b.stmts(fn.body)
	b.cur = outer
	return c
}

func (b *builder) call(e *callExpr, code *[]op) int {
	c := b.function(e.fn)
	for i, a := range e.args {
		src := b.owned(a, code)
		*code = append(*code, copySlots(c.params[i], src, a.typ().size()))
	}
	n := e.fn.ret.size()
	dst := b.alloc(n)
	*code = append(*code, func(m *machine) {
		runBlock(c.body, m)
		copy(m.r[dst:dst+n], m.r[c.ret:c.ret+n])
	})
	return dst
}

func (b *builder) builtin(e *builtinExpr, code *[]op) int {
	args := make([]int, len(e.args))
	for i, a := range e.args {
		args[i] = b.owned(a, code)
	}
	n := e.t.size()
	dst := b.alloc(n)
	switch e.b.name {
	case "length":
		k, x := e.args[0].typ().size(), args[0]
		*code = append(*code, func(m *machine) { m.r[dst] = norm(m.r[x:x+k], nil) })
		return dst
	case "distance":
		k, x, y := e.args[0].typ().size(), args[0], args[1]
		*code = append(*code, func(m *machine) { m.r[dst] = norm(m.r[x:x+k], m.r[y:y+k]) })
		return dst
	case "dot":
		k, x, y := e.args[0].typ().size(), args[0], args[1]
		*code = append(*code, func(m *machine) { m.r[dst] = dot(m.r[x:x+k], m.r[y:y+k]) })
		return dst
	case "cross":
		x, y := args[0], args[1]
		*code = append(*code, func(m *machine) {
			a, c := m.r[x:x+3], m.r[y:y+3]
			m.r[dst] = float32(a[1]*c[2]) - float32(c[1]*a[2])
			m.r[dst+1] = float32(a[2]*c[0]) - float32(c[2]*a[0])
			m.r[dst+2] = float32(a[0]*c[1]) - float32(c[0]*a[1])
		})
		return dst
	case "normalize":
		x := args[0]
		*code = append(*code, func(m *machine) {
			l := norm(m.r[x:x+n], nil)
			for i := range n {
				m.r[dst+i] = m.r[x+i] / l
			}
		})
		return dst
	case "faceforward":
		x, i, r := args[0], args[1], args[2]
		*code = append(*code, func(m *machine) {
			s := float32(1)
			if dot(m.r[r:r+n], m.r[i:i+n]) >= 0 {
				s = -1
			}
			for k := range n {
				m.r[dst+k] = float32(s * m.r[x+k])
			}
		})
		return dst
	case "reflect":
		i, r := args[0], args[1]
		*code = append(*code, func(m *machine) {
			d := float32(2 * dot(m.r[r:r+n], m.r[i:i+n]))
			for k := range n {
				m.r[dst+k] = m.r[i+k] - float32(d*m.r[r+k])
			}
		})
		return dst
	case "refract":
		i, r, eta := args[0], args[1], args[2]
		*code = append(*code, func(m *machine) {
			d, h := dot(m.r[r:r+n], m.r[i:i+n]), m.r[eta]
			k := 1 - float32(float32(h*h)*(1-float32(d*d)))
			if k < 0 {
				clear(m.r[dst : dst+n])
				return
			}
			w := float32(h*d) + float32(math.Sqrt(float64(k)))
			for c := range n {
				m.r[dst+c] = float32(h*m.r[i+c]) - float32(w*m.r[r+c])
			}
		})
		return dst
	case "matrixCompMult":
		x, y := args[0], args[1]
		*code = append(*code, func(m *machine) {
			for i := range n {
				m.r[dst+i] = float32(m.r[x+i] * m.r[y+i])
			}
		})
		return dst
	}
	steps := make([]int, len(e.args))
	for i, a := range e.args {
		steps[i] = stepOf(a.typ())
	}
	switch len(args) {
	case 1:
		f, x := e.b.f1, args[0]
		*code = append(*code, func(m *machine) {
			for i := range n {
				m.r[dst+i] = f(m.r[x+i])
			}
		})
	case 2:
		f, x, y, sx, sy := e.b.f2, args[0], args[1], steps[0], steps[1]
		*code = append(*code, func(m *machine) {
			for i := range n {
				m.r[dst+i] = f(m.r[x+i*sx], m.r[y+i*sy])
			}
		})
	default:
		f, x, y, z, sx, sy, sz := e.b.f3, args[0], args[1], args[2], steps[0], steps[1], steps[2]
		*code = append(*code, func(m *machine) {
			for i := range n {
				m.r[dst+i] = f(m.r[x+i*sx], m.r[y+i*sy], m.r[z+i*sz])
			}
		})
	}
	return dst
}

func dot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += float32(a[i] * b[i])
	}
	return sum
}

func norm(a, b []float32) float32 {
	var sum float64
	for i := range a {
		d := float64(a[i])
		if b != nil {
			d -= float64(b[i])
		}
		sum += float64(d * d)
	}
	return float32(math.Sqrt(sum))
}

func (b *builder) stmts(body []stmt) []stmtOp {
	out := make([]stmtOp, 0, len(body))
	for _, s := range body {
		out = append(out, b.stmt(s))
	}
	return out
}

func (b *builder) stmt(s stmt) stmtOp {
	switch s := s.(type) {
	case *declStmt:
		dst := b.slotOf(s.v)
		n := s.v.t.size()
		var code []op
		if s.init != nil {
			src := b.expr(s.init, &code)
			code = append(code, copySlots(dst, src, n))
		} else {
			code = append(code, func(m *machine) { clear(m.r[dst : dst+n]) })
		}
		return func(m *machine) signal {
			run(code, m)
			return flowNext
		}
	case *exprStmt:
		var code []op
		b.expr(s.e, &code)
		return func(m *machine) signal {
			run(code, m)
			return flowNext
		}
	case *blockStmt:
		body := b.stmts(s.body)
		return func(m *machine) signal { return runBlock(body, m) }
	case *ifStmt:
		var code []op
		c := b.expr(s.cond, &code)
		then, alt := b.stmts(s.then), b.stmts(s.alt)
		return func(m *machine) signal {
			run(code, m)
			if m.r[c] != 0 {
				return runBlock(then, m)
			}
			return runBlock(alt, m)
		}
	case *forStmt:
		return b.loop(s)
	case *breakStmt:
		return func(*machine) signal { return flowBreak }
	case *continueStmt:
		return func(*machine) signal { return flowContinue }
	case *returnStmt:
		if s.value == nil {
			return func(*machine) signal { return flowReturn }
		}
		var code []op
		src := b.expr(s.value, &code)
		code = append(code, copySlots(b.cur.ret, src, s.value.typ().size()))
		return func(m *machine) signal {
			run(code, m)
			return flowReturn
		}
	}
	panic("shader: a statement the compiler does not know")
}

func (b *builder) loop(s *forStmt) stmtOp {
	var init, cond, step []op
	dst := b.slotOf(s.v)
	src := b.expr(s.init, &init)
	init = append(init, copySlots(dst, src, 1))
	c := b.expr(s.cond, &cond)
	b.expr(s.step, &step)
	body := b.stmts(s.body)
	return func(m *machine) signal {
		run(init, m)
		for {
			run(cond, m)
			if m.r[c] == 0 {
				return flowNext
			}
			switch runBlock(body, m) {
			case flowBreak:
				return flowNext
			case flowReturn:
				return flowReturn
			}
			run(step, m)
		}
	}
}

func evalConstant(e expr) []float32 {
	b := newBuilder()
	var code []op
	at := b.expr(e, &code)
	m := &machine{r: b.init}
	run(code, m)
	return m.r[at : at+e.typ().size()]
}

// Inputs is what a shader reads of one pixel. Time and Resolution stay
// the same across a frame; the coordinates are the pixel's.
type Inputs struct {
	X, Y, U, V, Time, Width, Height float32
}

// Runner runs a program one pixel at a time on one goroutine at a time.
type Runner struct {
	m    machine
	main *compiled
}

func (p *Program) Runner(sample func(u, v float32) [4]float32) *Runner {
	b := newBuilder()
	c := b.function(p.main)
	return &Runner{m: machine{r: b.init, sample: sample}, main: c}
}

func (r *Runner) Shade(in Inputs) [4]float32 {
	s := r.m.r
	s[slotFragCoord], s[slotFragCoord+1], s[slotFragCoord+2], s[slotFragCoord+3] = in.X, in.Y, 0.5, 1
	s[slotFragColor], s[slotFragColor+1], s[slotFragColor+2], s[slotFragColor+3] = 0, 0, 0, 0
	s[slotTexcoord], s[slotTexcoord+1] = in.U, in.V
	s[slotTime], s[slotResolution], s[slotResolution+1] = in.Time, in.Width, in.Height
	runBlock(r.main.body, &r.m)
	return [4]float32{s[slotFragColor], s[slotFragColor+1], s[slotFragColor+2], s[slotFragColor+3]}
}
