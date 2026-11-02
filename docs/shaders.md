# Screen shaders

A screen shader is a fragment shader that post-processes an output's final
image: colour grading, a night light of your own, scanlines. It takes one key in
an [output rule](./config#outputs):

```yaml
outputs:
  - match: "*"
    shader: ~/.config/lys-wm/grayscale.frag
```

The path is absolute, starts with `~/`, or is relative to the config file. `lys-wmctl
reload` reads the file again, along with the rest of the config, so
editing a shader and reloading takes effect at once. Without the key an
output pays nothing: no extra pass, no intermediate image, and fullscreen windows
scan out directly as usual.

## The language

One source runs on all three renderers: a subset of **GLSL ES 1.00**, the
language of `#version 100` shaders. lys-wm parses and checks it itself, in pure
Go, the same way on every machine, and gives each renderer the form it takes:

| Renderer | Runs |
| --- | --- |
| GLES | The source itself, with precision forced to `highp` |
| Vulkan | SPIR-V that lys-wm generates; no compiler library is needed |
| Software | A program interpreted on the CPU, one thread per core, once per pixel |

The three agree to within 3 levels of 255 on [the examples below](#examples).

```glsl
precision mediump float;

uniform sampler2D tex;
varying vec2 v_texcoord;

void main() {
    gl_FragColor = vec4(1.0 - texture2D(tex, v_texcoord).rgb, 1.0);
}
```

These inputs exist, each to be declared before use; no other `uniform` or
`varying` does:

| Input | Is |
| --- | --- |
| `uniform sampler2D tex` | The output's final image, sampled bilinearly and clamped at the edges; `texture2D(tex, uv)` is the only way to read it, and `tex` passes nowhere else |
| `varying vec2 v_texcoord` | This pixel's place in the image, (0, 0) at the top left and (1, 1) at the bottom right |
| `uniform vec2 resolution` | The output's size in device pixels |
| `uniform float time` | Seconds, wrapping every hour. Reading it makes the output draw every frame for as long as the shader is on, so a shader without it costs nothing while nothing changes |
| `gl_FragCoord` | This pixel's centre in device pixels, y growing downward |
| `gl_FragColor` | What to show, written at least once; alpha is ignored, values clamp to 0 to 1 |

Supported: `float`, `int`, `bool`, `vec2` to `vec4` and `mat2` to `mat4`;
constructors, swizzles and constant indices; `const` globals and locals;
functions defined before they are called, with `in` parameters; `if`/`else`, `?:`,
`break`, `continue`, `return`; the operators of GLSL ES 1.00; the built-ins `radians`, `degrees`, `sin`, `cos`, `tan`, `asin`, `acos`,
`atan`, `pow`, `exp`, `log`, `exp2`, `log2`, `sqrt`, `inversesqrt`, `abs`,
`sign`, `floor`, `ceil`, `fract`, `mod`, `min`, `max`, `clamp`, `mix`, `step`,
`smoothstep`, `length`, `distance`, `dot`, `cross`, `normalize`, `faceforward`,
`reflect`, `refract`, `matrixCompMult` and `texture2D`.

Constraints, each refused with a message that names the line:

- **A shader always ends.** The only loop is `for` of constant length, as GLSL ES
  1.00's appendix A has it: `for (int i = 0; i < 8; i++)`. Its index cannot be
  assigned, and the loops of a shader run at most 1024 iterations per pixel,
  counting functions they call. There is no recursion and no `while`.
- There are no implicit conversions, as in the rest of GLSL ES: write `2.0`, or `float(n)`.
- Not supported: `#define` and every directive but `#version 100`, `#version 300 es`
  and its `in`/`out`/`texture()`, `struct`, arrays, `ivec` and `bvec`, mutable globals,
  `out` and `inout` parameters, prototypes, overloads, `discard`, derivatives, other
  textures. The source is at most 64 KiB.

## When it does not compile

A shader that does not compile, or a driver that refuses one, leaves the output
unshaded. The compositor reports the file, line and column in its log, and
`lys-wmctl outputs` shows it as the output's `shader_error` until a reload
brings a working file. A shader reads only the image, so it cannot crash the
session, whatever it computes.

## What sees the shaded image

The shader belongs to the display, as gamma does. Only the output sees it:

- **Screen capture sees the image without the shader**: wlr-screencopy,
  ext-image-copy-capture and everything built on them, such as screen sharing and
  screenshots. A shader that paints the screen black cannot blind a screen share,
  and a colour filter stays out of recordings.
- **The lock screen is never shaded.** While the session is locked the output
  draws without the shader, so a broken one cannot hide the password prompt. The
  shader comes back on unlocking.

## What it costs

The output's scene draws into an image of its own, with the usual damage, and the
shader then redraws the whole output from it, since it may read any pixel: on a
frame where only a clock changes, the shader still runs over every pixel. When
nothing changes it does not run, unless it reads `time`. The cursor is
drawn into the image rather than on a hardware plane, and fullscreen windows
do not scan out directly, on the shaded output only. A 10-bit output is
composed in 8 bits under a shader.

GPU renderers run a simple shader in a fraction of a frame. One that asks more
of the GPU than it can do slows the output like any heavy client, and a driver may
reset a GPU that one shader holds for seconds, which ends the session; this
limit is the GPU's, and no check here can know it. The software renderer
holds the session's only thread for the length of it. 8 threads at 1920x1080
took 62 ms per frame for `nightlight.frag`, 69 ms for `grayscale.frag`, 121 ms for
`scanlines.frag` and 704 ms for `sharpen.frag`.

## Examples

These are tested on all three renderers:

- [`grayscale.frag`](https://github.com/stubbedev/lys-wm/blob/main/docs/shaders/grayscale.frag): luminance only
- [`nightlight.frag`](https://github.com/stubbedev/lys-wm/blob/main/docs/shaders/nightlight.frag): a warm tint
- [`sharpen.frag`](https://github.com/stubbedev/lys-wm/blob/main/docs/shaders/sharpen.frag): an unsharp mask over the eight neighbours
- [`scanlines.frag`](https://github.com/stubbedev/lys-wm/blob/main/docs/shaders/scanlines.frag): moving scanlines and a vignette, reading `time`
