shader_type spatial;
render_mode unshaded, blend_mix, depth_draw_never, cull_disabled;

// ============================================================================
// Underwater post-process.
//
// Drawn on the camera's fullscreen "cover" quad (POSITION forced to the far
// plane, like grid.gdshader). It refracts + tints the view when the camera dips
// toward / under the global water plane (water_level) and draws a crisp, wavy
// WATERLINE where the surface crosses the view: above the line stays clear,
// below it the scene is wobbled, tinted, and fogged with distance. The boundary
// is a HARD edge (no meniscus/foam band) — a clean air/water cut.
//
// The waterline is found per-pixel from the VIEW RAY, not from the rendered
// water mesh (which is back-culled and so invisible from below): a virtual
// sample point one band-length ahead of the lens is tested against the water
// plane. As the camera sinks the line sweeps up the screen; at exactly water
// level it sits on the view horizon. A separate camera-height term forces the
// WHOLE view underwater once the lens drops `submerge_depth` below the surface,
// so going under always reads as fully submerged regardless of look direction.
//
// THREE MEDIA: the editor pushes both the water surface (`water_level`) and the
// terrain floor (`terrain_level`) under the camera. Each pixel is air, WATER, or
// ROCK depending on where the virtual sample sits relative to those two planes,
// so BOTH boundaries get a crisp sweeping transition plane: drop below the water
// surface -> blue water; drop further, below the terrain floor (a sea/river bed)
// -> the rock side texture rises across the view and you read as buried. Rock is
// opaque & DISCRETE (no alpha fade): a crisp bed-line that appears only once the
// lens crosses under the ground, then sweeps up the view as you sink — it never
// transitions in early while the camera is still above the surface.
//
// Treats water as a horizontal plane at the camera's local surface — a good
// approximation for sea/lakes/rivers right where you're swimming.
// ============================================================================

uniform float water_level = 0.0;     // water surface Y under the camera
uniform float terrain_level = 0.0;   // terrain floor Y under the camera
uniform float effect_enabled = 1.0;  // 0 = fully off (camera over the un-built void)

uniform vec4 color_near : source_color = vec4(0.10, 0.30, 0.38, 1.0); // tint right at the lens
uniform vec4 color_deep : source_color = vec4(0.02, 0.10, 0.18, 1.0); // tint in the murky distance

uniform float waterline_dist : hint_range(0.1, 8.0) = 2.0;    // how far ahead the surface is sampled (band thickness)
uniform float waterline_soft : hint_range(0.0, 4.0) = 0.05;   // edge softness (world units); small = hard crisp line
uniform float waterline_wobble : hint_range(0.0, 0.5) = 0.05; // ripple amplitude of the waterline
uniform float submerge_depth : hint_range(0.01, 3.0) = 0.3;   // depth below the plane at which the WHOLE view is underwater

uniform float fog_density : hint_range(0.0, 1.0) = 0.06;      // murk added per world unit of scene distance
uniform float near_tint : hint_range(0.0, 1.0) = 0.18;        // tint strength right at the lens
uniform float max_tint : hint_range(0.0, 1.0) = 0.95;         // tint strength in the murky distance

// Wavy refraction of the submerged view.
uniform float wave_amount : hint_range(0.0, 0.06) = 0.018;    // screen-space refraction amplitude
uniform float wave_scale : hint_range(1.0, 40.0) = 9.0;       // spatial frequency of the waviness
uniform float wave_speed : hint_range(0.0, 6.0) = 1.3;

// Underground rock fill (when the lens is below terrain that is above water).
uniform sampler2D rock_sampler : source_color, filter_linear_mipmap, repeat_enable;
uniform float rock_scale = 0.35;                              // world tiling of the rock texture
uniform float rock_darken : hint_range(0.0, 1.0) = 0.55;      // how dark the buried view is

uniform sampler2D screen_texture : hint_screen_texture, filter_linear_mipmap;
uniform sampler2D depth_texture : hint_depth_texture, filter_nearest;

void vertex() {
	POSITION = vec4(VERTEX.xy, 1.0, 1.0); // fullscreen, far plane
}

void fragment() {
	// Switched off entirely when the camera isn't over a revealed terrain tile:
	// out over the empty, un-extended world there is no surface or bed to read
	// against, so the waterline/rock fill would be meaningless.
	if (effect_enabled < 0.5) {
		discard;
	}

	vec2 uv = SCREEN_UV;
	vec3 cam_pos = CAMERA_POSITION_WORLD;

	// World-space view ray for this pixel (Vulkan-safe: ndc.z = 0 at near).
	vec2 ndc = uv * 2.0 - 1.0;
	vec3 eye_dir = normalize((INV_PROJECTION_MATRIX * vec4(ndc, 0.0, 1.0)).xyz);
	vec3 world_dir = normalize(mat3(INV_VIEW_MATRIX) * eye_dir);

	float W = water_level;     // water surface under the camera
	float T = terrain_level;   // terrain floor under the camera
	bool has_water = W > T + 0.001;

	// Virtual sample one band-length ahead of the lens; comparing its height to
	// the two planes is what sweeps each transition across the view. A little
	// wobble keeps the boundaries from reading as dead-straight cuts.
	float wob = (sin(uv.x * 24.0 + TIME * 1.4) + sin(uv.x * 11.0 - TIME * 0.9)) * 0.5 * waterline_wobble;
	float py = cam_pos.y + world_dir.y * waterline_dist + wob;

	// Camera-height term: once the lens drops below the water surface, water fills
	// the WHOLE view regardless of look direction (so going under always reads as
	// submerged). Rock deliberately gets NO such term — it must WIPE in along the
	// bed-line, never fade the screen uniformly (see below).
	float cam_in_water = has_water ? smoothstep(0.0, -submerge_depth, cam_pos.y - W) : 0.0;

	// View-ray terms: the crisp, wavy boundary plane sweeping across the screen.
	float ray_below_water = has_water ? smoothstep(-waterline_soft, waterline_soft, W - py) : 0.0;
	float ray_below_terrain = smoothstep(-waterline_soft, waterline_soft, T - py);

	// ROCK below the terrain floor. Driven PURELY by the view-ray boundary so it
	// WIPES up the screen following the bed-line as the lens sinks — no screen-
	// wide alpha fade. `buried` is an (almost) hard step at the floor that gates
	// the wipe so rock never shows while the lens is still above ground (a steep
	// down-look could otherwise sample below T early). No camera-height fill is
	// needed for look-up coverage: once the lens is more than `waterline_dist`
	// under, every ray (even straight up) samples below T, so the wipe has by
	// then swept the entire view to rock on its own.
	float buried = smoothstep(0.0, -waterline_soft, cam_pos.y - T);
	float rock = clamp(ray_below_terrain * buried, 0.0, 1.0);

	// WATER between the floor and the surface; rock (below the bed) takes over.
	float water = clamp(max(cam_in_water, ray_below_water) - rock, 0.0, 1.0);

	float a = max(water, rock);
	if (a <= 0.0) {
		discard; // clear air / normal ground at this pixel
	}

	// --- Water look: wavy refraction + depth-fogged tint --------------------
	vec2 wv = vec2(
		sin(uv.y * wave_scale + TIME * wave_speed) + sin(uv.x * wave_scale * 1.7 - TIME * wave_speed * 0.8),
		cos(uv.x * wave_scale * 1.3 + TIME * wave_speed * 1.1) + sin(uv.y * wave_scale * 0.9 - TIME * wave_speed)
	) * 0.5;
	vec2 duv = clamp(uv + wv * wave_amount * water, vec2(0.0), vec2(1.0));
	vec3 scene = texture(screen_texture, duv).rgb;
	float raw = textureLod(depth_texture, duv, 0.0).r;
	vec4 vp = INV_PROJECTION_MATRIX * vec4(ndc, raw, 1.0);
	float scene_dist = -(vp.z / max(vp.w, 1e-4));
	float fog = 1.0 - exp(-fog_density * scene_dist);
	vec3 water_tint = mix(color_near.rgb, color_deep.rgb, fog);
	float tint = clamp(near_tint + fog * (max_tint - near_tint), 0.0, max_tint);
	vec3 water_col = mix(scene, water_tint, tint);

	// --- Rock look: triplanar mineral from a point ahead of the lens --------
	vec3 rp = cam_pos + world_dir * 3.0;
	vec3 nb = abs(world_dir) + 1e-3;
	vec3 rock_col = texture(rock_sampler, rp.zy * rock_scale).rgb * nb.x
		+ texture(rock_sampler, rp.xz * rock_scale).rgb * nb.y
		+ texture(rock_sampler, rp.xy * rock_scale).rgb * nb.z;
	rock_col = (rock_col / (nb.x + nb.y + nb.z)) * rock_darken;

	// Weight the two looks by their actual amounts (so a dry-ground rock fill
	// never bleeds in water tint, and vice-versa); alpha is the stronger of the
	// two so both the waterline and the bed line stay crisp.
	ALBEDO = (water_col * water + rock_col * rock) / max(water + rock, 1e-4);
	ALPHA = clamp(a, 0.0, 1.0);
}
