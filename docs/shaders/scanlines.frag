precision highp float;

uniform sampler2D tex;
uniform vec2 resolution;
uniform float time;
varying vec2 v_texcoord;

const float pi = 3.14159265;

float vignette(vec2 uv) {
    vec2 d = uv - vec2(0.5);
    return 1.0 - 0.6 * dot(d, d);
}

void main() {
    float band = sin(v_texcoord.y * resolution.y * pi + time * 2.0);
    float shade = 0.92 + 0.08 * band;
    vec3 color = texture2D(tex, v_texcoord).rgb;
    gl_FragColor = vec4(color * shade * vignette(v_texcoord), 1.0);
}
