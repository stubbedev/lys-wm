package shader_test

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/shader"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

func exampleSources(t *testing.T) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob("../../docs/shaders/*.frag")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no example shaders: %v", err)
	}
	out := map[string][]byte{}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(path)] = src
	}
	return out
}

func TestTheExamplesCompile(t *testing.T) {
	for name, src := range exampleSources(t) {
		p, err := shader.Compile(src)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if want := name == "scanlines.frag"; p.UsesTime() != want {
			t.Errorf("%s: UsesTime %v", name, p.UsesTime())
		}
	}
}

func TestDiagnosticsNameTheLineAndWhatIsWrong(t *testing.T) {
	const pre = "uniform sampler2D tex;\nvarying vec2 v_texcoord;\n"
	for src, want := range map[string]string{
		"":                                  "no void main()",
		"void main() {}":                    "nothing writes gl_FragColor",
		"#version 300 es\nvoid main() {}":   "line 1, column 1: \"#version 300 es\": the only directive",
		"#define X 1\nvoid main() {}":       "use const instead of #define",
		"void main() { gl_FragColor = 1; }": "assigning int to vec4",
		"void main() {\n  gl_FragColor = vec4(1.0, 2.0);\n}":                                                            "line 2, column 18: vec4 takes 4 components, not 2",
		"void main() { float x = 1; gl_FragColor = vec4(x); }":                                                          "float x initialised with int",
		"void main() { gl_FragColor = vec4(1) * 2; }":                                                                   "no implicit conversions",
		"uniform float brightness;\nvoid main() { gl_FragColor = vec4(1.0); }":                                          "lys-wm provides only",
		"float g;\nvoid main() { gl_FragColor = vec4(1.0); }":                                                           "only const globals",
		"void main() { while (true) {} gl_FragColor = vec4(1.0); }":                                                     "only for loops of constant length",
		"void main() { discard; gl_FragColor = vec4(1.0); }":                                                            "discard is not supported",
		"float f(float x) { return f(x); }\nvoid main() { gl_FragColor = vec4(f(1.0)); }":                               "recursion is not supported",
		"float f(float x);\nvoid main() { gl_FragColor = vec4(f(1.0)); }":                                               "prototypes are not supported",
		"float f(float x) { if (x > 0.0) { return x; } }\nvoid main() { gl_FragColor = vec4(1.0); }":                    "does not return on every path",
		"void f(out float x) { x = 1.0; }\nvoid main() { gl_FragColor = vec4(1.0); }":                                   "out and inout parameters are not supported",
		"void main() { for (int i = 0; i < 100000; i++) { gl_FragColor = vec4(1.0); } }":                                "more than 1024 iterations",
		"void main() { for (int i = 0; i < 100; i++) { for (int j = 0; j < 100; j++) { gl_FragColor = vec4(1.0); } } }": "more than the 1024 allowed",
		"void main() { for (int i = 0; i < 4; i += 0) { gl_FragColor = vec4(1.0); } }":                                  "never ends",
		"void main() { for (int i = 0; i < 4; i++) { i = 2; gl_FragColor = vec4(1.0); } }":                              "the loop index i cannot be assigned",
		"void main() { int n = 2; for (int i = 0; i < n; i++) { gl_FragColor = vec4(1.0); } }":                          "compares the index i with a constant int",
		"void main() { vec2 v = vec2(1.0); gl_FragColor = vec4(v.xyz, 1.0); }":                                          "vec2 has no component z",
		"void main() { vec4 v = vec4(1.0); v.xx = vec2(1.0); gl_FragColor = v; }":                                       "repeated component",
		"void main() { int i = 0; vec2 v = vec2(1.0); gl_FragColor = vec4(v[i]); }":                                     "an index is a constant int",
		"void main() { gl_FragColor = texture2D(tex, vec2(0.0)); }":                                                     "tex is used without its declaration",
		"void main() { gl_FragColor = vec4(time); }":                                                                    "time is used without its declaration",
		pre + "void main() { gl_FragColor = texture2D(tex, 1.0); }":                                                     "texture2D takes a vec2",
		"void main() { gl_FragColor = vec4(sin(1)); }":                                                                  "sin takes a float or vector",
		"struct S { float x; };\nvoid main() {}":                                                                        "structs are not supported",
		"void main() { /* never":                                                                                        "a comment that never ends",
		"void main() { @ }":                                                                                             "unexpected character",
		"void main() {":                                                                                                 "a block that never ends",
		"void main() { float mix = 1.0; }":                                                                              `"mix" is a built-in`,
		"void main() { float gl_x = 1.0; }":                                                                             "reserved",
	} {
		_, err := shader.Compile([]byte(src))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", src, err, want)
		}
	}
	if _, err := shader.Compile(make([]byte, 64<<10+1)); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("an oversized source: %v", err)
	}
	if _, err := shader.Compile([]byte("void main() { gl_FragColor = vec4(" + strings.Repeat("(", 1000) + "1.0" + strings.Repeat(")", 1000) + "); }")); err == nil || !strings.Contains(err.Error(), "nested more than") {
		t.Errorf("a deep expression: %v", err)
	}
}

func TestGLESKeepsLinesAndForcesHighp(t *testing.T) {
	p, err := shader.Compile([]byte("#version 100\nprecision mediump float;\nuniform mediump float time;\nvoid main() {\n  lowp vec4 c = vec4(time);\n  gl_FragColor = c;\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := p.GLES()
	if strings.Contains(src, "mediump") || strings.Contains(src, "lowp") || strings.Contains(src, "#version") {
		t.Errorf("precision or version left in:\n%s", src)
	}
	if !strings.HasPrefix(src, "precision highp float;") {
		t.Errorf("no highp prefix:\n%s", src)
	}
	lines := strings.Split(src, "\n")
	if !strings.Contains(lines[4], "vec4 c = vec4(time)") {
		t.Errorf("line 5 moved: %q", lines[4])
	}
	if !p.UsesTime() {
		t.Error("UsesTime false for a shader reading time")
	}
}

const allFeatures = `
precision highp float;
uniform sampler2D tex;
uniform float time;
uniform vec2 resolution;
varying vec2 v_texcoord;

const int count = 4;
const float third = 1.0 / 3.0;
const mat2 turn = mat2(0.0, 1.0, -1.0, 0.0);

float square(float x) { return x * x; }

vec3 pick(bool flag, vec3 a, vec3 b) {
    if (flag) {
        return a;
    } else {
        return b;
    }
}

mat3 scaled(in float k) {
    mat3 m = mat3(k);
    m[1] = vec3(0.0, k * 2.0, 0.0);
    m[2][2] = k;
    return m;
}

void paint(vec4 c) {
    gl_FragColor = c;
    gl_FragColor.a = 1.0;
}

void main(void) {
    vec2 uv = v_texcoord;
    vec4 c = texture2D(tex, uv);
    float accum = 0.0;
    int n = 0;
    for (int i = 0; i < count; i++) {
        if (i == 2) continue;
        accum += float(i) * third;
        n += 1;
        if (n > 10) break;
    }
    for (float x = 1.0; x > 0.0; x -= 0.25) { accum += x; }
    vec3 v = vec3(0.1, 0.2, 0.3);
    v.xy = v.yx;
    v.z += 0.5;
    v = -v * 2.0 + 1.0 / vec3(2.0);
    mat3 m = scaled(0.5) * scaled(2.0);
    vec3 w = m * v + v * m;
    mat2 r = turn * turn + mat2(1.0) - mat2(mat3(1.0));
    vec2 rv = r * uv;
    bool flag = accum > 1.0 && n != 0 || !(uv.x < 0.5) ^^ false;
    float t = flag ? square(accum) : sin(time);
    float sum = radians(t) + degrees(t) + sin(t) + cos(t) + tan(t) + atan(t) + atan(t, 2.0)
        + pow(abs(t) + 1.0, 2.0) + exp(0.5) + log(2.0) + exp2(1.0) + log2(2.0) + sqrt(2.0) + inversesqrt(2.0)
        + sign(t) + floor(t) + ceil(t) + fract(t) + mod(t, 2.0) + min(t, 1.0) + max(t, 1.0)
        + clamp(t, 0.0, 1.0) + mix(0.0, 1.0, 0.5) + step(0.5, t) + smoothstep(0.0, 1.0, 0.5)
        + asin(0.5) + acos(0.5);
    vec3 lit = mix(v, w, 0.25) + mod(w, 2.0) + clamp(w, 0.0, 1.0) + step(0.5, w) + smoothstep(0.0, 1.0, w)
        + min(v, w) + max(v, 1.0) + normalize(v) + cross(v, w) + faceforward(v, w, v) + reflect(v, w) + refract(v, normalize(w), 0.5);
    float lengths = length(v) + distance(v, w) + dot(v, w) + dot(0.5, 0.25) + length(0.5);
    mat2 cm = matrixCompMult(r, r);
    int k = int(3.7) + int(true) + 7 / 2 - 2 * 3;
    k++;
    --k;
    float back = float(k) + float(flag);
    vec4 whole = vec4(vec2(uv), vec2(rv.x, 0.5));
    bool same = v == w || r == cm;
    vec3 chosen = pick(same, lit, v);
    paint(vec4(fract(chosen + sum + lengths + back), c.a) * 0.5 + whole.wzyx * 0.25 + gl_FragCoord / vec4(resolution, 1.0, 1.0) * 0.25);
}
`

func TestTheSPIRVValidates(t *testing.T) {
	val := testenv.Require(t, "spirv-val")
	sources := exampleSources(t)
	sources["allFeatures"] = []byte(allFeatures)
	for name, src := range sources {
		p, err := shader.Compile(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		path := filepath.Join(t.TempDir(), "shader.spv")
		if err := os.WriteFile(path, p.SPIRV(), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(val, "--target-env", "vulkan1.0", path).CombinedOutput(); err != nil {
			t.Errorf("%s: spirv-val: %v\n%s", name, err, out)
		}
	}
}

func TestTheGLSLAcceptsWhatWeAccept(t *testing.T) {
	glslang := testenv.Require(t, "glslangValidator")
	sources := exampleSources(t)
	sources["allFeatures"] = []byte(allFeatures)
	for name, src := range sources {
		p, err := shader.Compile(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		path := filepath.Join(t.TempDir(), "shader.frag")
		if err := os.WriteFile(path, []byte("#version 100\n"+p.GLES()), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(glslang, path).CombinedOutput(); err != nil {
			t.Errorf("%s: glslang: %v\n%s", name, err, out)
		}
	}
}

func shade(t *testing.T, src string, in shader.Inputs, sample func(u, v float32) [4]float32) [4]float32 {
	t.Helper()
	p, err := shader.Compile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return p.Runner(sample).Shade(in)
}

func near(a, b [4]float32) bool {
	for i := range a {
		if math.Abs(float64(a[i]-b[i])) > 1e-4 {
			return false
		}
	}
	return true
}

func TestTheRunnerComputes(t *testing.T) {
	flat := func(u, v float32) [4]float32 { return [4]float32{u, v, 0.5, 1} }
	in := shader.Inputs{X: 3.5, Y: 4.5, U: 0.25, V: 0.75, Time: 2, Width: 100, Height: 50}
	for src, want := range map[string][4]float32{
		"uniform sampler2D tex; varying vec2 v_texcoord; void main() { gl_FragColor = texture2D(tex, v_texcoord); }":                                         {0.25, 0.75, 0.5, 1},
		"void main() { gl_FragColor = gl_FragCoord; }":                                                                                                       {3.5, 4.5, 0.5, 1},
		"uniform float time; uniform vec2 resolution; void main() { gl_FragColor = vec4(time, resolution, 1.0); }":                                           {2, 100, 50, 1},
		"void main() { vec4 v = vec4(1.0, 2.0, 3.0, 4.0); gl_FragColor = v.wzyx; }":                                                                          {4, 3, 2, 1},
		"void main() { vec4 v = vec4(0.0); v.zx = vec2(1.0, 2.0); gl_FragColor = v; }":                                                                       {2, 0, 1, 0},
		"void main() { mat2 m = mat2(1.0, 2.0, 3.0, 4.0); gl_FragColor = vec4(m * vec2(1.0, 1.0), vec2(1.0, 1.0) * m); }":                                    {4, 6, 3, 7},
		"void main() { mat2 a = mat2(1.0, 2.0, 3.0, 4.0); mat2 b = a * a; gl_FragColor = vec4(b[0], b[1]); }":                                                {7, 10, 15, 22},
		"float f(float x) { for (int i = 0; i < 4; i++) { if (i == 2) { return x + float(i); } } return 0.0; } void main() { gl_FragColor = vec4(f(1.0)); }": {3, 3, 3, 3},
		"void main() { float s = 0.0; for (int i = 0; i < 5; i++) { if (i == 1) continue; if (i == 3) break; s += float(i); } gl_FragColor = vec4(s); }":     {2, 2, 2, 2},
		"void main() { int i = 1; int j = i++ + ++i; gl_FragColor = vec4(float(i), float(j), float(7 / 2), float(-7 / 2)); }":                                {3, 4, 3, -3},
		"void main() { float x = 2.0; bool t = true || (x = 5.0) > 0.0; gl_FragColor = vec4(x, float(t), 0.0, 1.0); }":                                       {2, 1, 0, 1},
		"void main() { gl_FragColor = vec4(mod(-1.0, 3.0), step(0.5, 0.25), smoothstep(0.0, 1.0, 0.5), mix(1.0, 3.0, 0.5)); }":                               {2, 0, 0.5, 2},
		"void main() { vec2 v = vec2(1.0); gl_FragColor = vec4(length(vec2(3.0, 4.0)), v * 2.0, 1.0); }":                                                     {5, 2, 2, 1},
		"void main() { mat3 m = mat3(mat2(2.0)); gl_FragColor = vec4(m[0][0], m[2][2], m[0][1], float(m == m)); }":                                           {2, 1, 0, 1},
	} {
		if got := shade(t, src, in, flat); !near(got, want) {
			t.Errorf("%s:\ngot %v, want %v", src, got, want)
		}
	}
}

func BenchmarkExamples(b *testing.B) {
	paths, err := filepath.Glob("../../docs/shaders/*.frag")
	if err != nil {
		b.Fatal(err)
	}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
		p, err := shader.Compile(src)
		if err != nil {
			b.Fatal(err)
		}
		r := p.Runner(func(u, v float32) [4]float32 { return [4]float32{u, v, 0.5, 1} })
		b.Run(filepath.Base(path), func(b *testing.B) {
			for b.Loop() {
				r.Shade(shader.Inputs{X: 3.5, Y: 4.5, U: 0.25, V: 0.75, Time: 2, Width: 1920, Height: 1080})
			}
		})
	}
}
