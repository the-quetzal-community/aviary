shader_type spatial;

// Realistic water surface shader.
//
// Faithful adaptation of the UnionBytes "Realistic Water Shader"
// (MIT, Copyright (c) 2019 Achim Menzel), ported to Godot 4.
// https://github.com/UnionBytes/Realistic-Water-Shader
//
// Features:
//   - Gerstner waves (three superimposed waves) displacing the plane in
//     vertex() and producing an analytic tangent/binormal/normal frame.
//     Waves are progressively disabled when the terrain floor is close to the
//     water level so really shallow areas are completely still.
//   - Two scrolling normal maps blended together, with a third distortion
//     map perturbing their UVs for a more organic surface.
//   - Depth-based dye colour: the scene depth behind the water is
//     reconstructed in view space and used to drive a Beer's-law blend
//     between a shallow and a deep colour, plus shoreline foam.
//   - A cheap screen-space refraction that offsets the screen sample by the
//     surface normal.
//   - Terrain-height-aware shallow calming: when the heightmap floor is close
//     to the water level we disable geometric waves, damp ripples/refraction,
//     and kill foam (prevents flickering in thin water over terrain).
//     Shoreline geometry (the vertex clamp) is unaffected.
render_mode blend_mix, depth_draw_always, cull_back;

// --- Wave parameters --------------------------------------------------------
// Each wave is packed as vec4(direction.x, direction.y, steepness, wavelength).
uniform float wave_speed = 0.35;
uniform vec4 wave_a = vec4(1.0, 1.0, 0.22, 3.0);
uniform vec4 wave_b = vec4(1.0, 0.6, 0.19, 1.55);
uniform vec4 wave_c = vec4(1.0, 1.3, 0.16, 0.9);

// --- Normal map sampling ----------------------------------------------------
uniform sampler2D normalmap_a_sampler : hint_normal, filter_linear_mipmap, repeat_enable;
uniform sampler2D normalmap_b_sampler : hint_normal, filter_linear_mipmap, repeat_enable;
uniform sampler2D uv_sampler : hint_default_black, filter_linear_mipmap, repeat_enable;
uniform sampler2D foam_sampler : source_color, filter_linear_mipmap, repeat_enable;

uniform vec2 sampler_scale = vec2(0.25, 0.25);
uniform vec2 sampler_direction = vec2(0.035, 0.028);
uniform vec2 uv_sampler_scale = vec2(0.25, 0.25);
uniform float uv_sampler_strength = 0.04;
uniform float normalmap_strength : hint_range(0.0, 4.0) = 1.0;

// --- Colour / depth ---------------------------------------------------------
uniform sampler2D screen_texture : hint_screen_texture, filter_linear_mipmap;
uniform sampler2D depth_texture : hint_depth_texture, filter_linear_mipmap;

uniform vec4 color_deep : source_color = vec4(0.1, 0.2, 0.35, 1.0);
uniform vec4 color_shallow : source_color = vec4(0.25, 0.55, 0.6, 1.0);
uniform float beers_law = 2.0;
uniform float depth_offset = -0.75;
uniform float refraction = 0.075;
uniform float foam_distance : hint_range(0.0, 2.0) = 0.4;

// When the terrain floor is within this vertical distance of the water level
// (i.e. max water column height < shallow_calming), we:
//   - scale Gerstner wave displacement + normals to zero (no waves at all)
//   - damp the scrolling normal map ripples and refraction
//   - kill shoreline foam
// This gives completely still water in thin puddles and prevents the glitchy
// flickering that used to appear over uneven terrain. Shorelines are unaffected.
uniform float shallow_calming : hint_range(0.0, 2.0) = 0.6;

// World-space Y of the base (un-waved) water surface. The editor pushes the
// current water level here whenever it changes.
uniform float water_level = 0.0;

// World-space position, forwarded so the normal maps can scroll over world
// coordinates (keeps neighbouring tiles seamless).
varying vec3 world_pos;

// Terrain floor (from CUSTOM0.r) forwarded to fragment for shallow calming.
// Also used in vertex to compute wave_scale so geometric Gerstner waves are
// fully disabled in really shallow water (consistent with the fragment damping).
varying float v_terrain_floor;

// Gerstner wave displacement for a single wave. Accumulates the analytic
// tangent and binormal so the surface normal can be derived afterwards.
// The final 'scale' factor (0..1) damps the wave completely in very shallow
// water so there is no geometric ripple at all when terrain is close.
vec3 gerstner_wave(vec4 wave, vec3 pos, inout vec3 tangent, inout vec3 binormal, float t, float scale) {
	float steepness = wave.z;
	float wavelength = max(wave.w, 0.0001);
	float k = 2.0 * PI / wavelength;
	float c = sqrt(9.8 / k);
	vec2 d = normalize(wave.xy);
	float f = k * (dot(d, pos.xz) - c * t);
	float a = steepness / k;

	float s = scale;
	tangent += vec3(
		-d.x * d.x * (steepness * sin(f)) * s,
		d.x * (steepness * cos(f)) * s,
		-d.x * d.y * (steepness * sin(f)) * s
	);
	binormal += vec3(
		-d.x * d.y * (steepness * sin(f)) * s,
		d.y * (steepness * cos(f)) * s,
		-d.y * d.y * (steepness * sin(f)) * s
	);
	return vec3(
		d.x * (a * cos(f)) * s,
		a * sin(f) * s,
		d.y * (a * cos(f)) * s
	);
}

void vertex() {
	world_pos = (MODEL_MATRIX * vec4(VERTEX, 1.0)).xyz;

	// CUSTOM0.r carries the world-space terrain floor at this vertex's XZ
	// (filled per-vertex by reloadWater for BOTH the plane and the side
	// walls). The water is clamped to stay at or above it so the shoreline
	// meets the terrain cleanly and the side walls become a real water
	// column instead of clipping through the ground.
	float terrain_floor = CUSTOM0.r;
	v_terrain_floor = terrain_floor;

	// Compute how much of the Gerstner waves we are allowed to apply here.
	// When the terrain floor is close to the water level we scale the entire
	// wave (displacement + tangent/binormal contributions) toward zero so
	// really shallow water has no visible waves at all.
	float max_column = water_level - terrain_floor;
	float wave_scale = 1.0;
	if (shallow_calming > 0.0001) {
		wave_scale = clamp(max_column / shallow_calming, 0.0, 1.0);
	}

	float t = TIME * wave_speed;
	vec3 tangent = vec3(1.0, 0.0, 0.0);
	vec3 binormal = vec3(0.0, 0.0, 1.0);
	vec3 p = world_pos;
	vec3 offset = vec3(0.0);
	offset += gerstner_wave(wave_a, p, tangent, binormal, t, wave_scale);
	offset += gerstner_wave(wave_b, p, tangent, binormal, t, wave_scale);
	offset += gerstner_wave(wave_c, p, tangent, binormal, t, wave_scale);

	// Apply the wave displacement in world space then bring it back into the
	// local space the vertex started in.
	world_pos += offset;

	// Clamp the (waved) surface between the terrain floor and the base water
	// level. Both the plane and the side walls run through this shader, so the
	// side's top edge (which shares the plane edge's world XZ) gets the same
	// wave + clamp and stays connected to the plane. clamp() copes with
	// terrain_floor > water_level (dry land): it collapses to water_level,
	// which is then occluded by the terrain — i.e. no visible water there.
	//
	// Note: when terrain is close we have already scaled the wave offset and
	// its tangent/binormal contributions toward zero via wave_scale, so the
	// surface is nearly flat before this clamp is applied.
	world_pos.y = clamp(world_pos.y, terrain_floor, water_level);

	VERTEX = (inverse(MODEL_MATRIX) * vec4(world_pos, 1.0)).xyz;

	vec3 nrm = normalize(cross(binormal, tangent));
	NORMAL = normalize((inverse(MODEL_MATRIX) * vec4(nrm, 0.0)).xyz);
	TANGENT = normalize((MODEL_MATRIX * vec4(tangent, 0.0)).xyz);
	BINORMAL = normalize((MODEL_MATRIX * vec4(binormal, 0.0)).xyz);
}

void fragment() {
	// Scroll the two normal maps in opposite-ish directions, distorting their
	// lookup with the uv distortion sampler for a less repetitive surface.
	vec2 base_uv = world_pos.xz;
	vec2 uv_offset = sampler_direction * TIME;
	vec2 uv_distortion = (texture(uv_sampler, base_uv * uv_sampler_scale).rg * 2.0 - 1.0) * uv_sampler_strength;

	vec2 uv_a = base_uv * sampler_scale + uv_offset + uv_distortion;
	vec2 uv_b = base_uv * sampler_scale - uv_offset + uv_distortion;

	vec3 n_a = texture(normalmap_a_sampler, uv_a).xyz;
	vec3 n_b = texture(normalmap_b_sampler, uv_b).xyz;
	vec3 normal_map = mix(n_a, n_b, 0.25); // 0.75 of A, 0.25 of B

	// --- Reconstruct scene depth behind the water ---------------------------
	// Godot 4 NDC depth is in [0, 1]; transform back into view space to get a
	// linear distance and compare against the water surface's view depth.
	float raw_depth = texture(depth_texture, SCREEN_UV).r;
	vec3 ndc = vec3(SCREEN_UV * 2.0 - 1.0, raw_depth);
	vec4 view = INV_PROJECTION_MATRIX * vec4(ndc, 1.0);
	view.xyz /= max(view.w, 0.0001);
	float scene = -view.z;
	float surf = -VERTEX.z;
	float water_depth = scene - surf;

	// --- Shallow water calming via explicit terrain height ------------------
	// We use the heightmap-derived terrain floor (stable, per-vertex) rather
	// than noisy depth deltas. When the terrain is close, the water should be
	// mild/still and foam-free. This does not affect shoreline geometry, which
	// is produced by the vertex clamp on world_pos.y.
	float max_column = water_level - v_terrain_floor;
	float calm = 1.0;
	if (shallow_calming > 0.0001) {
		calm = clamp(max_column / shallow_calming, 0.0, 1.0);
	}

	// Beer's-law absorption: deeper water tends toward color_deep.
	float beers = exp(-beers_law * max(water_depth + depth_offset, 0.0));
	float depth_blend = clamp(1.0 - beers, 0.0, 1.0);
	vec4 dye = mix(color_shallow, color_deep, depth_blend);

	// --- Screen-space refraction --------------------------------------------
	// Damp refraction in shallow zones for a calmer, less distorted look.
	float eff_refraction = refraction * calm;
	vec2 refraction_uv = SCREEN_UV + normal_map.xy * eff_refraction;
	vec3 refracted = texture(screen_texture, refraction_uv).rgb;
	vec3 water_color = mix(refracted, dye.rgb, clamp(depth_blend + 0.25, 0.0, 1.0));

	// --- Shoreline foam ------------------------------------------------------
	// Suppress foam entirely in very shallow areas (the main source of
	// glitchy flickering over bumpy terrain). The existing depth-based foam
	// is also multiplied by calm for a smooth handoff.
	if (foam_distance > 0.0) {
		float foam_factor = clamp(1.0 - water_depth / foam_distance, 0.0, 1.0);
		foam_factor *= calm;
		if (foam_factor > 0.0) {
			vec3 foam = texture(foam_sampler, uv_a).rgb;
			water_color = mix(water_color, water_color + foam, foam_factor);
		}
	}

	// Calm shallow water: progressively flatten the normal map for a still,
	// glassy appearance instead of high-frequency ripples that look wrong
	// when the water is only a few centimetres deep.
	vec3 flat_normal = vec3(0.5, 0.5, 1.0);
	normal_map = mix(flat_normal, normal_map, calm);

	ALBEDO = water_color;
	NORMAL_MAP = normal_map;
	NORMAL_MAP_DEPTH = normalmap_strength * calm;
	METALLIC = 0.1;
	ROUGHNESS = 0.2;
	SPECULAR = 0.5;
	// Shallow water is more transparent, deep water more opaque.
	ALPHA = clamp(0.35 + depth_blend * 0.55, 0.0, 1.0);
}
