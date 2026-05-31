shader_type spatial;

// Wind sway for the instanced (MultiMesh) terrain grass. Each blade is a
// MultiMesh instance, so MODEL_MATRIX carries that blade's world transform.
//
// This shader reproduces the look of the imported yughues grass material —
// double-sided, alpha-BLENDED (smooth cut-out, not a hard scissor), normal
// mapped, roughness ~0.5 — while adding the vertex sway. The per-surface
// look (albedo/normal/roughness/metallic) is copied from the source
// BaseMaterial3D in Go (see grassWindMaterial); the wind is shared.
//
// world_vertex_coords lets us read and write VERTEX in world space, so the
// wind blows in a single consistent world direction across the whole field
// (instead of each blade's randomly-yawed local axis) and the per-blade
// height mask is a plain world-Y difference from the blade's plant point.
//
// cull_disabled keeps the flat billboard blades visible from both sides
// (the source material is doubleSided).
render_mode blend_mix, depth_draw_opaque, cull_disabled, diffuse_burley, specular_schlick_ggx, world_vertex_coords;

uniform sampler2D albedo_texture : source_color, filter_linear_mipmap, repeat_enable;
uniform vec4 albedo : source_color = vec4(1.0);
uniform sampler2D normal_texture : hint_normal, filter_linear_mipmap, repeat_enable;
uniform bool has_normal_texture = false;
uniform float normal_scale : hint_range(-16.0, 16.0) = 1.0;
uniform float roughness : hint_range(0.0, 1.0) = 0.5;
uniform float metallic : hint_range(0.0, 1.0) = 0.0;

// Wind is driven from the environment "Wind" slider via global shader
// parameters (registered + updated in client.go), so every grass blade in the
// world reacts to the one shared wind with no per-material bookkeeping.
// strength is the horizontal lean (world units) at the tip of a 1-unit-tall
// blade during a full gust; the lean scales with height up the blade so grass
// of any size sways proportionally.
global uniform float grass_wind_strength;
global uniform float grass_wind_speed;
global uniform vec2  grass_wind_dir;
// 0 = symmetric travelling waves (light wind ripples across the field, passing
// back and forth through vertical). 1 = a steady one-directional push that
// holds the blades over downwind with the ripple riding on top as flutter —
// the hurricane look. Driven up with the slider (see updateWeatherIntensity).
global uniform float grass_wind_bias;

void vertex() {
	// The instance's plant point in world space. Every vertex of one blade
	// shares it, so the blade hinges as a unit; using it (rather than each
	// vertex) for the wave phase keeps a blade coherent while neighbouring
	// blades stay out of lockstep.
	vec3 root = MODEL_MATRIX[3].xyz;

	// Height of this vertex above its plant point. Blades stand up +Y in world
	// space once planted, so this measures distance up the blade. max() anchors
	// the base (and any verts modelled at/below the pivot) so the grass stays
	// rooted while the tips travel.
	float height = max(VERTEX.y - root.y, 0.0);

	vec2 dir = normalize(grass_wind_dir);
	float t = TIME * grass_wind_speed;
	// Phase advances along the wind direction so gusts visibly roll downwind.
	float phase = dot(root.xz, dir) * 0.35;

	// Light wind: a symmetric travelling wave that ripples across the field,
	// rocking the blades back and forth through vertical.
	float wave = sin(t + phase) * 0.7 + sin(t * 1.9 + phase * 1.7) * 0.3;

	// Strong wind: a steady downwind pull with rapid, small flutter on top —
	// the blades are yanked over one way and "pull" in fast jitters rather than
	// rolling in a wave. No low-frequency wave, and the offset stays positive
	// (always downwind) so it never rocks back through vertical.
	float jitter = sin(t * 5.3 + phase * 3.1) * 0.12 + sin(t * 8.7 + phase) * 0.06;
	float pull = 0.85 + jitter;

	// Cross-fade wave → pull as the wind rises (grass_wind_bias ramps in late),
	// so low/mid wind keeps the waves and a hurricane is a hard directional pull.
	float sway = mix(wave, pull, grass_wind_bias);

	// Lean expressed as a fraction of the blade's height (1.0 would lay the blade
	// flat on the ground). Cap it below 1 so even a hurricane only yanks the
	// blade hard over, never completely flat, and drop the tip (sqrt) so the
	// blade bends/pivots about its rooted base instead of stretching.
	const float MAX_LEAN = 0.8;
	float leanFrac = clamp(sway * grass_wind_strength, -MAX_LEAN, MAX_LEAN);
	vec2 disp = dir * (leanFrac * height);
	VERTEX.xz += disp;
	// Shift Y by the drop only; verts at/below the pivot (height == 0) are
	// left untouched so base geometry isn't disturbed.
	float dropped = sqrt(max(height * height - dot(disp, disp), 0.0));
	VERTEX.y += dropped - height;
}

void fragment() {
	vec4 tex = texture(albedo_texture, UV) * albedo;
	ALBEDO = tex.rgb;
	ALPHA = tex.a; // smooth alpha blend, matching the source BLEND material
	ROUGHNESS = roughness;
	METALLIC = metallic;
	if (has_normal_texture) {
		NORMAL_MAP = texture(normal_texture, UV).rgb;
		NORMAL_MAP_DEPTH = normal_scale;
	}
}
