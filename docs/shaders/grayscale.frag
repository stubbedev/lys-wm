#version 100
precision mediump float;

uniform sampler2D tex;
varying vec2 v_texcoord;

const vec3 luma = vec3(0.2126, 0.7152, 0.0722);

void main() {
    vec3 color = texture2D(tex, v_texcoord).rgb;
    gl_FragColor = vec4(vec3(dot(color, luma)), 1.0);
}
