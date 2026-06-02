#[compute]
#version 450

// AVIARY pass (not upstream): renders the cloud layer's coarse coverage into a
// top-down world-XZ texture — a "cloud shadow map". Ground shaders (terrain, and
// later grass/foliage/water/props-decal) sample it, projected along the sun, to
// cast shadows that actually match where the SunshineClouds2 clouds are. It reuses
// the exact large-scale placement (sampleSceneCoarse: large_noise × extra_large
// mask × height gradient, thresholded by coverage) and the same wind-driven noise
// positions/scales/coverage out of the shared GenericData buffer, so the shadow
// shapes coincide with the visible clouds. Fine medium/small erosion is skipped —
// it never reads in a soft ground shadow.

#include "./CloudsInc.comp"

layout(local_size_x = 8, local_size_y = 8, local_size_z = 1) in;

layout(binding = 0) uniform sampler2D extra_large_noise; // .a = extra-large mask
layout(binding = 1) uniform sampler3D large_noise;       // .r = large shape
layout(binding = 2) uniform sampler2D heightmask;        // height gradient
layout(binding = 3) uniform uniformBuffer {
	GenericData data;
} genericData;
layout(r32f, binding = 4) uniform restrict writeonly image2D shadow_map; // cloud coverage 0..1
layout(binding = 5) uniform ShadowMapParams {
	// xy = world centre XZ, z = half-extent (world units), w = map size (px)
	vec4 center_extent;
} sp;

// --- duplicated from SunshineCloudsCompute.glsl (lod = 0 path only) ----------
float quadraticIn(float t) { return t * t; }
float remap(float value, float min1, float max1, float min2, float max2) {
	return min2 + (value - min1) * (max2 - min2) / (max1 - min1);
}
float sampleEffectorAdditive(vec3 worldPosition) { return 0.0; } // effectors skipped (lod = 0)

float sampleSceneCoarse(vec3 largeNoisePos, vec3 worldPosition, float cloudceiling, float cloudfloor,
		float extralargeNoiseValue, float largenoisescale, float coverage, float lod) {
	float clampedWorldHeight = remap(worldPosition.y, cloudfloor, cloudceiling, 0.0, 1.0);
	vec4 gradientSample = texture(heightmask, vec2(clampedWorldHeight, 0.5)).rgba;
	float edgeFade = min(smoothstep(0.0, 0.1, clampedWorldHeight), smoothstep(1.0, 0.9, clampedWorldHeight));
	float extraLargeShape = extralargeNoiseValue * gradientSample.b;
	float effectorAdditive = 0.0;
	vec2 WindDirection = genericData.data.WindDirection;
	worldPosition += vec3(WindDirection.x, 0.0, WindDirection.y) * genericData.data.windSweptPower * quadraticIn(1.0 - clamp(clampedWorldHeight / genericData.data.windSweptRange, 0.0, 1.0));
	if (lod > 0.0) {
		effectorAdditive = sampleEffectorAdditive(worldPosition) * edgeFade;
	}
	float largeShape = texture(large_noise, (worldPosition - largeNoisePos) / largenoisescale).r * extraLargeShape;
	largeShape = smoothstep(coverage, coverage - 0.1, 1.0 - (largeShape * gradientSample.r)) + max(effectorAdditive, 0.0);
	float shape = largeShape + effectorAdditive;
	return clamp((shape * edgeFade), 0.0, 1.0);
}

void main() {
	ivec2 px = ivec2(gl_GlobalInvocationID.xy);
	int mapSize = int(sp.center_extent.w);
	if (px.x >= mapSize || px.y >= mapSize) { return; }

	// Texel -> world XZ over the [centre ± half-extent] window.
	vec2 t = (vec2(px) + 0.5) / float(mapSize);
	vec2 worldXZ = sp.center_extent.xy + (t * 2.0 - 1.0) * sp.center_extent.z;

	float floorH   = genericData.data.cloud_floor;
	float ceilH    = genericData.data.cloud_ceiling;
	float coverage = genericData.data.cloud_coverage * 1.01; // match the main compute
	float scale    = genericData.data.large_noise_scale;
	vec3  lpos     = genericData.data.largenoiseposition;
	vec3  elpos    = genericData.data.extralargenoiseposition;
	float elscale  = genericData.data.extralargenoisescale;

	// Vertically integrate the coarse shape through the slab; strongest layer wins
	// (a column with cloud anywhere in it casts a shadow).
	float cov = 0.0;
	for (int i = 0; i < 3; i++) {
		float h = mix(floorH, ceilH, (float(i) + 0.5) / 3.0);
		vec3 wp = vec3(worldXZ.x, h, worldXZ.y);
		float elv = texture(extra_large_noise, (wp.xz - elpos.xz) / elscale).a;
		cov = max(cov, sampleSceneCoarse(lpos, wp, ceilH, floorH, elv, scale, coverage, 0.0));
	}
	imageStore(shadow_map, px, vec4(cov, 0.0, 0.0, 1.0));
}
