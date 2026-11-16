shader_type spatial;
render_mode blend_mix, cull_back;

// Critter skin: two player-chosen colours and a generic pattern that
// decides where each goes. Driven from Go (CritterBody.applySkin) out
// of the critter's "skin/*" weights; every uniform's default is the
// look of a critter that has never been coloured (plain white).

uniform vec4 primary : source_color = vec4(1.0);
uniform vec4 secondary : source_color = vec4(0.4, 0.4, 0.4, 1.0);
// 0 none, 1 spots, 2 stripes, 3 blotches, 4 speckles.
uniform int pattern = 0;
// Features per body unit.
uniform float pattern_scale = 5.0;
// How far the secondary colour climbs up from the underside.
uniform float belly : hint_range(0.0, 1.0) = 0.0;
uniform float gloss : hint_range(0.0, 1.0) = 0.0;

// Rest-pose position, uploaded in UV (x, y) + UV2.x (z). Sampling the
// pattern there rather than at VERTEX keeps it glued to the skin while
// the skeleton breathes and the legs step.
varying vec3 rest;
varying float under;

void vertex() {
	rest = vec3(UV.x, UV.y, UV2.x);
	under = -NORMAL.y;
}

vec3 hash3(vec3 p) {
	p = vec3(dot(p, vec3(127.1, 311.7, 74.7)), dot(p, vec3(269.5, 183.3, 246.1)), dot(p, vec3(113.5, 271.9, 124.6)));
	return fract(sin(p) * 43758.5453123);
}

// Distance to the nearest feature point of a jittered grid.
float cells(vec3 p) {
	vec3 i = floor(p);
	vec3 f = fract(p);
	float d = 8.0;
	for (int x = -1; x <= 1; x++) {
		for (int y = -1; y <= 1; y++) {
			for (int z = -1; z <= 1; z++) {
				vec3 g = vec3(float(x), float(y), float(z));
				vec3 r = g + hash3(i + g) - f;
				d = min(d, dot(r, r));
			}
		}
	}
	return sqrt(d);
}

float value_noise(vec3 p) {
	vec3 i = floor(p);
	vec3 f = fract(p);
	f = f * f * (3.0 - 2.0 * f);
	float n000 = hash3(i).x;
	float n100 = hash3(i + vec3(1.0, 0.0, 0.0)).x;
	float n010 = hash3(i + vec3(0.0, 1.0, 0.0)).x;
	float n110 = hash3(i + vec3(1.0, 1.0, 0.0)).x;
	float n001 = hash3(i + vec3(0.0, 0.0, 1.0)).x;
	float n101 = hash3(i + vec3(1.0, 0.0, 1.0)).x;
	float n011 = hash3(i + vec3(0.0, 1.0, 1.0)).x;
	float n111 = hash3(i + vec3(1.0, 1.0, 1.0)).x;
	return mix(mix(mix(n000, n100, f.x), mix(n010, n110, f.x), f.y),
			mix(mix(n001, n101, f.x), mix(n011, n111, f.x), f.y), f.z);
}

void fragment() {
	// Mirror across the midline so both flanks carry the same marks.
	vec3 p = vec3(abs(rest.x), rest.y, rest.z) * pattern_scale;
	float mark = 0.0;
	if (pattern == 1) {
		mark = 1.0 - smoothstep(0.30, 0.36, cells(p));
	} else if (pattern == 2) {
		float warp = value_noise(p * 0.7) * 2.5;
		mark = smoothstep(0.15, 0.3, sin(p.z * 3.0 + warp));
	} else if (pattern == 3) {
		float n = value_noise(p * 0.6) * 0.65 + value_noise(p * 1.3) * 0.35;
		mark = smoothstep(0.50, 0.54, n);
	} else if (pattern == 4) {
		mark = 1.0 - smoothstep(0.12, 0.16, cells(p * 3.0));
	}
	// belly 0 hides the underside band entirely; 1 brings it up past
	// the flanks.
	float edge = mix(1.05, -0.6, belly);
	mark = max(mark, smoothstep(edge, edge + 0.35, under) * step(0.001, belly));
	ALBEDO = mix(primary.rgb, secondary.rgb, mark);
	ROUGHNESS = mix(0.9, 0.25, gloss);
	SPECULAR = 0.5;
}
