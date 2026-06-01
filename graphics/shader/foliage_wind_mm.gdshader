shader_type spatial;

// Wind sway for the instanced (MultiMesh) terrain foliage scattered by the
// foliage dressing brush. Each plant is one MultiMesh instance, so
// MODEL_MATRIX[3] carries that instance's world plant point.
//
// This is the foliage sibling of grass_wind.gdshader. Grass leans every vertex
// purely by its height above the root, which would bend a foliage trunk like
// rubber. Here the motion is split in two so a rigid stem can stay planted:
//
//   * trunk bend  — a gentle whole-plant lean that scales with height. Small,
//     so even a tall stem only flexes a little near the top.
//   * canopy flutter — a higher-frequency wobble that scales with each vertex's
//     RADIAL distance from the vertical axis through the root. Trunk verts sit
//     near that axis (reach ≈ 0) so they barely move; branch/leaf tips reach far
//     out and flutter. This is the cheap geometric proxy for "trunk vs canopy"
//     that needs no extra per-vertex authoring on the library mesh.
//
// Wind comes from the same shared global uniforms as grass (driven off the
// environment Wind slider in client.go), so foliage and grass react to one
// consistent world wind. world_vertex_coords lets us read/write VERTEX in world
// space, so the wind blows in one world direction and the displacement is
// independent of each instance's random scale.
//
// cull_disabled keeps flat leaf cards visible from both sides; the trunk mesh
// is closed so its back faces are hidden anyway. Alpha is a hard scissor (not a
// blend) so thousands of instances render in the opaque pass with no
// transparency sorting — matching foliage_wind.gdshader's cut-out look.
render_mode blend_mix, depth_draw_opaque, cull_disabled, diffuse_burley, specular_schlick_ggx, world_vertex_coords;

// Per-surface look, copied from the source BaseMaterial3D in Go (see
// foliageWindMaterial) so the swaying instances match the same mesh placed as
// scenery.
uniform sampler2D albedo_texture : source_color, filter_linear_mipmap, repeat_enable;
uniform vec4 albedo : source_color = vec4(1.0);
uniform sampler2D normal_texture : hint_normal, filter_linear_mipmap, repeat_enable;
uniform bool has_normal_texture = false;
uniform float normal_scale : hint_range(-16.0, 16.0) = 1.0;
uniform float roughness : hint_range(0.0, 1.0) = 0.5;
uniform float metallic : hint_range(0.0, 1.0) = 0.0;
// Dielectric specular reflectance (Godot's f0; 0.5 is the engine default, ~4%).
// Defaults to 0 — matte — to match grass_wind: the flat leaf cards are usually
// seen near edge-on, where Fresnel drives the AMBIENT specular toward white, so
// under the colour-source day/night ambient any non-zero value sheens the
// canopy in a way the matte terrain never does. Overridable per-material for a
// glossier leaf. NOTE: foliageWindMaterial copies albedo/roughness/metallic/
// normal from the source but NOT specular, so this default is the operative
// value at runtime regardless of the imported material.
uniform float specular : hint_range(0.0, 1.0) = 0.0;
uniform float alpha_scissor : hint_range(0.0, 1.0) = 0.5;

// Shared world wind — the very same globals grass_wind.gdshader reads, so one
// slider drives the whole world. (registered + updated in client.go.)
global uniform float grass_wind_strength;
global uniform float grass_wind_speed;
global uniform vec2  grass_wind_dir;
global uniform float grass_wind_bias;

// Height-brush hover preview. Mirrors grass_wind.gdshader so scattered foliage
// rides the previewed terrain shift before a stroke commits; the editor re-plants
// on the real surface on commit and zeroes grass_brush_height when not previewing.
// grass_brush_mode: 0 additive (raise/lower/river), 1 plateau (flat-top flatten),
// 2 smooth (soft-cone flatten); the flatten modes pull toward grass_brush_target_y.
global uniform vec2  grass_brush_uplift;
global uniform float grass_brush_height;
global uniform float grass_brush_radius;
global uniform float grass_brush_mode;
global uniform float grass_brush_target_y;

// Foliage tuning. trunk_bend scales the whole-plant lean (kept small so stems
// stay upright); canopy_flutter scales the radial branch/leaf wobble. Both are
// deliberately gentler than grass — foliage should drift, not thrash.
uniform float trunk_bend     : hint_range(0.0, 1.0) = 0.08;
uniform float canopy_flutter : hint_range(0.0, 2.0) = 0.18;

// Master multiplier for ALL wind motion (lean + flutter + bob). The dressing
// brushes reuse this shader for rigid scatter that must still ride the height
// preview but should NOT wobble — pebbles, mushrooms and boulders set
// sway_amount = 0 (see foliageWindMaterial), keeping the grass_brush preview
// block below active while zeroing the sway. Foliage proper leaves it at 1.0.
// NB: named sway_amount, NOT sway — `sway` is a local in vertex() (the gust↔pull
// wind mix) and a uniform of the same name would shadow it / fail to compile.
uniform float sway_amount : hint_range(0.0, 1.0) = 1.0;

void vertex() {
	// The instance's plant point in world space (shared by every vertex of this
	// plant, so the whole thing hinges coherently about its base).
	vec3 root = MODEL_MATRIX[3].xyz;

	// Height above the plant point and radial reach out from the vertical trunk
	// axis. max() anchors the base; reach ~0 at the stem, large at leaf tips.
	float height = max(VERTEX.y - root.y, 0.0);
	vec2  radial = VERTEX.xz - root.xz;
	float reach  = length(radial);

	vec2  dir   = normalize(grass_wind_dir);
	float t     = TIME * grass_wind_speed;
	float phase = dot(root.xz, dir) * 0.35;

	// Whole-plant gust → steady downwind pull as the wind rises (grass_wind_bias
	// ramps in late), shared with the grass look so a storm pushes both one way.
	float gust = sin(t + phase) * 0.7 + sin(t * 1.7 + phase * 1.6) * 0.3;
	float pull = 0.85 + sin(t * 4.0 + phase * 2.0) * 0.1;
	float sway = mix(gust, pull, grass_wind_bias);

	// Trunk bend: lean grows with height but is scaled small, so a tall stem only
	// flexes near the top instead of toppling. (sway_amount 0 → rigid, e.g. rocks.)
	vec2 lean = dir * (sway_amount * sway * grass_wind_strength * trunk_bend * height);

	// Canopy flutter: a soft wobble that grows with radial reach, so the stem
	// (reach ~0) is still while the foliage tips drift. Low frequencies keep it a
	// lazy sway rather than a buzz; phase varies with the vertex world position so
	// neighbouring leaves don't move in lockstep.
	float fl = sin(t * 1.9 + VERTEX.x * 1.3 + VERTEX.z * 1.0)
	         + sin(t * 3.1 + VERTEX.y * 1.6) * 0.4;
	float flAmp = sway_amount * fl * grass_wind_strength * canopy_flutter * reach;
	vec2  flutter = dir * flAmp;

	VERTEX.xz += lean + flutter;
	// A small vertical bob on the canopy so it doesn't feel rigidly horizontal.
	VERTEX.y  += flAmp * 0.1;

	// Height-brush preview (see the grass_brush_* uniforms / grass_wind). The whole
	// plant shifts as a unit (evaluated at its root). mode 0 lifts additively
	// (raise/lower/river); the flatten modes pull toward grass_brush_target_y —
	// plateau with a flat-topped falloff (so it rides the mesa, NOT lifted by the
	// strength fraction, which was the floating bug), smooth with a soft cone.
	// PLATEAU_FLAT_CORE matches plateauFlatCore in editor_terrain.go.
	float br2 = grass_brush_radius * grass_brush_radius;
	if (grass_brush_height != 0.0 && br2 > 0.0) {
		vec2  bd  = root.xz - grass_brush_uplift;
		float bd2 = dot(bd, bd);
		if (bd2 < br2) {
			float w = 1.0 - bd2 / br2;
			float newy;
			if (grass_brush_mode > 1.5) {
				// Smooth: pull toward the target with a soft 1−d²/r² cone.
				float fac = clamp(grass_brush_height * w, 0.0, 1.0);
				newy = mix(root.y, grass_brush_target_y, fac);
			} else if (grass_brush_mode > 0.5) {
				// Plateau: flat-topped falloff so the plant rides the mesa.
				float tt = sqrt(bd2) / grass_brush_radius;
				float ff = 1.0 - smoothstep(0.6, 1.0, tt);
				float fac = clamp(grass_brush_height * ff, 0.0, 1.0);
				newy = mix(root.y, grass_brush_target_y, fac);
			} else {
				// Additive lift (raise/lower/river).
				newy = root.y + grass_brush_height * w;
			}
			VERTEX.y += max(newy, -2.0) - root.y;
		}
	}
}

void fragment() {
	vec4 tex = texture(albedo_texture, UV) * albedo;
	ALBEDO = tex.rgb;
	ALPHA = tex.a;
	ALPHA_SCISSOR_THRESHOLD = alpha_scissor;
	ROUGHNESS = roughness;
	METALLIC = metallic;
	// Matte by default (see the `specular` uniform): f0 == 0 removes the direct
	// AND the ambient specular, so the canopy responds to ambient through diffuse
	// only, like the terrain.
	SPECULAR = specular;
	if (has_normal_texture) {
		NORMAL_MAP = texture(normal_texture, UV).rgb;
		NORMAL_MAP_DEPTH = normal_scale;
	}
}
