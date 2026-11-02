precision mediump float;

uniform sampler2D tex;
varying vec2 v_texcoord;

const vec3 warmth = vec3(1.0, 0.8, 0.55);

void main() {
    gl_FragColor = vec4(texture2D(tex, v_texcoord).rgb * warmth, 1.0);
}
