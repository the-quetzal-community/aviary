shader_type spatial;

// ============================================================================
// Simplified water surface for the LOWEST graphics tier (QualityToaster).
//
// A flat blue transparent surface with a single scrolling normal map for "basic
// normals" — and nothing else. Compared with water.gdshader it drops, per
// fragment: the depth-texture fetch, the screen-texture refraction, the
// screen-space reflection ray-march, the foam octaves, and the second/contrast-
// preserved normal layers; and, per vertex, the geometric Gerstner swell. That
// leaves a couple of texture taps where the full shader does a dozen-plus — the
// kind of bandwidth the potato tier can't spare — while still reading as water.
//
// It honours the SAME per-vertex geometry contract as water.gdshader (VERTEX.y =
// base surface; CUSTOM0.r = terrain floor; CUSTOM0.gb = flow; CUSTOM0.a = heave
// weight / slope) and mirrors the SAME brush-preview + water-level-glide
// uniforms, so the editor's terrain edits keep the water welded to the previewed
// ground exactly as on the higher tiers — only the fragment shading is cheaper.
//
// The material instance is shared with water.gdshader (TerrainEditor.water_shader);
// Client.applyWaterQuality binds whichever Shader matches the active tier, so the
// Go uniform pushes (which all target that one material) keep working unchanged.
// ============================================================================

render_mode blend_mix, depth_draw_always, cull_back;

uniform sampler2D normalmap_a_sampler : hint_normal, filter_linear_mipmap, repeat_enable;

uniform float normal_scale = 0.18;                     // world tiling of the ripple normal map
uniform float normal_strength : hint_range(0.0, 2.0) = 0.5;
uniform float still_speed = 0.05;                      // drift speed of the ripple
uniform float cycle_seconds = 2.5;                     // scroll cycle length

// Depth tint comes from the heightfield water column (surface - floor), NOT a
// depth-texture fetch: shallow water reads light, deep water the deep blue.
uniform vec4 color_shallow : source_color = vec4(0.20, 0.45, 0.52, 1.0);
uniform vec4 color_deep : source_color = vec4(0.06, 0.16, 0.28, 1.0);
uniform float depth_fade = 1.0;                        // how fast colour saturates with depth
uniform float min_alpha : hint_range(0.0, 1.0) = 0.45;

// --- Brush-preview + water-level uniforms (mirror water.gdshader) -----------
// Declared so the shared-material Go pushes resolve when this shader is bound,
// and so the water tracks raise/lower/flatten/river previews and water-level
// glides identically on the low tier. See water.gdshader for the full rationale.
uniform float water_level = 0.0;
uniform float water_rise = 0.0;
uniform vec3 uplift;
uniform float height = 0.0;
uniform float radius = 1.0;
uniform float river_preview = 0.0;
uniform float brush_mode = 0.0;

// Declared (unused here) so the Highest-tier reflection push and the wind-driven
// swell push are harmless no-ops while this shader is bound — keeps the
// shader-swap symmetric with no orphaned parameters.
uniform float reflection_strength : hint_range(0.0, 1.0) = 0.0;
uniform float wave_height : hint_range(0.0, 3.0) = 0.0;

varying vec3 world_pos;
varying float v_surface;
varying float v_floor;
varying vec2 v_flow;
varying float v_side;

void vertex() {
	world_pos = (MODEL_MATRIX * vec4(VERTEX, 1.0)).xyz; // XZ kept undisplaced — stable texture UVs
	v_floor = CUSTOM0.r;
	v_flow = CUSTOM0.gb;
	float ground_ref = CUSTOM0.r;

	// Terrain-sculpt preview (identical falloffs to water.gdshader): keep the
	// water welded to the previewed ground before the stroke commits.
	if (brush_mode > 0.5 && height != 0.0 && radius > 0.0) {
		// Flatten preview (plateau / smooth): the floor follows the target; the
		// lake surface itself doesn't move.
		float bdx = world_pos.x - uplift.x;
		float bdz = world_pos.z - uplift.z;
		float bd2 = bdx * bdx + bdz * bdz;
		if (bd2 < radius * radius) {
			float ff;
			if (brush_mode > 1.5) {
				ff = 1.0 - bd2 / (radius * radius);   // smooth cone
			} else {
				float tt = sqrt(bd2) / radius;        // plateau flat top
				ff = 1.0 - smoothstep(0.6, 1.0, tt);
			}
			float fac = clamp(height * ff, 0.0, 1.0);
			v_floor = max(mix(v_floor, uplift.y, fac), -2.0);
		}
	} else if (height != 0.0 && radius > 0.0) {
		float bdx = world_pos.x - uplift.x;
		float bdz = world_pos.z - uplift.z;
		float bd2 = bdx * bdx + bdz * bdz;
		if (bd2 < radius * radius) {
			float disp = height * (1.0 - bd2 / (radius * radius));
			v_floor = max(v_floor + disp, -2.0);
			// A river riding the raised/lowered ground follows; the carve brush
			// (river_preview > 0) leaves the surface to the fill block below.
			if (length(v_flow) > 0.001 && river_preview <= 0.0) {
				world_pos.y += disp;
			}
		}
	}

	// River-tool preview: lift the otherwise-hidden still-water plane so the
	// channel's water shows before the stroke commits (flat fill, +1 cell collar,
	// clamped up only). Matches reloadWater's committed surface.
	if (river_preview > 0.0 && radius > 0.0) {
		float rdx = world_pos.x - uplift.x;
		float rdz = world_pos.z - uplift.z;
		float rd2 = rdx * rdx + rdz * rdz;
		float reach = radius + 1.0;
		if (rd2 < reach * reach) {
			float target = ground_ref - 0.2;        // 0.2 == waterSurfaceDrop
			target = max(target, water_level);
			world_pos.y = max(world_pos.y, target);
		}
	}

	// Cut-through walls carry a horizontal mesh normal; the flat surface an up
	// normal. Flag the walls so their ripple is flattened in fragment.
	vec3 mesh_normal = NORMAL;
	float side = (abs(mesh_normal.y) < 0.5) ? 1.0 : 0.0;
	v_side = side;

	// Smooth water-level changes: still water rides water_rise toward the new
	// level; on a wall only the TOP follows (CUSTOM0.a heave weight), the bed
	// stays pinned. Rivers keep their own ground-relative lip (gated on flow).
	if (water_rise != 0.0 && length(v_flow) < 0.001) {
		world_pos.y += water_rise * ((side > 0.5) ? clamp(CUSTOM0.a, 0.0, 1.0) : 1.0);
	}

	v_surface = world_pos.y;
	VERTEX = (inverse(MODEL_MATRIX) * vec4(world_pos, 1.0)).xyz;

	if (side > 0.5) {
		// Keep the wall's outward normal so the slice is lit as a vertical face.
		NORMAL = normalize(mesh_normal);
	} else {
		// Flat surface: a fixed tangent frame (no Gerstner) for the ripple normals.
		NORMAL = vec3(0.0, 1.0, 0.0);
		TANGENT = vec3(1.0, 0.0, 0.0);
		BINORMAL = vec3(0.0, 0.0, 1.0);
	}
}

void fragment() {
	vec2 buv = world_pos.xz;

	// Basic ripple normals: a single scrolling map, sampled twice at half-cycle-
	// offset phases (both fract()'d into [0,1) so the scroll offset is ALWAYS
	// bounded — no long-session float-precision decay) and triangle cross-blended
	// to hide each cycle's reset. Cheap stand-in for the full shader's two-map
	// contrast-preserving flow.
	vec2 adv = vec2(0.7, 0.4) * (still_speed * cycle_seconds) * normal_scale;
	float phase = TIME / cycle_seconds;
	float p0 = fract(phase);
	float p1 = fract(phase + 0.5);
	float w = abs(1.0 - 2.0 * p0);
	vec2 uv = buv * normal_scale;
	vec3 na = texture(normalmap_a_sampler, uv - adv * p0).rgb;
	vec3 nb = texture(normalmap_a_sampler, uv - adv * p1).rgb;
	vec3 nmap = mix(na, nb, w);

	// Depth tint straight from the heightfield column (no depth-texture fetch).
	float column = max(v_surface - v_floor, 0.0);
	float fade = clamp(1.0 - exp(-depth_fade * column), 0.0, 1.0);
	vec3 dye = mix(color_shallow.rgb, color_deep.rgb, fade);

	// Flatten the ripple on the cut-through walls (XZ-keyed normals smear on a
	// vertical face) — same treatment as the full shader.
	nmap = mix(nmap, vec3(0.5, 0.5, 1.0), v_side);

	ALBEDO = dye;
	NORMAL_MAP = nmap;
	NORMAL_MAP_DEPTH = normal_strength * (1.0 - v_side);
	METALLIC = 0.0;
	ROUGHNESS = 0.12;
	SPECULAR = 0.5;
	// Transparent: shallow water lets more of the ground through, deep water
	// saturates toward opaque.
	ALPHA = clamp(min_alpha + fade * (1.0 - min_alpha), 0.0, 1.0);
}
