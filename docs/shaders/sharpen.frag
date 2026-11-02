precision highp float;

uniform sampler2D tex;
uniform vec2 resolution;
varying vec2 v_texcoord;

const float strength = 0.5;

void main() {
    vec2 px = 1.0 / resolution;
    vec3 sum = vec3(0.0);
    for (int x = -1; x <= 1; x++) {
        for (int y = -1; y <= 1; y++) {
            if (x == 0 && y == 0) {
                continue;
            }
            sum += texture2D(tex, v_texcoord + px * vec2(float(x), float(y))).rgb;
        }
    }
    vec3 center = texture2D(tex, v_texcoord).rgb;
    gl_FragColor = vec4(clamp(center + strength * (center - sum / 8.0), 0.0, 1.0), 1.0);
}
