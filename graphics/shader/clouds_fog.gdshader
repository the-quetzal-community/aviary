shader_type fog;

// World-space volumetric cloud layer for the Highest tier (the fly-through clouds
// on a box FogVolume; see client.go cloudLayer*). The density model follows the
// Guerrilla "Nubis" recipe (Horizon Zero Dawn, SIGGRAPH 2015/2017): the thing that
// makes this read as clouds rather than smoke is NOT more fractal noise but four
// specific ingredients —
//   1. billowy Worley (cellular) noise for the cauliflower cloud structure,
//   2. a cumulus density-height gradient (flat base, rounded billowing top),
//   3. a coverage 'remap' that carves the field into discrete clouds with gaps,
//   4. high-frequency Worley 'erosion' that nibbles wispy detail into the edges.
// We only output DENSITY/ALBEDO; the engine lights + self-shadows the froxels from
// the scene DirectionalLight. The 'fog' processor forbids 'return', so the empty
// case falls through to DENSITY = 0. Uniforms hot-reload in a running Godot.

// 0 = clear, 1 = full cover. Driven by the Clouds slider (Client.applyCloudDensity).
uniform float coverage : hint_range(0.0, 1.0) = 0.0;
// World-units -> base noise scale (smaller = larger puffs); detail_scale is the
// erosion frequency relative to the base; erosion_strength is how hard it carves.
uniform float cloud_scale = 0.06;
uniform float detail_scale = 4.0;
uniform float erosion_strength = 0.4;
// Wind drift (noise units / second); scaled by the Wind slider in client.go.
uniform vec2 cloud_wind = vec2(0.03, 0.01);
// World-Y band the clouds occupy. The FogVolume box is deliberately much taller
// than this (so the camera stays inside it and it's never frustum-culled); the
// clouds' real altitude is confined here, in world space, not by the box.
uniform float cloud_base = 12.0;
uniform float cloud_top = 20.0;
// UVW radius (centre 0.5 = the camera, the box follows it) by which the clouds
// fully fade, so they dissolve before the volumetric-fog cut-off rather than
// hard-ending at a ring. Keep a touch below VolumetricFogLength / cloudLayerSpanXZ.
uniform float horizon_fade = 0.36;
// Per-unit extinction of a fully-dense froxel × the band thickness ≈ optical depth;
// keep ~O(1) or the layer becomes an opaque wall. Higher = thicker/more opaque.
uniform float density_mul = 1.2;
// A hair off pure white: real cloud droplets scatter near-neutrally but a literal
// vec3(1.0) albedo, lit hard by the sun, reads as a digital "neon" white. The
// faint warm desaturation keeps the sunlit faces bright but believable.
uniform vec3 cloud_albedo : source_color = vec3(0.97, 0.96, 0.93);

float remap(float v, float a, float b, float c, float d) {
	return c + (v - a) * (d - c) / (b - a);
}

// --- value-noise fbm: the connected, Perlin-like large-scale variation ----------
float hash13(vec3 p) {
	p = fract(p * 0.1031);
	p += dot(p, p.zyx + 31.32);
	return fract((p.x + p.y) * p.z);
}

float vnoise(vec3 x) {
	vec3 i = floor(x);
	vec3 f = fract(x);
	f = f * f * (3.0 - 2.0 * f);
	float n000 = hash13(i + vec3(0.0, 0.0, 0.0));
	float n100 = hash13(i + vec3(1.0, 0.0, 0.0));
	float n010 = hash13(i + vec3(0.0, 1.0, 0.0));
	float n110 = hash13(i + vec3(1.0, 1.0, 0.0));
	float n001 = hash13(i + vec3(0.0, 0.0, 1.0));
	float n101 = hash13(i + vec3(1.0, 0.0, 1.0));
	float n011 = hash13(i + vec3(0.0, 1.0, 1.0));
	float n111 = hash13(i + vec3(1.0, 1.0, 1.0));
	return mix(mix(mix(n000, n100, f.x), mix(n010, n110, f.x), f.y),
			mix(mix(n001, n101, f.x), mix(n011, n111, f.x), f.y), f.z);
}

float fbm3(vec3 p) {
	float v = 0.0;
	float a = 0.5;
	for (int i = 0; i < 4; i++) {
		v += a * vnoise(p);
		p *= 2.0;
		a *= 0.5;
	}
	return v;
}

// --- Worley / cellular noise: the billowy cauliflower cloud structure -----------
vec3 hash33(vec3 p) {
	p = fract(p * vec3(0.1031, 0.1030, 0.0973));
	p += dot(p, p.yxz + 33.33);
	return fract((p.xxy + p.yzz) * p.zyx);
}

// Distance to the nearest feature point in the 3×3×3 cell neighbourhood. Returns
// ~0 at a feature point, growing toward the cell edges; (1 - worley) is billowy.
float worley(vec3 p) {
	vec3 id = floor(p);
	vec3 f = fract(p);
	float md = 1.0;
	for (int x = -1; x <= 1; x++) {
		for (int y = -1; y <= 1; y++) {
			for (int z = -1; z <= 1; z++) {
				vec3 c = vec3(float(x), float(y), float(z));
				vec3 o = hash33(id + c);
				vec3 r = c + o - f;
				md = min(md, dot(r, r)); // squared distance (cheaper; sqrt once below)
			}
		}
	}
	return sqrt(md);
}

// Two octaves of inverted Worley -> billowy, multi-scale cloud lumps in [0,1].
float worleyFBM(vec3 p) {
	return (1.0 - worley(p)) * 0.7 + (1.0 - worley(p * 2.7)) * 0.3;
}

// Cumulus density-height gradient: 0 at the base, full through the lower-middle,
// rounding off toward the top so the layer billows upward instead of being a slab.
float height_gradient(float h) {
	return clamp(remap(h, 0.0, 0.12, 0.0, 1.0), 0.0, 1.0)
		* clamp(remap(h, 0.45, 0.95, 1.0, 0.0), 0.0, 1.0);
}

void fog() {
	// Height fraction across the world-Y cloud band (NOT the box, which is much
	// taller so the camera stays inside it — see client.go). This both confines the
	// clouds to their altitude and shapes them into cumulus via height_gradient.
	float h = clamp((WORLD_POSITION.y - cloud_base) / max(cloud_top - cloud_base, 0.001), 0.0, 1.0);
	float grad = height_gradient(h);

	float d = 0.0;
	if (grad > 0.0 && coverage > 0.001) {
		vec3 wind = vec3(cloud_wind.x, 0.0, cloud_wind.y) * TIME;
		vec3 p = WORLD_POSITION * cloud_scale + wind;

		// Base shape: billowy Worley cauliflower, varied at large scale by the
		// connected value-noise fbm. This is the smoke -> cloud difference.
		float base = worleyFBM(p) * 0.65 + fbm3(p) * 0.35;

		// Cumulus vertical profile.
		base *= grad;

		// Coverage carves the field into discrete clouds with clear-sky gaps
		// (Nubis-style remap: raises the floor, so only the densest bits survive).
		base = clamp(remap(base, 1.0 - coverage, 1.0, 0.0, 1.0), 0.0, 1.0);

		// Erosion: high-frequency Worley nibbles wispy detail into the cloud edges.
		float erosion = 1.0 - worley(p * detail_scale);
		base = clamp(remap(base, erosion * erosion_strength, 1.0, 0.0, 1.0), 0.0, 1.0);

		// Soft radial fade so the layer dissolves before the fog cut-off (the box
		// follows the camera, so UVW.xz centre 0.5 is the camera).
		float r = length(UVW.xz - vec2(0.5));
		float edge = 1.0 - smoothstep(horizon_fade * 0.3, horizon_fade, r);

		d = base * edge;
	}

	DENSITY = d * density_mul;
	ALBEDO = cloud_albedo;
}
